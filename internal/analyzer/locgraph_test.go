package analyzer

import (
	"testing"
)

// Build a synthetic Result with a known bucket timeline and verify
// BuildLocGraph produces the expected nodes and edges, including
// teleport classification.
func TestBuildLocGraph_BasicTransitionsAndTeleport(t *testing.T) {
	const D = 0.05

	// Locs: "A" at (0,0), "B" at (100,0), "C" at (5000,0) — C is far enough
	// from A that a direct A→C transition in one bucket qualifies as a
	// teleport.
	locTable := []string{"", "A", "B", "C"}
	locationData := []MapLocation{
		{Name: "A", X: 0, Y: 0},
		{Name: "B", X: 100, Y: 0},
		{Name: "C", X: 5000, Y: 0},
	}

	p := func(x, y float32, li int, d, sp bool) *HighResPlayerData {
		return &HighResPlayerData{X: x, Y: y, H: 100, Li: li, D: d, Sp: sp}
	}

	// Player "p1" (team "red") walks A → A → B, then is teleported to C.
	// Player "p2" (team "blue") stays in A the entire time.
	buckets := []HighResBucket{
		{T: 0.00, P: map[string]*HighResPlayerData{
			"p1": p(1, 0, 1, false, false),
			"p2": p(10, 10, 1, false, false),
		}},
		{T: 0.05, P: map[string]*HighResPlayerData{
			"p1": p(50, 0, 1, false, false),
			"p2": p(10, 10, 1, false, false),
		}},
		{T: 0.10, P: map[string]*HighResPlayerData{
			"p1": p(100, 0, 2, false, false), // A → B normal
			"p2": p(10, 10, 1, false, false),
		}},
		{T: 0.15, P: map[string]*HighResPlayerData{
			"p1": p(5000, 0, 3, false, false), // B → C teleport
			"p2": p(10, 10, 1, false, false),
		}},
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

	// Node A: p1 spent 2 buckets there, p2 spent 4 buckets → 6*D total.
	if got := nodes["A"].Total; !approxEq(got, 6*D) {
		t.Errorf("A total = %v, want %v", got, 6*D)
	}
	if got := nodes["A"].ByPlayer["p1"]; !approxEq(got, 2*D) {
		t.Errorf("A byPlayer[p1] = %v, want %v", got, 2*D)
	}
	if got := nodes["A"].ByPlayer["p2"]; !approxEq(got, 4*D) {
		t.Errorf("A byPlayer[p2] = %v, want %v", got, 4*D)
	}
	if got := nodes["A"].ByTeam["red"]; !approxEq(got, 2*D) {
		t.Errorf("A byTeam[red] = %v, want %v", got, 2*D)
	}
	if got := nodes["A"].ByTeam["blue"]; !approxEq(got, 4*D) {
		t.Errorf("A byTeam[blue] = %v, want %v", got, 4*D)
	}

	// Node B and C each have exactly one bucket of p1 time.
	if got := nodes["B"].Total; !approxEq(got, D) {
		t.Errorf("B total = %v, want %v", got, D)
	}
	if got := nodes["C"].Total; !approxEq(got, D) {
		t.Errorf("C total = %v, want %v", got, D)
	}

	// Coordinates copied from LocationData.
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

func TestBuildLocGraph_DeathResetsCursor(t *testing.T) {
	const D = 0.05
	locTable := []string{"", "A", "B"}
	p := func(x, y float32, li int, d, sp bool) *HighResPlayerData {
		return &HighResPlayerData{X: x, Y: y, H: 100, Li: li, D: d, Sp: sp}
	}

	// p1 in A, dies, respawns in B. Should NOT produce an A→B edge because
	// the death/spawn buckets reset the cursor.
	buckets := []HighResBucket{
		{T: 0.00, P: map[string]*HighResPlayerData{"p1": p(0, 0, 1, false, false)}},
		{T: 0.05, P: map[string]*HighResPlayerData{"p1": p(0, 0, 1, true, false)}},
		{T: 0.10, P: map[string]*HighResPlayerData{"p1": p(100, 0, 2, false, true)}},
		{T: 0.15, P: map[string]*HighResPlayerData{"p1": p(100, 0, 2, false, false)}},
	}

	result := &Result{
		TimelineAnalysis: &TimelineAnalysisResult{
			HighResDuration: D,
			HighResBuckets:  buckets,
			LocTable:        locTable,
		},
	}
	graph := BuildLocGraph(result)
	if graph == nil {
		t.Fatal("expected graph, got nil")
	}
	if len(graph.Edges) != 0 {
		t.Errorf("expected no edges across death/spawn, got %+v", graph.Edges)
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
