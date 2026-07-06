//go:build !(js && wasm)

package loc

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed data/*.loc
var embeddedLocs embed.FS

// locDirOverride, if non-empty, makes LoadForMap read from this
// directory instead of the embedded corpus. Used by cmd/mapgen to pull
// from a working `.loc` collection outside the module tree.
var locDirOverride string

// SetLocDir points LoadForMap at an on-disk directory instead of the
// embedded .loc corpus. Only meaningful for native builds; WASM callers
// route through the host-provided fetchLocSync regardless.
// Pass "" to revert to the embedded corpus.
//
// Not safe to call concurrently with LoadForMap, and it does not
// invalidate locvis's memoised Finder (which bakes in the loc table) —
// today's only callers (cmd/mapgen, tests) set it once at startup before
// any analysis. If a corpus switch mid-process is ever needed, add the
// invalidation then.
func SetLocDir(dir string) {
	locDirOverride = dir
}

// LoadForMap returns a Finder for the given map name. It first looks
// for an override directory set via SetLocDir; otherwise it reads from
// the .loc corpus embedded into the binary at build time.
func LoadForMap(mapName string) (*Finder, error) {
	base := NormalizeMapName(mapName)
	if locDirOverride != "" {
		data, err := os.ReadFile(filepath.Join(locDirOverride, base+".loc"))
		if err != nil {
			return nil, fmt.Errorf("no loc file for map %s: %w", base, err)
		}
		return buildFinder(base, data)
	}
	data, err := embeddedLocs.ReadFile("data/" + base + ".loc")
	if err != nil {
		return nil, fmt.Errorf("no loc file for map %s: %w", base, err)
	}
	return buildFinder(base, data)
}
