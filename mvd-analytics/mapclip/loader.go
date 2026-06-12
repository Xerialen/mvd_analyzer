package mapclip

import (
	"fmt"

	"github.com/mvd-analyzer/mvd-analytics/mapbsp"
	"github.com/mvd-analyzer/mvd-analytics/mapgen/bsp"
)

// LoadForMap builds the worldspawn player clip hull for a map from its
// provisioned BSP, returning an error when no BSP is available (so the
// caller leaves the floor column absent) or the BSP is a format we don't
// parse (HL v30 / Quake 2). The BSP bytes come from the shared mapbsp
// loader — the same source locvis uses for the visibility filter — so a
// deployment only ships BSPs once. No separate corpus is generated or
// embedded; a map update is just a new .bsp.
func LoadForMap(mapName string) (*Hull, error) {
	world, _, err := LoadForMapWithMovers(mapName, nil)
	return world, err
}

// LoadForMapWithMovers builds the worldspawn hull plus hull 1 of each
// requested submodel ("*N" → N), parsing the provisioned BSP once. The
// submodel hulls key the returned map by submodel index. Submodels the
// BSP doesn't have (a stale demo / mismatched BSP) or whose clip tree
// is malformed are skipped — the worldspawn hull is the only hard
// requirement, and a missing mover degrades to the pre-mover behaviour
// for that entity (the static floor beneath it).
func LoadForMapWithMovers(mapName string, subModels []int) (*Hull, map[int]*Hull, error) {
	data := mapbsp.LoadBytes(mapName)
	if data == nil {
		return nil, nil, fmt.Errorf("mapclip: no BSP for map %s", mapName)
	}
	parsed, err := bsp.ParseBytes(data)
	if err != nil {
		return nil, nil, fmt.Errorf("mapclip: parse BSP for map %s: %w", mapName, err)
	}
	world, err := Build(parsed)
	if err != nil {
		return nil, nil, err
	}
	var movers map[int]*Hull
	for _, sub := range subModels {
		if sub <= 0 || sub >= len(parsed.Models) {
			continue
		}
		h, err := BuildModel(parsed, sub)
		if err != nil {
			continue
		}
		if movers == nil {
			movers = make(map[int]*Hull, len(subModels))
		}
		movers[sub] = h
	}
	return world, movers, nil
}
