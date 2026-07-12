package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

func TestStreamsActiveWeaponRawNoClamp(t *testing.T) {
	a := NewTimelineAnalyzer()
	a.timing.Started = true

	updates := []struct {
		value int
		time  float64
	}{
		{value: 32, time: 1.0},
		{value: 32, time: 1.5},
		{value: 4096, time: 2.0},
		{value: 1, time: 2.5},
	}
	for _, update := range updates {
		if err := a.OnEvent(&events.StatUpdateEvent{
			PlayerNum: 3,
			StatIndex: events.StatActiveWeapon,
			Value:     update.value,
			Time:      update.time,
		}); err != nil {
			t.Fatalf("OnEvent: %v", err)
		}
	}

	state := a.playerState[3]
	if state == nil {
		t.Fatal("no player state recorded for slot 3")
	}
	want := []changeI16{{t: 1000, v: 32}, {t: 2000, v: 4096}, {t: 2500, v: 1}}
	if got := state.streams.activeWeapon; !equalChangeI16(got, want) {
		t.Fatalf("activeWeapon = %+v, want %+v", got, want)
	}
}

func TestStreamsActiveWeaponSurfacesToPlayerStream(t *testing.T) {
	var builder streamBuilder
	builder.recordActiveWeapon(1000, 32)
	builder.recordActiveWeapon(2000, 4096)

	stream := builder.toPlayerStream("plr", "team")
	want := []changeI16{{t: 1000, v: 32}, {t: 2000, v: 4096}}
	got := make([]changeI16, len(stream.ActiveWeapon))
	for i, entry := range stream.ActiveWeapon {
		got[i] = changeI16{t: entry.T, v: entry.V}
	}
	if !equalChangeI16(got, want) {
		t.Fatalf("ActiveWeapon = %+v, want %+v", got, want)
	}
}

func equalChangeI16(got, want []changeI16) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
