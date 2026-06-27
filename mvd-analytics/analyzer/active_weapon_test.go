package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// TestStreams_ActiveWeaponRawNoClamp verifies the STAT_ACTIVEWEAPON dispatch
// records the active-weapon id as a sparse change stream that (a) dedups
// against the last value and (b) — unlike Armor — applies NO upper-bound
// clamp: a value above the armor cap (IT_AXE = 4096) must be surfaced raw.
func TestStreams_ActiveWeaponRawNoClamp(t *testing.T) {
	a := NewTimelineAnalyzer()
	a.timing.Started = true

	updates := []struct {
		v    int
		time float64
	}{
		{32, 1.0},   // IT_ROCKET_LAUNCHER
		{32, 1.5},   // duplicate value -> dropped
		{4096, 2.0}, // IT_AXE: > 200, must NOT be clamped (the armor path would reject it)
		{1, 2.5},    // IT_SHOTGUN
	}
	for _, u := range updates {
		if err := a.OnEvent(&events.StatUpdateEvent{
			PlayerNum: 3, StatIndex: events.StatActiveWeapon, Value: u.v, Time: u.time,
		}); err != nil {
			t.Fatalf("OnEvent: %v", err)
		}
	}

	st := a.playerState[3]
	if st == nil {
		t.Fatal("no player state recorded for slot 3")
	}
	got := st.streams.activeWeapon
	want := []changeI16{{t: 1000, v: 32}, {t: 2000, v: 4096}, {t: 2500, v: 1}}
	if len(got) != len(want) {
		t.Fatalf("activeWeapon = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestStreams_ActiveWeaponSurfacesToPlayerStream verifies the builder's
// activeWeapon stream is carried into result.PlayerStream.ActiveWeapon
// (the "w" field) by toPlayerStream.
func TestStreams_ActiveWeaponSurfacesToPlayerStream(t *testing.T) {
	var b streamBuilder
	b.recordActiveWeapon(1000, 32)
	b.recordActiveWeapon(2000, 4096)

	ps := b.toPlayerStream("plr", "team")
	if len(ps.ActiveWeapon) != 2 {
		t.Fatalf("ps.ActiveWeapon = %+v, want 2 entries", ps.ActiveWeapon)
	}
	if ps.ActiveWeapon[0].T != 1000 || ps.ActiveWeapon[0].V != 32 ||
		ps.ActiveWeapon[1].T != 2000 || ps.ActiveWeapon[1].V != 4096 {
		t.Errorf("ps.ActiveWeapon = %+v, want [{1000 32} {2000 4096}]", ps.ActiveWeapon)
	}
}
