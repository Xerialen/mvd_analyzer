package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// A reconnect during intermission re-initializes the slot with
// svc_updatefrags 0, which must not clobber the final score — the match
// scoreboard is immutable once the match ends. Observed on hub 212483
// (Doomie: 34 → 0) and 212545 (squeeze: 55 → 0); the pre-phase-1 0-frag
// filter masked the corruption by dropping the player entirely.
func TestMatchAnalyzer_PostMatchFragResetIgnored(t *testing.T) {
	a := NewMatchAnalyzer()
	ctx := &Context{}
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "Doomie", Team: "BhB"}
	if err := a.Init(ctx); err != nil {
		t.Fatal(err)
	}

	_ = a.OnEvent(&events.PrintEvent{Level: 2, Message: "The match has begun!\n", Time: 5})
	_ = a.OnEvent(&events.FragUpdateEvent{PlayerNum: 0, Frags: 34, Time: 1197})
	_ = a.OnEvent(&events.PrintEvent{Level: 2, Message: "The match is over\n", Time: 1210})
	// Post-match reconnect: the server re-inits the slot's scoreboard.
	_ = a.OnEvent(&events.FragUpdateEvent{PlayerNum: 0, Frags: 0, Time: 1213})

	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}
	if res.Match == nil || len(res.Match.Players) != 1 {
		t.Fatalf("match.players = %+v, want one entry", res.Match)
	}
	if got := res.Match.Players[0].Frags; got != 34 {
		t.Errorf("Frags = %d, want 34 (post-match reset must not clobber the final score)", got)
	}
	if len(res.Match.Teams) != 1 || res.Match.Teams[0].Frags != 34 {
		t.Errorf("Teams = %+v, want BhB with 34", res.Match.Teams)
	}
}

// In-match updates (including a mid-match reconnect's restore, which KTX
// re-asserts itself) apply normally.
func TestMatchAnalyzer_InMatchFragUpdatesApply(t *testing.T) {
	a := NewMatchAnalyzer()
	ctx := &Context{}
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "rusti", Team: "jah"}
	if err := a.Init(ctx); err != nil {
		t.Fatal(err)
	}

	_ = a.OnEvent(&events.PrintEvent{Level: 2, Message: "The match has begun!\n", Time: 5})
	_ = a.OnEvent(&events.FragUpdateEvent{PlayerNum: 0, Frags: 16, Time: 571})
	// Mid-match reconnect: re-init to 0, then the server restores.
	_ = a.OnEvent(&events.FragUpdateEvent{PlayerNum: 0, Frags: 0, Time: 613})
	_ = a.OnEvent(&events.FragUpdateEvent{PlayerNum: 0, Frags: 16, Time: 614})
	_ = a.OnEvent(&events.FragUpdateEvent{PlayerNum: 0, Frags: 17, Time: 629})

	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}
	if got := res.Match.Players[0].Frags; got != 17 {
		t.Errorf("Frags = %d, want 17", got)
	}
}
