package analyzer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mvd-analyzer/qwanalytics/loc"
	"github.com/mvd-analyzer/qwdemo/events"
)

// ItemAnalyzer builds the per-item pickup / respawn timeline by
// listening to the ItemSpawnEvent and ItemStateEvent signals the
// parser synthesises from the wire-level entity state stream
// (qwdemo/parser/entities.go). No KTX-print parsing, no BSP-entity
// preprocessing, no position-snap for item identity — the protocol
// itself tells us which entity is which item and when it's taken or
// back up.
//
// Player attribution (TakenBy / Team) still uses the nearest-player
// snap at the moment of pickup, because the entity-state stream
// doesn't carry "who touched it". That's a best-effort label; if
// multiple players are within the touch radius the nearest wins.
type ItemAnalyzer struct {
	ctx       *Context
	items     map[int]*itemEntity // entNum -> tracked item
	playerPos map[int][3]float32  // slot -> last known origin
	mapName   string              // resolved from fullserverinfo, used for loc lookup
	locFinder *loc.Finder
	// Match window — events during warmup and intermission shouldn't
	// create observable phases in the result.
	timing MatchTimingDetector

	// Megahealth holder tracking. The MH respawn timer only starts 20 s
	// after the holder's health drops to ≤ 100 (either by rot tick-down
	// or by death), with a 5 s minimum-hold floor enforced by KTX's
	// `item_megahealth_rot` (ktx/src/items.c:353 — first rot tick fires
	// 5 s post-pickup). Until that crossing the UI should show "held /
	// pending" rather than a countdown; see the frontend's `itemStatus`
	// in qw-web/static/app.js, which already treats RespawnAt==0 as the
	// pending state. We leave RespawnAt at 0 through the rot phase and
	// stamp it at the crossing.
	mhPickup     map[int]float64 // MH entNum -> pickup time
	heldMHs      map[int][]int   // slot -> MH entNums they currently hold
	playerHealth map[int]int     // slot -> last seen StatHealth value
}

type itemEntity struct {
	kind   string
	origin [3]float32
	phases []ItemPhase
}

func NewItemAnalyzer() *ItemAnalyzer {
	return &ItemAnalyzer{
		items:        make(map[int]*itemEntity),
		playerPos:    make(map[int][3]float32),
		mhPickup:     make(map[int]float64),
		heldMHs:      make(map[int][]int),
		playerHealth: make(map[int]int),
	}
}

// Standard Quake 1 / KTX / ktpro respawn times in seconds, keyed by the
// compact item kind strings emitted by the parser (qwdemo/parser/entities.go).
// These are the canonical DM/TDM values; practice mode, freshteams, and
// HoonyMode override some of them, but those modes are out of scope here.
//
// MH uses 20 for the final countdown only — the rot phase that precedes
// it is handled separately via holder health tracking (handleStatUpdate /
// handleDeath below), so the 20 stamped here is only a fallback when the
// holder's health transition is missing from the stream.
var kindRespawnSec = map[string]float64{
	"rl":      30,
	"lg":      30,
	"ssg":     30,
	"sng":     30,
	"ng":      30,
	"gl":      30,
	"ra":      20,
	"ya":      20,
	"ga":      20,
	"h25":     20,
	"h15":     20,
	"shells":  30,
	"nails":   30,
	"rockets": 30,
	"cells":   30,
	"quad":    60,
	"suit":    60,
	"pent":    300,
	"ring":    300,
}

func (a *ItemAnalyzer) Name() string { return "items" }

func (a *ItemAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

func (a *ItemAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.PrintEvent:
		a.timing.OnPrint(e)
	case *events.IntermissionEvent:
		a.timing.OnIntermission(e.Time)
	case *events.StuffTextEvent:
		if strings.HasPrefix(e.Command, "fullserverinfo ") {
			a.extractMapName(e.Command)
		}
	case *events.PlayerPositionEvent:
		a.playerPos[e.PlayerNum] = [3]float32{e.Origin[0], e.Origin[1], e.Origin[2]}
	case *events.ItemSpawnEvent:
		a.handleItemSpawn(e)
	case *events.ItemStateEvent:
		a.handleItemState(e)
	case *events.StatUpdateEvent:
		a.handleStatUpdate(e)
	case *events.DeathEvent:
		a.handleDeath(e)
	}
	return nil
}

func (a *ItemAnalyzer) extractMapName(cmd string) {
	rest := strings.TrimPrefix(cmd, "fullserverinfo ")
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, "\"")
	if i := strings.LastIndexByte(rest, '"'); i >= 0 {
		rest = rest[:i]
	}
	parts := strings.Split(rest, "\\")
	start := 0
	if len(parts) > 0 && parts[0] == "" {
		start = 1
	}
	for i := start; i+1 < len(parts); i += 2 {
		if parts[i] == "map" {
			a.mapName = parts[i+1]
			return
		}
	}
}

