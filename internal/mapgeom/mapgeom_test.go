package mapgeom

import (
	"testing"

	"github.com/mvd-analyzer/internal/bsp"
	"github.com/mvd-analyzer/internal/loc"
)

// buildTwoFloorBSP constructs an in-memory BSP containing two quads
// stacked vertically: the "low" quad at z=0 and the "high" quad at
// z=128. Both are worldspawn faces.
//
// Layout:
//
//	vertices 0..3 → low quad  (z=0)   at (0..64, 0..64, 0)
//	vertices 4..7 → high quad (z=128) at (0..64, 0..64, 128)
func buildTwoFloorBSP() *bsp.BSP {
	return &bsp.BSP{
		Version: 29,
		Planes: []bsp.Plane{
			{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}, Dist: 0, Type: 2},
			{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}, Dist: 128, Type: 2},
		},
		Vertices: []bsp.Vec3{
			{X: 0, Y: 0, Z: 0},
			{X: 64, Y: 0, Z: 0},
			{X: 64, Y: 64, Z: 0},
			{X: 0, Y: 64, Z: 0},
			{X: 0, Y: 0, Z: 128},
			{X: 64, Y: 0, Z: 128},
			{X: 64, Y: 64, Z: 128},
			{X: 0, Y: 64, Z: 128},
		},
		Edges: []bsp.Edge{
			{V: [2]uint32{0, 0}}, // sentinel
			{V: [2]uint32{0, 1}},
			{V: [2]uint32{1, 2}},
			{V: [2]uint32{2, 3}},
			{V: [2]uint32{3, 0}},
			{V: [2]uint32{4, 5}},
			{V: [2]uint32{5, 6}},
			{V: [2]uint32{6, 7}},
			{V: [2]uint32{7, 4}},
		},
		Surfedges: []int32{
			1, 2, 3, 4, // low face
			5, 6, 7, 8, // high face
		},
		Faces: []bsp.Face{
			{PlaneID: 0, Side: 0, FirstEdge: 0, NumEdges: 4},
			{PlaneID: 1, Side: 0, FirstEdge: 4, NumEdges: 4},
		},
		Models: []bsp.Model{
			{FirstFace: 0, NumFaces: 2},
		},
	}
}

func TestBuild_AssignsFacesToCorrectLoc(t *testing.T) {
	b := buildTwoFloorBSP()

	// Two locs centered over the quads, one on each floor. The "RL"
	// keyword is in ITEM_KEYWORDS so it stays uppercase after
	// normalization; "start" is generic so it gets lowercased.
	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 0, Name: "start"},
		{X: 32, Y: 32, Z: 128, Name: "RL"},
	})

	regions, stats := Build("test", b, finder)

	if stats.FacesTotal != 2 {
		t.Errorf("FacesTotal = %d, want 2", stats.FacesTotal)
	}
	if stats.FacesKept != 2 {
		t.Errorf("FacesKept = %d, want 2", stats.FacesKept)
	}
	if stats.Locs != 2 {
		t.Fatalf("Locs = %d, want 2", stats.Locs)
	}
	if len(regions.Locs) != 2 {
		t.Fatalf("regions.Locs len = %d, want 2", len(regions.Locs))
	}

	byName := map[string]LocRegion{}
	for _, l := range regions.Locs {
		byName[l.Name] = l
	}

	rl, ok := byName["RL"]
	if !ok {
		t.Fatalf("missing RL region, got %+v", byName)
	}
	if rl.Z != 128 {
		t.Errorf("RL.Z = %v, want 128", rl.Z)
	}
	// A quad fan-triangulates into 2 triangles → 12 float32s.
	if len(rl.Tris) != 12 {
		t.Errorf("RL.Tris len = %d, want 12", len(rl.Tris))
	}

	start, ok := byName["start"]
	if !ok {
		t.Fatalf("missing start region, got %+v", byName)
	}
	if start.Z != 0 {
		t.Errorf("start.Z = %v, want 0", start.Z)
	}

	// Bounds should cover the XY footprint of both quads.
	if regions.Bounds.MinX != 0 || regions.Bounds.MaxX != 64 {
		t.Errorf("bounds X = (%v,%v), want (0,64)", regions.Bounds.MinX, regions.Bounds.MaxX)
	}
	if regions.Bounds.MinY != 0 || regions.Bounds.MaxY != 64 {
		t.Errorf("bounds Y = (%v,%v), want (0,64)", regions.Bounds.MinY, regions.Bounds.MaxY)
	}
}

