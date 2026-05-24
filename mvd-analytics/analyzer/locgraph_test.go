package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// makePlayerStream builds a synthetic PlayerStream from a per-bucket
// (time, x, y, locIndex, dead, spawn) sequence. Used by the locgraph
// tests below — BuildLocGraph internally derives 50 ms buckets from
// streams via view.Buckets, so the test scaffolding has to provide
// streams shaped to produce those bucket frames.
type bucketSpec struct {
	t  float64
	x  int32
	y  int32
	li int16
	d  bool
	sp bool
}

func makePlayerStreamFromBuckets(name, team string, specs []bucketSpec) result.PlayerStream {
	ps := result.PlayerStream{Name: name, Team: team}
	pt := &result.PositionTrack{}
	healthCur := int16(0)
	for _, s := range specs {
		// Position: append every sample (no dedup). pt.T is int32 ms
		// in schema v8.
		tMs := int32(s.t * 1000)
		pt.T = append(pt.T, tMs)
		pt.X = append(pt.X, s.x)
		pt.Y = append(pt.Y, s.y)
		pt.Z = append(pt.Z, 0)
		pt.Li = append(pt.Li, s.li)

		// Loc: dedup against last value. ChangeI16.T is int32 ms in
		// schema v8.
		if len(ps.Loc) == 0 || ps.Loc[len(ps.Loc)-1].V != s.li {
			ps.Loc = append(ps.Loc, result.ChangeI16{T: tMs, V: s.li})
		}

		// Health: 100 by default; dead frames go to 0; spawn back to 100.
		want := int16(100)
		if s.d {
			want = 0
		}
		if want != healthCur {
			ps.Health = append(ps.Health, result.ChangeI16{T: tMs, V: want})
			healthCur = want
		}

		// Spawn / death timestamps are int32 ms in schema v8.
		if s.sp {
			ps.Spawns = append(ps.Spawns, tMs)
		}
		if s.d {
			ps.Deaths = append(ps.Deaths, tMs)
		}
	}
	if len(pt.T) > 0 {
		ps.Position = pt
	}
	return ps
}

