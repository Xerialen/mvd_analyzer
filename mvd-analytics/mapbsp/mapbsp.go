// Package mapbsp is the single source of raw per-map BSP bytes at
// analyze time. Both the visibility-aware loc finder (locvis) and the
// floor-height clip hull (mapclip) pull the same provisioned .bsp
// through here, so a deployment only has to ship BSPs once and both
// features light up (or degrade) together.
//
// BSPs are best-effort: a missing file is not an error, it just means
// the dependent feature falls back (locvis → V1 Euclidean nearest;
// mapclip → no floor column).
//
// A one-entry cache keyed by the normalised map name memoises the most
// recently loaded BSP bytes. One demo analysis reads the same BSP up to
// six times (locvis ×3, mapclip, the timeline liquid pass, and the lazy
// LOS pass) — on WASM each is a fresh multi-MB synchronous fetch — so a
// single entry collapses those to one read. One demo = one map, so one
// entry suffices.
package mapbsp

import (
	"sync"

	"github.com/mvd-analyzer/mvd-analytics/loc"
)

// The cache holds the last (normalised name → bytes) pair. mvd-api
// analyses demos concurrently, so access is mutex-guarded: the whole
// (name,bytes) pair is swapped under the lock, meaning a stale entry
// (evicted by another goroutine loading a different map) merely triggers
// a reload — a torn read of a mismatched name/bytes pair cannot occur.
// The cached bytes are only ever read (bsp.ParseBytes / bspvis.LoadBytes
// slice into them without mutating), so sharing one slice across readers
// and across concurrent analyses is safe. cacheGen guards the
// load-outside-the-lock window: SetDir bumps it, and a load that started
// before the bump discards its store instead of re-caching bytes read
// from the old directory.
var (
	cacheMu   sync.Mutex
	cacheName string
	cacheData []byte
	cacheHave bool
	cacheGen  uint64

	// loadCalls counts cache misses that reached the platform loader.
	// Read by the cache test to prove the memo elides repeat reads.
	loadCalls int
)

// LoadBytes returns the raw bytes of a map's BSP, or nil if none is
// found. The map name is normalised with the same rules as the loc
// corpus so aliases resolve consistently. A found-or-not result is cached
// (including the nil "not found" outcome, so repeated misses don't re-hit
// the disk / host).
func LoadBytes(mapName string) []byte {
	base := loc.NormalizeMapName(mapName)

	cacheMu.Lock()
	if cacheHave && cacheName == base {
		data := cacheData
		cacheMu.Unlock()
		return data
	}
	gen := cacheGen
	dir := currentDir()
	cacheMu.Unlock()

	// Do the (potentially slow, on WASM blocking) load outside the lock so
	// concurrent loads of different maps don't serialise.
	data := readBSP(dir, base)

	cacheMu.Lock()
	loadCalls++
	if cacheGen == gen {
		cacheName, cacheData, cacheHave = base, data, true
	}
	cacheMu.Unlock()
	return data
}

// invalidateCache drops the memoised entry and bumps the generation.
// Called by SetDir so a later load re-reads from the new directory rather
// than serving bytes from the old one, and so an in-flight load that
// started before the switch cannot re-populate the cache with old-dir
// bytes.
func invalidateCache() {
	cacheMu.Lock()
	cacheName, cacheData, cacheHave = "", nil, false
	cacheGen++
	cacheMu.Unlock()
}
