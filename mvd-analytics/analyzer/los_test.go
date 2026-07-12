package analyzer

import (
	"reflect"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/bspvis"
	"github.com/mvd-analyzer/mvd-analytics/result"
)

// zFloorBSP splits worldspawn on z=0: the front half (z>=0) is empty, the back
// half (z<0) is solid. With a single plane a segment is blocked iff either
// endpoint lies in the back (solid) half — which makes line-of-sight depend on
// eye height, the simplest geometry that yields an asymmetric A↔B result.
func zFloorBSP() *bspvis.BSP {
	return &bspvis.BSP{
		Version: "v29",
		Planes:  []bspvis.Plane{{Normal: bspvis.Vec3{Z: 1}, Dist: 0, Type: 2}}, // Type 2 = Z axis (BoxLeafs fast path)
		Nodes:   []bspvis.Node{{PlaneID: 0, Children: [2]int32{-2, -1}}},       // front(z>=0) empty, back solid
		Leaves: []bspvis.Leaf{
			{Contents: bspvis.ContentsSolid},
			{Contents: bspvis.ContentsEmpty},
		},
		Models: []bspvis.Model{
			{HeadNodes: [4]int32{0}, Mins: bspvis.Vec3{X: -9999, Y: -9999, Z: -9999}, Maxs: bspvis.Vec3{X: 9999, Y: 9999, Z: 9999}},
		},
	}
}

// zCeilingBSP is zFloorBSP flipped: the front half (z>0) is solid, the back
// half (z<=0) is empty. Because a player's eye (origin+22) sits above the body
// origin, this is the only way to give a player an empty body leaf but a solid
// eye leaf — the configuration the LOS asymmetry test needs now that LOS is
// gated on the body leaf (a body in solid is never potentially visible).
func zCeilingBSP() *bspvis.BSP {
	return &bspvis.BSP{
		Version: "v29",
		Planes:  []bspvis.Plane{{Normal: bspvis.Vec3{Z: 1}, Dist: 0, Type: 2}}, // Type 2 = Z axis (BoxLeafs fast path)
		Nodes:   []bspvis.Node{{PlaneID: 0, Children: [2]int32{-1, -2}}},       // front(z>0) solid, back empty
		Leaves: []bspvis.Leaf{
			{Contents: bspvis.ContentsSolid},
			{Contents: bspvis.ContentsEmpty},
		},
		Models: []bspvis.Model{
			{HeadNodes: [4]int32{0}, Mins: bspvis.Vec3{X: -9999, Y: -9999, Z: -9999}, Maxs: bspvis.Vec3{X: 9999, Y: 9999, Z: 9999}},
		},
	}
}

// openWithSlabBSP has an all-empty worldspawn (Models[0] points straight at an
// empty leaf, so it never blocks) plus an inline brush submodel (Models[1])
// that is a solid slab occupying -8 <= x < 8, used to test mover occlusion.
func openWithSlabBSP() *bspvis.BSP {
	return &bspvis.BSP{
		Version: "v29",
		Planes: []bspvis.Plane{
			{Normal: bspvis.Vec3{X: 1}, Dist: -8},
			{Normal: bspvis.Vec3{X: 1}, Dist: 8},
		},
		Nodes: []bspvis.Node{
			{PlaneID: 0, Children: [2]int32{1, -2}},  // x>=-8 -> node1, else empty
			{PlaneID: 1, Children: [2]int32{-2, -1}}, // x>=8 -> empty, else solid (so -8<=x<8 solid)
		},
		Leaves: []bspvis.Leaf{
			{Contents: bspvis.ContentsSolid},
			{Contents: bspvis.ContentsEmpty},
		},
		Models: []bspvis.Model{
			{HeadNodes: [4]int32{-2}, Mins: bspvis.Vec3{X: -9999, Y: -9999, Z: -9999}, Maxs: bspvis.Vec3{X: 9999, Y: 9999, Z: 9999}},
			{HeadNodes: [4]int32{0}, Mins: bspvis.Vec3{X: -8, Y: -64, Z: -64}, Maxs: bspvis.Vec3{X: 8, Y: 64, Z: 64}},
		},
	}
}

func staticTrack(t []int32, x, y, z float32) *result.PositionTrack {
	pt := &result.PositionTrack{T: t}
	for range t {
		pt.X = append(pt.X, x)
		pt.Y = append(pt.Y, y)
		pt.Z = append(pt.Z, z)
	}
	return pt
}

