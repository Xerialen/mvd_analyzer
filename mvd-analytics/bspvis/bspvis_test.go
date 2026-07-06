package bspvis

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// bspsDir returns the top-level bsps/ directory at the repo root. We
// resolve it from runtime.Caller so the test still works when invoked
// via `go test ./...` from anywhere in the workspace.
func bspsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	// thisFile = .../mvd-analytics/bspvis/bspvis_test.go
	// bspsDir  = .../bsps  (repo root)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "bsps")
}

// requireBSP loads the named BSP, skipping (not failing) if missing —
// the BSPs are gitignored and populated by `make bsps`, so they may not
// be present in every checkout.
func requireBSP(t *testing.T, name string) *BSP {
	t.Helper()
	path := filepath.Join(bspsDir(t), name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("BSP %s not available (%v) — run `make bsps` to populate the bsps/ directory", name, err)
	}
	b, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	return b
}

// Diagnostic floor points in world units (raw .loc coord ÷ 8). Each is
// a real .loc entry from mvd-analytics/loc/data/<map>.loc, picked so it
// sits well above the floor of a known room.
var floorPoints = map[string][3]float32{
	// dm6 "big" — chasm floor. Raw .loc: 8998 -8739 -319.
	"dm6.bsp": {8998. / 8, -8739. / 8, -319. / 8},
	// dm3 "bridge.low". Raw .loc: 11956 -284 -1343.
	"dm3.bsp": {11956. / 8, -284. / 8, -1343. / 8},
	// aerowalk "yard". Raw .loc: -1647 -757 192.
	"aerowalk.bsp": {-1647. / 8, -757. / 8, 192. / 8},
}

var farOutsidePoint = [3]float32{0, 0, 10000}

func TestLoad_AllMaps(t *testing.T) {
	for _, name := range []string{"dm6.bsp", "dm3.bsp", "aerowalk.bsp"} {
		name := name
		t.Run(name, func(t *testing.T) {
			b := requireBSP(t, name)
			if b.LeafCount() == 0 {
				t.Fatalf("%s: zero leaves", name)
			}
			if len(b.Nodes) == 0 {
				t.Fatalf("%s: zero nodes", name)
			}
			if len(b.Planes) == 0 {
				t.Fatalf("%s: zero planes", name)
			}
			if len(b.Models) == 0 {
				t.Fatalf("%s: zero models", name)
			}
			if got := b.LeafContents(0); got != ContentsSolid {
				t.Errorf("%s: leaf 0 contents = %d, want %d (CONTENTS_SOLID)", name, got, ContentsSolid)
			}
			if root := b.Models[0].HeadNodes[0]; root < 0 || int(root) >= len(b.Nodes) {
				t.Errorf("%s: Models[0].HeadNodes[0] = %d, want valid node in [0,%d)", name, root, len(b.Nodes))
			}
			t.Logf("%s flavour=%s nodes=%d leaves=%d planes=%d visBytes=%d",
				name, b.Version, len(b.Nodes), len(b.Leaves), len(b.Planes), len(b.VisData))
		})
	}
}

func TestPointInLeaf_FloorPointIsNotSolid(t *testing.T) {
	for name, pt := range floorPoints {
		name, pt := name, pt
		t.Run(name, func(t *testing.T) {
			b := requireBSP(t, name)
			leaf := b.PointInLeaf(pt)
			contents := b.LeafContents(leaf)
			if leaf == 0 || contents == ContentsSolid {
				t.Errorf("%s: floor point %v landed in leaf %d contents %d, want non-solid",
					name, pt, leaf, contents)
			}
			t.Logf("%s floor %v -> leaf %d contents %d", name, pt, leaf, contents)
		})
	}
}

func TestPointInLeaf_FarOutsideIsSolidish(t *testing.T) {
	for _, name := range []string{"dm6.bsp", "dm3.bsp", "aerowalk.bsp"} {
		name := name
		t.Run(name, func(t *testing.T) {
			b := requireBSP(t, name)
			leaf := b.PointInLeaf(farOutsidePoint)
			contents := b.LeafContents(leaf)
			if contents == ContentsEmpty {
				t.Errorf("%s: far-outside point %v landed in EMPTY leaf %d — traversal bug?",
					name, farOutsidePoint, leaf)
			}
			t.Logf("%s far-outside %v -> leaf %d contents %d", name, farOutsidePoint, leaf, contents)
		})
	}
}

func TestRayHitsSolid_ShortRayInEmptySpace(t *testing.T) {
	for name, pt := range floorPoints {
		name, pt := name, pt
		t.Run(name, func(t *testing.T) {
			b := requireBSP(t, name)
			a := pt
			c := [3]float32{pt[0] + 4, pt[1] + 4, pt[2] + 4}
			if b.RayHitsSolid(a, c) {
				t.Errorf("%s: 4-unit ray inside empty leaf reported HIT (a=%v c=%v)", name, a, c)
			}
		})
	}
}

func TestRayHitsSolid_FromEmptyToFarOutside(t *testing.T) {
	for name, pt := range floorPoints {
		name, pt := name, pt
		t.Run(name, func(t *testing.T) {
			b := requireBSP(t, name)
			if !b.RayHitsSolid(pt, farOutsidePoint) {
				t.Errorf("%s: ray from empty %v to far-outside %v did NOT report HIT", name, pt, farOutsidePoint)
			}
		})
	}
}

func TestLeafPVS_NonEmptyForLeaf1(t *testing.T) {
	for _, name := range []string{"dm6.bsp", "dm3.bsp", "aerowalk.bsp"} {
		name := name
		t.Run(name, func(t *testing.T) {
			b := requireBSP(t, name)
			row := b.LeafPVS(1)
			if len(row) == 0 {
				t.Fatalf("%s: PVS row for leaf 1 has zero length", name)
			}
			if !b.PVSContains(row, 1) {
				t.Errorf("%s: PVS for leaf 1 does not include itself", name)
			}
			t.Logf("%s PVS[leaf 1]: leaf count %d (row=%d bytes)",
				name, b.LeafCount(), len(row))
		})
	}
}

func TestLeafPVS_LeafZeroAllVisible(t *testing.T) {
	b := requireBSP(t, "dm6.bsp")
	row := b.LeafPVS(0)
	rowBytes := (b.LeafCount() + 7) >> 3
	if len(row) != rowBytes {
		t.Fatalf("leaf 0 row length = %d, want %d", len(row), rowBytes)
	}
	for i, by := range row {
		if by != 0xff {
			t.Errorf("leaf 0 row byte %d = %#x, want 0xff", i, by)
		}
	}
}

func TestPVSContains_OffByOne(t *testing.T) {
	row := []byte{0x01}
	if !((&BSP{}).PVSContains(row, 1)) {
		t.Errorf("PVS bit 0 should be leaf 1 (per cmodel.c:1144)")
	}
	if (&BSP{}).PVSContains(row, 2) {
		t.Errorf("PVS bit 1 should NOT be set in row=0x01")
	}
	if !((&BSP{}).PVSContains(row, 0)) {
		t.Errorf("PVSContains(row, 0) must report true (engine convention for solid sink)")
	}
}