// Build a synthetic Result with two players and verify BuildLocGraph
// produces the expected nodes and edges, including teleport
// classification.
func TestBuildLocGraph_BasicTransitionsAndTeleport(t *testing.T) {
	locTable := []string{"", "A", "B", "C"}
	locationData := []MapLocation{
		{Name: "A", X: 0, Y: 0},
		{Name: "B", X: 100, Y: 0},
		{Name: "C", X: 5000, Y: 0},
	}

	// p1 (red): in A from t=0 to t=0.05, B from t=0.10 to t=0.15, C
	// (teleport) from t=0.20 to t=0.25. p2 (blue) stays in A.
	p1Specs := []bucketSpec{
		{t: 0.00, x: 1, y: 0, li: 1},
		{t: 0.05, x: 50, y: 0, li: 1},
		{t: 0.10, x: 98, y: 0, li: 2},
		{t: 0.15, x: 100, y: 0, li: 2},
		{t: 0.20, x: 5000, y: 0, li: 3},
		{t: 0.25, x: 5002, y: 0, li: 3},
	}
	p2Specs := []bucketSpec{
		{t: 0.00, x: 10, y: 10, li: 1},
		{t: 0.05, x: 10, y: 10, li: 1},
		{t: 0.10, x: 10, y: 10, li: 1},
		{t: 0.15, x: 10, y: 10, li: 1},
		{t: 0.20, x: 10, y: 10, li: 1},
		{t: 0.25, x: 10, y: 10, li: 1},
	}

	res := &Result{
		Streams: &result.Streams{
			Global: result.GlobalStream{MatchStart: 0, MatchEnd: 300},
			Players: []result.PlayerStream{
				makePlayerStreamFromBuckets("p1", "red", p1Specs),
				makePlayerStreamFromBuckets("p2", "blue", p2Specs),
			},
		},
		TimelineAnalysis: &TimelineAnalysisResult{
			LocTable:     locTable,
			LocationData: locationData,
		},
		DemoInfo: &DemoInfoResult{
			Players: []DemoInfoPlayer{
				{Name: "p1", Team: "red"},
				{Name: "p2", Team: "blue"},
			},
		},
	}

	graph := BuildLocGraph(res)
	if graph == nil {
		t.Fatal("expected graph, got nil")
	}

	nodes := map[string]LocNode{}
	for _, n := range graph.Locs {
		nodes[n.Name] = n
	}

	// p1 visits A 2 buckets, B 2 buckets, C 2 buckets; p2 visits A 6
	// buckets. So node-time A=8, B=2, C=2 in 50 ms units.
	const D = 0.05
	if got := nodes["A"].Total; !approxEq(got, 8*D) {
		t.Errorf("A total = %v, want %v", got, 8*D)
	}
	if got := nodes["B"].Total; !approxEq(got, 2*D) {
		t.Errorf("B total = %v, want %v", got, 2*D)
	}
	if got := nodes["C"].Total; !approxEq(got, 2*D) {
		t.Errorf("C total = %v, want %v", got, 2*D)
	}
	if got := nodes["A"].ByTeam["blue"]; !approxEq(got, 6*D) {
		t.Errorf("A byTeam[blue] = %v, want %v", got, 6*D)
	}

	if nodes["C"].X != 5000 {
		t.Errorf("C.X = %v, want 5000", nodes["C"].X)
	}

	edges := map[string]LocEdge{}
	for _, e := range graph.Edges {
		edges[e.From+"→"+e.To] = e
	}

	ab := edges["A→B"]
	if ab.Total != 1 || ab.Kind != "normal" {
		t.Errorf("A→B = %+v, want total=1 kind=normal", ab)
	}
	if ab.ByPlayer["p1"] != 1 || ab.ByTeam["red"] != 1 {
		t.Errorf("A→B breakdown = %+v", ab)
	}
	bc := edges["B→C"]
	if bc.Total != 1 || bc.Kind != "teleport" {
		t.Errorf("B→C = %+v, want total=1 kind=teleport", bc)
	}
	if _, exists := edges["A→C"]; exists {
		t.Errorf("unexpected A→C edge: %+v", edges["A→C"])
	}
}

