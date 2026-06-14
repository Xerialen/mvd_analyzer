package view

import (
	"reflect"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// columnarToRow rebuilds a row-major BucketsView from a ColumnarBuckets,
// the inverse of BucketsColumnar. It is the parity oracle: a faithful
// columnar build must reproduce exactly what Buckets emits. Player maps
// come from the dense columns (honouring Alive + ValidFrom + the
// x/y/z→"pos" recombination); teams reuse aggregateTeams so team
// key-presence semantics are not re-encoded here. Bucket T is recomputed
// from the original grid (StartMs is truncated int32 ms and cannot
// reproduce a float64 second start exactly).
func columnarToRow(r *result.Result, opts BucketsOptions, cb *ColumnarBuckets) *BucketsView {
	bv := &BucketsView{WindowMs: cb.WindowMs}
	if cb.Count == 0 {
		return bv
	}
	g, _ := resolveBucketGrid(r, opts)
	bv.Buckets = make([]ViewBucket, cb.Count)
	for i := 0; i < cb.Count; i++ {
		bStart, _ := g.bounds(i)
		vb := ViewBucket{T: bStart, Players: make(map[string]map[string]any)}
		if g.hasPartial && i == cb.Count-1 {
			vb.Partial = true
		}
		for name, cp := range cb.Players {
			if i < cp.First || i >= cp.First+cp.N {
				continue
			}
			if !cp.Alive[i-cp.First] {
				continue
			}
			pdata := make(map[string]any)
			if xv := cp.valAt("x", i); xv != nil {
				pdata[FieldPosition] = [3]result.Coord{
					result.Coord(cp.valAt("x", i).(float32)),
					result.Coord(cp.valAt("y", i).(float32)),
					result.Coord(cp.valAt("z", i).(float32)),
				}
			}
			for field := range cp.Cols {
				if field == "x" || field == "y" || field == "z" {
					continue
				}
				if v := cp.valAt(field, i); v != nil {
					pdata[field] = v
				}
			}
			if len(pdata) == 0 {
				continue
			}
			vb.Players[name] = pdata
		}
		if opts.IncludeTeam {
			vb.Team = aggregateTeams(r.Streams.Players, vb.Players)
		}
		bv.Buckets[i] = vb
	}
	return bv
}

// assertParity builds both layouts and deep-equals the columnar one
// (round-tripped back to row) against the row builder.
func assertParity(t *testing.T, r *result.Result, opts BucketsOptions) {
	t.Helper()
	// Columnar always emits the raw loc index; compare against the row
	// builder in index mode so loc values line up.
	rowOpts := opts
	rowOpts.LocIndex = true
	row, err := Buckets(r, rowOpts)
	if err != nil {
		t.Fatalf("Buckets: %v", err)
	}
	cb, err := BucketsColumnar(r, opts)
	if err != nil {
		t.Fatalf("BucketsColumnar: %v", err)
	}
	if cb.Count != len(row.Buckets) {
		t.Fatalf("count = %d, want %d", cb.Count, len(row.Buckets))
	}
	got := columnarToRow(r, opts, cb)
	if !reflect.DeepEqual(got, row) {
		// Narrow the failure to the first diverging bucket.
		for i := range row.Buckets {
			if !reflect.DeepEqual(got.Buckets[i], row.Buckets[i]) {
				t.Fatalf("bucket %d diverges:\n columnar→row: %+v\n row:          %+v",
					i, got.Buckets[i], row.Buckets[i])
			}
		}
		t.Fatalf("parity mismatch outside bucket bodies:\n got %+v\n want %+v", got, row)
	}
}

// gapFixture: 3 players across [0,10s). p1 (red) dies mid-match (one
// dead bucket at 1s windows) and picks up armor after spawn (a late
// validFrom); p2 (red) carries LG; p3 (blue) carries both RL and LG.
// Positions on p1 exercise the x/y/z split.
func gapFixture() *result.Result {
	pos := &result.PositionTrack{}
	for ms := int32(0); ms <= 9000; ms += 1000 {
		pos.T = append(pos.T, ms)
		pos.X = append(pos.X, float32(ms))
		pos.Y = append(pos.Y, float32(-ms))
		pos.Z = append(pos.Z, 64)
		pos.Li = append(pos.Li, int16(ms/1000))
	}
	return &result.Result{
		Streams: &result.Streams{
			Global: result.GlobalStream{MatchStart: 0, MatchEnd: 10000},
			Players: []result.PlayerStream{
				{
					Name: "p1", Team: "red",
					Health:   []result.ChangeI16{{T: 0, V: 100}, {T: 2500, V: 50}, {T: 5200, V: 100}},
					Armor:    []result.ChangeI16{{T: 2000, V: 100}}, // late → ValidFrom["a"] > first
					RL:       []result.Interval{{Start: 1000, End: 3000}},
					Loc:      []result.ChangeI16{{T: 0, V: 1}, {T: 4000, V: 2}},
					Position: pos,
					Spawns:   []int32{0, 5200},
					Deaths:   []int32{3500},
				},
				{
					Name: "p2", Team: "red",
					Health: []result.ChangeI16{{T: 0, V: 80}},
					LG:     []result.Interval{{Start: 0, End: 10000}},
					Spawns: []int32{0},
				},
				{
					Name: "p3", Team: "blue",
					Health: []result.ChangeI16{{T: 0, V: 90}},
					RL:     []result.Interval{{Start: 0, End: 10000}},
					LG:     []result.Interval{{Start: 0, End: 10000}},
					Spawns: []int32{0},
				},
			},
		},
	}
}

func TestColumnarParitySynthetic(t *testing.T) {
	r := gapFixture()
	for _, win := range []int{50, 100, 200, 1000} {
		for _, team := range []bool{false, true} {
			opts := BucketsOptions{WindowMs: win, IncludeTeam: team}
			assertParity(t, r, opts)
		}
	}
}

func TestColumnarParityBasicHealth(t *testing.T) {
	r := makeStream(t, result.PlayerStream{
		Name:   "p1",
		Health: []result.ChangeI16{{T: 0, V: 100}, {T: 300, V: 60}, {T: 1200, V: 30}, {T: 2000, V: 100}},
		Spawns: []int32{0},
	})
	assertParity(t, r, BucketsOptions{WindowMs: 1000, Fields: []string{FieldHealth}})
}

func TestColumnarGapsAlivesValidFrom(t *testing.T) {
	r := gapFixture()
	cb, err := BucketsColumnar(r, BucketsOptions{WindowMs: 1000})
	if err != nil {
		t.Fatalf("BucketsColumnar: %v", err)
	}
	p1 := cb.Players["p1"]
	if p1 == nil {
		t.Fatal("p1 missing")
	}
	// p1 spawns at 0, dies at 3.5s, respawns at 5.2s → bucket 4 [4,5)s
	// is the dead gap; first/last active buckets are 0 and 9.
	if p1.First != 0 {
		t.Fatalf("p1.First = %d, want 0", p1.First)
	}
	if p1.N != 10 {
		t.Fatalf("p1.N = %d, want 10", p1.N)
	}
	if p1.Alive[4] {
		t.Fatalf("p1 expected dead at bucket 4, Alive=%v", p1.Alive)
	}
	// Alive runs: [0,4) on-map, bucket 4 dead, [5,10) on-map again.
	for _, idx := range []int{0, 1, 2, 3, 5, 6, 7, 8, 9} {
		if !p1.Alive[idx] {
			t.Fatalf("p1 expected alive at bucket %d, Alive=%v", idx, p1.Alive)
		}
	}
	// Armor first appears at t=2s → ValidFrom["a"] = 2.
	if got := p1.ValidFrom["a"]; got != 2 {
		t.Fatalf("p1.ValidFrom[a] = %d, want 2", got)
	}
	// Health is present from bucket 0 → no ValidFrom entry.
	if _, ok := p1.ValidFrom["h"]; ok {
		t.Fatalf("p1.ValidFrom[h] should be absent")
	}
	// Position split present.
	if _, ok := p1.Cols["x"]; !ok {
		t.Fatal("p1 missing x column")
	}
	if _, ok := p1.Cols["pos"]; ok {
		t.Fatal("p1 should not carry a raw pos column (split into x/y/z)")
	}
}

func TestColumnarTeamArrays(t *testing.T) {
	r := gapFixture()
	cb, err := BucketsColumnar(r, BucketsOptions{WindowMs: 1000, IncludeTeam: true})
	if err != nil {
		t.Fatalf("BucketsColumnar: %v", err)
	}
	red := cb.Teams["red"]
	if red == nil {
		t.Fatal("red team missing")
	}
	// Bucket 1 [1,2)s: p1 holds RL (interval 1-3s), p2 holds LG → rl=1, lg=1, w=2.
	if red.Cols["rl"][1] != 1 {
		t.Fatalf("red rl[1] = %d, want 1", red.Cols["rl"][1])
	}
	if red.Cols["lg"][1] != 1 {
		t.Fatalf("red lg[1] = %d, want 1", red.Cols["lg"][1])
	}
	if red.Cols["w"][1] != 2 {
		t.Fatalf("red w[1] = %d, want 2", red.Cols["w"][1])
	}
	blue := cb.Teams["blue"]
	if blue == nil || blue.Cols["rllg"][0] != 1 {
		t.Fatalf("blue rllg[0] = %v, want 1", blue.Cols["rllg"])
	}
}

// TestColumnarSameBucketDeathRespawn pins how a death and respawn that
// fall in the same window surface: the bucket stays alive (no spurious
// gap) and carries both the "d" and "sp" markers. There is no lives
// table — true life counts come from the spawn/death event streams.
func TestColumnarSameBucketDeathRespawn(t *testing.T) {
	// Death and respawn at the same timestamp (same server frame),
	// inside the 1 s bucket 4 ([4,5)s). Player is alive throughout.
	r := makeStream(t, result.PlayerStream{
		Name:   "p1",
		Health: []result.ChangeI16{{T: 0, V: 100}, {T: 4500, V: 100}},
		Spawns: []int32{0, 4500},
		Deaths: []int32{4500},
	})
	cb, err := BucketsColumnar(r, BucketsOptions{WindowMs: 1000, Fields: []string{FieldSpawns, FieldDeaths}})
	if err != nil {
		t.Fatalf("BucketsColumnar: %v", err)
	}
	p1 := cb.Players["p1"]
	if p1 == nil {
		t.Fatal("p1 missing")
	}
	rel := 4 - p1.First
	if !p1.Alive[rel] {
		t.Fatalf("bucket 4 should be alive (spawn-in-window), Alive=%v", p1.Alive)
	}
	d := p1.Cols[FieldDeaths].([]bool)
	sp := p1.Cols[FieldSpawns].([]bool)
	if !d[rel] || !sp[rel] {
		t.Fatalf("bucket 4 expected both d and sp set; d=%v sp=%v", d[rel], sp[rel])
	}
	// Parity must still hold for this fixture.
	assertParity(t, r, BucketsOptions{WindowMs: 1000})
}

func TestColumnarLiIndexAlways(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{
			Players: []result.PlayerStream{{
				Name:   "p1",
				Loc:    []result.ChangeI16{{T: 0, V: 2}},
				Spawns: []int32{0},
			}},
			Global: result.GlobalStream{MatchStart: 0, MatchEnd: 10000},
		},
		TimelineAnalysis: &result.TimelineAnalysisResult{LocTable: []string{"", "rl", "ya"}},
	}
	// Even with LocIndex=false, columnar emits the raw int16 index "li",
	// never resolved "loc" names.
	cb, err := BucketsColumnar(r, BucketsOptions{WindowMs: 1000, Fields: []string{FieldLoc}, LocIndex: false})
	if err != nil {
		t.Fatalf("BucketsColumnar: %v", err)
	}
	p1 := cb.Players["p1"]
	li, ok := p1.Cols["li"].([]int16)
	if !ok {
		t.Fatalf("li column type = %T, want []int16", p1.Cols["li"])
	}
	if li[0] != 2 {
		t.Fatalf("li[0] = %d, want 2", li[0])
	}
	if _, present := p1.Cols["loc"]; present {
		t.Fatal("columnar must not emit resolved loc names")
	}
}

