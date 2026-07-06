// Package locvis is a visibility-aware drop-in replacement for the
// nearest-neighbour loc attribution provided by mvd-analytics/loc.
//
// V1 (the production Euclidean nearest-neighbour in loc.Finder) picks
// the geometrically closest loc-point, with no awareness of intervening
// walls. On certain trajectories — e.g. a player jumping over an open
// area whose far wall happens to be very close to a different loc-point
// — V1 produces brief "wall-bleed" loc visits to a place the player
// never visibly entered. These pollute downstream loc-trails, region-
// control, and loc-graph analytics.
//
// locvis wraps loc.Finder with a per-map BSP and runs the V6 algorithm:
// the nearest loc-point whose containing BSP leaf is in the player's
// PVS row wins; if every loc is PVS-vetoed (or the player is jittered
// into a SOLID leaf and the wiggle can't escape), it falls back to V1's
// nearest. The algorithm is validated on demo 216406 (e1m2): 178 wall-
// bleed spans corrected, zero false positives — see
// experiments/locattr/V2b-V6-HANDOFF.md.
//
// Implementation choice: at LoadForMap we walk every leaf once and
// precompute `leafVisLocs[L]` — the list of loc indices whose containing
// leaf is in L's PVS row. At query time we resolve the player's leaf,
// look up that leaf's pre-filtered candidate list, and linear-scan it
// for Euclidean nearest. That replaces a per-query O(N log N) sort with
// an O(M) scan where M is "locs visible from the player's current leaf"
// — typically 30–80 candidates on competitive maps. Per-leaf list
// construction is one-shot at map load (O(leafCount × N) bit-tests,
// ~300 µs for dm6-class maps). PVS row decompression happens during
// preprocessing only, never on the hot path.
//
// When no BSP is available for the current map (file missing, parse
// error, or the WASM host did not install fetchBspSync), the Finder
// degenerates to the bare loc.Finder — bit-identical V1 behaviour. So
// locvis is always safe to swap in.
//
// The active algorithm is a compile-time constant (ActiveAlgorithm
// below). AlgoV6 is the default; AlgoV1 disables the veto for A/B-ing
// against the baseline at compile time. Earlier iterations also shipped
// a raycast variant (V6a) but it was strictly more expensive and
// produced false positives in the research corpus, so it's been dropped.
package locvis

import (
	"github.com/mvd-analyzer/mvd-analytics/bspvis"
	"github.com/mvd-analyzer/mvd-analytics/loc"
)

// Algorithm identifies the attribution strategy FindNearest runs when a
// BSP is loaded for the current map.
type Algorithm int

const (
	// AlgoV1 disables the veto entirely. FindNearest delegates straight
	// to loc.Finder.FindNearest even when a BSP is loaded. Useful for
	// comparing locvis output against the V1 baseline at compile time.
	AlgoV1 Algorithm = iota

	// AlgoV6 picks the Euclidean-nearest loc whose containing BSP leaf
	// is in the player's PVS row. Falls back to V1 when the player is
	// jittered into solid (and wiggle can't escape) or no loc is
	// visible from the player's leaf.
	AlgoV6
)

// ActiveAlgorithm selects the attribution strategy at compile time.
// Change this constant and rebuild to compare V6 against bare V1.
const ActiveAlgorithm Algorithm = AlgoV6

// Finder wraps a loc.Finder with an optional BSP-backed visibility
// filter. When bsp is nil the Finder is functionally identical to its
// underlying loc.Finder.
type Finder struct {
	base *loc.Finder
	bsp  *bspvis.BSP // nil → no visibility filter, FindNearest = V1

	// leafVisLocs is the precomputed per-leaf visible-loc table.
	// leafVisLocs[L] is the slice of loc indices (into base.Locations())
	// whose containing leaf is in leaf L's PVS row. Locs that landed in
	// CONTENTS_SOLID are dropped at construction time — they can never
	// appear here. nil when bsp is nil.
	//
	// Index 0 (the universal CONTENTS_SOLID sink) is intentionally left
	// nil; FindNearest never queries it (resolveQueryLeaf wiggles out
	// of solid before lookup).
	leafVisLocs [][]int32
}

