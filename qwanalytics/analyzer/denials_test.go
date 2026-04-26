package analyzer

import (
	"testing"
)

// makeDenialsResult assembles a synthetic Result for the denials
// post-processor. Edges are passed as 4-tuples (from, to, total) and
// flipped automatically in either direction the caller specifies.
func makeDenialsResult(
	t *testing.T,
	locTable []string,
	edges [][3]any,
	bucketT float64,
	bucketPlayers map[string]*HighResPlayerData,
	demoPlayers []DemoInfoPlayer,
	itemKind, itemLoc string,
	taker string,
	takerTeam string,
	takenAt float64,
) *Result {
	t.Helper()
	graphLocs := make([]LocNode, 0, len(locTable))
	for _, n := range locTable {
		if n == "" {
			continue
		}
		graphLocs = append(graphLocs, LocNode{Name: n})
	}
	graphEdges := make([]LocEdge, 0, len(edges))
	for _, e := range edges {
		graphEdges = append(graphEdges, LocEdge{
			From:  e[0].(string),
			To:    e[1].(string),
			Kind:  "normal",
			Total: e[2].(int),
		})
	}
	return &Result{
		TimelineAnalysis: &TimelineAnalysisResult{
			HighResDuration: 0.05,
			HighResBuckets: []HighResBucket{
				{T: bucketT, P: bucketPlayers},
			},
			LocTable: locTable,
			PlayerUserIDs: map[string]int{
				taker: 42,
			},
		},
		LocGraph: &LocGraphResult{Locs: graphLocs, Edges: graphEdges},
		DemoInfo: &DemoInfoResult{Players: demoPlayers},
		Items: &ItemsResult{Items: []ItemTimeline{
			{
				Name: itemKind, Kind: itemKind, Loc: itemLoc,
				Phases: []ItemPhase{{
					AvailableFrom: 0,
					TakenAt:       takenAt,
					TakenBy:       taker,
					Team:          takerTeam,
				}},
			},
		}},
	}
}

func TestBuildDenialsPost_DenialOnly(t *testing.T) {
	// Picker p1 (red) takes RA at "ra-loc" without weapon. p2 (blue) is in
	// "big" with RL — the BIG↔RA edge has 20 traversals each direction so
	// BIG is in the region. No red weapon-bearer is in the region: this
	// is a clean denial.
	r := makeDenialsResult(t,
		[]string{"", "ra-loc", "big", "weak"},
		[][3]any{
			{"ra-loc", "big", 20},
			{"big", "ra-loc", 20},
			// "weak" connection — only one direction qualifies, so it
			// must NOT count as part of the region.
			{"ra-loc", "weak", 20},
			{"weak", "ra-loc", 5},
		},
		1.0,
		map[string]*HighResPlayerData{
			"p1": {H: 100, A: 0, Li: 1},                  // red, at ra-loc, no weapon
			"p2": {H: 100, A: 100, AT: "ra", RL: true, Li: 2}, // blue, at big, RL
			"p3": {H: 100, A: 0, Li: 3, RL: true},        // red, at "weak" — should NOT count (one-way)
		},
		[]DemoInfoPlayer{
			{Name: "p1", Team: "red"},
			{Name: "p2", Team: "blue"},
			{Name: "p3", Team: "red"},
		},
		"ra", "ra-loc",
		"p1", "red", 1.0,
	)

	buildDenialsPost(r, nil)
	if r.Denials == nil {
		t.Fatal("expected Denials, got nil")
	}
	if len(r.Denials.Denials) != 1 {
		t.Fatalf("expected 1 denial, got %d", len(r.Denials.Denials))
	}
	d := r.Denials.Denials[0]
	if d.Player != "p1" || d.Item != "ra" || d.EnemyWeapons != 1 || d.Team != "red" {
		t.Errorf("unexpected denial: %+v", d)
	}
	if d.PlayerUserID != 42 {
		t.Errorf("PlayerUserID not propagated: %d", d.PlayerUserID)
	}
	if len(r.Denials.Hoovers) != 0 {
		t.Errorf("did not expect hoovers, got %d", len(r.Denials.Hoovers))
	}
}

func TestBuildDenialsPost_NotADenialWhenSameTeamHasWeapon(t *testing.T) {
	// Picker p1 (red, no weapon). p2 (red) in region with RL — that's
	// just a contested grab, not a denial.
	r := makeDenialsResult(t,
		[]string{"", "ra-loc", "big"},
		[][3]any{
			{"ra-loc", "big", 20},
			{"big", "ra-loc", 20},
		},
		1.0,
		map[string]*HighResPlayerData{
			"p1": {H: 100, A: 0, Li: 1},
			"p2": {H: 100, A: 100, RL: true, Li: 2}, // red, in region with RL
			"p3": {H: 100, A: 50, RL: true, Li: 2},  // blue, in region with RL
		},
		[]DemoInfoPlayer{
			{Name: "p1", Team: "red"},
			{Name: "p2", Team: "red"},
			{Name: "p3", Team: "blue"},
		},
		"ra", "ra-loc",
		"p1", "red", 1.0,
	)
	buildDenialsPost(r, nil)
	if r.Denials != nil && len(r.Denials.Denials) > 0 {
		t.Errorf("did not expect denial, got %+v", r.Denials.Denials)
	}
}

