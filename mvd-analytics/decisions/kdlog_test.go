package decisions

import (
	"strings"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func fixtureResult() *result.Result {
	return &result.Result{
		TimelineAnalysis: &result.TimelineAnalysisResult{
			PlayerSlots: map[string]int{"cand-1": 1, "ctrl-7": 7},
			LocTable:    []string{"", "RA", "YA"},
		},
		Streams: &result.Streams{Players: []result.PlayerStream{
			{Name: "cand-1", Team: "red", Position: &result.PositionTrack{
				T: []int32{0, 1000, 2000}, X: []float32{1, 2, 3}, Y: []float32{0, 0, 0},
				Z: []float32{0, 0, 0}, Li: []int16{1, 1, 2}}},
			{Name: "ctrl-7", Team: "blue"},
		}},
		Items: &result.ItemsResult{Items: []result.ItemTimeline{
			{Name: "ya_1", Kind: "ya", EntNum: 79, X: 1232, Y: -904, Z: -48, Loc: "YA"},
			{Name: "pent", Kind: "pent", EntNum: 143, X: 1008, Y: 800, Z: -296, Loc: "RL"},
		}},
	}
}

const fixtureLog = `[2026-07-05 19:09:59] KDLOG_ANCHOR v=1 emitter=kbot-0.23.0-dlog map=dm3 level_time=15.551 match_start=15.527 dlog=1
[2026-07-05 19:09:59] KDLOG t=0.200 ed=2 type=goal trig=item_taken m=45 h=100 a=50 it=12303 aw=32 sh=75 nl=0 rk=5 cl=0 pos=1520.1,448.5,-88.0 chosen=cls=item_artifact_invulnerability;ied=143;org=1008.0,800.0,-296.0;m=64;des=300.00;tt=8.42;sc=482.358 c1=cls=item_artifact_invulnerability;ied=143;org=1008.0,800.0,-296.0;m=64;des=300.00;tt=8.42;sc=482.358 c2=cls=item_armor2;ied=79;org=1232.0,-904.0,-48.0;m=10;des=300.00;tt=8.97;sc=451.589
garbage line that should be ignored
[2026-07-05 19:10:01] KDLOG t=2.455 ed=2 type=enemy ted=8 dist=450 m=45 h=90 a=0 it=4353 aw=1 sh=23 nl=0 rk=5 cl=0 pos=222.9,-208.9,75.3
[2026-07-05 19:10:02] KDLOG t=3.100 ed=2 type=evade on=1 h=90 a=0 it=4353 aw=1 sh=23 nl=0 rk=5 cl=0 pos=1.0,2.0,3.0
[2026-07-05 19:10:03] KDLOG t=4.000 ed=2 type=play play=gapjump lane=ra2ya phase=land h=90 a=0 it=4353 aw=1 sh=23 nl=0 rk=5 cl=0 pos=1.0,2.0,3.0
KDLOG t=broken ed=2 type=goal
`

func TestResolveKDLog(t *testing.T) {
	res := fixtureResult()
	dec, err := ResolveKDLog(res, strings.NewReader(fixtureLog))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Source != "kdlog" || dec.EmitterVersion != "kbot-0.23.0-dlog" || dec.DlogLevel != 1 {
		t.Fatalf("anchor not parsed: %+v", dec)
	}
	if len(dec.Records) != 4 {
		t.Fatalf("want 4 records, got %d (errors: %v)", len(dec.Records), dec.Errors)
	}
	if len(dec.Errors) != 1 || !strings.Contains(dec.Errors[0], "bad t=") {
		t.Fatalf("want 1 bad-t error, got %v", dec.Errors)
	}

	g := dec.Records[0]
	if g.Type != "goal" || g.Player != "cand-1" || g.Team != "red" || g.T != 200 {
		t.Fatalf("goal identity wrong: %+v", g)
	}
	if g.Trigger != "item_taken" {
		t.Fatalf("trigger wrong: %q", g.Trigger)
	}
	if g.Chosen == nil || g.Chosen.Kind != "pent" || g.Chosen.Name != "pent" || g.Chosen.Loc != "RL" {
		t.Fatalf("chosen not resolved via entNum join: %+v", g.Chosen)
	}
	if g.Chosen.TravelMs != 8420 || g.Chosen.Marker != 64 {
		t.Fatalf("chosen scores wrong: %+v", g.Chosen)
	}
	if len(g.Candidates) != 2 || g.Candidates[1].Kind != "ya" || g.Candidates[1].Name != "ya_1" {
		t.Fatalf("candidates wrong: %+v", g.Candidates)
	}
	// it=12303 = 0x300F: ssg+ng+sng+sg + shells + ra(1<<15 would be 32768; 12303&(1<<13,14)) — decode checks:
	if g.State == nil || g.State.H != 100 || g.State.A != 50 {
		t.Fatalf("state wrong: %+v", g.State)
	}
	if g.State.AW != "rl" { // aw=32 = ITRocketLauncher (1<<5)
		t.Fatalf("aw wrong: %q", g.State.AW)
	}
	// Loc from the player's own position stream at t=200ms -> li=1 -> "RA".
	if g.Loc != "RA" {
		t.Fatalf("decider loc wrong: %q", g.Loc)
	}

	e := dec.Records[1]
	if e.Type != "enemy" || e.Target != "ctrl-7" || e.Dist != 450 {
		t.Fatalf("enemy record wrong: %+v", e)
	}

	v := dec.Records[2]
	if v.Type != "evade" || v.On == nil || !*v.On {
		t.Fatalf("evade record wrong: %+v", v)
	}
	// t=3100ms -> last sample at 2000ms -> li=2 -> "YA".
	if v.Loc != "YA" {
		t.Fatalf("evade loc wrong: %q", v.Loc)
	}

	p := dec.Records[3]
	if p.Type != "play" || p.Play != "gapjump" || p.Lane != "ra2ya" || p.Phase != "land" {
		t.Fatalf("play record wrong: %+v", p)
	}
}
