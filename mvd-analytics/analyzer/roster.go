package analyzer

import "github.com/mvd-analyzer/mvd-reader/events"

// Roster is the canonical player/team table with the duel (player-name-as-team)
// rewrite folded in. It is produced once by RosterAnalyzer (a CoreProducer)
// from co.DemoInfo and published on CoreOutputs, so every producer labels teams
// correctly AT BIRTH instead of a whole-Result rewrite fixing them afterwards.
//
// Publishing it replaces the old normalizeDuelTeams post-processor: instead of
// stamping the raw userinfo team on every record and then re-pointing them all
// at the synthetic name-per-player teams, each producer calls TeamFor as it
// emits — "born correct". The label decision is a local, testable call rather
// than an entry in one giant enumerating function.
//
// Nil-safe like Clock: a nil Roster (hand-built registries / unit tests that
// don't wire it) is a non-duel passthrough — TeamFor returns the raw team and
// Duel reports false, so the raw signal flows through untouched.
type Roster struct {
	// isDuel is true for a 1v1: the team concept is meaningless (each player
	// picks an arbitrary colour tag, or a frogbot has no team at all), so team
	// labels are rewritten to the player's own name.
	isDuel bool

	// demoDecided records that DemoInfo carried a players list, which is KTX's
	// authoritative end-of-match participant snapshot. When set, the duel
	// verdict is final and the no-demoinfo match fallback (noteMatchParticipants)
	// is vetoed — mirroring isDuelResult's "DemoInfo with players is
	// authoritative" rule.
	demoDecided bool

	// participants is the set of names whose team label is rewritten to the name
	// itself in a duel. Keyed by the display name every producer resolves to
	// (demoinfo player name, or the match participant name in the no-demoinfo
	// fallback).
	participants map[string]struct{}

	// order lists the participant names in demoinfo player order (or match
	// order in the fallback). The DemoInfo.Teams / Match.Teams rebuilds follow
	// it so the synthetic one-player-per-team layout stays deterministic.
	order []string
}

// newRoster builds the roster from the parsed demoinfo. DemoInfo with a players
// list is authoritative (KTX never lists spectators), so a duel is exactly two
// demoinfo players; any other count is a team game. A demoinfo with no players
// (failed JSON parse, non-KTX server) leaves the verdict undecided for the
// no-demoinfo match fallback. Mirrors the old isDuelResult demoinfo branch.
func newRoster(di *DemoInfoResult) *Roster {
	r := &Roster{participants: make(map[string]struct{})}
	if di == nil || len(di.Players) == 0 {
		return r
	}
	r.demoDecided = true
	if len(di.Players) == 2 {
		r.isDuel = true
		for _, p := range di.Players {
			r.participants[p.Name] = struct{}{}
			r.order = append(r.order, p.Name)
		}
	}
	return r
}

// noteMatchParticipants promotes a match with no usable demoinfo to duel mode
// when MatchAnalyzer built exactly two participants — the case the old
// isDuelResult covered via its match-players fallback. A no-op when the demoinfo
// already decided the verdict (demoDecided) or the count isn't two, so a real
// team game whose demoinfo happens to be absent is never misclassified. Called
// from MatchAnalyzer.Finalize; it mutates the shared co.Roster in place, so a
// label-emitting producer that finalizes later picks up the promoted verdict.
// (This fallback only fires on non-KTX demos with no demoinfo; on the KTX
// corpus demoDecided is always set, so it is a no-op.)
func (r *Roster) noteMatchParticipants(names []string) {
	if r == nil || r.demoDecided || len(names) != 2 {
		return
	}
	r.isDuel = true
	for _, n := range names {
		if _, ok := r.participants[n]; ok {
			continue
		}
		r.participants[n] = struct{}{}
		r.order = append(r.order, n)
	}
}

// Duel reports whether the match is a 1v1. Nil-safe (nil roster → false).
func (r *Roster) Duel() bool { return r != nil && r.isDuel }

// TeamFor returns the FINAL team label a producer should stamp for a record it
// attributes to name: the player's own name in a duel (when name is a tracked
// participant), otherwise the raw resolved team unchanged. Nil-safe and a
// no-op outside duel mode, so migrating a producer to call it is byte-identical
// on team games and folds in the duel rewrite on 1v1s.
func (r *Roster) TeamFor(name, rawTeam string) string {
	if r != nil && r.isDuel {
		if _, ok := r.participants[name]; ok {
			return name
		}
	}
	return rawTeam
}

// Participants returns the participant names in their canonical order (demoinfo
// player order, or match order in the no-demoinfo fallback). Used by the
// DemoInfo.Teams / Match.Teams rebuilds. The returned slice must not be mutated.
func (r *Roster) Participants() []string {
	if r == nil {
		return nil
	}
	return r.order
}

// RosterAnalyzer is the CoreProducer node that produces the Roster. It collects
// no events — the duel verdict and participant set come entirely from
// co.DemoInfo, which its `requires` edge on "demoinfo" guarantees is populated
// first. It writes nothing to Result directly except the in-place duel rewrite
// of the DemoInfo team labels it owns; every other producer reads the published
// Roster at Finalize.
type RosterAnalyzer struct{}

// NewRosterAnalyzer creates the roster analyzer.
func NewRosterAnalyzer() *RosterAnalyzer { return &RosterAnalyzer{} }

func (a *RosterAnalyzer) Name() string { return "roster" }

func (a *RosterAnalyzer) Init(ctx *Context) error { return nil }

func (a *RosterAnalyzer) OnEvent(event events.Event) error { return nil }

// Finalize is a no-op: the roster writes nothing of its own to Result. The
// DemoInfo team rewrite is applied in PopulateCore (co.DemoInfo is the same
// pointer demoinfo.Finalize wrote to result.DemoInfo).
func (a *RosterAnalyzer) Finalize(result *Result) error { return nil }

// PopulateCore builds and publishes the Roster from the demoinfo the demoinfo
// analyser produced (roster's `requires` edge on "demoinfo" schedules it
// first), and applies the duel rewrite to the DemoInfo team labels it owns.
// Every team-labelling producer declares a `requires` edge on "roster", so the
// DAG schedules them after this and each sees a complete Roster and a
// duel-rewritten DemoInfo.
func (a *RosterAnalyzer) PopulateCore(co *CoreOutputs) {
	r := newRoster(co.DemoInfo)
	co.Roster = r

	// DemoInfo team rewrite (the old normalizeDuelTeams DemoInfo block). In a
	// duel each player's team becomes their own name and DemoInfo.Teams is
	// rebuilt as the one-player-per-team layout in player order. The NameTable
	// (built from the raw teams earlier in demoinfo.PopulateCore) is left
	// untouched, so producers still resolve raw teams and apply the roster label
	// on top.
	if r.isDuel && co.DemoInfo != nil {
		for i := range co.DemoInfo.Players {
			co.DemoInfo.Players[i].Team = co.DemoInfo.Players[i].Name
		}
		teams := make([]string, len(r.order))
		copy(teams, r.order)
		co.DemoInfo.Teams = teams
	}
}