func TestBuild_SingleLocClaimsAllFloors(t *testing.T) {
	b := buildTwoFloorBSP()

	// Only one loc, placed on the low floor. Matching ezQuake's
	// TP_LocationName, every face picks its nearest loc with no
	// rejection threshold — so the high floor also maps to "start".
	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 0, Name: "start"},
	})

	regions, stats := Build("test", b, finder)

	if stats.FacesKept != 2 {
		t.Errorf("FacesKept = %d, want 2", stats.FacesKept)
	}
	if stats.FacesUnnamed != 0 {
		t.Errorf("FacesUnnamed = %d, want 0", stats.FacesUnnamed)
	}
	if len(regions.Locs) != 1 {
		t.Fatalf("regions.Locs len = %d, want 1 (start only)", len(regions.Locs))
	}
	if regions.Locs[0].Name != "start" {
		t.Errorf("regions.Locs[0].Name = %q, want \"start\"", regions.Locs[0].Name)
	}
}

func TestBuild_NoFinderEmitsUnnamedBackdrop(t *testing.T) {
	b := buildTwoFloorBSP()

	regions, stats := Build("test", b, nil)

	if stats.FacesKept != 2 {
		t.Errorf("FacesKept = %d, want 2", stats.FacesKept)
	}
	if stats.FacesUnnamed != 2 {
		t.Errorf("FacesUnnamed = %d, want 2", stats.FacesUnnamed)
	}
	if len(regions.Locs) != 1 {
		t.Fatalf("regions.Locs len = %d, want 1 (unnamed only)", len(regions.Locs))
	}
	if regions.Locs[0].Name != UnnamedRegionKey {
		t.Errorf("region name = %q, want UnnamedRegionKey", regions.Locs[0].Name)
	}
	// Both quads → 2 faces × 2 triangles × 6 floats = 24 floats.
	if len(regions.Locs[0].Tris) != 24 {
		t.Errorf("unnamed.Tris len = %d, want 24", len(regions.Locs[0].Tris))
	}
	if regions.Bounds.MinX != 0 || regions.Bounds.MaxX != 64 {
		t.Errorf("bounds X = (%v,%v), want (0,64)", regions.Bounds.MinX, regions.Bounds.MaxX)
	}
}

// buildStackedTrioBSP constructs a single-loc-group BSP with three
// horizontal quads stacked at the same XY footprint (0..64, 0..64) at
// z=0 (floor), z=128 (platform within threshold), and z=384 (ceiling
// well above threshold). Used by TestBuild_DropsCeilingAboveFloor.
func buildStackedTrioBSP() *bsp.BSP {
	return &bsp.BSP{
		Version: 29,
		Planes: []bsp.Plane{
			{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}, Dist: 0, Type: 2},
			{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}, Dist: 128, Type: 2},
			{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}, Dist: 384, Type: 2},
		},
		Vertices: []bsp.Vec3{
			{X: 0, Y: 0, Z: 0}, {X: 64, Y: 0, Z: 0},
			{X: 64, Y: 64, Z: 0}, {X: 0, Y: 64, Z: 0},
			{X: 0, Y: 0, Z: 128}, {X: 64, Y: 0, Z: 128},
			{X: 64, Y: 64, Z: 128}, {X: 0, Y: 64, Z: 128},
			{X: 0, Y: 0, Z: 384}, {X: 64, Y: 0, Z: 384},
			{X: 64, Y: 64, Z: 384}, {X: 0, Y: 64, Z: 384},
		},
		Edges: []bsp.Edge{
			{V: [2]uint32{0, 0}}, // sentinel
			{V: [2]uint32{0, 1}}, {V: [2]uint32{1, 2}},
			{V: [2]uint32{2, 3}}, {V: [2]uint32{3, 0}},
			{V: [2]uint32{4, 5}}, {V: [2]uint32{5, 6}},
			{V: [2]uint32{6, 7}}, {V: [2]uint32{7, 4}},
			{V: [2]uint32{8, 9}}, {V: [2]uint32{9, 10}},
			{V: [2]uint32{10, 11}}, {V: [2]uint32{11, 8}},
		},
		Surfedges: []int32{
			1, 2, 3, 4, // floor
			5, 6, 7, 8, // platform
			9, 10, 11, 12, // ceiling
		},
		Faces: []bsp.Face{
			{PlaneID: 0, Side: 0, FirstEdge: 0, NumEdges: 4},
			{PlaneID: 1, Side: 0, FirstEdge: 4, NumEdges: 4},
			{PlaneID: 2, Side: 0, FirstEdge: 8, NumEdges: 4},
		},
		Models: []bsp.Model{
			{FirstFace: 0, NumFaces: 3},
		},
	}
}

