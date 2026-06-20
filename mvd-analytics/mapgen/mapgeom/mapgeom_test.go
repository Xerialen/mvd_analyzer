package mapgeom

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/mapgen/bsp"
	"github.com/mvd-analyzer/mvd-analytics/loc"
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

	// Two locs centered over the quads, one on each floor. ezquake's
	// addloc records the player origin (cl.simorg), which sits 24 above
	// the floor for a standing player — so loc.Z = floor.Z + 24. The
	// "RL" keyword is in ITEM_KEYWORDS so it stays uppercase after
	// normalization; "start" is generic so it gets lowercased.
	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 24, Name: "start"},
		{X: 32, Y: 32, Z: 152, Name: "RL"},
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
	// A quad fan-triangulates into 2 triangles → 2 × 9 = 18 float32s.
	if len(rl.Tris) != 18 {
		t.Errorf("RL.Tris len = %d, want 18", len(rl.Tris))
	}
	// Every vertex of the high quad carries its own Z (version 2).
	for i := 2; i < len(rl.Tris); i += 3 {
		if rl.Tris[i] != 128 {
			t.Errorf("RL.Tris[%d] (vertex z) = %v, want 128", i, rl.Tris[i])
		}
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

	// Only one loc, placed at standing-player height (24 above) the
	// low floor. Matching ezQuake's TP_LocationName, every face picks
	// its nearest loc with no rejection threshold — so the high floor
	// also maps to "start" (and is within the global ceiling cap
	// because the gap to the high floor is just at the threshold).
	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 24, Name: "start"},
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
	// Both quads → 2 faces × 2 triangles × 9 floats = 36 floats.
	if len(regions.Locs[0].Tris) != 36 {
		t.Errorf("unnamed.Tris len = %d, want 36", len(regions.Locs[0].Tris))
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

	// Single loc point at standing-player height (24 above) the low
	// floor. The floor (z=0) and the platform (z=128, exactly at the
	// global cap) are both kept; the ceiling (z=384, well above the
	// cap) is dropped. Cap = maxLocZ + ceilingMaxAboveLoc -
	//                       playerOriginAboveFloor
	//                     = 24 + 128 - 24 = 128.
	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 24, Name: "room"},
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
	// Floor (z=0) and platform (z=128) kept → 2 quads × 2 tris × 9
	// floats = 36.
	if got := len(regions.Locs[0].Tris); got != 36 {
		t.Errorf("room.Tris len = %d, want 36 (ceiling should be dropped)", got)
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
		{X: 32, Y: 32, Z: 24, Name: "lifts"},
		{X: 32, Y: 32, Z: 408, Name: "lifts"},
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
	// Both quads → 2 faces × 2 triangles × 9 floats = 36 floats.
	if got := len(regions.Locs[0].Tris); got != 36 {
		t.Errorf("lifts.Tris len = %d, want 36", got)
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
	regions, stats := Build("test", b, finder)
	if stats.FacesKept != 0 {
		t.Errorf("FacesKept = %d, want 0 (vertical face is not a floor)", stats.FacesKept)
	}
	// The vertical face is classified as a wall (one quad → 2 triangles) but
	// no longer stored — the occluding "solid" render it fed was removed.
	if stats.WallsKept != 1 {
		t.Errorf("WallsKept = %d, want 1", stats.WallsKept)
	}
	if stats.WallTris != 2 {
		t.Errorf("WallTris = %d, want 2", stats.WallTris)
	}
	if got := len(regions.Walls); got != 0 {
		t.Errorf("Walls len = %d, want 0 (walls no longer stored)", got)
	}
}

func TestBuildParams_StricterSlopeRejectsRamp(t *testing.T) {
	// A 40°-from-horizontal ramp (normal Z ≈ 0.766) is a floor at the
	// default threshold (0.7) but becomes a wall when the caller
	// tightens FloorNormalZ above it.
	b := &bsp.BSP{
		Version: 29,
		Planes: []bsp.Plane{
			{Normal: bsp.Vec3{X: 0.643, Y: 0, Z: 0.766}, Type: 2},
		},
		Vertices: []bsp.Vec3{
			{X: 0, Y: 0, Z: 0},
			{X: 64, Y: 0, Z: 54},
			{X: 64, Y: 64, Z: 54},
			{X: 0, Y: 64, Z: 0},
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
		{X: 32, Y: 32, Z: 51, Name: "ramp"},
	})

	_, defStats := Build("test", b, finder)
	if defStats.FacesKept != 1 || defStats.WallsKept != 0 {
		t.Errorf("default params: FacesKept=%d WallsKept=%d, want 1/0",
			defStats.FacesKept, defStats.WallsKept)
	}

	_, strict := BuildParams("test", b, finder, Params{FloorNormalZ: 0.8})
	if strict.FacesKept != 0 || strict.WallsKept != 1 {
		t.Errorf("FloorNormalZ=0.8: FacesKept=%d WallsKept=%d, want 0/1 (ramp reclassified as wall)",
			strict.FacesKept, strict.WallsKept)
	}
}

// buildCollinearFloorBSP constructs a single floor quad with a redundant
// vertex sitting exactly on the midpoint of the bottom edge — a pentagon
// ring (0,0)-(32,0)-(64,0)-(64,64)-(0,64) at z=0. Fan triangulation from
// vertex 0 produces three triangles; the first, (0,0)-(32,0)-(64,0), is
// collinear and therefore zero-area. Quake compilers emit exactly this
// shape where a T-junction was healed by splitting an edge.
func buildCollinearFloorBSP() *bsp.BSP {
	return &bsp.BSP{
		Version: 29,
		Planes: []bsp.Plane{
			{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}, Dist: 0, Type: 2},
		},
		Vertices: []bsp.Vec3{
			{X: 0, Y: 0, Z: 0},
			{X: 32, Y: 0, Z: 0}, // collinear mid-edge vertex
			{X: 64, Y: 0, Z: 0},
			{X: 64, Y: 64, Z: 0},
			{X: 0, Y: 64, Z: 0},
		},
		Edges: []bsp.Edge{
			{V: [2]uint32{0, 0}}, // sentinel
			{V: [2]uint32{0, 1}},
			{V: [2]uint32{1, 2}},
			{V: [2]uint32{2, 3}},
			{V: [2]uint32{3, 4}},
			{V: [2]uint32{4, 0}},
		},
		Surfedges: []int32{1, 2, 3, 4, 5},
		Faces: []bsp.Face{
			{PlaneID: 0, Side: 0, FirstEdge: 0, NumEdges: 5},
		},
		Models: []bsp.Model{
			{FirstFace: 0, NumFaces: 1},
		},
	}
}

