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

func TestEventsPickupDefault(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{
			Global: result.GlobalStream{MatchStart: 0, MatchEnd: 60000},
		},
		Items: &result.ItemsResult{Items: []result.ItemTimeline{
			{
				Name: "ya_1", Kind: "ya", EntNum: 42, Loc: "tower",
				Phases: []result.ItemPhase{
					{AvailableFrom: 0, TakenAt: 5000, TakenBy: "p1", Team: "red", RespawnAt: 25000},
					{AvailableFrom: 25000},
				},
			},
			{
				Name: "rl_1", Kind: "rl", EntNum: 43, Loc: "cathedral",
				Phases: []result.ItemPhase{
					{AvailableFrom: 0, TakenAt: 7000, TakenBy: "p3", Team: "blue"},
				},
			},
		}},
		WeaponPickups: []result.WeaponPickup{
			// World pickup: must NOT be double-reported (rl_1's phase above
			// already covers the take).
			{Time: 7000, Player: "p3", Team: "blue", Weapon: "rl", Source: "world"},
			{Time: 9000, Player: "p2", Team: "red", Weapon: "rl", Source: "backpack",
				BackpackEnt: 77, Dropper: "p3", DropTime: 8500},
		},
	}
	v, err := Events(r, EventsFilter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var pickups []TaggedEvent
	for _, e := range v.Events {
		if e.Type == "pickup" {
			pickups = append(pickups, e)
		}
	}
	if len(pickups) != 3 {
		t.Fatalf("len(pickups) = %d, want 3 (ya world, rl world, rl backpack): %+v", len(pickups), pickups)
	}
	ya := pickups[0]
	if ya.T != 5.0 || ya.Player != "p1" {
		t.Fatalf("ya pickup = %+v", ya)
	}
	for k, want := range map[string]any{
		"item": "ya_1", "kind": "ya", "entNum": 42, "loc": "tower",
		"source": "world", "team": "red",
	} {
		if got := ya.Detail[k]; got != want {
			t.Errorf("ya detail[%s] = %v, want %v", k, got, want)
		}
	}
	// Exactly one pickup at t=7 (the item-timeline row; the WeaponPickups
	// world row is suppressed).
	if pickups[1].T != 7.0 || pickups[1].Detail["item"] != "rl_1" || pickups[1].Detail["source"] != "world" {
		t.Fatalf("rl world pickup = %+v", pickups[1])
	}
	bp := pickups[2]
	if bp.T != 9.0 || bp.Player != "p2" {
		t.Fatalf("backpack pickup = %+v", bp)
	}
	for k, want := range map[string]any{
		"item": "rl", "source": "backpack", "entNum": 77, "dropper": "p3",
	} {
		if got := bp.Detail[k]; got != want {
			t.Errorf("backpack detail[%s] = %v, want %v", k, got, want)
		}
	}
}

func TestEventsPickupPlayerAndWindowFilter(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{Global: result.GlobalStream{MatchEnd: 60000}},
		Items: &result.ItemsResult{Items: []result.ItemTimeline{
			{
				Name: "ra_1", Kind: "ra", EntNum: 9,
				Phases: []result.ItemPhase{
					{AvailableFrom: 0, TakenAt: 5000, TakenBy: "p1"},
					{AvailableFrom: 25000, TakenAt: 30000, TakenBy: "p2"},
				},
			},
		}},
	}
	v, err := Events(r, EventsFilter{Types: []string{"pickup"}, Players: []string{"p2"}})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(v.Events) != 1 || v.Events[0].Player != "p2" {
		t.Fatalf("player-filtered pickups = %+v, want p2's only", v.Events)
	}
	v, err = Events(r, EventsFilter{Types: []string{"pickup"}, EndTime: 10})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(v.Events) != 1 || v.Events[0].Player != "p1" {
		t.Fatalf("windowed pickups = %+v, want the t=5 take only", v.Events)
	}
}

func TestEventsSpawnLoc(t *testing.T) {
	r := &result.Result{
		TimelineAnalysis: &result.TimelineAnalysisResult{
			LocTable: []string{"", "mid", "countdown-spot", "spawn-a", "spawn-b"},
		},
		Streams: &result.Streams{
			Global: result.GlobalStream{MatchEnd: 60000},
			Players: []result.PlayerStream{
				{
					Name:   "p1",
					Spawns: []int32{0, 5000},
					Loc: []result.ChangeI16{
						{T: 0, V: 2},    // carry-forward: countdown-end loc
						{T: 60, V: 3},   // match-start respawn teleport landing
						{T: 5080, V: 4}, // second spawn's teleport landing
					},
				},
			},
		},
	}
	v, err := Events(r, EventsFilter{Types: []string{"spawn"}})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(v.Events) != 2 {
		t.Fatalf("len(spawns) = %d, want 2", len(v.Events))
	}
	// t=0 spawn resolves to the post-teleport entry, not the countdown carry.
	if got := v.Events[0].Detail["loc"]; got != "spawn-a" {
		t.Fatalf("spawn[0] loc = %v, want spawn-a", got)
	}
	if got := v.Events[1].Detail["loc"]; got != "spawn-b" {
		t.Fatalf("spawn[1] loc = %v, want spawn-b", got)
	}
	// LocIndex mode emits the raw index under li.
	v, err = Events(r, EventsFilter{Types: []string{"spawn"}, LocIndex: true})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if got := v.Events[0].Detail["li"]; got != 3 {
		t.Fatalf("spawn[0] li = %v, want 3", got)
	}
}