// handleItemSpawn records the item's identity and opens the initial
// available phase. Fires once per entity (or again if a baseline is
// resent mid-match, which is rare).
func (a *ItemAnalyzer) handleItemSpawn(e *events.ItemSpawnEvent) {
	// Backpacks are handled by BackpackAnalyzer (backpacks.go), which
	// emits one entry per RL/LG drop from KTX's //ktx drop hint. Skip
	// them here so the per-item phase model stays clean — the
	// entity-state stream for backpack edicts is noisy and not used
	// for tracking today.
	if e.Kind == "" || e.Kind == "backpack" {
		return
	}
	it := a.items[e.EntNum]
	if it == nil {
		it = &itemEntity{
			kind:   e.Kind,
			origin: e.Origin,
			phases: []ItemPhase{{AvailableFrom: 0}},
		}
		a.items[e.EntNum] = it
		return
	}
	// Update position / kind if the baseline changed. Don't touch
	// phases — the existing timeline is authoritative.
	it.kind = e.Kind
	it.origin = e.Origin
}

// handleItemState closes or opens a phase for a tracked item.
//
// Taken=true → close the current available phase with TakenAt, attribute
// the picker by nearest-player, and stamp RespawnAt from the standard
// kind→seconds table. MH is the exception: it uses holder-health tracking
// (handleStatUpdate / handleDeath) to compute the real 20 s countdown
// that only begins after rot ends, so we leave RespawnAt at 0 here and
// the UI renders that as "pending".
//
// Taken=false → respawn: open a new available phase. We intentionally
// don't stamp RespawnAt from the wire time any more — the wire respawn
// can slip by a full cycle on insta-regrabs (see qwdemo/MVD_FORMAT.md's
// "insta-regrab invisibility" note), which is why quad was showing 120 s
// and RL 60 s countdowns in the UI.
func (a *ItemAnalyzer) handleItemState(e *events.ItemStateEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	it := a.items[e.EntNum]
	if it == nil {
		return
	}
	if len(it.phases) == 0 {
		it.phases = []ItemPhase{{AvailableFrom: 0}}
	}
	last := &it.phases[len(it.phases)-1]

	if e.Taken {
		// Close the current available phase. If the last phase was
		// already closed (bug or duplicate state event), skip.
		if last.TakenAt > 0 {
			return
		}
		last.TakenAt = e.Time
		playerSlot, name, team := a.attributePickup(it.origin, e.Time)
		last.TakenBy = name
		last.Team = team

		if it.kind == "mh" {
			// Start holder tracking; RespawnAt stays 0 until the
			// holder's health drops to ≤ 100.
			a.mhPickup[e.EntNum] = e.Time
			if playerSlot >= 0 {
				a.heldMHs[playerSlot] = append(a.heldMHs[playerSlot], e.EntNum)
			}
			return
		}
		if sec, ok := kindRespawnSec[it.kind]; ok {
			last.RespawnAt = e.Time + sec
		}
		return
	}

	// Wire respawn: open the next available phase. Don't overwrite
	// RespawnAt that's already been computed from the kind table (or,
	// for MH, from the holder-health crossing). The one exception is
	// an unclosed phase at match start where we never observed the
	// take — leave RespawnAt at 0 and open a fresh phase.
	it.phases = append(it.phases, ItemPhase{AvailableFrom: e.Time})
}

// handleStatUpdate watches StatHealth for every slot that currently
// holds an MH. When a holder's health crosses from > 100 to ≤ 100 (rot
// tick-down, direct damage, telefrag, anything), that's the rot-end
// instant — stamp RespawnAt = max(pickup + 5, crossing) + 20 on every
// MH that slot is holding and forget them. The 5 s floor is KTX's first
// rot tick delay (items.c:353).
//
// StatItems updates are also tracked so the holder-forgets-they-have-MH
// edge case (IT_SUPERHEALTH bit clearing while health is still > 100,
// which shouldn't happen but might if we mis-attributed the pickup) is
// handled symmetrically.
func (a *ItemAnalyzer) handleStatUpdate(e *events.StatUpdateEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	switch e.StatIndex {
	case events.StatHealth:
		// Mirror TimelineAnalyzer's sentinel filter (ktx/src/combat.c:1001
		// sets health = 1000 + damage as a damage-indicator hint; real
		// player health caps at 250). Treat sentinels as "no update" so
		// they don't mask the real rot-end transition.
		if e.Value > 250 {
			return
		}
		prev := a.playerHealth[e.PlayerNum]
		a.playerHealth[e.PlayerNum] = e.Value
		if prev > 100 && e.Value <= 100 {
			a.stampHeldMHs(e.PlayerNum, e.Time)
		}
	case events.StatItems:
		if e.Value&events.ITSuperHealth != 0 {
			return
		}
		// Player's IT_SUPERHEALTH bit just cleared. KTX clears it from
		// inside item_megahealth_rot at rot-end (items.c:401), so this
		// is redundant with the health crossing above in the normal
		// case but catches the path where the health stream is thin.
		a.stampHeldMHs(e.PlayerNum, e.Time)
	}
}

