package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// TestFragDeaths_TeamkillVictimCountedViaDeathEvent: a killer-first
// teamkill obituary ("X mows down a teammate") names only the attacker,
// so the victim's death can't be attributed from the message. The
// authoritative protocol DeathEvent must still count it.
func TestFragDeaths_TeamkillVictimCountedViaDeathEvent(t *testing.T) {
	a := NewFragAnalyzer()
	ctx := &Context{}
	ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "killa", Team: "red"}
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "victim", Team: "red"}
	_ = a.Init(ctx)
	a.timing.Started = true // simulate match running

	// Teamkill obituary that names only the attacker.
	_ = a.OnEvent(&events.PrintEvent{Level: 1, Message: "killa mows down a teammate\n", Time: 10})
	// The protocol death for the (unnamed) victim on slot 2.
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 2, Time: 10, TimeMs: 10_000})

	a.UseCoreOutputs(&CoreOutputs{Slots: map[int]SlotInfo{
		1: {Name: "killa", Team: "red"},
		2: {Name: "victim", Team: "red"},
	}})
	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}

	if got := res.Frags.ByPlayer["victim"].Deaths; got != 1 {
		t.Errorf("victim deaths = %d, want 1 (teamkill death must be counted via DeathEvent)", got)
	}
	// The attacker isn't credited a kill for a teamkill.
	if p, ok := res.Frags.ByPlayer["killa"]; ok && p.Kills != 0 {
		t.Errorf("killa kills = %d, want 0 (teamkills aren't kills)", p.Kills)
	}
}

// TestFragDeaths_GatedToMatchTime: deaths outside the match window
// (warmup before start, intermission after end) are not counted — KTX
// only bumps deaths while match_in_progress.
func TestFragDeaths_GatedToMatchTime(t *testing.T) {
	a := NewFragAnalyzer()
	ctx := &Context{}
	ctx.Players[3] = &events.PlayerInfo{Slot: 3, Name: "p", Team: "red"}
	_ = a.Init(ctx)

	co := &CoreOutputs{Slots: map[int]SlotInfo{3: {Name: "p", Team: "red"}}}

	// Warmup death (before any start print): ignored.
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 3, Time: 1, TimeMs: 1_000})
	// Match starts.
	_ = a.OnEvent(&events.PrintEvent{Level: 2, Message: "The match has begun!\n", Time: 5})
	// In-match death: counted.
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 3, Time: 6, TimeMs: 6_000})
	// Match ends (intermission), then a post-match death: ignored.
	_ = a.OnEvent(&events.IntermissionEvent{Time: 20})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 3, Time: 21, TimeMs: 21_000})

	a.UseCoreOutputs(co)
	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}
	if got := res.Frags.ByPlayer["p"].Deaths; got != 1 {
		t.Errorf("deaths = %d, want 1 (only the in-match death)", got)
	}
}

// TestFragDeaths_ReconnectFoldsBothSlots: a player who reconnects onto a
// new slot mid-match has deaths on both slots; resolving each death by
// the identity at death-time must fold them into one player.
func TestFragDeaths_ReconnectFoldsBothSlots(t *testing.T) {
	a := NewFragAnalyzer()
	ctx := &Context{}
	_ = a.Init(ctx)
	a.timing.Started = true

	// Two deaths on slot 7 (first occupancy), two on slot 2 (after reconnect).
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 7, TimeMs: 100_000})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 7, TimeMs: 200_000})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 2, TimeMs: 700_000})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 2, TimeMs: 800_000})

	a.UseCoreOutputs(&CoreOutputs{Sessions: map[int][]ResolvedSession{
		7: {{StartMs: minInt32, EndMs: maxInt32, Name: "rusti", Team: "jah", IdentityKey: "id:0"}},
		2: {{StartMs: minInt32, EndMs: maxInt32, Name: "rusti", Team: "jah", IdentityKey: "id:0"}},
	}})
	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}
	if got := res.Frags.ByPlayer["rusti"].Deaths; got != 4 {
		t.Errorf("rusti deaths = %d, want 4 (both slots folded into one identity)", got)
	}
}
