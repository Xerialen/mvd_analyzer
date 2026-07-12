package locvis

import (
	"sync"

	"github.com/mvd-analyzer/mvd-analytics/loc"
	"github.com/mvd-analyzer/mvd-analytics/mapbsp"
)

// SetBspDir points BSP lookups at an on-disk directory. Delegates to the
// shared mapbsp loader so the floor-height clip hull (mapclip) resolves
// from the same place, and drops the memoised Finder so a later load
// re-reads from the new dir. Pass "" to revert to the env-var lookup
// (MVDA_BSP_DIR, then ./bsps). Native-only effect; on WASM mapbsp.SetDir
// is a no-op (the host owns BSP delivery via fetchBspSync). The platform
// variance is fully encapsulated in mapbsp, so this loader needs no
// build-tagged split.
func SetBspDir(dir string) {
	mapbsp.SetDir(dir)
	invalidateFinderCache()
}

// The Finder cache holds the last (normalised name → *Finder) pair. One
// demo analysis calls LoadForMap up to three times (items,
// timeline-finalize, backpacks); memoising the built Finder collapses the
// per-leaf PVS precompute (O(leafCount×N) — the package's dominant setup
// cost) to a single build. One demo = one map, so one entry suffices.
//
// Concurrency: mvd-api analyses demos in parallel. The whole (name,Finder)
// pair is swapped under the mutex, so a stale entry (evicted by another
// map) just triggers a rebuild and a torn read cannot occur. The built
// Finder is immutable and its query path (attributeV6 / loc.Finder) is
// read-only — loc's pencil index guards its lazy build with sync.Once —
// so sharing one Finder across concurrent analyses on the same map is safe.
// finderGen guards the build-outside-the-lock window: SetBspDir bumps it,
// and a build that started before the bump discards its store instead of
// re-caching a Finder built from the old directory's BSP.
var (
	finderMu    sync.Mutex
	finderName  string
	finderCache *Finder
	finderHave  bool
	finderGen   uint64
)

func invalidateFinderCache() {
	finderMu.Lock()
	finderName, finderCache, finderHave = "", nil, false
	finderGen++
	finderMu.Unlock()
}

// LoadForMap returns a Finder for the given map. The loc corpus is
// always required (forwards to loc.LoadForMap). The BSP is best-effort:
// if not present, malformed, or the BSP dir is unset, the Finder is
// returned with no BSP and FindNearest degenerates to V1.
//
// Native BSP lookup order (mapbsp): SetBspDir, $MVDA_BSP_DIR, ./bsps.
func LoadForMap(mapName string) (*Finder, error) {
	norm := loc.NormalizeMapName(mapName)

	finderMu.Lock()
	if finderHave && finderName == norm {
		f := finderCache
		finderMu.Unlock()
		return f, nil
	}
	gen := finderGen
	finderMu.Unlock()

	base, err := loc.LoadForMap(mapName)
	if err != nil {
		return nil, err
	}
	f := newFinder(base, mapbsp.LoadBytes(mapName))

	finderMu.Lock()
	if finderGen == gen {
		finderName, finderCache, finderHave = norm, f, true
	}
	finderMu.Unlock()
	return f, nil
}
