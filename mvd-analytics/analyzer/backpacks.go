package analyzer

import (
	"sort"
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/locvis"
	"github.com/mvd-analyzer/mvd-reader/events"
)

// BackpackAnalyzer emits one BackpackDrop per RL/LG backpack the
// dying player leaves behind. It consumes KTX's
// `//ktx drop <ent> <items> <player_ent>` STUFFCMD_DEMOONLY
// directive (ktx/src/items.c:2740), which the parser surfaces as
// BackpackDropHintEvent. The hint is emitted exactly once per
// real drop and carries the dropper's slot directly, so
// attribution is authoritative — no closest-player snap.
//
// Pickup tracking is intentionally NOT implemented. The wire-level
// ItemStateEvent stream for backpack edicts produces phantom
// visibility cycles in the 200 ms class (same edict going
// taken/untaken repeatedly without real pickups in between) that
// we cannot currently distinguish from genuine fast pickups. Rather
// than report unreliable data, pickups are deferred to a later
// branch that diagnoses the flutter source first.
//
// Non-RL/LG drops (SSG/NG/SNG/GL/empty) are not surfaced: KTX only
// emits the //ktx drop hint for RL and LG, and the QW protocol
// does not transmit backpack contents as wire-level entity state.
type BackpackAnalyzer struct {
	ctx       *Context
	core      *CoreOutputs
	playerPos map[int][3]float32 // slot -> last-known origin (for drop origin)
	drops     []BackpackDrop
	mapName   string
	locFinder *locvis.Finder
	timing    MatchTimingDetector
}

// UseCoreOutputs wires CoreOutputs so Finalize can read the clock (co.Clock)
// to rebase drop times to the match-relative origin.
func (a *BackpackAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

// IT_* bit values for the //ktx drop ItemFlags argument, mirroring
// ktx/src/items.c:2738 where the hint is emitted.
const (
	itemFlagRL = 1 << 5 // IT_ROCKET_LAUNCHER
	itemFlagLG = 1 << 6 // IT_LIGHTNING
)

func NewBackpackAnalyzer() *BackpackAnalyzer {
	return &BackpackAnalyzer{
		playerPos: make(map[int][3]float32),
	}
}

func (a *BackpackAnalyzer) Name() string { return "backpacks" }

func (a *BackpackAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

func (a *BackpackAnalyzer) OnEvent(event events.Event) error {
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
		a.playerPos[e.PlayerNum] = e.Origin
	case *events.BackpackDropHintEvent:
		a.handleHint(e)
	}
	return nil
}

// handleHint records one BackpackDrop. The hint's PlayerEnt is the
// dropper's edict (player_slot + 1); their most recent position is
// the drop origin (KTX spawns the backpack at the dying player's
// s.v.origin). Defensive: skip on unrecognised flag combos.
func (a *BackpackAnalyzer) handleHint(e *events.BackpackDropHintEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	weapon := weaponFromItemFlags(e.ItemFlags)
	if weapon == "" {
		return
	}
	slot := e.PlayerEnt - 1
	if slot < 0 || slot >= len(a.ctx.Players) || a.ctx.Players[slot] == nil {
		return
	}
	pl := a.ctx.Players[slot]
	a.drops = append(a.drops, BackpackDrop{
		Time:   msTime(e.Time),
		Player: pl.Name,
		Team:   pl.Team,
		Weapon: weapon,
		Origin: a.playerPos[slot],
		EntNum: e.BackpackEnt,
	})
}

func weaponFromItemFlags(flags int) string {
	hasRL := flags&itemFlagRL != 0
	hasLG := flags&itemFlagLG != 0
	switch {
	case hasRL && !hasLG:
		return "rl"
	case hasLG && !hasRL:
		return "lg"
	default:
		// Both bits set or neither → unrecognised. Real KTX drops
		// always send exactly one; anything else gets dropped
		// defensively.
		return ""
	}
}

func (a *BackpackAnalyzer) extractMapName(cmd string) {
	if v, ok := parseInfoString(cmd)["map"]; ok {
		a.mapName = v
	}
}

// Finalize returns the collected drops sorted by time, with Loc
// resolved from the map's .loc corpus when available.
func (a *BackpackAnalyzer) Finalize(result *Result) error {
	if len(a.drops) == 0 {
		return nil
	}
	if a.locFinder == nil && a.mapName != "" {
		if f, err := locvis.LoadForMap(a.mapName); err == nil {
			a.locFinder = f
		}
	}
	sort.Slice(a.drops, func(i, j int) bool { return a.drops[i].Time < a.drops[j].Time })
	if a.locFinder != nil {
		for i := range a.drops {
			a.drops[i].Loc = a.locFinder.FindNearest(a.drops[i].Origin[0], a.drops[i].Origin[1], a.drops[i].Origin[2])
		}
	}
	result.Backpacks = a.drops

	// Born-correct timestamps: rebase drop times to the match clock.
	if ms := a.core.MatchStartMs(); ms > 0 {
		for i := range result.Backpacks {
			result.Backpacks[i].Time -= ms
		}
	}
	return nil
}