// handleDeath is the backup path for the "holder died" case. DeathEvent
// is derived from the same StatHealth transition that would already
// trigger stampHeldMHs via handleStatUpdate, but subscribing to both
// is cheap insurance against event-ordering quirks between the two
// streams.
func (a *ItemAnalyzer) handleDeath(e *events.DeathEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	a.playerHealth[e.PlayerNum] = 0
	a.stampHeldMHs(e.PlayerNum, e.Time)
}

// stampHeldMHs closes out every MH phase currently owned by the given
// slot by stamping RespawnAt = max(pickup + 5, crossing) + 20. Idempotent
// — calling it twice for the same slot has no effect the second time
// because heldMHs[slot] is cleared.
func (a *ItemAnalyzer) stampHeldMHs(slot int, crossing float64) {
	ents := a.heldMHs[slot]
	if len(ents) == 0 {
		return
	}
	for _, ent := range ents {
		it := a.items[ent]
		if it == nil || len(it.phases) == 0 {
			continue
		}
		last := &it.phases[len(it.phases)-1]
		if last.TakenAt == 0 || last.RespawnAt != 0 {
			continue
		}
		pickup := a.mhPickup[ent]
		rotEnd := crossing
		if pickup+5 > rotEnd {
			rotEnd = pickup + 5
		}
		last.RespawnAt = rotEnd + 20
		delete(a.mhPickup, ent)
	}
	delete(a.heldMHs, slot)
}

// attributePickup finds the player whose last-known position is
// closest to the item origin. Used to label TakenBy on phase close.
// Returns ("","") if no player has a position recorded yet.
//
// Slots are iterated in numeric order (not map order) so that ties at
// the same squared distance resolve deterministically — without this,
// run-to-run output flips for items where two players land within
// float-precision of equal range.
func (a *ItemAnalyzer) attributePickup(itemPos [3]float32, _ float64) (int, string, string) {
	bestSlot := -1
	bestDistSq := float32(1e18)
	slots := make([]int, 0, len(a.playerPos))
	for slot := range a.playerPos {
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	for _, slot := range slots {
		pos := a.playerPos[slot]
		dx := pos[0] - itemPos[0]
		dy := pos[1] - itemPos[1]
		dz := pos[2] - itemPos[2]
		d := dx*dx + dy*dy + dz*dz
		if d < bestDistSq {
			bestDistSq = d
			bestSlot = slot
		}
	}
	if bestSlot < 0 {
		return -1, "", ""
	}
	if bestSlot >= len(a.ctx.Players) || a.ctx.Players[bestSlot] == nil {
		return bestSlot, "", ""
	}
	pl := a.ctx.Players[bestSlot]
	// Note: pl.Name is read at OnEvent time (when ItemStateEvent fires),
	// so it always carries the userinfo name. Demoinfo-resolved display
	// names aren't available yet — the demoinfo analyser hasn't
	// finalised when an item pickup is recorded. For the test corpus
	// userinfo == display, so the output is correct in practice; the
	// auth-override case is captured in NOTES-pickup-attribution-quality.md.
	return bestSlot, pl.Name, pl.Team
}

// Finalize builds the ItemsResult. Item names are kind-scoped
// ("ra", "mh_1", "mh_2", ...) and ordered deterministically by world
// position. Loc labels are attached best-effort from the .loc
// corpus — absent loc file yields empty Loc strings; the item list
// itself is always populated when the demo has any item events.
func (a *ItemAnalyzer) Finalize() (interface{}, error) {
	if len(a.items) == 0 {
		return nil, nil
	}

	// Best-effort loc lookup — does NOT affect whether items appear.
	if a.locFinder == nil && a.mapName != "" {
		if f, err := loc.LoadForMap(a.mapName); err == nil {
			a.locFinder = f
		}
	}

	type entry struct {
		entNum int
		it     *itemEntity
	}
	byKind := map[string][]entry{}
	for ent, it := range a.items {
		byKind[it.kind] = append(byKind[it.kind], entry{entNum: ent, it: it})
	}

	out := make([]ItemTimeline, 0, len(a.items))
	for kind, list := range byKind {
		sort.Slice(list, func(i, j int) bool {
			a, b := list[i].it, list[j].it
			if a.origin[0] != b.origin[0] {
				return a.origin[0] < b.origin[0]
			}
			if a.origin[1] != b.origin[1] {
				return a.origin[1] < b.origin[1]
			}
			return a.origin[2] < b.origin[2]
		})
		for i, e := range list {
			name := kind
			if len(list) > 1 {
				name = fmt.Sprintf("%s_%d", kind, i+1)
			}
			locName := ""
			if a.locFinder != nil {
				locName = a.locFinder.FindNearest(e.it.origin[0], e.it.origin[1], e.it.origin[2])
			}
			out = append(out, ItemTimeline{
				Name:   name,
				Kind:   kind,
				EntNum: e.entNum,
				X:      e.it.origin[0],
				Y:      e.it.origin[1],
				Z:      e.it.origin[2],
				Loc:    locName,
				Phases: e.it.phases,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})

	return &ItemsResult{Items: out}, nil
}
