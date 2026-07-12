package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// TestFinalize_EmptyTeamPlayerKeepsFragAndDeathEvents reproduces gameId
// 224758: a duel player whose userinfo *and* demoinfo team are both
// empty (iddQd) resolves to team "" for the whole match. FragEvents and
// DeathEvents used to gate on a resolvable team, silently dropping every
// event of that player — 34 deaths missing from the drill-down while the
// opponent's showed fine. The exports must gate on a resolvable name
// only; team stays best-effort ("" here, rewritten by normalizeDuelTeams
// for 1v1 results).
func TestFinalize_EmptyTeamPlayerKeepsFragAndDeathEvents(t *testing.T) {
	a := NewTimelineAnalyzer()
	a.ctx = &Context{}
	a.timing.Started = true
	a.timing.StartTime = 10.0
	a.timing.Ended = true
	a.timing.EndTime = 300.0

	// Slot 0: empty team (the iddQd case). Slot 1: normal team.
	a.ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "noteam", Team: ""}
	a.ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "red", Team: "red"}
	a.playerNames[0] = "noteam"
	a.playerNames[1] = "red"

	a.rawFrags = append(a.rawFrags,
		fragEvent{Time: 20.0, PlayerNum: 0, Delta: 1},
		fragEvent{Time: 30.0, PlayerNum: 1, Delta: 1},
	)
	a.rawDeaths = append(a.rawDeaths,
		deathEvent{Time: 30.0, PlayerNum: 0},
		deathEvent{Time: 20.0, PlayerNum: 1},
	)

	var result Result
	if err := a.Finalize(&result); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	ta := result.TimelineAnalysis
	if ta == nil {
		t.Fatal("nil TimelineAnalysis")
	}

	countBy := func(player string) (frags, deaths int) {
		for _, fe := range ta.FragEvents {
			if fe.Player == player {
				frags++
			}
		}
		for _, de := range ta.DeathEvents {
			if de.Player == player {
				deaths++
			}
		}
		return
	}

	if f, d := countBy("noteam"); f != 1 || d != 1 {
		t.Errorf("empty-team player: got %d fragEvents / %d deathEvents, want 1/1", f, d)
	}
	if f, d := countBy("red"); f != 1 || d != 1 {
		t.Errorf("teamed player: got %d fragEvents / %d deathEvents, want 1/1", f, d)
	}
}
