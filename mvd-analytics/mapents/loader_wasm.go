//go:build js && wasm

package mapents

import (
	"fmt"

	"github.com/mvd-analyzer/mvd-analytics/internal/jshost"
	"github.com/mvd-analyzer/mvd-analytics/loc"
)

// LoadForMap pulls the per-map entity JSON from the JS host via the shared
// jshost bridge (host callback fetchMapEntsSync, a sync XHR against
// mapents/<name>.json). A missing file is an error so the caller leaves
// the section absent.
func LoadForMap(mapName string) (*MapEntities, error) {
	base := loc.NormalizeMapName(mapName)
	data, err := jshost.FetchSync("fetchMapEntsSync", base)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no map-entities for map %s", base)
	}
	return parse(base, data)
}