// Verify the Armed (RL/LG) and Quad conditioned weights only count the
// samples whose timestamp falls inside the player's presence intervals,
// and never exceed the unconditioned node total.
func TestBuildLocGraph_ArmedAndQuadConditioning(t *testing.T) {
	locTable := []string{"", "A", "B"}
	locationData := []MapLocation{{Name: "A", X: 0, Y: 0}, {Name: "B", X: 100, Y: 0}}

	// p1: loc A at ms 0,50; loc B at ms 100,150. Every gap is 50 ms so
	// each sample contributes dt = 0.05 s.
	specs := []bucketSpec{
		{t: 0.00, x: 0, y: 0, li: 1},
		{t: 0.05, x: 0, y: 0, li: 1},
		{t: 0.10, x: 100, y: 0, li: 2},
		{t: 0.15, x: 100, y: 0, li: 2},
	}
	p1 := makePlayerStreamFromBuckets("p1", "red", specs)
	// Armed for [0,101) ms — covers samples at 0, 50, 100 but not 150.
	p1.RL = []result.Interval{{Start: 0, End: 101}}
	// Quad for [140,1000) ms — covers only the sample at 150.
	p1.Quad = []result.Interval{{Start: 140, End: 1000}}
	// Pent for [0,40) ms — covers only the first A sample (ms 0).
	p1.Pent = []result.Interval{{Start: 0, End: 40}}

	res := &Result{
		Streams: &result.Streams{
			Global:  result.GlobalStream{MatchStart: 0, MatchEnd: 300},
			Players: []result.PlayerStream{p1},
		},
		TimelineAnalysis: &TimelineAnalysisResult{LocTable: locTable, LocationData: locationData},
		DemoInfo:         &DemoInfoResult{Players: []DemoInfoPlayer{{Name: "p1", Team: "red"}}},
	}

	graph := BuildLocGraph(res)
	if graph == nil {
		t.Fatal("expected graph, got nil")
	}
	nodes := map[string]LocNode{}
	for _, n := range graph.Locs {
		nodes[n.Name] = n
	}
	const D = 0.05
	A, B := nodes["A"], nodes["B"]

	if !approxEq(A.Total, 2*D) || !approxEq(B.Total, 2*D) {
		t.Fatalf("totals A=%v B=%v want %v each", A.Total, B.Total, 2*D)
	}

	// Armed: both A samples, but only the first B sample (ms 100).
	if A.Armed == nil || !approxEq(A.Armed.Total, 2*D) {
		t.Errorf("A.Armed = %+v, want total %v", A.Armed, 2*D)
	} else {
		if !approxEq(A.Armed.ByPlayer["p1"], 2*D) {
			t.Errorf("A.Armed.ByPlayer[p1] = %v, want %v", A.Armed.ByPlayer["p1"], 2*D)
		}
		if !approxEq(A.Armed.ByTeam["red"], 2*D) {
			t.Errorf("A.Armed.ByTeam[red] = %v, want %v", A.Armed.ByTeam["red"], 2*D)
		}
	}
	if B.Armed == nil || !approxEq(B.Armed.Total, 1*D) {
		t.Errorf("B.Armed = %+v, want total %v", B.Armed, 1*D)
	}

	// Quad: only the B sample at ms 150 — A never had quad, so it stays nil.
	if A.Quad != nil {
		t.Errorf("A.Quad = %+v, want nil", A.Quad)
	}
	if B.Quad == nil || !approxEq(B.Quad.Total, 1*D) {
		t.Errorf("B.Quad = %+v, want total %v", B.Quad, 1*D)
	}

	// Pent: only the first A sample (ms 0) — B never had pent.
	if A.Pent == nil || !approxEq(A.Pent.Total, 1*D) {
		t.Errorf("A.Pent = %+v, want total %v", A.Pent, 1*D)
	}
	if B.Pent != nil {
		t.Errorf("B.Pent = %+v, want nil", B.Pent)
	}

	// Unarmed is the complement of Armed: only the ms-150 B sample lacked
	// RL/LG, so A (both samples armed) has no Unarmed and B has one sample.
	if A.Unarmed != nil {
		t.Errorf("A.Unarmed = %+v, want nil", A.Unarmed)
	}
	if B.Unarmed == nil || !approxEq(B.Unarmed.Total, 1*D) {
		t.Errorf("B.Unarmed = %+v, want total %v", B.Unarmed, 1*D)
	}
	// Armed + Unarmed time must reconstitute each loc's total.
	armedB := 0.0
	if B.Armed != nil {
		armedB = B.Armed.Total
	}
	if !approxEq(armedB+B.Unarmed.Total, B.Total) {
		t.Errorf("B armed+unarmed = %v, want total %v", armedB+B.Unarmed.Total, B.Total)
	}

	// Conditioned metrics can never exceed the unconditioned total.
	if B.Armed != nil && B.Armed.Total > B.Total+1e-9 {
		t.Errorf("B.Armed.Total %v > B.Total %v", B.Armed.Total, B.Total)
	}

	// The A→B transition fires at the destination sample (ms 100), which is
	// armed but not quad — so the edge carries an Armed weight, no Quad.
	edges := map[string]LocEdge{}
	for _, e := range graph.Edges {
		edges[e.From+"→"+e.To] = e
	}
	ab := edges["A→B"]
	if ab.Total != 1 {
		t.Fatalf("A→B total = %d, want 1", ab.Total)
	}
	if ab.Armed == nil || ab.Armed.Total != 1 || ab.Armed.ByPlayer["p1"] != 1 || ab.Armed.ByTeam["red"] != 1 {
		t.Errorf("A→B.Armed = %+v, want total/p1/red = 1", ab.Armed)
	}
	if ab.Quad != nil {
		t.Errorf("A→B.Quad = %+v, want nil", ab.Quad)
	}
}

func approxEq(a, b float64) bool {
	// Node-time is float64 accumulated from int32-ms-derived dts;
	// a small fp tolerance is still sensible since the *.05 dts
	// individually aren't representable exactly, even if there's no
	// per-sample roundtrip.
	const eps = 1e-9
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}