// TestColumnarOmitsFieldlessPlayer: a player with no value for any
// requested field (here a stats-less observer with only health, queried
// for armor) is omitted from the columnar output, matching the row
// builder — so the two layouts carry the same player set.
func TestColumnarOmitsFieldlessPlayer(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{
			Global: result.GlobalStream{MatchStart: 0, MatchEnd: 10000},
			Players: []result.PlayerStream{
				{Name: "fighter", Health: []result.ChangeI16{{T: 0, V: 100}}, Armor: []result.ChangeI16{{T: 0, V: 50}}, Spawns: []int32{0}},
				{Name: "observer", Health: []result.ChangeI16{{T: 0, V: 100}}, Spawns: []int32{0}}, // no armor stream
			},
		},
	}
	cb, err := BucketsColumnar(r, BucketsOptions{WindowMs: 1000, Fields: []string{FieldArmor}})
	if err != nil {
		t.Fatalf("BucketsColumnar: %v", err)
	}
	if _, ok := cb.Players["observer"]; ok {
		t.Fatalf("observer has no armor — should be omitted, got %v", cb.Players["observer"])
	}
	if _, ok := cb.Players["fighter"]; !ok {
		t.Fatal("fighter has armor — should be present")
	}
	// And it must still match the row builder's player set.
	assertParity(t, r, BucketsOptions{WindowMs: 1000, Fields: []string{FieldArmor}})
}

func TestColumnarEmpty(t *testing.T) {
	cb, err := BucketsColumnar(nil, BucketsOptions{WindowMs: 50})
	if err != nil {
		t.Fatalf("BucketsColumnar(nil): %v", err)
	}
	if cb.WindowMs != 50 || cb.Count != 0 {
		t.Fatalf("empty columnar = %+v", cb)
	}
}
