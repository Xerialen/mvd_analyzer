//go:build js && wasm

package loc

import (
	"fmt"

	"github.com/mvd-analyzer/mvd-analytics/internal/jshost"
)

// LoadForMap pulls the .loc file from the JS host via the shared jshost
// bridge (host callback fetchLocSync, a sync XHR against locs/<name>.loc).
// The raw bytes matter: loc files commonly carry high-bit-ASCII item
// shorthands (e.g. "ssg" as 0xf3 0xf3 0xe7) that substituteVariables
// strips bit 7 from, so a Uint8Array return is preferred (a legacy string
// return is still accepted by jshost). A missing file is an error so the
// caller leaves the map's locs absent.
func LoadForMap(mapName string) (*Finder, error) {
	base := NormalizeMapName(mapName)
	data, err := jshost.FetchSync("fetchLocSync", base)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no loc file for map %s", base)
	}
	return buildFinder(base, data)
}
