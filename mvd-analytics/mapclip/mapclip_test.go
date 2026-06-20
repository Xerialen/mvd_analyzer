package mapclip

import (
	"math"
	"os"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/mapbsp"
	"github.com/mvd-analyzer/mvd-analytics/mapgen/bsp"
)

// floorHull builds a one-plane clip hull: a horizontal clip plane at
// Z=24 with EMPTY above and SOLID below. Because the player origin rests
// PlayerFeetOffset (24) above the floor it stands on, a clip plane at 24
// represents a real floor surface at Z=0 — FloorBelow should return ~0.
func floorHull(t *testing.T) *bsp.BSP {
	t.Helper()
	return &bsp.BSP{
		Planes: []bsp.Plane{{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}, Dist: 24, Type: 2}},
		Models: []bsp.Model{{Mins: bsp.Vec3{Z: -100}, HeadNodes: [4]int32{0, 0, 0, 0}}},
		// child[0] (front, Z>=plane) → EMPTY, child[1] (back) → SOLID.
		ClipNodes: []bsp.ClipNode{{PlaneNum: 0, Children: [2]int32{contentsEmpty, contentsSolid}}},
	}
}

func TestFloorBelow_SinglePlane(t *testing.T) {
	h, err := Build(floorHull(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Well above the floor → floor surface ≈ 0 (clip plane 24 − feet 24).
	z, ok := h.FloorBelow(0, 0, 200)
	if !ok {
		t.Fatalf("FloorBelow(0,0,200) found no floor")
	}
	if math.Abs(float64(z)) > 0.5 {
		t.Errorf("floor Z = %v, want ≈ 0", z)
	}

	// A grounded player sits PlayerFeetOffset above the floor.
	if z2, ok := h.FloorBelow(0, 0, float32(PlayerFeetOffset)); !ok || math.Abs(float64(z2)) > 0.5 {
		t.Errorf("grounded FloorBelow = (%v,%v), want (≈0,true)", z2, ok)
	}

	// A point below the clip plane starts inside solid → not attributable.
	if _, ok := h.FloorBelow(0, 0, -50); ok {
		t.Errorf("FloorBelow below solid should report no floor")
	}

	// HeightAboveFloor reads ~0 grounded and the airborne delta otherwise.
	if hg, ok := h.HeightAboveFloor(0, 0, float32(PlayerFeetOffset)); !ok || math.Abs(float64(hg)) > 0.5 {
		t.Errorf("grounded height = (%v,%v), want (≈0,true)", hg, ok)
	}
	if ha, ok := h.HeightAboveFloor(0, 0, float32(PlayerFeetOffset)+100); !ok || math.Abs(float64(ha-100)) > 0.5 {
		t.Errorf("airborne height = (%v,%v), want (≈100,true)", ha, ok)
	}
}

// TestFloorBelow_StartSolidNudge is the regression for int-truncated
// origins on fractional-Z surfaces: stream positions are stored as
// truncated int32, so a grounded origin over a slope can reconstruct up
// to one unit inside the inflated hull. The first trace reads
// start-solid; the one-unit retry must stand the player back up. A
// start deeper than the nudge stays embedded and keeps reporting no
// floor.
func TestFloorBelow_StartSolidNudge(t *testing.T) {
	h, err := Build(floorHull(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Half a unit inside the clip plane at Z=24 (the truncation case):
	// the retry from z+1 finds the floor surface at ≈0.
	fz, ok := h.FloorBelow(0, 0, 23.5)
	if !ok {
		t.Fatalf("FloorBelow(0,0,23.5) found no floor — start-solid retry missing")
	}
	if math.Abs(float64(fz)) > 0.5 {
		t.Errorf("floor Z = %v, want ≈ 0", fz)
	}

	// The height a caller derives stays within the documented grounded
	// slack (the found surface sits a fraction above the original z).
	if hg, ok := h.HeightAboveFloor(0, 0, 23.5); !ok || math.Abs(float64(hg)) > 1.0 {
		t.Errorf("nudged grounded height = (%v,%v), want (|h|<=1,true)", hg, ok)
	}

	// Deeper than the nudge → genuinely embedded → still no floor.
	if _, ok := h.FloorBelow(0, 0, 22.5); ok {
		t.Errorf("FloorBelow(0,0,22.5) found a floor through >1u of solid")
	}
}

func TestFloorBelow_NoFloorOverVoid(t *testing.T) {
	// Both sides empty → nothing solid below → no floor anywhere.
	b := &bsp.BSP{
		Planes:    []bsp.Plane{{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}, Dist: 24, Type: 2}},
		Models:    []bsp.Model{{Mins: bsp.Vec3{Z: -100}, HeadNodes: [4]int32{0, 0, 0, 0}}},
		ClipNodes: []bsp.ClipNode{{PlaneNum: 0, Children: [2]int32{contentsEmpty, contentsEmpty}}},
	}
	h, err := Build(b)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := h.FloorBelow(0, 0, 200); ok {
		t.Errorf("expected no floor over void")
	}
}

// rimWellHull builds a two-region hull split by the X=0 plane: the back
// half (X<0) is a rim with its floor at Z=0 (clip plane Z=24); the front
// half (X>=0) is a deep well with its floor at Z=-400 (clip plane
// Z=-376). A player whose origin sits just inside the well but within a
// box-width of the rim is the well-rim-skim case from anwalked RA.
func rimWellHull(t *testing.T) *Hull {
	t.Helper()
	h, err := Build(&bsp.BSP{
		Planes: []bsp.Plane{
			{Normal: bsp.Vec3{X: 1}, Dist: 0, Type: 0},    // 0: X=0 split
			{Normal: bsp.Vec3{Z: 1}, Dist: -376, Type: 2}, // 1: well floor (Z=-400)
			{Normal: bsp.Vec3{Z: 1}, Dist: 24, Type: 2},   // 2: rim floor (Z=0)
		},
		Models: []bsp.Model{{Mins: bsp.Vec3{Z: -500}, HeadNodes: [4]int32{0, 0, 0, 0}}},
		ClipNodes: []bsp.ClipNode{
			{PlaneNum: 0, Children: [2]int32{1, 2}},                       // 0: X>=0 → well, X<0 → rim
			{PlaneNum: 1, Children: [2]int32{contentsEmpty, contentsSolid}}, // 1: well
			{PlaneNum: 2, Children: [2]int32{contentsEmpty, contentsSolid}}, // 2: rim
		},
	})
	if err != nil {
		t.Fatalf("build rim/well hull: %v", err)
	}
	return h
}

// TestHeightAboveFloorBox_RimSkim is the regression for the well-rim
// false airgib: a player skimming the rim of a well has their origin
// momentarily over the pit, so the single-column HeightAboveFloor plunges
// to the distant well floor and reads a huge height, while the
// footprint-aware HeightAboveFloorBox still finds the near rim under the
// overhanging box and reads small. Mirrors anwalked RA, where the
// single-point trace logged a bogus 553-unit airgib.
func TestHeightAboveFloorBox_RimSkim(t *testing.T) {
	h := rimWellHull(t)

	// Origin just inside the well (within footprintMargin of the X=0 rim
	// edge), feet 9 above the rim plane: z = PlayerFeetOffset + 9 = 33.
	const y, z = 0, float32(PlayerFeetOffset) + 9
	x := float32(footprintMargin) / 2

	// Single column drops through the well opening to the floor at Z=-400.
	single, ok := h.HeightAboveFloor(x, y, z)
	if !ok || single < 350 {
		t.Fatalf("single-column height = (%v,%v), want a large well-floor height", single, ok)
	}

	// Footprint query catches the rim (Z=0) under the overhanging box →
	// the player reads ~9 above the rim, not ~409 above the well floor.
	box, ok := h.HeightAboveFloorBox(x, y, z)
	if !ok {
		t.Fatalf("box height found no floor")
	}
	if math.Abs(float64(box-9)) > 0.5 {
		t.Errorf("box height = %v, want ≈9 (above the rim)", box)
	}

	// Far enough into the well that the whole footprint is over the pit:
	// no rim to catch, so box and single agree on the well-floor height.
	farX := float32(footprintMargin) + 16
	bFar, okb := h.HeightAboveFloorBox(farX, 0, z)
	sFar, oks := h.HeightAboveFloor(farX, 0, z)
	if !okb || !oks || math.Abs(float64(bFar-sFar)) > 0.5 {
		t.Errorf("far-over-well: box=%v single=%v, want equal (no rim in footprint)", bFar, sFar)
	}
}

// liftSceneBSP builds a two-model BSP: model 0 (worldspawn) is a deep
// shaft with its floor at Z=-400 (clip plane Z=-376); model 1 is a lift
// platform whose top surface is at local Z=0 (clip plane Z=24),
// 128x128 wide per its model bounds. Both hulls live in the shared
// CLIPNODES array, entered at their own HeadNodes[1] — the layout
// BuildModel walks on a real BSP.
func liftSceneBSP(t *testing.T) *bsp.BSP {
	t.Helper()
	return &bsp.BSP{
		Planes: []bsp.Plane{
			{Normal: bsp.Vec3{Z: 1}, Dist: -376, Type: 2}, // 0: shaft floor (Z=-400)
			{Normal: bsp.Vec3{Z: 1}, Dist: 24, Type: 2},   // 1: lift top (local Z=0)
		},
		Models: []bsp.Model{
			{Mins: bsp.Vec3{X: -1000, Y: -1000, Z: -500}, Maxs: bsp.Vec3{X: 1000, Y: 1000, Z: 500}, HeadNodes: [4]int32{0, 0, 0, 0}},
			{Mins: bsp.Vec3{X: -64, Y: -64, Z: -16}, Maxs: bsp.Vec3{X: 64, Y: 64, Z: 0}, HeadNodes: [4]int32{0, 1, 0, 0}},
		},
		ClipNodes: []bsp.ClipNode{
			{PlaneNum: 0, Children: [2]int32{contentsEmpty, contentsSolid}}, // 0: shaft
			{PlaneNum: 1, Children: [2]int32{contentsEmpty, contentsSolid}}, // 1: lift
		},
	}
}

// BuildModel must enter the requested submodel's own clip tree and
// carry its bounds; FloorBelowAt poses it at an entity origin.
func TestBuildModel_PosedSubmodel(t *testing.T) {
	b := liftSceneBSP(t)
	lift, err := BuildModel(b, 1)
	if err != nil {
		t.Fatalf("BuildModel(1): %v", err)
	}
	if lift.mins != [3]float32{-64, -64, -16} || lift.maxs != [3]float32{64, 64, 0} {
		t.Errorf("lift bounds = %v..%v, want model 1 bounds", lift.mins, lift.maxs)
	}

	// At rest (zero origin) the lift top reads local floor 0.
	if fz, ok := lift.FloorBelow(0, 0, 200); !ok || math.Abs(float64(fz)) > 0.5 {
		t.Errorf("lift at rest: floor = (%v,%v), want (≈0,true)", fz, ok)
	}
	// Risen 64: the same trace posed at org Z=64 reads world floor 64.
	org := [3]float32{0, 0, 64}
	if fz, ok := lift.FloorBelowAt(0, 0, 200, org); !ok || math.Abs(float64(fz-64)) > 0.5 {
		t.Errorf("lift risen: floor = (%v,%v), want (≈64,true)", fz, ok)
	}

	if _, err := BuildModel(b, 2); err == nil {
		t.Errorf("BuildModel(2) on a two-model BSP should error")
	}
}

// The scene query stands a rider on the posed lift hull rather than
// the shaft floor far below, leaves players outside the lift's posed
// AABB on the world floor, and degrades to the worldspawn-only answer
// with no movers.
func TestHeightAboveFloorBoxScene_LiftRider(t *testing.T) {
	b := liftSceneBSP(t)
	world, err := Build(b)
	if err != nil {
		t.Fatalf("Build world: %v", err)
	}
	lift, err := BuildModel(b, 1)
	if err != nil {
		t.Fatalf("BuildModel(1): %v", err)
	}

	// Rider: lift risen to -100, player origin resting on it.
	org := [3]float32{0, 0, -100}
	z := float32(PlayerFeetOffset) - 100 + 0.5
	movers := []PosedHull{{H: lift, Origin: org}}
	h, ok := HeightAboveFloorBoxScene(world, movers, 0, 0, z)
	if !ok || math.Abs(float64(h)) > 1.0 {
		t.Errorf("rider height = (%v,%v), want (≈0,true)", h, ok)
	}

	// Without the lift the same origin reads the shaft floor at -400.
	hNo, ok := HeightAboveFloorBoxScene(world, nil, 0, 0, z)
	if !ok || hNo < 300 {
		t.Errorf("no-mover height = (%v,%v), want the large shaft height", hNo, ok)
	}

	// A player horizontally outside the lift bounds (+16 box +slack)
	// is rejected by the posed AABB and stands on the world floor.
	far := float32(64 + playerBoxHalf + sceneAABBSlack + 8)
	hFar, ok := HeightAboveFloorBoxScene(world, movers, far+8, 0, z)
	if !ok || hFar < 300 {
		t.Errorf("outside-AABB height = (%v,%v), want the shaft height", hFar, ok)
	}

	// Player far beneath the lift, grounded on the shaft floor (origin
	// just above the clip plane at -376): the lift hull is above the
	// trace start and contributes nothing; the shaft floor answers ≈0.
	deep := float32(-376 + 0.5)
	hDeep, ok := HeightAboveFloorBoxScene(world, []PosedHull{{H: lift, Origin: [3]float32{0, 0, 64}}}, 0, 0, deep)
	if !ok || math.Abs(float64(hDeep)) > 1.0 {
		t.Errorf("under-lift height = (%v,%v), want (≈0,true)", hDeep, ok)
	}
}

// TestLoadForMap_FromBSP checks the runtime path: point the shared
// mapbsp loader at the vendored maps/ tree and build dm2's hull straight
// from its .bsp. Skips when maps/ is absent (CI has no BSPs — floor then
// degrades to absent, same as locvis visibility).
func TestLoadForMap_FromBSP(t *testing.T) {
	const dir = "../../maps"
	if _, err := os.Stat(dir + "/dm2.bsp"); err != nil {
		t.Skip("no vendored maps/dm2.bsp; skipping BSP-load test")
	}
	mapbsp.SetDir(dir)
	defer mapbsp.SetDir("")

	h, err := LoadForMap("dm2")
	if err != nil {
		t.Fatalf("LoadForMap(dm2): %v", err)
	}
	if len(h.nodes) == 0 || len(h.planes) == 0 {
		t.Fatalf("dm2 hull empty: %d nodes, %d planes", len(h.nodes), len(h.planes))
	}
	if _, err := LoadForMap("definitely-not-a-real-map-xyz"); err == nil {
		t.Errorf("expected error for missing map")
	}
}

// TestFloorBelow_RealBSP is a developer smoke test against a vendored
// BSP. It traces straight down from each player-spawn origin: a spawn
// point's origin is the standing player origin, so the floor under it
// must sit PlayerFeetOffset (24) below — within a couple of units after
// integer rounding and slope. Skips when the per-machine maps/ tree is
// absent (CI has no BSPs).
func TestFloorBelow_RealBSP(t *testing.T) {
	const path = "../../maps/dm2.bsp"
	if _, err := os.Stat(path); err != nil {
		t.Skip("no vendored maps/dm2.bsp; skipping real-BSP smoke test")
	}
	parsed, err := bsp.Parse(path)
	if err != nil {
		t.Fatalf("parse dm2.bsp: %v", err)
	}
	h, err := Build(parsed)
	if err != nil {
		t.Fatalf("Build dm2: %v", err)
	}
	ents, err := bsp.ReadEntities(path)
	if err != nil {
		t.Fatalf("ReadEntities dm2: %v", err)
	}
	tested, found := 0, 0
	for _, e := range ents {
		if e.Classname != "info_player_deathmatch" {
			continue
		}
		// Trace from a little above the spawn origin so we never start
		// exactly on a clip boundary.
		fz, ok := h.FloorBelow(e.Origin[0], e.Origin[1], e.Origin[2]+16)
		tested++
		if !ok {
			// Legitimate: some dm2 spawns rest on the RA/quad lift, a
			// moving brush model pruned from the worldspawn hull. No
			// static floor below is the correct answer here — the scene
			// query (HeightAboveFloorBoxScene) is what stands riders on
			// posed mover hulls.
			continue
		}
		found++
		gap := float64(e.Origin[2] - fz)
		if gap < 16 || gap > 32 {
			t.Errorf("spawn at %v: origin-floor gap %.1f, want ≈24", e.Origin, gap)
		}
	}
	if tested == 0 {
		t.Fatal("no spawn points found in dm2.bsp")
	}
	// The vast majority of spawns sit on static world floor; only a few
	// lift spawns legitimately miss. Guard against a wholesale failure
	// (e.g. a broken trace returning no floor everywhere).
	if found*2 < tested {
		t.Errorf("only %d/%d spawns found a static floor — trace likely broken", found, tested)
	}
	t.Logf("dm2 spawns: %d/%d on static floor (rest on lifts)", found, tested)
}