func TestBuild_SkipsDegenerateTriangles(t *testing.T) {
	b := buildCollinearFloorBSP()
	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 24, Name: "room"},
	})

	regions, stats := Build("test", b, finder)

	// The pentagon fans into 3 triangles, one of which is collinear.
	if stats.DegenerateTris != 1 {
		t.Errorf("DegenerateTris = %d, want 1", stats.DegenerateTris)
	}
	if stats.Triangles != 2 {
		t.Errorf("Triangles = %d, want 2 (zero-area sliver dropped)", stats.Triangles)
	}
	if len(regions.Locs) != 1 {
		t.Fatalf("regions.Locs len = %d, want 1", len(regions.Locs))
	}
	// 2 kept triangles × 9 floats; the dropped sliver must not appear.
	if got := len(regions.Locs[0].Tris); got != 18 {
		t.Errorf("room.Tris len = %d, want 18", got)
	}
	// No emitted triangle may be zero-area.
	tris := regions.Locs[0].Tris
	for i := 0; i+8 < len(tris); i += 9 {
		a := bsp.Vec3{X: tris[i], Y: tris[i+1], Z: tris[i+2]}
		bb := bsp.Vec3{X: tris[i+3], Y: tris[i+4], Z: tris[i+5]}
		c := bsp.Vec3{X: tris[i+6], Y: tris[i+7], Z: tris[i+8]}
		if triArea(a, bb, c) < minTriArea {
			t.Errorf("triangle %d is degenerate (area %v)", i/9, triArea(a, bb, c))
		}
	}
}

