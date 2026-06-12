package locvis

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/loc"
)

// bspsDir is the repo-root bsps/ directory. We resolve it via
// runtime.Caller so the test works regardless of which package
// directory `go test` was invoked from.
func bspsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	// thisFile = .../mvd-analytics/locvis/locvis_test.go
	// bspsDir  = .../bsps  (repo root)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "bsps")
}

// requireBspDir skips the test when bsps/<map>.bsp isn't present —
// the BSP corpus is gitignored and only populated by `make bsps`.
func requireBspDir(t *testing.T, mapName string) string {
	t.Helper()
	dir := bspsDir(t)
	if _, err := os.Stat(filepath.Join(dir, mapName+".bsp")); err != nil {
		t.Skipf("bsps/%s.bsp not available (%v) — run `make bsps`", mapName, err)
	}
	return dir
}

// TestColjMHLowWallBleed reproduces the canonical validated case from
// experiments/locattr/V2b-V6-HANDOFF.md:
//
// D2.coj at 06:11 on demo 216406 (e1m2), mid-jump over cross-water,
// gets attributed to MH.low (loc#90) by V1 for 23 consecutive samples.
// All 23 are wall-bleed: MH.low's loc-point sits behind a wall from the
// jump trajectory. V6 (PVS-veto) rejects MH.low and picks cross.water.
//
// Player position used here is the midpoint of the 23-sample window
// (player position ranged roughly (390..394, 120..230, 280..308) during
// the bleed). Loc-name expectations match the .loc file after variable
// substitution — see mvd-analytics/loc/data/e1m2.loc.
func TestColjMHLowWallBleed_PrototypePosition(t *testing.T) {
	bspDir := requireBspDir(t, "e1m2")

	SetBspDir(bspDir)
	defer SetBspDir("")

	const playerX, playerY, playerZ = 392, 175, 295

	// V1 baseline — the bare loc.Finder. Must reproduce the wall-bleed
	// label MH.low or the corpus has shifted (re-derive fixture).
	baseFinder, err := loc.LoadForMap("e1m2")
	if err != nil {
		t.Fatalf("loc.LoadForMap(e1m2): %v", err)
	}
	v1Label := baseFinder.FindNearest(playerX, playerY, playerZ)
	if v1Label != "MH.low" {
		t.Fatalf("V1 baseline label = %q, want %q — corpus drift, refresh fixture from V2b-V6-HANDOFF.md", v1Label, "MH.low")
	}

	// Locvis Finder loaded for the same map.
	f, err := LoadForMap("e1m2")
	if err != nil {
		t.Fatalf("LoadForMap(e1m2): %v", err)
	}
	if !f.HasVisibility() {
		t.Fatalf("expected HasVisibility=true (BSP was found in %s)", bspDir)
	}

	// V6 (PVS-veto) must pick cross.water.
	v6Label := f.attributeV6(playerX, playerY, playerZ)
	if v6Label != "cross.water" {
		t.Errorf("V6 label = %q, want %q (wall-bleed should be suppressed)", v6Label, "cross.water")
	}

	// The dispatch under the default ActiveAlgorithm must agree with
	// whichever variant is selected at compile time.
	dispatched := f.FindNearest(playerX, playerY, playerZ)
	switch ActiveAlgorithm {
	case AlgoV1:
		if dispatched != v1Label {
			t.Errorf("FindNearest under AlgoV1 = %q, want %q", dispatched, v1Label)
		}
	case AlgoV6:
		if dispatched != v6Label {
			t.Errorf("FindNearest under AlgoV6 = %q, want %q", dispatched, v6Label)
		}
	}
}

// TestFloorPointPicksItself sanity-checks that V6 does NOT suppress
// the obvious correct answer on a clean indoor position. We use a loc
// point known to be on a regularly-visited floor; standing right at it
// (player position == loc position) must return that loc's name.
func TestFloorPointPicksItself(t *testing.T) {
	bspDir := requireBspDir(t, "dm6")
	SetBspDir(bspDir)
	defer SetBspDir("")

	f, err := LoadForMap("dm6")
	if err != nil {
		t.Fatalf("LoadForMap(dm6): %v", err)
	}
	if !f.HasVisibility() {
		t.Fatalf("expected HasVisibility=true")
	}

	locs := f.Locations()
	if len(locs) == 0 {
		t.Fatalf("dm6 has no locs?")
	}
	// Pick the first loc whose leaf is non-solid and appears in its
	// own leaf's visible list (the trivial case — loc visible from
	// the leaf containing it).
	target := -1
	for L := 1; L < len(f.leafVisLocs); L++ {
		if len(f.leafVisLocs[L]) == 0 {
			continue
		}
		target = int(f.leafVisLocs[L][0])
		break
	}
	if target < 0 {
		t.Skipf("dm6: no leaf has visible locs? unexpected")
	}
	want := locs[target].Name
	got := f.attributeV6(locs[target].X, locs[target].Y, locs[target].Z)
	if got != want {
		t.Errorf("V6 standing-on-loc: got %q want %q (loc#%d)", got, want, target)
	}
}

// TestFallbackWithoutBSP confirms that locvis.Finder is bit-identical
// to loc.Finder when no BSP is available (HasVisibility() == false).
func TestFallbackWithoutBSP(t *testing.T) {
	// Point at an empty dir so the BSP load fails for every map.
	tmp := t.TempDir()
	SetBspDir(tmp)
	defer SetBspDir("")

	// Clear MVDA_BSP_DIR too, in case the developer environment sets it.
	prevEnv, hadEnv := os.LookupEnv("MVDA_BSP_DIR")
	os.Setenv("MVDA_BSP_DIR", tmp)
	defer func() {
		if hadEnv {
			os.Setenv("MVDA_BSP_DIR", prevEnv)
		} else {
			os.Unsetenv("MVDA_BSP_DIR")
		}
	}()

	// Use a map that's certain to be in the embedded loc corpus.
	const mapName = "dm6"

	f, err := LoadForMap(mapName)
	if err != nil {
		t.Fatalf("LoadForMap(%s): %v", mapName, err)
	}
	if f.HasVisibility() {
		t.Fatalf("HasVisibility = true, expected false with empty BSP dir")
	}

	base, err := loc.LoadForMap(mapName)
	if err != nil {
		t.Fatalf("loc.LoadForMap(%s): %v", mapName, err)
	}

	// Sample a handful of arbitrary points; FindNearest must agree
	// exactly with the bare loc.Finder.
	points := [][3]float32{
		{0, 0, 0},
		{1000, -1000, 0},
		{-500, 200, -100},
		{12345, 6789, -42},
	}
	for _, p := range points {
		got := f.FindNearest(p[0], p[1], p[2])
		want := base.FindNearest(p[0], p[1], p[2])
		if got != want {
			t.Errorf("FindNearest(%v): locvis=%q, loc=%q (must match without BSP)", p, got, want)
		}
	}
}
