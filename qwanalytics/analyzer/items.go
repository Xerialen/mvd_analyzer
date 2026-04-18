package analyzer

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/mvd-analyzer/qwanalytics/items"
	"github.com/mvd-analyzer/qwanalytics/loc"
	"github.com/mvd-analyzer/qwdemo/events"
)

// ItemAnalyzer tracks pickup and respawn events for every item on
// the map, driven by the KTX demo-only stuffcmd protocol
// (`ktx/src/items.c`):
//
//	//ktx took  <ent> <respawn_sec> <player_ent>   pickup
//	//ktx timer <ent> <respawn_sec>                delayed timer (MH rot end)
//	//ktx drop  <ent> <item_flags>  <player_ent>   player dropped a weapon
//
// Entity numbers are opaque server-side IDs. We bind each ent to a
// MapItem from the BSP-derived items corpus the first time we see
// it, by position-matching the picking player's last-known origin
// to the nearest MapItem. Subsequent events just update the bound
// item's phase timeline.
type ItemAnalyzer struct {
	ctx              *Context
	mapItems         []items.MapItem
	corpusLoaded     bool
	mapName          string              // map short name ("rocka"), resolved from fullserverinfo
	locFinder        *loc.Finder         // map loc-name lookup, resolved lazily (same source as TimelineAnalyzer)
	entBinding       map[int]*itemEntity // ent-num -> bound item state
	boundMapItemsIdx map[int]bool        // index into mapItems, prevents double-binding
	playerPos        map[int][3]float32  // slot -> last known origin
	// Player slot ↔ ent mapping: KTX uses `player_ent = slot + 1`
	// (edict 0 is world). Stored explicitly so the lookup is obvious.
	matchStarted   bool
	matchEnded     bool
	matchStartTime float64
}

// itemEntity holds the per-entity phase timeline for one bound item.
// MapItemIdx refers back into a.mapItems for Kind / position.
type itemEntity struct {
	mapItemIdx int
	phases     []ItemPhase
}

// NewItemAnalyzer creates the analyzer. The map item corpus is
// resolved lazily in Init via ctx.ServerData / DemoInfo map name;
// maps not in the corpus just yield an empty result.
func NewItemAnalyzer() *ItemAnalyzer {
	return &ItemAnalyzer{
		entBinding:       make(map[int]*itemEntity),
		boundMapItemsIdx: make(map[int]bool),
		playerPos:        make(map[int][3]float32),
	}
}

func (a *ItemAnalyzer) Name() string { return "items" }

func (a *ItemAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

func (a *ItemAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.PrintEvent:
		a.detectMatchBoundary(e)
	case *events.PlayerPositionEvent:
		// Track origin even during warmup so the first pickup at
		// match start has a usable position for binding.
		a.playerPos[e.PlayerNum] = [3]float32{e.Origin[0], e.Origin[1], e.Origin[2]}
	case *events.IntermissionEvent:
		if a.matchStarted {
			a.matchEnded = true
		}
	case *events.StuffTextEvent:
		if strings.HasPrefix(e.Command, "fullserverinfo ") {
			a.extractMapName(e.Command)
		}
		if a.matchStarted && !a.matchEnded {
			a.handleKTX(e)
		}
	}
	return nil
}

// extractMapName pulls the short map name ("rocka") out of a
// `fullserverinfo "\map\rocka\..."` stufftext. The parser also
// records a MapFile on ServerData, but that gets overwritten by
// later model-list chunks — fullserverinfo is the stable source.
func (a *ItemAnalyzer) extractMapName(cmd string) {
	rest := strings.TrimPrefix(cmd, "fullserverinfo ")
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, "\"")
	if i := strings.LastIndexByte(rest, '"'); i >= 0 {
		rest = rest[:i]
	}
	parts := strings.Split(rest, "\\")
	// fullserverinfo values usually begin with a separator, leaving
	// an empty first element — skip it so key/value alignment is
	// correct.
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

// detectMatchBoundary mirrors TimelineAnalyzer's start / end match
// detection — keeps the item corpus in sync with the same match
// window. Only in-match events go into ItemsResult.
func (a *ItemAnalyzer) detectMatchBoundary(e *events.PrintEvent) {
	msg := e.Message
	if !a.matchStarted {
		if strings.Contains(msg, "match has begun") ||
			strings.Contains(msg, "Fight!") ||
			strings.Contains(msg, "begins in 1") ||
			strings.Contains(msg, "Go!") {
			a.matchStarted = true
			a.matchStartTime = e.Time
		}
		return
	}
	if a.matchEnded {
		return
	}
	if strings.Contains(msg, "the match is over") ||
		strings.Contains(msg, "match ended") ||
		strings.Contains(msg, "game over") ||
		strings.Contains(msg, "match complete") ||
		strings.Contains(msg, "timelimit hit") ||
		strings.Contains(msg, "fraglimit hit") {
		a.matchEnded = true
	}
}

