package analyzer_test

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-reader/events"
)

// userinfo is a tiny helper for feeding a slot occupancy / name into the
// identity analyzer at a given time (seconds).
func userinfo(slot, userid int, name string, t float64) *events.UserInfoEvent {
	return &events.UserInfoEvent{
		Player: &events.PlayerInfo{Slot: slot, UserID: userid, Name: name},
		Time:   t,
	}
}

func runIdentity(t *testing.T, di *result.DemoInfoResult, evs ...events.Event) *analyzer.CoreOutputs {
	t.Helper()
	ctx := &analyzer.Context{DemoInfo: di}
	a := analyzer.NewIdentityAnalyzer()
	if err := a.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, e := range evs {
		if err := a.OnEvent(e); err != nil {
			t.Fatalf("onEvent: %v", err)
		}
	}
	co := &analyzer.CoreOutputs{}
	a.PopulateCore(co)
	return co
}

// TestIdentity_ReconnectDifferentSlot_DemoinfoJoin reproduces gameId
// 216835: a player plays the first half on one slot, reconnects onto
// another for the second half, and their vacated slot is later stamped
// with a different (phantom) name. The pre-reconnect events must resolve
// to the real player, the phantom name to itself, and both real-player
// sessions must share one identity.
func TestIdentity_ReconnectDifferentSlot_DemoinfoJoin(t *testing.T) {
	di := &result.DemoInfoResult{Players: []result.DemoInfoPlayer{
		{Name: "rusti", Team: "jah"},
		{Name: "biggz", Team: "jah"},
	}}
	co := runIdentity(t, di,
		userinfo(7, 8, "rusti", 5),    // rusti, first half, slot 7
		userinfo(2, 14, "rusti", 609), // rusti reconnects on slot 2
		userinfo(7, 15, "Luk", 766),   // slot 7 reused by a phantom
	)

	if got := co.SlotIdentityAt(7, 300_000).Name; got != "rusti" {
		t.Errorf("slot 7 @300s: got %q, want rusti", got)
	}
	if got := co.SlotIdentityAt(2, 900_000).Name; got != "rusti" {
		t.Errorf("slot 2 @900s: got %q, want rusti", got)
	}
	if got := co.SlotIdentityAt(7, 900_000).Name; got != "Luk" {
		t.Errorf("slot 7 @900s: got %q, want Luk (phantom keeps its own name)", got)
	}

	// Both real-player sessions resolve to the same identity key.
	early := identityKeyAt(co, 7, 300_000)
	late := identityKeyAt(co, 2, 900_000)
	if early == "" || early != late {
		t.Errorf("rusti's two sessions should share an identity key, got %q and %q", early, late)
	}
	// The phantom is a distinct identity.
	if k := identityKeyAt(co, 7, 900_000); k == early {
		t.Errorf("phantom Luk should not share rusti's identity key")
	}
}

// TestIdentity_ReconnectByKTXPrint unifies across a reconnect using the
// KTX rejoin broadcast, with no demoinfo present (the fallback signal
// for unauthenticated players).
func TestIdentity_ReconnectByKTXPrint(t *testing.T) {
	co := runIdentity(t, nil,
		userinfo(7, 8, "rusti", 5),
		&events.PrintEvent{Message: "rusti left the game with 16 frags", Time: 605, TargetPlayerNum: -1},
		userinfo(2, 14, "rusti", 609),
		&events.PrintEvent{Message: "rusti [jah] rejoins the game with 16 frags", Time: 610, TargetPlayerNum: -1},
	)
	if a, b := identityKeyAt(co, 7, 300_000), identityKeyAt(co, 2, 900_000); a == "" || a != b {
		t.Errorf("KTX rejoin print should unify rusti's sessions, got %q and %q", a, b)
	}
}

// TestIdentity_FallbackNameJoin unifies by netname when there is no
// demoinfo, no auth and no reconnect print (a bare / old demo).
func TestIdentity_FallbackNameJoin(t *testing.T) {
	co := runIdentity(t, nil,
		userinfo(7, 8, "rusti", 5),
		userinfo(2, 14, "rusti", 609),
	)
	if a, b := identityKeyAt(co, 7, 100_000), identityKeyAt(co, 2, 900_000); a == "" || a != b {
		t.Errorf("bare-demo same-name sessions should unify, got %q and %q", a, b)
	}
}

// TestIdentity_RenameSameSlotStaysOneSession verifies a plain rename
// (same userid) does not open a new session, so the slot resolves to a
// single identity throughout (today's correct behaviour, preserved).
func TestIdentity_RenameSameSlotStaysOneSession(t *testing.T) {
	di := &result.DemoInfoResult{Players: []result.DemoInfoPlayer{{Name: "newname", Team: "red"}}}
	co := runIdentity(t, di,
		userinfo(3, 9, "oldname", 5),
		userinfo(3, 9, "newname", 200), // rename, same userid
	)
	if n := len(co.Sessions[3]); n != 1 {
		t.Fatalf("rename with same userid should stay one session, got %d", n)
	}
	if got := co.SlotIdentityAt(3, 10_000).Name; got != "newname" {
		t.Errorf("renamed slot should resolve to final demoinfo name, got %q", got)
	}
}

// identityKeyAt returns the IdentityKey of the session covering tMs on
// slot, or "" if none.
func identityKeyAt(co *analyzer.CoreOutputs, slot int, tMs int32) string {
	for _, s := range co.Sessions[slot] {
		if tMs >= s.StartMs && tMs < s.EndMs {
			return s.IdentityKey
		}
	}
	return ""
}