// MapName returns the normalised map name the underlying loc.Finder was
// loaded for.
func (f *Finder) MapName() string {
	if f == nil || f.base == nil {
		return ""
	}
	return f.base.MapName()
}

// LocationCount returns the number of loc points loaded.
func (f *Finder) LocationCount() int {
	if f == nil || f.base == nil {
		return 0
	}
	return f.base.LocationCount()
}

// HasVisibility reports whether a BSP is available for the current map.
// When false, FindNearest delegates straight to V1 (loc.Finder).
func (f *Finder) HasVisibility() bool {
	return f != nil && f.bsp != nil
}

// Locations returns the full slice of loc points (pass-through to the
// underlying loc.Finder). Used by analyzers that need to enumerate loc
// names (e.g. region-control auto-detection).
func (f *Finder) Locations() []loc.Location {
	if f == nil || f.base == nil {
		return nil
	}
	return f.base.Locations()
}

// FindNearest returns the name of the nearest loc-point to (x, y, z),
// subject to the active visibility veto. Same signature as
// loc.Finder.FindNearest; safe drop-in replacement.
func (f *Finder) FindNearest(x, y, z float32) string {
	if f == nil || f.base == nil {
		return ""
	}
	if f.bsp == nil || ActiveAlgorithm == AlgoV1 {
		return f.base.FindNearest(x, y, z)
	}
	return f.attributeV6(x, y, z)
}

// newFinder is the shared constructor used by both loader_native.go and
// loader_wasm.go. bspBytes==nil means "no BSP available, run as V1".
// A BSP parse failure also degenerates silently to V1 rather than
// propagating an error — wall-bleed correction is best-effort, never a
// hard requirement.
//
// Preprocessing cost (one-shot per map load):
//   - PointInLeaf for every loc: O(N · BSP-depth), ~N × 10 ns.
//   - For every non-solid leaf L: LeafPVS decompress (O(visdata bytes))
//     and N bit-tests to materialise leafVisLocs[L]. Worst-case
//     O(leafCount × N) bit-tests — ~300 µs for dm6-class maps.
//
// This pays the visibility work upfront so the hot path (FindNearest)
// is a leaf lookup + linear scan over a pre-filtered ~M-element list.
func newFinder(base *loc.Finder, bspBytes []byte) *Finder {
	f := &Finder{base: base}
	if len(bspBytes) == 0 {
		return f
	}
	bsp, err := bspvis.LoadBytes(bspBytes)
	if err != nil {
		return f
	}
	locs := base.Locations()

	// Per-loc leaf index. Solid-landing locs map to -1 and are dropped
	// from every leaf's visible list (they're corpus artifacts).
	locLeaves := make([]int, len(locs))
	for i := range locs {
		leaf := bsp.PointInLeaf([3]float32{locs[i].X, locs[i].Y, locs[i].Z})
		if bsp.LeafContents(leaf) == bspvis.ContentsSolid {
			locLeaves[i] = -1
			continue
		}
		locLeaves[i] = leaf
	}

	// Per-leaf visible-loc lists. Leaf 0 stays nil (solid sink — never
	// queried after resolveQueryLeaf's wiggle).
	leafCount := bsp.LeafCount()
	visLocs := make([][]int32, leafCount)
	for L := 1; L < leafCount; L++ {
		pvs := bsp.LeafPVS(L)
		var visible []int32
		for i, leaf := range locLeaves {
			if leaf < 0 {
				continue
			}
			if bsp.PVSContains(pvs, leaf) {
				visible = append(visible, int32(i))
			}
		}
		visLocs[L] = visible
	}

	f.bsp = bsp
	f.leafVisLocs = visLocs
	return f
}
