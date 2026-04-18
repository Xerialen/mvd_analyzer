package analyzer

import (
	"testing"
)

// Small helper to build HighResPlayerData tersely.
func pd(x, y float32, li int, d, sp bool) *HighResPlayerData {
	return &HighResPlayerData{X: x, Y: y, H: 100, Li: li, D: d, Sp: sp}
}

// Build a synthetic Result with a pre-filtered bucket timeline and
// verify BuildLocGraph produces the expected nodes and edges,
// including teleport classification. The input here is what
// applyBlipFilter would hand us: one edge per filtered-loc change.
func TestBuildLocGraph_BasicTransitionsAndTeleport(t *testing.T) {
	const D = 0.05

	// Locs: "A" at (0,0), "B" at (100,0), "C" at (5000,0) — C is far enough
	// from B that a direct B→C transition in one bucket qualifies as a
	// teleport.
	locTable := []string{"", "A", "B", "C"}
	locationData := []MapLocation{
		{Name: "A", X: 0, Y: 0},
		{Name: "B", X: 100, Y: 0},
		{Name: "C", X: 5000, Y: 0},
	}

	// p1 (red): walks A,A → B,B → C,C (teleport). p2 (blue) stays in A the
	// whole time.
	buckets := []HighResBucket{
		{T: 0.00, P: map[string]*HighResPlayerData{"p1": pd(1, 0, 1, false, false), "p2": pd(10, 10, 1, false, false)}},
		{T: 0.05, P: map[string]*HighResPlayerData{"p1": pd(50, 0, 1, false, false), "p2": pd(10, 10, 1, false, false)}},
		{T: 0.10, P: map[string]*HighResPlayerData{"p1": pd(98, 0, 2, false, false), "p2": pd(10, 10, 1, false, false)}},  // first B
		{T: 0.15, P: map[string]*HighResPlayerData{"p1": pd(100, 0, 2, false, false), "p2": pd(10, 10, 1, false, false)}}, // second B → commit A→B
		{T: 0.20, P: map[string]*HighResPlayerData{"p1": pd(5000, 0, 3, false, false), "p2": pd(10, 10, 1, false, false)}}, // first C (teleport jump)
		{T: 0.25, P: map[string]*HighResPlayerData{"p1": pd(5002, 0, 3, false, false), "p2": pd(10, 10, 1, false, false)}}, // second C → commit B→C (teleport)
	}

	result := &Result{
		TimelineAnalysis: &TimelineAnalysisResult{
			HighResDuration: D,
			HighResBuckets:  buckets,
			LocTable:        locTable,
			LocationData:    locationData,
		},
		DemoInfo: &DemoInfoResult{
			Players: []DemoInfoPlayer{
				{Name: "p1", Team: "red"},
				{Name: "p2", Team: "blue"},
			},
		},
	}

	graph := BuildLocGraph(result)
	if graph == nil {
		t.Fatal("expected graph, got nil")
	}

	nodes := map[string]LocNode{}
	for _, n := range graph.Locs {
		nodes[n.Name] = n
	}

	// Node-time uses raw per-bucket loc — p1: 2 in A, 2 in B, 2 in C;
	// p2: 6 in A. So A=8D, B=2D, C=2D.
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

// Classic death path: the p.D / p.Sp markers are set on the death and
// respawn buckets, so the cursor resets and no A→B edge bridges the death.
func TestBuildLocGraph_DeathResetsCursor(t *testing.T) {
	const D = 0.05
	locTable := []string{"", "A", "B"}

	// Two buckets in A before death, dead frame, spawn frame, two buckets
	// in B post-spawn. Without the reset, the 2 buckets in B would commit
	// an A→B transition.
	buckets := []HighResBucket{
		{T: 0.00, P: map[string]*HighResPlayerData{"p1": pd(1, 0, 1, false, false)}},
		{T: 0.05, P: map[string]*HighResPlayerData{"p1": pd(1, 0, 1, false, false)}},
		{T: 0.10, P: map[string]*HighResPlayerData{"p1": pd(1, 0, 1, true, false)}},  // death
		{T: 0.15, P: map[string]*HighResPlayerData{"p1": pd(100, 0, 2, false, true)}}, // spawn in B
		{T: 0.20, P: map[string]*HighResPlayerData{"p1": pd(100, 0, 2, false, false)}},
		{T: 0.25, P: map[string]*HighResPlayerData{"p1": pd(100, 0, 2, false, false)}},
	}

	result := &Result{TimelineAnalysis: &TimelineAnalysisResult{
		HighResDuration: D, HighResBuckets: buckets, LocTable: locTable,
	}}
	graph := BuildLocGraph(result)
	if graph == nil {
		t.Fatal("expected graph, got nil")
	}
	if len(graph.Edges) != 0 {
		t.Errorf("expected no edges across death/spawn, got %+v", graph.Edges)
	}
}

// Simulates the instant-gib-respawn scenario where both p.D and p.Sp might
// be missed because death and respawn happen within the same sampling
// window. The authoritative FragResult still records the death, and
// BuildLocGraph must use it to reset the cursor so no A→B edge is created.
func TestBuildLocGraph_FragEventResetsCursor(t *testing.T) {
	const D = 0.05
	locTable := []string{"", "A", "B"}

	// 3 buckets in A (cursor commits A), then 3 buckets in B with NO D or
	// Sp flag set anywhere — simulating a missed intra-bucket gib+respawn.
	// Without the frag-based reset, this would commit an A→B edge after
	// the 2nd B bucket.
	buckets := []HighResBucket{
		{T: 0.00, P: map[string]*HighResPlayerData{"p1": pd(1, 0, 1, false, false)}},
		{T: 0.05, P: map[string]*HighResPlayerData{"p1": pd(1, 0, 1, false, false)}},
		{T: 0.10, P: map[string]*HighResPlayerData{"p1": pd(1, 0, 1, false, false)}},
		// Death happens at t=0.12 per FragResult; no marker in the stream.
		{T: 0.15, P: map[string]*HighResPlayerData{"p1": pd(100, 0, 2, false, false)}},
		{T: 0.20, P: map[string]*HighResPlayerData{"p1": pd(100, 0, 2, false, false)}},
		{T: 0.25, P: map[string]*HighResPlayerData{"p1": pd(100, 0, 2, false, false)}},
	}

	result := &Result{
		TimelineAnalysis: &TimelineAnalysisResult{
			HighResDuration: D, HighResBuckets: buckets, LocTable: locTable,
		},
		Frags: &FragResult{
			Frags: []FragEntry{
				{Time: 0.12, Killer: "other", Victim: "p1", Weapon: "rl"},
			},
		},
	}
	graph := BuildLocGraph(result)
	if graph == nil {
		t.Fatal("expected graph, got nil")
	}
	for _, e := range graph.Edges {
		if e.From == "A" && e.To == "B" {
			t.Errorf("FragResult should have reset cursor across death; unexpected edge %+v", e)
		}
	}
}

// Without a blip filter upstream, BuildLocGraph emits an edge on every
// filtered-loc change — that's exactly its job. Smoothing is a separate
// concern tested in timeline_blipfilter_test.go. This test pins the
// pass-through behavior: if the buckets already have sustained locs, a
// single A→C edge is emitted and no A↔B jitter sneaks in.
func TestBuildLocGraph_EmitsOnFilteredChange(t *testing.T) {
	const D = 0.05
	locTable := []string{"", "A", "C"}

	// Pre-filtered buckets: 3 in A, 3 in C. One A→C edge expected.
	buckets := []HighResBucket{
		{T: 0.00, P: map[string]*HighResPlayerData{"p1": pd(50, 0, 1, false, false)}},
		{T: 0.05, P: map[string]*HighResPlayerData{"p1": pd(49, 0, 1, false, false)}},
		{T: 0.10, P: map[string]*HighResPlayerData{"p1": pd(51, 0, 1, false, false)}},
		{T: 0.15, P: map[string]*HighResPlayerData{"p1": pd(200, 0, 2, false, false)}},
		{T: 0.20, P: map[string]*HighResPlayerData{"p1": pd(202, 0, 2, false, false)}},
		{T: 0.25, P: map[string]*HighResPlayerData{"p1": pd(204, 0, 2, false, false)}},
	}

	result := &Result{TimelineAnalysis: &TimelineAnalysisResult{
		HighResDuration: D, HighResBuckets: buckets, LocTable: locTable,
	}}
	graph := BuildLocGraph(result)
	if graph == nil {
		t.Fatal("expected graph, got nil")
	}

	seen := map[string]LocEdge{}
	for _, e := range graph.Edges {
		seen[e.From+"→"+e.To] = e
	}
	if e, ok := seen["A→C"]; !ok || e.Total != 1 {
		t.Errorf("expected A→C=1, got %+v (all edges: %+v)", e, graph.Edges)
	}
	if len(graph.Edges) != 1 {
		t.Errorf("expected exactly 1 edge, got %d: %+v", len(graph.Edges), graph.Edges)
	}
}

func approxEq(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}