// buildLiquidSubmodelBSP builds a worldspawn (model 0) with a plain floor,
// a '*water2' surface, a vertical '*lava1' face and a '*teleport' floor,
// plus a brush model (model 1) with one 'door01' face and one 'trigger'
// face. Each face carries its own texinfo→miptex name (TexNames[i] is the
// texture of face i).
func buildLiquidSubmodelBSP() *bsp.BSP {
	quad := func(base uint32) []bsp.Edge {
		return []bsp.Edge{
			{V: [2]uint32{base, base + 1}},
			{V: [2]uint32{base + 1, base + 2}},
			{V: [2]uint32{base + 2, base + 3}},
			{V: [2]uint32{base + 3, base}},
		}
	}
	edges := []bsp.Edge{{V: [2]uint32{0, 0}}} // sentinel
	for f := 0; f < 6; f++ {
		edges = append(edges, quad(uint32(f*4))...)
	}
	var surfedges []int32
	for i := int32(1); i <= 24; i++ {
		surfedges = append(surfedges, i)
	}
	faces := make([]bsp.Face, 6)
	for i := range faces {
		plane := uint32(0) // horizontal (up)
		if i == 2 || i == 4 {
			plane = 1 // vertical (lava face, door face)
		}
		faces[i] = bsp.Face{PlaneID: plane, FirstEdge: int32(i * 4), NumEdges: 4, TexinfoID: uint32(i)}
	}
	texinfos := make([]bsp.Texinfo, 6)
	for i := range texinfos {
		texinfos[i] = bsp.Texinfo{MipTex: int32(i)}
	}
	return &bsp.BSP{
		Version: 29,
		Planes: []bsp.Plane{
			{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}, Type: 2},
			{Normal: bsp.Vec3{X: 1, Y: 0, Z: 0}, Type: 0},
		},
		Vertices: []bsp.Vec3{
			{X: 0, Y: 0, Z: 0}, {X: 64, Y: 0, Z: 0}, {X: 64, Y: 64, Z: 0}, {X: 0, Y: 64, Z: 0}, // 0-3 floor
			{X: 0, Y: 0, Z: 64}, {X: 64, Y: 0, Z: 64}, {X: 64, Y: 64, Z: 64}, {X: 0, Y: 64, Z: 64}, // 4-7 water surface
			{X: 0, Y: 0, Z: 0}, {X: 0, Y: 64, Z: 0}, {X: 0, Y: 64, Z: 64}, {X: 0, Y: 0, Z: 64}, // 8-11 lava (vertical)
			{X: 128, Y: 0, Z: 0}, {X: 192, Y: 0, Z: 0}, {X: 192, Y: 64, Z: 0}, {X: 128, Y: 64, Z: 0}, // 12-15 teleport floor
			{X: 256, Y: 0, Z: 0}, {X: 256, Y: 64, Z: 0}, {X: 256, Y: 64, Z: 64}, {X: 256, Y: 0, Z: 64}, // 16-19 door (vertical)
			{X: 300, Y: 0, Z: 0}, {X: 364, Y: 0, Z: 0}, {X: 364, Y: 64, Z: 0}, {X: 300, Y: 64, Z: 0}, // 20-23 trigger
		},
		Edges:     edges,
		Surfedges: surfedges,
		Faces:     faces,
		Models: []bsp.Model{
			{FirstFace: 0, NumFaces: 4}, // worldspawn: floor, water, lava, teleport
			{FirstFace: 4, NumFaces: 2}, // brush model 1: door, trigger
		},
		Texinfos: texinfos,
		TexNames: []string{"floor1", "*water2", "*lava1", "*teleport", "door01", "trigger"},
	}
}