func TestBuildDenialsPost_HooverYA(t *testing.T) {
	// Picker p1 (red, no weapon) takes YA. Teammate p2 (red, RL) is in
	// region with armor 30 (< 50 threshold). No enemy in region.
	r := makeDenialsResult(t,
		[]string{"", "ya-loc", "big"},
		[][3]any{
			{"ya-loc", "big", 12},
			{"big", "ya-loc", 15},
		},
		2.5,
		map[string]*HighResPlayerData{
			"p1": {H: 100, A: 0, Li: 1},
			"p2": {H: 100, A: 30, AT: "ga", RL: true, Li: 2},
		},
		[]DemoInfoPlayer{
			{Name: "p1", Team: "red"},
			{Name: "p2", Team: "red"},
		},
		"ya", "ya-loc",
		"p1", "red", 2.5,
	)
	buildDenialsPost(r, nil)
	if r.Denials == nil || len(r.Denials.Hoovers) != 1 {
		t.Fatalf("expected 1 hoover; got %+v", r.Denials)
	}
	h := r.Denials.Hoovers[0]
	if h.Player != "p1" || h.Item != "ya" || h.NeedyTeammate != "p2" || h.NeedyStat != "armor" || h.NeedyValue != 30 {
		t.Errorf("unexpected hoover: %+v", h)
	}
}

func TestBuildDenialsPost_HooverMHHealth(t *testing.T) {
	// MH hoover threshold is health <= 50. Teammate at 50 HP qualifies;
	// teammate at 51 HP does not.
	cases := []struct {
		hp        int
		expectHit bool
	}{
		{30, true},
		{50, true},
		{51, false},
		{100, false},
	}
	for _, tc := range cases {
		r := makeDenialsResult(t,
			[]string{"", "mh-loc", "near"},
			[][3]any{
				{"mh-loc", "near", 25},
				{"near", "mh-loc", 25},
			},
			3.0,
			map[string]*HighResPlayerData{
				"picker":   {H: 100, A: 0, Li: 1},
				"teammate": {H: tc.hp, A: 100, RL: true, Li: 2},
			},
			[]DemoInfoPlayer{
				{Name: "picker", Team: "red"},
				{Name: "teammate", Team: "red"},
			},
			"mh", "mh-loc",
			"picker", "red", 3.0,
		)
		buildDenialsPost(r, nil)
		got := 0
		if r.Denials != nil {
			got = len(r.Denials.Hoovers)
		}
		want := 0
		if tc.expectHit {
			want = 1
		}
		if got != want {
			t.Errorf("hp=%d: want %d hoovers, got %d", tc.hp, want, got)
		}
	}
}

func TestBuildDenialsPost_RegionGate(t *testing.T) {
	// "far" is connected only via a single direction with 8 traversals
	// — both directions need ≥10. So an enemy in "far" is NOT in the
	// region of "ra-loc". Picker is alone in "ra-loc" with no enemy in
	// region → no denial.
	r := makeDenialsResult(t,
		[]string{"", "ra-loc", "far"},
		[][3]any{
			{"ra-loc", "far", 30},
			{"far", "ra-loc", 8},
		},
		1.0,
		map[string]*HighResPlayerData{
			"p1": {H: 100, Li: 1},
			"p2": {H: 100, RL: true, Li: 2}, // blue, in "far", out of region
		},
		[]DemoInfoPlayer{
			{Name: "p1", Team: "red"},
			{Name: "p2", Team: "blue"},
		},
		"ra", "ra-loc",
		"p1", "red", 1.0,
	)
	buildDenialsPost(r, nil)
	if r.Denials != nil && len(r.Denials.Denials) > 0 {
		t.Errorf("expected no denial (one-way edge below threshold), got %+v", r.Denials.Denials)
	}
}

func TestBuildDenialsPost_PickerWithWeaponSkipped(t *testing.T) {
	// If the picker themselves holds RL/LG, the pickup is not a denial
	// (and not a hoover). Quad-only is still "without weapon" for the
	// picker.
	r := makeDenialsResult(t,
		[]string{"", "ra-loc", "big"},
		[][3]any{
			{"ra-loc", "big", 20},
			{"big", "ra-loc", 20},
		},
		1.0,
		map[string]*HighResPlayerData{
			"p1": {H: 100, A: 0, Li: 1, RL: true},
			"p2": {H: 100, RL: true, Li: 2},
		},
		[]DemoInfoPlayer{
			{Name: "p1", Team: "red"},
			{Name: "p2", Team: "blue"},
		},
		"ra", "ra-loc",
		"p1", "red", 1.0,
	)
	buildDenialsPost(r, nil)
	if r.Denials != nil {
		t.Errorf("picker with RL should not produce events: %+v", r.Denials)
	}
}

func TestBuildDenialsPost_QuadCountsAsControl(t *testing.T) {
	// Enemy player has Quad but no RL/LG — still counts as a
	// weapon-bearer for region control purposes.
	r := makeDenialsResult(t,
		[]string{"", "pent-loc", "near"},
		[][3]any{
			{"pent-loc", "near", 14},
			{"near", "pent-loc", 14},
		},
		5.0,
		map[string]*HighResPlayerData{
			"p1": {H: 100, A: 0, Li: 1},
			"p2": {H: 100, A: 0, Q: true, Li: 2},
		},
		[]DemoInfoPlayer{
			{Name: "p1", Team: "red"},
			{Name: "p2", Team: "blue"},
		},
		"pent", "pent-loc",
		"p1", "red", 5.0,
	)
	buildDenialsPost(r, nil)
	if r.Denials == nil || len(r.Denials.Denials) != 1 {
		t.Fatalf("expected 1 denial via quad-only enemy; got %+v", r.Denials)
	}
	if r.Denials.Denials[0].EnemyWeapons != 1 {
		t.Errorf("expected EnemyWeapons=1 (Quad counts), got %d", r.Denials.Denials[0].EnemyWeapons)
	}
}
