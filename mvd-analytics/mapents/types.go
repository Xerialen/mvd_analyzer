// Package mapents is the static per-map entity corpus: the designed
// layout of a Quake map — item spawns, player spawnpoints, teleport
// destinations/sources, buttons — keyed by map name. The corpus is
// generated offline from BSP entity lumps by cmd/mapgen and loaded at
// analyze time by map name, mirroring the loc corpus.
//
// This is the map's *designed* layout, independent of any demo. For the
// per-match "what actually spawned and who took it" timeline, see
// result.ItemsResult (derived from the MVD entity stream).
//
// Regeneration dependency: each entity's Name/Loc fields bake in the loc
// corpus (loc/data) that existed when cmd/mapgen ran. Updating loc/data
// therefore silently de-syncs mapents/data until the entity corpus is
// regenerated too — after changing loc files, re-run
// `cmd/mapgen -entities-out mvd-analytics/mapents/data` so the two stay
// consistent. Bump CorpusVersion whenever the on-disk shape changes; parse
// rejects a corpus whose version does not match the running binary.
package mapents

import (
	"encoding/json"
	"fmt"
)

// parse decodes a per-map entity JSON document. Shared by the native and
// WASM loaders.
func parse(base string, data []byte) (*MapEntities, error) {
	var me MapEntities
	if err := json.Unmarshal(data, &me); err != nil {
		return nil, fmt.Errorf("map-entities %s: %w", base, err)
	}
	// A version mismatch means the corpus predates (or postdates) this
	// binary's MapEntity shape: fields would unmarshal partially or to
	// their zero value and load silently wrong. Reject it so the caller
	// leaves the section absent rather than serving a stale layout — the
	// same graceful degradation as a missing corpus file.
	if me.Version != CorpusVersion {
		return nil, fmt.Errorf("map-entities %s: corpus version %d, want %d (regenerate with cmd/mapgen -entities-out)", base, me.Version, CorpusVersion)
	}
	return &me, nil
}

// CorpusVersion is the schema version of the per-map entity JSON files
// under data/. Bump when the MapEntity shape changes so regenerated and
// stale corpora are distinguishable.
const CorpusVersion = 1

// Entity Type values.
const (
	TypeItem        = "item"        // weapon / ammo / armor / health / powerup pickup
	TypeSpawn       = "spawn"       // player spawnpoint
	TypeTeleportDst = "teleportDst" // info_teleport_destination (point)
	TypeTeleportSrc = "teleportSrc" // trigger_teleport (brush volume)
	TypeButton      = "button"      // func_button (brush)
	TypeDoor        = "door"        // func_door (brush)
)

// MapEntities is the corpus entry for one map.
type MapEntities struct {
	Map      string      `json:"map"`
	Version  int         `json:"version"`
	Entities []MapEntity `json:"entities"`
}

// MapEntity is one static entity. Position is the entity origin for point
// entities; for brush entities (teleportSrc/button/door) it is the bmodel
// bbox centre. Kind is set only for items and uses the same compact
// vocabulary as result.ItemTimeline.Kind ("rl","lg","ra","mh",…).
type MapEntity struct {
	Type       string  `json:"type"`
	Class      string  `json:"class"`          // raw BSP classname
	Kind       string  `json:"kind,omitempty"` // items only
	Name       string  `json:"name"`           // loc-based label, disambiguated
	X          float32 `json:"x"`
	Y          float32 `json:"y"`
	Z          float32 `json:"z"`
	Loc        string  `json:"loc,omitempty"`        // nearest named loc, when a loc file exists
	Target     string  `json:"target,omitempty"`     // teleportSrc → destination targetname
	TargetName string  `json:"targetName,omitempty"` // teleportDst → its own targetname
	Spawnflags int     `json:"spawnflags,omitempty"`
	Bounds     *Bounds `json:"bounds,omitempty"` // brush entities (teleportSrc/button/door): trigger volume
}

// Bounds is an axis-aligned bounding box in world coordinates, carried
// by brush entities so consumers have the trigger/door volume (not just
// the centre point in X/Y/Z).
type Bounds struct {
	Min [3]float32 `json:"min"`
	Max [3]float32 `json:"max"`
}

// Category maps an item Kind to the coarse class used by the /items and
// /map-entities filters ("armor","mega","health","powerup","weapon",
// "ammo"). Mirrors result.ItemTimeline.Category(). Returns "" for
// non-item entities and unknown kinds.
func (e MapEntity) Category() string {
	switch e.Kind {
	case "ga", "ya", "ra":
		return "armor"
	case "mh":
		return "mega"
	case "h15", "h25":
		return "health"
	case "quad", "pent", "ring", "suit":
		return "powerup"
	case "rl", "lg", "gl", "ssg", "sng", "ng":
		return "weapon"
	case "shells", "nails", "rockets", "cells":
		return "ammo"
	default:
		return ""
	}
}