func (a *ItemAnalyzer) handleKTX(e *events.StuffTextEvent) {
	// Commands come with the `//` prefix and may or may not have a
	// trailing newline. Trim both sides cheaply, then split once on
	// the verb.
	cmd := strings.TrimSpace(e.Command)
	if !strings.HasPrefix(cmd, "//ktx ") {
		return
	}
	rest := cmd[len("//ktx "):]
	sp := strings.IndexByte(rest, ' ')
	if sp < 0 {
		return
	}
	verb := rest[:sp]
	args := strings.Fields(rest[sp+1:])

	switch verb {
	case "took":
		// "took <ent> <respawn> <player_ent>"
		if len(args) != 3 {
			return
		}
		ent, _ := strconv.Atoi(args[0])
		respawn, _ := strconv.Atoi(args[1])
		playerEnt, _ := strconv.Atoi(args[2])
		a.handleTook(ent, respawn, playerEnt-1, e.Time)
	case "timer":
		// "timer <ent> <respawn>"
		if len(args) != 2 {
			return
		}
		ent, _ := strconv.Atoi(args[0])
		respawn, _ := strconv.Atoi(args[1])
		a.handleTimer(ent, respawn, e.Time)
	case "drop":
		// Dropped weapons have their own edict numbers and no BSP
		// spawn position — they don't map to any MapItem. Ignored
		// for v1; could surface as a separate drops array later.
	}
}

func (a *ItemAnalyzer) handleTook(ent, respawn, slot int, t float64) {
	state := a.entBinding[ent]
	if state == nil {
		bound := a.bind(ent, respawn, slot)
		if bound == nil {
			return
		}
		state = bound
		a.entBinding[ent] = state
	}

	// Lazily open a new available phase if the item has respawned
	// since its last pickup.
	if n := len(state.phases); n > 0 {
		last := &state.phases[n-1]
		if last.TakenAt > 0 && last.RespawnAt > 0 && last.RespawnAt <= t {
			state.phases = append(state.phases, ItemPhase{
				AvailableFrom: last.RespawnAt,
			})
		}
	}
	if len(state.phases) == 0 {
		state.phases = append(state.phases, ItemPhase{AvailableFrom: 0})
	}

	last := &state.phases[len(state.phases)-1]
	if last.TakenAt > 0 {
		// Shouldn't happen — item already held. Ignore the
		// duplicate rather than corrupting the phase list.
		return
	}

	player := ""
	team := ""
	if slot >= 0 && slot < len(a.ctx.Players) && a.ctx.Players[slot] != nil {
		player = a.ctx.Players[slot].Name
		team = a.ctx.Players[slot].Team
	}

	last.TakenAt = t
	last.TakenBy = player
	last.Team = team
	if respawn > 0 {
		last.RespawnAt = t + float64(respawn)
	}
	// respawn == 0 leaves RespawnAt at 0 — filled in by a later
	// `//ktx timer` for MH.
}

func (a *ItemAnalyzer) handleTimer(ent, respawn int, t float64) {
	state := a.entBinding[ent]
	if state == nil || len(state.phases) == 0 {
		return
	}
	// The pending phase is the most recent closed pickup with
	// RespawnAt == 0 — i.e., the MH whose rot just ended.
	for i := len(state.phases) - 1; i >= 0; i-- {
		p := &state.phases[i]
		if p.TakenAt > 0 && p.RespawnAt == 0 {
			p.RespawnAt = t + float64(respawn)
			return
		}
	}
}

// ensureCorpus lazily loads the map-item list. ServerData's MapFile
// gets filled in from the model-list message that follows the
// initial svc_serverdata, so the name isn't guaranteed to be
// available on the first event — retry on every bind attempt until
// we succeed or exhaust our sources.
func (a *ItemAnalyzer) ensureCorpus() {
	if a.corpusLoaded {
		return
	}
	if a.mapName == "" {
		return
	}
	if list, err := items.LoadForMap(a.mapName); err == nil {
		a.mapItems = list
	}
	a.corpusLoaded = true
}

