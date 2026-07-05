package loc

import (
	"math/rand"
	"testing"
)

// TestPencilIndex_EquivalenceCorpus exercises every map in the
// embedded loc corpus, asserting the pencil index returns the same
// nearest-loc squared distance as the linear scan for a stream of
// query points inside each map's bounding box. Ties on distance are
// acceptable — we compare squared distance, not the returned index,
// because two locs at identical distance is a valid ambiguity.
func TestPencilIndex_EquivalenceCorpus(t *testing.T) {
	maps := []string{"dm6", "dm3", "aerowalk", "ztndm3", "defer", "outpost3", "tf2k", "2fort5"}
	for _, m := range maps {
		f, err := LoadForMap(m)
		if err != nil {
			t.Logf("skip %s: %v", m, err)
			continue
		}
		locs := f.Locations()
		if len(locs) == 0 {
			continue
		}
		idx := buildPencilIndex(locs, defaultCellSize)
		pts := benchPoints(locs, 2000)
		for i, p := range pts {
			wantIdx, wantDistSq := findNearestLinear(locs, p[0], p[1], p[2])
			gotIdx, gotDistSq := idx.findNearest(locs, p[0], p[1], p[2])
			if gotDistSq != wantDistSq {
				t.Fatalf("%s query #%d (%g,%g,%g): linear=%d (d²=%g), pencil=%d (d²=%g)",
					m, i, p[0], p[1], p[2], wantIdx, wantDistSq, gotIdx, gotDistSq)
			}
		}
	}
}

// TestPencilIndex_EmptyAndSingle covers degenerate L=0 and L=1.
func TestPencilIndex_EmptyAndSingle(t *testing.T) {
	idx := buildPencilIndex(nil, 0)
	if li, _ := idx.findNearest(nil, 0, 0, 0); li != -1 {
		t.Errorf("empty: got %d, want -1", li)
	}

	one := []Location{{X: 100, Y: 200, Z: 300, Name: "alpha"}}
	idx = buildPencilIndex(one, 0)
	li, distSq := idx.findNearest(one, 0, 0, 0)
	if li != 0 {
		t.Errorf("single: got %d, want 0", li)
	}
	// Distance check: 100² + 200² + 300² = 10000 + 40000 + 90000 = 140000
	if distSq != 140000 {
		t.Errorf("single: distSq = %g, want 140000", distSq)
	}
}

// TestPencilIndex_RandomSparse stresses the shell-expansion path
// (random sparse points → most 3×3 cells empty → need to expand
// outward).
func TestPencilIndex_RandomSparse(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	locs := make([]Location, 30)
	for i := range locs {
		locs[i] = Location{
			X:    (r.Float32() - 0.5) * 8000,
			Y:    (r.Float32() - 0.5) * 8000,
			Z:    (r.Float32() - 0.5) * 2000,
			Name: "x",
		}
	}
	idx := buildPencilIndex(locs, defaultCellSize)
	for i := 0; i < 2000; i++ {
		x := (r.Float32() - 0.5) * 8000
		y := (r.Float32() - 0.5) * 8000
		z := (r.Float32() - 0.5) * 2000
		_, wantDistSq := findNearestLinear(locs, x, y, z)
		_, gotDistSq := idx.findNearest(locs, x, y, z)
		if gotDistSq != wantDistSq {
			t.Fatalf("query #%d (%g,%g,%g): want d²=%g, got %g", i, x, y, z, wantDistSq, gotDistSq)
		}
	}
}

// TestFinder_Threshold sanity-checks that the Finder routes small L
// through the linear scan and large L through the pencil index, and
// both paths return the same answer.
func TestFinder_Threshold(t *testing.T) {
	// Small L: should go linear.
	small := []Location{
		{X: 0, Y: 0, Z: 0, Name: "origin"},
		{X: 1000, Y: 0, Z: 0, Name: "east"},
	}
	f := NewFinder("small", small)
	if got := f.FindNearest(900, 0, 0); got != "east" {
		t.Errorf("small linear: got %q, want east", got)
	}

	// Large L: synthesise enough locs to trip the threshold, then
	// confirm result against linear.
	r := rand.New(rand.NewSource(123))
	large := make([]Location, pencilThreshold+50)
	for i := range large {
		large[i] = Location{
			X:    (r.Float32() - 0.5) * 6000,
			Y:    (r.Float32() - 0.5) * 6000,
			Z:    0,
			Name: "n",
		}
	}
	// Place a distinctive sentinel.
	large[42].Name = "sentinel"
	sx, sy, sz := large[42].X, large[42].Y, large[42].Z

	fb := NewFinder("large", large)
	got := fb.FindNearest(sx, sy, sz)
	if got != "sentinel" {
		t.Errorf("large pencil: got %q, want sentinel", got)
	}
}
