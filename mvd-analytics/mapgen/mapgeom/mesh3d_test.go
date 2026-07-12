package mapgeom

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/mapgen/bsp"
)

// floorAndWallBSP is one floor quad (z=0 plane, normal +Z) plus one wall quad
// (x=0 plane, normal +X), both in worldspawn (model 0). Build keeps only the
// floor; BuildMesh3D keeps both.
func floorAndWallBSP() *bsp.BSP {
	return &bsp.BSP{
		Version: 29,
		Planes: []bsp.Plane{
			{Normal: bsp.Vec3{X: 0, Y: 0, Z: 1}}, // floor
			{Normal: bsp.Vec3{X: 1, Y: 0, Z: 0}}, // wall
		},
		Vertices: []bsp.Vec3{
			{X: 0, Y: 0, Z: 0}, {X: 64, Y: 0, Z: 0}, {X: 64, Y: 64, Z: 0}, {X: 0, Y: 64, Z: 0}, // floor
			{X: 0, Y: 0, Z: 0}, {X: 0, Y: 64, Z: 0}, {X: 0, Y: 64, Z: 64}, {X: 0, Y: 0, Z: 64}, // wall
		},
		Edges: []bsp.Edge{
			{V: [2]uint32{0, 0}},                                                                   // sentinel
			{V: [2]uint32{0, 1}}, {V: [2]uint32{1, 2}}, {V: [2]uint32{2, 3}}, {V: [2]uint32{3, 0}}, // floor
			{V: [2]uint32{4, 5}}, {V: [2]uint32{5, 6}}, {V: [2]uint32{6, 7}}, {V: [2]uint32{7, 4}}, // wall
		},
		Surfedges: []int32{1, 2, 3, 4, 5, 6, 7, 8},
		Faces: []bsp.Face{
			{PlaneID: 0, FirstEdge: 0, NumEdges: 4},
			{PlaneID: 1, FirstEdge: 4, NumEdges: 4},
		},
		Models: []bsp.Model{{FirstFace: 0, NumFaces: 2}},
	}
}

func TestBuildMesh3D_KeepsWallsAndFloors(t *testing.T) {
	m := BuildMesh3D("test", floorAndWallBSP())
	if m.Faces != 2 {
		t.Errorf("Faces = %d, want 2 (floor + wall both kept)", m.Faces)
	}
	if m.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", m.Skipped)
	}
	// Two quads → four fan triangles → 36 floats.
	if got := len(m.Tris); got != 36 {
		t.Fatalf("len(Tris) = %d, want 36", got)
	}
	want := Bounds3D{MinX: 0, MinY: 0, MinZ: 0, MaxX: 64, MaxY: 64, MaxZ: 64}
	if m.Bounds != want {
		t.Errorf("Bounds = %+v, want %+v", m.Bounds, want)
	}
}

func TestBuildMesh3D_SkipsSkyLiquidTrigger(t *testing.T) {
	cases := []struct{ name, tex string }{
		{"sky", "sky1"},
		{"liquid", "*04water"},
		{"trigger", "trigger"},
	}
	for _, tc := range cases {
		b := floorAndWallBSP()
		// Floor keeps a benign texture; only the wall (index 1) gets the
		// skip texture, so exactly one face is dropped.
		b.Texinfos = []bsp.Texinfo{{MipTex: 0}, {MipTex: 1}}
		b.TexNames = []string{"base", tc.tex}
		b.Faces[0].TexinfoID = 0
		b.Faces[1].TexinfoID = 1
		m := BuildMesh3D("test", b)
		if m.Faces != 1 {
			t.Errorf("%s: Faces = %d, want 1 (only the floor)", tc.name, m.Faces)
		}
		if m.Skipped != 1 {
			t.Errorf("%s: Skipped = %d, want 1", tc.name, m.Skipped)
		}
	}
}

func TestEncodeMesh3D_RoundTrip(t *testing.T) {
	m := BuildMesh3D("test", floorAndWallBSP())
	blob := EncodeMesh3D(m)

	if want := 36 + len(m.Tris)*4; len(blob) != want {
		t.Fatalf("len(blob) = %d, want %d", len(blob), want)
	}
	if string(blob[0:4]) != "M3D1" {
		t.Errorf("magic = %q, want M3D1", blob[0:4])
	}
	if v := binary.LittleEndian.Uint32(blob[4:8]); v != Mesh3DVersion {
		t.Errorf("version = %d, want %d", v, Mesh3DVersion)
	}
	if tc := binary.LittleEndian.Uint32(blob[8:12]); tc != uint32(len(m.Tris)/9) {
		t.Errorf("triCount = %d, want %d", tc, len(m.Tris)/9)
	}
	// First bounds float (minX) must round-trip.
	if minX := math.Float32frombits(binary.LittleEndian.Uint32(blob[12:16])); minX != m.Bounds.MinX {
		t.Errorf("bounds minX = %v, want %v", minX, m.Bounds.MinX)
	}
}