func TestBuild_DropsCeilingAboveFloor(t *testing.T) {
	b := buildStackedTrioBSP()

	// Single loc point at z=0. The floor (z=0) and the platform
	// (z=128, exactly at threshold) are both kept; the ceiling
	// (z=384, 384u above the loc) is dropped.
	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 0, Name: "room"},
	})

	regions, stats := Build("test", b, finder)

	if stats.FacesTotal != 3 {
		t.Errorf("FacesTotal = %d, want 3", stats.FacesTotal)
	}
	if stats.FacesKept != 2 {
		t.Errorf("FacesKept = %d, want 2 (ceiling dropped)", stats.FacesKept)
	}
	if stats.FacesCeiling != 1 {
		t.Errorf("FacesCeiling = %d, want 1", stats.FacesCeiling)
	}
	if len(regions.Locs) != 1 {
		t.Fatalf("regions.Locs len = %d, want 1", len(regions.Locs))
	}
	// Floor (z=0) and platform (z=128) kept → 2 quads × 2 tris × 6
	// floats = 24.
	if got := len(regions.Locs[0].Tris); got != 24 {
		t.Errorf("room.Tris len = %d, want 24 (ceiling should be dropped)", got)
	}
}

func TestBuild_MultiLevelRegionKeepsAllFloors(t *testing.T) {
	// Stacked quads at z=0 and z=384 (well beyond the 128 threshold).
	// Both loc points share the same name "lifts" — a single region
	// with two vertical levels anchored by one loc point per level.
	// Both faces must be kept because each face's nearest loc point
	// sits at its own level.
	b := buildTwoFloorBSP()
	// Override the high plane/verts so the gap is 384, not 128, to
	// prove the threshold applies per-loc-point and not per-region.
	b.Planes[1] = bsp.Plane{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}, Dist: 384, Type: 2}
	for i := 4; i < 8; i++ {
		b.Vertices[i].Z = 384
	}

	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 0, Name: "lifts"},
		{X: 32, Y: 32, Z: 384, Name: "lifts"},
	})

	regions, stats := Build("test", b, finder)

	if stats.FacesCeiling != 0 {
		t.Errorf("FacesCeiling = %d, want 0 (multi-level region must keep all floors)", stats.FacesCeiling)
	}
	if stats.FacesKept != 2 {
		t.Errorf("FacesKept = %d, want 2", stats.FacesKept)
	}
	if len(regions.Locs) != 1 {
		t.Fatalf("regions.Locs len = %d, want 1 (single region 'lifts')", len(regions.Locs))
	}
	// Both quads → 2 faces × 2 triangles × 6 floats = 24 floats.
	if got := len(regions.Locs[0].Tris); got != 24 {
		t.Errorf("lifts.Tris len = %d, want 24", got)
	}
}

func TestBuild_RejectsNonFloorFaces(t *testing.T) {
	// Walls (vertical plane) should be rejected by the normal test.
	b := &bsp.BSP{
		Version: 29,
		Planes: []bsp.Plane{
			{Normal: bsp.Vec3{X: 1, Y: 0, Z: 0}}, // vertical
		},
		Vertices: []bsp.Vec3{
			{X: 0, Y: 0, Z: 0},
			{X: 0, Y: 64, Z: 0},
			{X: 0, Y: 64, Z: 64},
			{X: 0, Y: 0, Z: 64},
		},
		Edges: []bsp.Edge{
			{V: [2]uint32{0, 0}},
			{V: [2]uint32{0, 1}},
			{V: [2]uint32{1, 2}},
			{V: [2]uint32{2, 3}},
			{V: [2]uint32{3, 0}},
		},
		Surfedges: []int32{1, 2, 3, 4},
		Faces:     []bsp.Face{{PlaneID: 0, FirstEdge: 0, NumEdges: 4}},
		Models:    []bsp.Model{{FirstFace: 0, NumFaces: 1}},
	}
	finder := loc.NewFinder("test", []loc.Location{
		{X: 0, Y: 32, Z: 32, Name: "wall"},
	})
	_, stats := Build("test", b, finder)
	if stats.FacesKept != 0 {
		t.Errorf("FacesKept = %d, want 0 (vertical face)", stats.FacesKept)
	}
}