func TestLosTargets(t *testing.T) {
	var dst [9][3]float32
	losTargets(100, 200, 50, &dst)
	want := map[[3]float32]bool{
		{84, 184, 26}: true, {84, 184, 82}: true,
		{84, 216, 26}: true, {84, 216, 82}: true,
		{116, 184, 26}: true, {116, 184, 82}: true,
		{116, 216, 26}: true, {116, 216, 82}: true,
		{100, 200, 54}: true, // midpoint = origin + (0,0,4)
	}
	if len(want) != 9 {
		t.Fatalf("test setup: want 9 distinct points, got %d", len(want))
	}
	for i, p := range dst {
		if !want[p] {
			t.Errorf("target %d = %v not among expected corners+midpoint", i, p)
		}
	}
}

func TestLosAliveAt(t *testing.T) {
	// Realistic KTX ordering: the match-start spawn is NOT recorded, so the
	// first event is a death; each recorded spawn is a respawn that follows it.
	deaths := []int32{300, 700}
	spawns := []int32{450, 900}
	cases := []struct {
		t    int32
		want bool
	}{
		{50, true},   // before first death → alive since match start (no spawn recorded yet)
		{200, true},  // still pre-first-death → alive
		{300, false}, // at first death → dead
		{400, false}, // dead between death and respawn
		{450, true},  // respawn
		{600, true},  // alive
		{700, false}, // second death
		{800, false}, // dead awaiting respawn
		{900, true},  // second respawn
	}
	for _, c := range cases {
		if got := losAliveAt(spawns, deaths, c.t); got != c.want {
			t.Errorf("losAliveAt(t=%d) = %v, want %v", c.t, got, c.want)
		}
	}
	// No spawn/death records → alive throughout.
	if !losAliveAt(nil, nil, 1234) {
		t.Errorf("empty spawns/deaths must read alive")
	}
	// Deaths only (no respawn recorded) → alive until the death, dead after.
	if !losAliveAt(nil, []int32{500}, 400) {
		t.Errorf("deaths-only: should be alive before the death")
	}
	if losAliveAt(nil, []int32{500}, 600) {
		t.Errorf("deaths-only: should be dead after the death")
	}
}

// TestComputeLosAB_Asymmetry: with solid above z=0, both players' bodies sit in
// the empty lower half (so each is potentially visible), but B's eye (origin+22)
// pokes into the solid ceiling while A's stays below it. A→B has a clear ray;
// B→A starts in solid and is blocked — so the two directions differ.
func TestComputeLosAB_Asymmetry(t *testing.T) {
	vb := zCeilingBSP()
	ts := []int32{0, 50}
	a := &result.PlayerStream{Name: "A", Position: staticTrack(ts, 0, 0, -30)}   // body z=-30, eye z=-8: both empty
	b := &result.PlayerStream{Name: "B", Position: staticTrack(ts, 100, 0, -10)} // body z=-10 empty, eye z=12 (in solid)

	ab := computeLosAB(vb, a, b, nil, 100)
	if len(ab) == 0 {
		t.Errorf("A→B: expected a visible interval (A's eye below the ceiling sees B's body), got none")
	}
	ba := computeLosAB(vb, b, a, nil, 100)
	if len(ba) != 0 {
		t.Errorf("B→A: expected no visibility (B's eye is inside solid), got %v", ba)
	}
}

// TestComputeLosAB_Transitions: visibility toggles as A's eye dips below the
// floor and back; expect intervals only over the visible spans.
func TestComputeLosAB_Transitions(t *testing.T) {
	vb := zFloorBSP()
	ts := []int32{0, 100, 200, 300}
	a := &result.PlayerStream{Name: "A", Position: &result.PositionTrack{
		T: ts,
		X: []float32{0, 0, 0, 0},
		Y: []float32{0, 0, 0, 0},
		Z: []float32{0, -50, 0, 0}, // eye front, back, front, front
	}}
	b := &result.PlayerStream{Name: "B", Position: staticTrack([]int32{0}, 100, 0, 10)} // body above floor (empty)

	got := computeLosAB(vb, a, b, nil, 400)
	want := []result.Interval{{Start: 0, End: 100}, {Start: 200, End: 300}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("transitions: got %v, want %v", got, want)
	}
}

