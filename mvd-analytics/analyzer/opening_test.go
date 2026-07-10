package analyzer

import (
	"reflect"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func TestSynthesizeMatchStartSpawns(t *testing.T) {
	streams := &result.Streams{Players: []result.PlayerStream{
		{ // alive through the countdown: the missed match-start spawn.
			Name:   "alive",
			Health: []result.ChangeI16{{T: 0, V: 100}},
			Spawns: []int32{15000},
		},
		{ // dead when the countdown ended: the KTX respawn was wire-visible.
			Name:   "wire-visible",
			Health: []result.ChangeI16{{T: 0, V: 100}},
			Spawns: []int32{40},
		},
		{ // alive at start, died early, respawned inside the dedup window:
			// that near-0 spawn follows a death, so it must not suppress
			// the synthesis.
			Name:   "early-death",
			Health: []result.ChangeI16{{T: 0, V: 100}},
			Deaths: []int32{300},
			Spawns: []int32{800},
		},
		{ // dead at match start (carry V<=0): their spawn will be real.
			Name:   "dead-at-start",
			Health: []result.ChangeI16{{T: 0, V: 0}},
			Spawns: []int32{2000},
		},
		{ // joined mid-match: no carry entry at t=0, nothing to synthesize.
			Name:   "late-joiner",
			Health: []result.ChangeI16{{T: 90000, V: 100}},
			Spawns: []int32{90000},
		},
	}}
	synthesizeMatchStartSpawns(streams)

	want := map[string][]int32{
		"alive":         {0, 15000},
		"wire-visible":  {40},
		"early-death":   {0, 800},
		"dead-at-start": {2000},
		"late-joiner":   {90000},
	}
	for _, p := range streams.Players {
		if !reflect.DeepEqual(p.Spawns, want[p.Name]) {
			t.Errorf("%s spawns = %v, want %v", p.Name, p.Spawns, want[p.Name])
		}
	}
}

func TestOpeningPost(t *testing.T) {
	res := &Result{
		TimelineAnalysis: &result.TimelineAnalysisResult{
			LocTable: []string{"", "mid", "countdown-spot", "spawn-a"},
		},
		Streams: &result.Streams{Players: []result.PlayerStream{
			{
				Name: "p1", Team: "red",
				Health: []result.ChangeI16{{T: 0, V: 100}},
				Loc: []result.ChangeI16{
					{T: 0, V: 2},  // countdown carry
					{T: 60, V: 3}, // respawn teleport landing
				},
			},
			{
				Name: "p2", Team: "blue",
				Health: []result.ChangeI16{{T: 0, V: 100}},
				Loc:    []result.ChangeI16{{T: 0, V: 1}}, // loc unchanged across respawn
			},
			{ // not present at match start: excluded.
				Name: "late", Team: "blue",
				Health: []result.ChangeI16{{T: 30000, V: 100}},
			},
		}},
		Items: &result.ItemsResult{Items: []result.ItemTimeline{
			{
				Name: "ya_1", Kind: "ya", EntNum: 42, Loc: "tower",
				Phases: []result.ItemPhase{
					{AvailableFrom: -3000, TakenAt: -2000, TakenBy: "p1"}, // warmup take: skipped
					{AvailableFrom: 0, TakenAt: 4000, TakenBy: "p2", Team: "blue"},
					{AvailableFrom: 24000, TakenAt: 31000, TakenBy: "p1", Team: "red"},
				},
			},
			{
				Name: "rl_1", Kind: "rl", EntNum: 43, Loc: "cathedral",
				Phases: []result.ItemPhase{
					{AvailableFrom: 0, TakenAt: 6000, TakenBy: "p1", Team: "red"},
				},
			},
			{ // tracked kinds only: small health is not an opening objective.
				Name: "h25_1", Kind: "h25", EntNum: 44,
				Phases: []result.ItemPhase{
					{AvailableFrom: 0, TakenAt: 1000, TakenBy: "p2"},
				},
			},
			{ // never taken: no entry.
				Name:   "quad_1",
				Kind:   "quad",
				EntNum: 45,
				Phases: []result.ItemPhase{{AvailableFrom: 0}},
			},
		}},
	}
	co := &CoreOutputs{Clock: &Clock{MatchStartMs: 10000}}
	openingPost(res, co)

	if res.Opening == nil {
		t.Fatal("Opening not set")
	}
	wantPlayers := []result.OpeningPlayer{
		{Name: "p2", Team: "blue", Loc: "mid"},
		{Name: "p1", Team: "red", Loc: "spawn-a"},
	}
	if !reflect.DeepEqual(res.Opening.Players, wantPlayers) {
		t.Errorf("players = %+v, want %+v", res.Opening.Players, wantPlayers)
	}
	wantTakes := []result.OpeningTake{
		{Item: "ya_1", Kind: "ya", EntNum: 42, Loc: "tower", Time: 4000, TakenBy: "p2", Team: "blue"},
		{Item: "rl_1", Kind: "rl", EntNum: 43, Loc: "cathedral", Time: 6000, TakenBy: "p1", Team: "red"},
	}
	if !reflect.DeepEqual(res.Opening.FirstTakes, wantTakes) {
		t.Errorf("firstTakes = %+v, want %+v", res.Opening.FirstTakes, wantTakes)
	}
}

func TestOpeningPostNoMatchStart(t *testing.T) {
	res := &Result{Streams: &result.Streams{Players: []result.PlayerStream{
		{Name: "p1", Health: []result.ChangeI16{{T: 0, V: 100}}},
	}}}
	openingPost(res, &CoreOutputs{}) // no clock ⇒ no detected match start
	if res.Opening != nil {
		t.Fatalf("Opening = %+v, want nil on a no-match-start demo", res.Opening)
	}
}