func TestBuild_LiquidsAndSubmodels(t *testing.T) {
	b := buildLiquidSubmodelBSP()
	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 24, Name: "room"},
		{X: 160, Y: 32, Z: 24, Name: "tp"},
	})

	regions, stats := Build("test", b, finder)

	if regions.Version != 4 {
		t.Errorf("Version = %d, want 4", regions.Version)
	}

	// Liquids: '*water2' surface and vertical '*lava1' — emitted water
	// then lava; '*teleport' is NOT a liquid.
	if len(regions.Liquids) != 2 {
		t.Fatalf("Liquids = %d meshes, want 2 (water, lava); got %+v", len(regions.Liquids), regions.Liquids)
	}
	if regions.Liquids[0].Kind != "water" || len(regions.Liquids[0].Tris) != 18 {
		t.Errorf("Liquids[0] = %q/%d tris, want water/18", regions.Liquids[0].Kind, len(regions.Liquids[0].Tris)/9)
	}
	if regions.Liquids[1].Kind != "lava" || len(regions.Liquids[1].Tris) != 18 {
		t.Errorf("Liquids[1] = %q/%d tris, want lava/18", regions.Liquids[1].Kind, len(regions.Liquids[1].Tris)/9)
	}
	if stats.LiquidFaces != 2 {
		t.Errorf("LiquidFaces = %d, want 2", stats.LiquidFaces)
	}

	// SubModels: brush model 1, door face only (trigger skipped).
	if len(regions.SubModels) != 1 {
		t.Fatalf("SubModels = %d, want 1", len(regions.SubModels))
	}
	if regions.SubModels[0].ID != 1 || len(regions.SubModels[0].Tris) != 18 {
		t.Errorf("SubModels[0] = ID %d/%d tris, want ID 1/18", regions.SubModels[0].ID, len(regions.SubModels[0].Tris)/9)
	}

	// Locs: the plain floor and the teleport floor stay walkable; the
	// water surface and lava face must NOT appear as floor or wall.
	names := map[string]bool{}
	for _, l := range regions.Locs {
		names[l.Name] = true
	}
	if !names["room"] || !names["tp"] {
		t.Errorf("loc regions = %v, want room + tp", names)
	}
	// The water surface (normal up, would otherwise be floor) was routed
	// to liquids, so "room" holds only the single plain floor quad.
	for _, l := range regions.Locs {
		if l.Name == "room" && len(l.Tris) != 18 {
			t.Errorf("room.Tris = %d tris, want 18 (water surface must not be a floor)", len(l.Tris)/9)
		}
	}
	// The vertical lava face was a liquid, not a wall.
	if len(regions.Walls) != 0 {
		t.Errorf("Walls = %d floats, want 0 (lava is a liquid, door is a submodel)", len(regions.Walls))
	}
}

func TestBuildParams_RoofCapTunable(t *testing.T) {
	// Stacked trio: floor z=0, platform z=128 (at the default cap),
	// ceiling z=384. Lowering CeilingMaxAboveLoc to 100 also drops the
	// platform; raising it to 400 keeps all three.
	b := buildStackedTrioBSP()
	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 24, Name: "room"},
	})

	_, low := BuildParams("test", b, finder, Params{CeilingMaxAboveLoc: 100})
	if low.FacesKept != 1 || low.FacesCeiling != 2 {
		t.Errorf("cap=100: FacesKept=%d FacesCeiling=%d, want 1/2", low.FacesKept, low.FacesCeiling)
	}

	_, high := BuildParams("test", b, finder, Params{CeilingMaxAboveLoc: 400})
	if high.FacesKept != 3 || high.FacesCeiling != 0 {
		t.Errorf("cap=400: FacesKept=%d FacesCeiling=%d, want 3/0", high.FacesKept, high.FacesCeiling)
	}
}
