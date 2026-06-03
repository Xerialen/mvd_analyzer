package view

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func TestEventsDefaultExcludesHealth(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{
			Global: result.GlobalStream{MatchStart: 0, MatchEnd: 10000},
			Players: []result.PlayerStream{
				{
					Name:   "p1",
					Health: []result.ChangeI16{{T: 1000, V: 100}, {T: 2000, V: 50}},
					// Spawns/Deaths are int32 ms in schema v8.
					Spawns: []int32{500},
				},
			},
		},
	}
	v, err := Events(r, EventsFilter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, e := range v.Events {
		if e.Type == "health" {
			t.Fatalf("default Events should not include health, got %+v", e)
		}
	}
	// Spawn IS in the default set.
	gotSpawn := false
	for _, e := range v.Events {
		if e.Type == "spawn" {
			gotSpawn = true
		}
	}
	if !gotSpawn {
		t.Fatalf("expected spawn event in default Events output")
	}
}

func TestEventsHealthOptIn(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{
			Global: result.GlobalStream{MatchStart: 0, MatchEnd: 10000},
			Players: []result.PlayerStream{
				{
					Name:   "p1",
					Health: []result.ChangeI16{{T: 1000, V: 100}, {T: 2000, V: 50}},
				},
			},
		},
	}
	v, err := Events(r, EventsFilter{Types: []string{"health"}})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(v.Events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(v.Events))
	}
	if v.Events[0].Type != "health" {
		t.Fatalf("Type = %s, want health", v.Events[0].Type)
	}
}

func TestEventsTimeOrdered(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{
			Global: result.GlobalStream{MatchStart: 0, MatchEnd: 10000},
			Players: []result.PlayerStream{
				{
					Name:   "p1",
					Spawns: []int32{1000, 5000},
					Deaths: []int32{3000, 7000},
				},
			},
		},
	}
	v, _ := Events(r, EventsFilter{})
	last := -1.0
	for _, e := range v.Events {
		if e.T < last {
			t.Fatalf("events out of order: %v", v.Events)
		}
		last = e.T
	}
}

func TestEventsDamageOptIn(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{Global: result.GlobalStream{MatchStart: 0, MatchEnd: 10000}},
		Damage: &result.DamageResult{
			Events: []result.DamageEntry{
				{Time: 2000, Attacker: "killer", Victim: "target", Weapon: "rl", Damage: 89, VictimWep: "rl"},
			},
		},
	}

	// Not in the default set.
	def, err := Events(r, EventsFilter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, e := range def.Events {
		if e.Type == "damage" {
			t.Fatalf("default Events should not include damage, got %+v", e)
		}
	}

	// Opt-in surfaces it with the expected Detail shape.
	v, err := Events(r, EventsFilter{Types: []string{"damage"}})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(v.Events) != 1 {
		t.Fatalf("damage events = %d, want 1", len(v.Events))
	}
	e := v.Events[0]
	if e.Type != "damage" || e.Player != "killer" || e.T != 2.0 {
		t.Errorf("event = %+v, want damage/killer/2.0", e)
	}
	if e.Detail["victim"] != "target" || e.Detail["damage"] != 89 ||
		e.Detail["weapon"] != "rl" || e.Detail["victimWep"] != "rl" {
		t.Errorf("detail = %v", e.Detail)
	}

	// A player filter matches damage they received, not just dealt.
	vv, err := Events(r, EventsFilter{Types: []string{"damage"}, Players: []string{"target"}})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(vv.Events) != 1 {
		t.Fatalf("victim-filtered damage events = %d, want 1", len(vv.Events))
	}
}

func TestEventsTelefragOptIn(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{Global: result.GlobalStream{MatchStart: 0, MatchEnd: 10000}},
		Damage: &result.DamageResult{
			Telefrags: []result.PositionalKill{
				{Time: 3000, Attacker: "tp", Victim: "victim"},
			},
		},
	}

	// Not in the default set.
	def, err := Events(r, EventsFilter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, e := range def.Events {
		if e.Type == "telefrag" {
			t.Fatalf("default Events should not include telefrag, got %+v", e)
		}
	}

	// Opt-in surfaces it.
	v, err := Events(r, EventsFilter{Types: []string{"telefrag"}})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(v.Events) != 1 {
		t.Fatalf("telefrag events = %d, want 1", len(v.Events))
	}
	e := v.Events[0]
	if e.Type != "telefrag" || e.Player != "tp" || e.T != 3.0 || e.Detail["victim"] != "victim" {
		t.Errorf("event = %+v", e)
	}
}

func TestEventsStompOptIn(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{Global: result.GlobalStream{MatchStart: 0, MatchEnd: 10000}},
		Damage: &result.DamageResult{
			Stomps: []result.PositionalKill{{Time: 4000, Attacker: "jumper", Victim: "squished"}},
		},
	}
	def, err := Events(r, EventsFilter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, e := range def.Events {
		if e.Type == "stomp" {
			t.Fatalf("default Events should not include stomp, got %+v", e)
		}
	}
	v, err := Events(r, EventsFilter{Types: []string{"stomp"}})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(v.Events) != 1 || v.Events[0].Type != "stomp" || v.Events[0].Player != "jumper" {
		t.Errorf("stomp events = %+v", v.Events)
	}
}