// TestComputeLosAB_MoverBlocks: an open arena where A would see B, but a solid
// mover slab posed between them blocks every ray; once the mover is not drawn
// (Vis=false) the sightline returns.
func TestComputeLosAB_MoverBlocks(t *testing.T) {
	vb := openWithSlabBSP()
	ts := []int32{0, 50}
	a := &result.PlayerStream{Name: "A", Position: staticTrack(ts, -50, 0, 0)}
	b := &result.PlayerStream{Name: "B", Position: staticTrack(ts, 50, 0, 0)}

	// No movers → open arena → A sees B the whole time.
	if iv := computeLosAB(vb, a, b, nil, 100); len(iv) == 0 {
		t.Fatalf("no movers: expected A to see B in the open, got none")
	}

	// Mover (SubModel 1) posed at the origin, drawn → slab sits between them.
	visible := []result.MoverStream{{
		SubModel: 1, T: []int32{0}, X: []float32{0}, Y: []float32{0}, Z: []float32{0}, Vis: []bool{true},
	}}
	if iv := computeLosAB(vb, a, b, buildLosMovers(visible), 100); len(iv) != 0 {
		t.Errorf("visible mover between players should block LOS, got %v", iv)
	}

	// Same mover, not drawn (entity removed) → no longer solid → A sees B.
	hidden := []result.MoverStream{{
		SubModel: 1, T: []int32{0}, X: []float32{0}, Y: []float32{0}, Z: []float32{0}, Vis: []bool{false},
	}}
	if iv := computeLosAB(vb, a, b, buildLosMovers(hidden), 100); len(iv) == 0 {
		t.Errorf("hidden mover should not block LOS, got none")
	}
}

// TestComputePvsAB_SupersetOfLos: PVS ⊇ LOS, and PVS can be on while LOS is off
// (the "potentially visible, no clear ray" gap). In the open-arena BSP (no vis
// lump, so every empty leaf potentially sees every other) A and B have both LOS
// and PVS; drop a solid mover slab between them and LOS goes dark while PVS stays
// on. Since LOS is gated on PVS, los ⊆ pvs by construction in both cases.
func TestComputePvsAB_SupersetOfLos(t *testing.T) {
	vb := openWithSlabBSP()
	ts := []int32{0, 50}
	a := &result.PlayerStream{Name: "A", Position: staticTrack(ts, -50, 0, 0)}
	b := &result.PlayerStream{Name: "B", Position: staticTrack(ts, 50, 0, 0)}

	// Open arena: A sees B, and B is potentially visible.
	if los := computeLosAB(vb, a, b, nil, 100); len(los) == 0 {
		t.Fatalf("open arena: expected LOS, got none")
	}
	if pvs := computePvsAB(vb, a, b, nil, 100); len(pvs) == 0 {
		t.Fatalf("open arena: expected PVS, got none")
	}

	// Solid slab between them: the ray is blocked (LOS off) but they remain in
	// the same vis region (PVS on) — the gap the metric surfaces.
	slab := buildLosMovers([]result.MoverStream{{
		SubModel: 1, T: []int32{0}, X: []float32{0}, Y: []float32{0}, Z: []float32{0}, Vis: []bool{true},
	}})
	if los := computeLosAB(vb, a, b, slab, 100); len(los) != 0 {
		t.Errorf("slab between players should block LOS, got %v", los)
	}
	if pvs := computePvsAB(vb, a, b, slab, 100); len(pvs) == 0 {
		t.Errorf("slab blocks the ray but not PVS — expected a PVS gap interval, got none")
	}
}

// TestComputeLOS_NoBSP: a map with no provisioned BSP leaves LOS absent, and
// the pass marks itself computed so it is not retried.
func TestComputeLOS_NoBSP(t *testing.T) {
	ts := []int32{0, 50}
	res := &Result{
		DemoInfo: &DemoInfoResult{Map: "zzz_no_such_map_xyz"},
		Streams: &result.Streams{
			Players: []result.PlayerStream{
				{Name: "A", Position: staticTrack(ts, 0, 0, 0)},
				{Name: "B", Position: staticTrack(ts, 100, 0, 0)},
			},
		},
	}
	ComputeLOS(res)
	if !res.Streams.LOSComputed {
		t.Errorf("ComputeLOS should mark LOSComputed even with no BSP")
	}
	for i := range res.Streams.Players {
		if res.Streams.Players[i].LOS != nil {
			t.Errorf("player %q got LOS on a BSP-less map: %v", res.Streams.Players[i].Name, res.Streams.Players[i].LOS)
		}
		if res.Streams.Players[i].PVS != nil {
			t.Errorf("player %q got PVS on a BSP-less map: %v", res.Streams.Players[i].Name, res.Streams.Players[i].PVS)
		}
	}
}
