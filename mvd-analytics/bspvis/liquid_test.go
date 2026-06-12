package bspvis

import (
	"math"
	"testing"
)

// poolBSP builds a minimal hull-0 tree: open space above Z=0, a liquid
// volume (contents configurable) from Z=0 down to the pool bottom at
// Z=-200, solid below. Horizontal extent is unbounded — fine for point
// and straight-down queries.
func poolBSP(liquid int32) *BSP {
	return &BSP{
		Planes: []Plane{
			{Normal: Vec3{Z: 1}, Dist: 0},    // 0: liquid surface
			{Normal: Vec3{Z: 1}, Dist: -200}, // 1: pool bottom
		},
		Nodes: []Node{
			{PlaneID: 0, Children: [2]int32{-2, 1}},  // front: empty leaf 1; back: node 1
			{PlaneID: 1, Children: [2]int32{-3, -4}}, // front: liquid leaf 2; back: solid leaf 3
		},
		Leaves: []Leaf{
			{Contents: ContentsSolid}, // universal sink
			{Contents: ContentsEmpty},
			{Contents: liquid},
			{Contents: ContentsSolid},
		},
		Models: []Model{{Mins: Vec3{Z: -200}, HeadNodes: [4]int32{0, 0, 0, 0}}},
	}
}

// WaterLevel mirrors PM_CategorizePosition's three probes: feet (z-23),
// waist (z+4), eyes (z+22) against the surface at Z=0.
func TestWaterLevel(t *testing.T) {
	b := poolBSP(ContentsWater)
	cases := []struct {
		z     float32
		level int
		cont  int32
	}{
		{200, 0, ContentsEmpty}, // dry, well above
		{24, 0, ContentsEmpty},  // feet probe at +1: still dry
		{10, 1, ContentsWater},  // feet wet (-13), waist dry (+14)
		{-10, 2, ContentsWater}, // waist wet (-6), eyes dry (+12)
		{-30, 3, ContentsWater}, // eyes wet (-8)
	}
	for _, c := range cases {
		level, cont := b.WaterLevel(0, 0, c.z)
		if level != c.level || cont != c.cont {
			t.Errorf("WaterLevel(z=%v) = (%d,%d), want (%d,%d)", c.z, level, cont, c.level, c.cont)
		}
	}

	// Liquid type follows the leaf contents.
	if level, cont := poolBSP(ContentsLava).WaterLevel(0, 0, -30); level != 3 || cont != ContentsLava {
		t.Errorf("lava pool: WaterLevel = (%d,%d), want (3,%d)", level, cont, ContentsLava)
	}

	// Sky is deliberately NOT liquid (deviation from the engine's
	// cont <= CONTENTS_WATER drag predicate — see liquid.go).
	if level, _ := poolBSP(ContentsSky).WaterLevel(0, 0, -30); level != 0 {
		t.Errorf("sky volume: WaterLevel = %d, want 0", level)
	}
}

// The downward walk finds the empty→liquid crossing; starting inside
// the liquid records no crossing (the surface is above, not below).
func TestLiquidSurfaceBelow(t *testing.T) {
	b := poolBSP(ContentsWater)

	surf, cont, ok := b.LiquidSurfaceBelow(0, 0, 150)
	if !ok || cont != ContentsWater || math.Abs(float64(surf)) > 0.01 {
		t.Errorf("from above: surface = (%v,%d,%v), want (≈0,water,true)", surf, cont, ok)
	}

	if _, _, ok := b.LiquidSurfaceBelow(0, 0, -50); ok {
		t.Errorf("start inside liquid: want ok=false (surface is above)")
	}

	if _, _, ok := b.LiquidSurfaceBelow(0, 0, -300); ok {
		t.Errorf("start below the world bottom: want ok=false")
	}
}

// A solid ledge between the start and the liquid blocks the walk — the
// floor answers, not the water under it.
func TestLiquidSurfaceBelow_SolidBlocks(t *testing.T) {
	// Open above Z=100, solid slab 100..0, water 0..-200, solid below.
	b := &BSP{
		Planes: []Plane{
			{Normal: Vec3{Z: 1}, Dist: 100}, // slab top
			{Normal: Vec3{Z: 1}, Dist: 0},   // slab bottom / water surface
			{Normal: Vec3{Z: 1}, Dist: -200},
		},
		Nodes: []Node{
			{PlaneID: 0, Children: [2]int32{-2, 1}},  // above slab: empty
			{PlaneID: 1, Children: [2]int32{-4, 2}},  // slab: solid leaf 3
			{PlaneID: 2, Children: [2]int32{-3, -4}}, // water leaf 2 / solid
		},
		Leaves: []Leaf{
			{Contents: ContentsSolid},
			{Contents: ContentsEmpty},
			{Contents: ContentsWater},
			{Contents: ContentsSolid},
		},
		Models: []Model{{Mins: Vec3{Z: -200}, HeadNodes: [4]int32{0, 0, 0, 0}}},
	}
	if _, _, ok := b.LiquidSurfaceBelow(0, 0, 150); ok {
		t.Errorf("solid slab above the water: want ok=false (floor blocks first)")
	}
}
