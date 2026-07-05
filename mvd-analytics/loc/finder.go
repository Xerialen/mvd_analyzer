package loc

import "math"

// pencilThreshold is the loc-count past which the pencil index
// beats a linear scan on the QW corpus. Below it, the linear
// scan's cache-resident inner loop is faster than 9 map lookups +
// expanding-shell logic. See bench_test.go for the measurement
// that justifies this constant.
const pencilThreshold = 500

// FindNearest returns the name of the closest location to the given
// coordinates. Empty string if no locations are loaded.
//
// For competitive QW maps (L ≤ ~316) we just linear-scan — the loc
// array fits in L1 cache and runs ~1.3 ns per loc. For bigger custom
// maps (defer, tf2k, 2fort5 at L=900–3300) we lazily build an
// XY-pencil cell index — each cell covers a 256×256 XY square
// extending the full Z range — and scan only the 3×3 shell of
// pencils around the query, which beats the linear scan 2–5×.
func (f *Finder) FindNearest(x, y, z float32) string {
	if len(f.locations) == 0 {
		return ""
	}
	if len(f.locations) < pencilThreshold {
		li, _ := findNearestLinear(f.locations, x, y, z)
		if li < 0 {
			return ""
		}
		return f.locations[li].Name
	}
	idx := f.pencilIndex()
	li, _ := idx.findNearest(f.locations, x, y, z)
	if li < 0 {
		return ""
	}
	return f.locations[li].Name
}

// findNearestLinear is the cache-resident scan, used directly for
// small L (and as a reference in tests/benchmarks).
func findNearestLinear(locs []Location, x, y, z float32) (int, float32) {
	bestIdx := -1
	bestDistSq := float32(math.MaxFloat32)
	for i, loc := range locs {
		dx := x - loc.X
		dy := y - loc.Y
		dz := z - loc.Z
		d := dx*dx + dy*dy + dz*dz
		if d < bestDistSq {
			bestDistSq = d
			bestIdx = i
		}
	}
	return bestIdx, bestDistSq
}

// pencilIndex returns the lazily-built pencil index. Safe for
// concurrent use via the embedded sync.Once.
func (f *Finder) pencilIndex() *pencilIndex {
	f.index.once.Do(func() {
		f.index.idx = buildPencilIndex(f.locations, defaultCellSize)
	})
	return f.index.idx
}

// Locations returns all locations in the finder
func (f *Finder) Locations() []Location {
	return f.locations
}
