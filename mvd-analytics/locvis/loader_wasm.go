//go:build js && wasm

package locvis

import (
	"github.com/mvd-analyzer/mvd-analytics/loc"
	"github.com/mvd-analyzer/mvd-analytics/mapbsp"
)

// LoadForMap returns a Finder for the given map. The loc corpus is
// always required (loc.LoadForMap routes through the host's
// fetchLocSync). The BSP is best-effort via the shared mapbsp loader
// (host fetchBspSync); if absent the Finder degenerates to V1.
func LoadForMap(mapName string) (*Finder, error) {
	base, err := loc.LoadForMap(mapName)
	if err != nil {
		return nil, err
	}
	return newFinder(base, mapbsp.LoadBytes(mapName)), nil
}
