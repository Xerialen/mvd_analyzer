package bspvis

import "testing"

// halfSpaceBSP builds a minimal BSP whose worldspawn (Models[0]) splits on the
// plane x=0: the front half (x>=0) is empty, the back half (x<0) is solid.
// Models[1] reuses the same tree as an inline brush submodel, bounded to a box
// around the origin so RayHitsSolidModel's AABB reject is exercised.
func halfSpaceBSP() *BSP {
	return &BSP{
		Version: "v29",
		Planes:  []Plane{{Normal: Vec3{X: 1}, Dist: 0}},
		// front child (x>=0) -> leaf 1 (empty); back child (x<0) -> leaf 0 (solid).
		Nodes: []Node{{PlaneID: 0, Children: [2]int32{-2, -1}}},
		Leaves: []Leaf{
			{Contents: ContentsSolid},
			{Contents: ContentsEmpty},
		},
		Models: []Model{
			{HeadNodes: [4]int32{0}, Mins: Vec3{-99, -99, -99}, Maxs: Vec3{99, 99, 99}},
			{HeadNodes: [4]int32{0}, Mins: Vec3{-16, -16, -16}, Maxs: Vec3{0, 16, 16}},
		},
	}
}

func TestRayHitsSolidModel_OutOfRange(t *testing.T) {
	b := halfSpaceBSP()
	if b.RayHitsSolidModel(5, [3]float32{}, [3]float32{-10, 0, 0}, [3]float32{10, 0, 0}) {
		t.Errorf("out-of-range modelIdx must never block")
	}
	if b.RayHitsSolidModel(-1, [3]float32{}, [3]float32{-10, 0, 0}, [3]float32{10, 0, 0}) {
		t.Errorf("negative modelIdx must never block")
	}
}

func TestRayHitsSolidModel_AtRest(t *testing.T) {
	b := halfSpaceBSP()
	origin := [3]float32{0, 0, 0}

	// Segment crossing x=0 enters the solid back half -> blocked.
	if !b.RayHitsSolidModel(1, origin, [3]float32{10, 0, 0}, [3]float32{-10, 0, 0}) {
		t.Errorf("segment crossing into solid half should be blocked")
	}
	// Segment entirely in the empty front half (also outside the AABB) -> clear.
	if b.RayHitsSolidModel(1, origin, [3]float32{10, 0, 0}, [3]float32{5, 0, 0}) {
		t.Errorf("segment in empty half should be clear")
	}
}

func TestRayHitsSolidModel_Posed(t *testing.T) {
	b := halfSpaceBSP()
	// Pose the submodel 100 units along +x: its solid half now lives around
	// world x in [84,100]. The trace must run in the model's local frame.
	origin := [3]float32{100, 0, 0}

	// World segment crossing the posed solid (world x 90 is local x -10) -> blocked.
	if !b.RayHitsSolidModel(1, origin, [3]float32{110, 0, 0}, [3]float32{90, 0, 0}) {
		t.Errorf("segment crossing the posed solid should be blocked")
	}
	// World segment that stays on the empty side of the posed model -> clear.
	if b.RayHitsSolidModel(1, origin, [3]float32{110, 0, 0}, [3]float32{105, 0, 0}) {
		t.Errorf("segment clear of the posed solid should be clear")
	}
	// The same world segment that WAS blocked at rest is now clear, because the
	// solid moved away — proves the origin translation actually applies.
	if b.RayHitsSolidModel(1, origin, [3]float32{10, 0, 0}, [3]float32{-10, 0, 0}) {
		t.Errorf("at-rest-blocking segment should be clear once the model is posed away")
	}
}

func TestBoxLeafs(t *testing.T) {
	b := halfSpaceBSP() // front (x>=0) = leaf 1 (empty), back (x<0) = leaf 0 (solid)

	has := func(leaves []int, want int) bool {
		for _, l := range leaves {
			if l == want {
				return true
			}
		}
		return false
	}

	// A box straddling x=0 touches both leaves.
	straddle := b.BoxLeafs([3]float32{-4, -4, -4}, [3]float32{4, 4, 4}, nil)
	if !has(straddle, 0) || !has(straddle, 1) {
		t.Errorf("straddling box should touch both leaves, got %v", straddle)
	}
	// A box wholly in the front half touches only the empty leaf.
	front := b.BoxLeafs([3]float32{10, -4, -4}, [3]float32{20, 4, 4}, nil)
	if has(front, 0) || !has(front, 1) {
		t.Errorf("front-only box should touch just leaf 1, got %v", front)
	}
	// A box wholly in the back half touches only the solid leaf.
	back := b.BoxLeafs([3]float32{-20, -4, -4}, [3]float32{-10, 4, 4}, nil)
	if !has(back, 0) || has(back, 1) {
		t.Errorf("back-only box should touch just leaf 0, got %v", back)
	}
}

func TestSegmentHitsAABB(t *testing.T) {
	min := [3]float32{-8, -8, -8}
	max := [3]float32{8, 8, 8}
	cases := []struct {
		name   string
		p0, p1 [3]float32
		want   bool
	}{
		{"through", [3]float32{-20, 0, 0}, [3]float32{20, 0, 0}, true},
		{"inside", [3]float32{0, 0, 0}, [3]float32{1, 0, 0}, true},
		{"miss above", [3]float32{-20, 20, 0}, [3]float32{20, 20, 0}, false},
		{"endpoint touches", [3]float32{8, 0, 0}, [3]float32{20, 0, 0}, true},
		{"parallel outside", [3]float32{-20, 20, 0}, [3]float32{-20, -20, 0}, false},
	}
	for _, c := range cases {
		if got := segmentHitsAABB(c.p0, c.p1, min, max); got != c.want {
			t.Errorf("%s: segmentHitsAABB = %v, want %v", c.name, got, c.want)
		}
	}
}
