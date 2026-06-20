package mapgeom

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/mapgen/bsp"
	"github.com/mvd-analyzer/mvd-analytics/loc"
)

// buildSingleFloorBSP is one floor quad (0..64, 0..64) at z=0.
func buildSingleFloorBSP() *bsp.BSP {
	return &bsp.BSP{
		Version: 29,
		Planes:  []bsp.Plane{{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}, Dist: 0, Type: 2}},
		Vertices: []bsp.Vec3{
			{X: 0, Y: 0, Z: 0}, {X: 64, Y: 0, Z: 0}, {X: 64, Y: 64, Z: 0}, {X: 0, Y: 64, Z: 0},
		},
		Edges: []bsp.Edge{
			{V: [2]uint32{0, 0}},
			{V: [2]uint32{0, 1}}, {V: [2]uint32{1, 2}}, {V: [2]uint32{2, 3}}, {V: [2]uint32{3, 0}},
		},
		Surfedges: []int32{1, 2, 3, 4},
		Faces:     []bsp.Face{{PlaneID: 0, FirstEdge: 0, NumEdges: 4}},
		Models:    []bsp.Model{{FirstFace: 0, NumFaces: 1}},
	}
}

func TestBuildPruned_DropsUntouchedFloor(t *testing.T) {
	// Two stacked quads at the same XY footprint: low z=0, high z=128.
	b := buildTwoFloorBSP()
	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 24, Name: "start"},
		{X: 32, Y: 32, Z: 152, Name: "RL"},
	})

	// A player stood only on the low floor.
	u := NewFloorUsage()
	u.AddDemo()
	u.AddPoint(32, 32, 0)

	regions, stats := BuildPruned("test", b, finder, Params{}, u)

	if stats.FacesPruned != 1 {
		t.Errorf("FacesPruned = %d, want 1 (high floor untouched)", stats.FacesPruned)
	}
	if stats.FacesKept != 1 {
		t.Errorf("FacesKept = %d, want 1", stats.FacesKept)
	}
	names := map[string]bool{}
	for _, l := range regions.Locs {
		names[l.Name] = true
	}
	if !names["start"] || names["RL"] {
		t.Errorf("loc regions = %v, want start only (RL pruned)", names)
	}
	if regions.Pruned == nil || regions.Pruned.Demos != 1 || regions.Pruned.Points != 1 || regions.Pruned.FacesDropped != 1 {
		t.Errorf("Pruned = %+v, want {Demos:1 Points:1 FacesDropped:1}", regions.Pruned)
	}
}

func TestBuildPruned_XYTolerance(t *testing.T) {
	finder := loc.NewFinder("test", []loc.Location{{X: 32, Y: 32, Z: 24, Name: "room"}})

	// 20 units past the +X edge — within the default reach (24) → kept.
	near := NewFloorUsage()
	near.AddDemo()
	near.AddPoint(84, 32, 0)
	if _, s := BuildPruned("t", buildSingleFloorBSP(), finder, Params{}, near); s.FacesPruned != 0 || s.FacesKept != 1 {
		t.Errorf("20u outside: FacesPruned=%d FacesKept=%d, want 0/1 (kept)", s.FacesPruned, s.FacesKept)
	}

	// 30 units past the edge — beyond reach → pruned.
	far := NewFloorUsage()
	far.AddDemo()
	far.AddPoint(94, 32, 0)
	if _, s := BuildPruned("t", buildSingleFloorBSP(), finder, Params{}, far); s.FacesPruned != 1 || s.FacesKept != 0 {
		t.Errorf("30u outside: FacesPruned=%d FacesKept=%d, want 1/0 (pruned)", s.FacesPruned, s.FacesKept)
	}
}

func TestBuildPruned_ZMismatchDoesNotKeep(t *testing.T) {
	finder := loc.NewFinder("test", []loc.Location{{X: 32, Y: 32, Z: 24, Name: "room"}})
	// Right XY, but the contact Z is 128 above the z=0 quad's plane.
	u := NewFloorUsage()
	u.AddDemo()
	u.AddPoint(32, 32, 128)
	if _, s := BuildPruned("t", buildSingleFloorBSP(), finder, Params{}, u); s.FacesPruned != 1 || s.FacesKept != 0 {
		t.Errorf("z-mismatch: FacesPruned=%d FacesKept=%d, want 1/0", s.FacesPruned, s.FacesKept)
	}
}

func TestBuildPruned_NilUsageMatchesBuildParams(t *testing.T) {
	b := buildTwoFloorBSP()
	finder := loc.NewFinder("test", []loc.Location{
		{X: 32, Y: 32, Z: 24, Name: "start"},
		{X: 32, Y: 32, Z: 152, Name: "RL"},
	})

	rp, sp := BuildParams("test", b, finder, DefaultParams())
	rn, sn := BuildPruned("test", b, finder, DefaultParams(), nil)

	if sp.FacesKept != sn.FacesKept || len(rp.Locs) != len(rn.Locs) {
		t.Errorf("nil-usage BuildPruned differs from BuildParams: kept %d vs %d, locs %d vs %d",
			sp.FacesKept, sn.FacesKept, len(rp.Locs), len(rn.Locs))
	}
	if rn.Pruned != nil {
		t.Errorf("nil usage must not set Pruned, got %+v", rn.Pruned)
	}
}

func TestDistPointToPolygonXY(t *testing.T) {
	ring := []bsp.Vec3{{X: 0, Y: 0}, {X: 64, Y: 0}, {X: 64, Y: 64}, {X: 0, Y: 64}}
	if d, _, _ := distPointToPolygonXY(32, 32, ring); d != 0 {
		t.Errorf("inside: dist = %v, want 0", d)
	}
	if d, cx, cy := distPointToPolygonXY(84, 32, ring); d != 20 || cx != 64 || cy != 32 {
		t.Errorf("outside +x: dist=%v nearest=(%v,%v), want 20 (64,32)", d, cx, cy)
	}
}