// bind snaps a first-pickup event to a MapItem from the corpus by
// position. Respawn value filters candidate kinds; the nearest
// unbound MapItem within the search radius wins.
func (a *ItemAnalyzer) bind(ent, respawn, slot int) *itemEntity {
	a.ensureCorpus()
	if len(a.mapItems) == 0 {
		return nil
	}
	pos, ok := a.playerPos[slot]
	if !ok {
		return nil
	}
	allowed := allowedKindsForRespawn(respawn)

	best := -1
	bestDist := float32(math.MaxFloat32)
	const maxBindRadius = float32(128.0)
	for i := range a.mapItems {
		if a.boundMapItemsIdx[i] {
			continue
		}
		mi := &a.mapItems[i]
		if !allowed[mi.Kind] {
			continue
		}
		dx := pos[0] - mi.X
		dy := pos[1] - mi.Y
		dz := pos[2] - mi.Z
		d := dx*dx + dy*dy + dz*dz
		if d < bestDist {
			bestDist = d
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	if bestDist > maxBindRadius*maxBindRadius {
		return nil
	}
	a.boundMapItemsIdx[best] = true
	return &itemEntity{
		mapItemIdx: best,
		phases:     []ItemPhase{{AvailableFrom: 0}},
	}
}

// allowedKindsForRespawn translates a KTX respawn-seconds value
// into the set of map-item kinds that can fire with that timer.
// Derived from ktx/src/items.c:
//
//   - items.c:355     MH pickup emits respawn=0 (rot hasn't armed the timer yet)
//   - items.c:406     MH rot-end emits timer=20 (standard 20s respawn)
//   - items.c:540     Armors emit took=20
//   - items.c:808/1048 Weapons emit took=weapon_time (default 30, k_freshteams overrides)
//   - items.c:2074    Pent/Ring emit took=300 (normal) or 60/180/240 (HoonyMode variants)
//   - items.c:2083    Quad emits took=60 (normal)
//   - items.c:2091    All powerups emit took=30 in practice mode
//
// Items with no `//ktx took` emission (small health 15/25, ammo
// boxes) aren't tracked — a known KTX protocol gap, not a bug here.
func allowedKindsForRespawn(r int) map[string]bool {
	switch r {
	case 0:
		return map[string]bool{"mh": true}
	case 20:
		return map[string]bool{
			"ga": true, "ya": true, "ra": true,
			"mh": true, // post-rot timer
		}
	case 30:
		// Weapons (default) + powerups in practice mode.
		return map[string]bool{
			"ssg": true, "ng": true, "sng": true,
			"gl": true, "rl": true, "lg": true,
			"quad": true, "pent": true, "ring": true,
		}
	case 60:
		// Quad (normal) or Pent/Ring (HoonyMode 5-min rounds).
		return map[string]bool{"quad": true, "pent": true, "ring": true}
	case 180, 240, 300:
		return map[string]bool{"pent": true, "ring": true}
	}
	return map[string]bool{}
}

// Finalize builds the ItemsResult from per-entity phase timelines.
// Item names are kind-scoped and numbered by world position so the
// output is deterministic ("mh_1", "mh_2") across runs.
func (a *ItemAnalyzer) Finalize() (interface{}, error) {
	if len(a.entBinding) == 0 {
		return nil, nil
	}

	// Group bound entities by kind to assign `_N` suffix within kind.
	type entry struct {
		state *itemEntity
		mi    *items.MapItem
	}
	byKind := map[string][]entry{}
	for _, st := range a.entBinding {
		mi := &a.mapItems[st.mapItemIdx]
		byKind[mi.Kind] = append(byKind[mi.Kind], entry{state: st, mi: mi})
	}

	// Loc lookup uses the same .loc corpus TimelineAnalyzer consumes.
	// Resolve lazily here so ItemAnalyzer isn't ordering-dependent on
	// DemoInfo.Finalize.
	if a.locFinder == nil && a.mapName != "" {
		if f, err := loc.LoadForMap(a.mapName); err == nil {
			a.locFinder = f
		}
	}

	out := make([]ItemTimeline, 0, len(a.entBinding))
	for kind, list := range byKind {
		// Sort within kind by (x, y, z) for stable numbering.
		sort.Slice(list, func(i, j int) bool {
			a, b := list[i].mi, list[j].mi
			if a.X != b.X {
				return a.X < b.X
			}
			if a.Y != b.Y {
				return a.Y < b.Y
			}
			return a.Z < b.Z
		})
		for i, e := range list {
			name := kind
			if len(list) > 1 {
				name = fmt.Sprintf("%s_%d", kind, i+1)
			}
			locName := ""
			if a.locFinder != nil {
				locName = a.locFinder.FindNearest(e.mi.X, e.mi.Y, e.mi.Z)
			}
			out = append(out, ItemTimeline{
				Name:   name,
				Kind:   kind,
				X:      e.mi.X,
				Y:      e.mi.Y,
				Z:      e.mi.Z,
				Loc:    locName,
				Phases: e.state.phases,
			})
		}
	}

	// Final ordering: by kind alphabetical, then by name, so the
	// JSON output is stable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})

	return &ItemsResult{Items: out}, nil
}
