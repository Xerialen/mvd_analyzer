//go:build !(js && wasm)

package locvis

import (
	"github.com/mvd-analyzer/mvd-analytics/loc"
	"github.com/mvd-analyzer/mvd-analytics/mapbsp"
)

// SetBspDir points BSP lookups at an on-disk directory. Delegates to the
// shared mapbsp loader so the floor-height clip hull (mapclip) resolves
// from the same place. Pass "" to revert to the env-var lookup
// (MVDA_BSP_DIR, then ./bsps). Native-only; WASM callers route through
// the host fetchBspSync.
func SetBspDir(dir string) {
	mapbsp.SetDir(dir)
}

// LoadForMap returns a Finder for the given map. The loc corpus is
// always required (forwards to loc.LoadForMap). The BSP is best-effort:
// if not present, malformed, or the BSP dir is unset, the Finder is
// returned with no BSP and FindNearest degenerates to V1.
//
// Native BSP lookup order (mapbsp): SetBspDir, $MVDA_BSP_DIR, ./bsps.
func LoadForMap(mapName string) (*Finder, error) {
	base, err := loc.LoadForMap(mapName)
	if err != nil {
		return nil, err
	}
	return newFinder(base, mapbsp.LoadBytes(mapName)), nil
}
