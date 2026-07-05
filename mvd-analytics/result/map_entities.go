package result

// MapEntitiesResult is the static, designed layout of the match's map:
// item spawns, player spawnpoints, teleport destinations/sources, and
// buttons, each with a type and a location. It is sourced from the
// offline-generated mapents corpus (BSP entity lumps), keyed by map name,
// so it is identical for every demo on a given map and independent of
// what happened in this match. Absent when no corpus exists for the map.
//
// This is the map's *designed* layout. For the per-match pickup timeline
// — which items actually spawned, who took each one, and when it
// respawned — see ItemsResult. The two can be joined by (Kind, nearest
// origin).
type MapEntitiesResult struct {
	Map      string      `json:"map"`
	Entities []MapEntity `json:"entities"`
}

// MapEntity is one static map entity. Position is the entity origin for
// point entities (items, spawns, teleport destinations) and the bmodel
// bbox centre for brush entities (teleport sources, buttons, doors). Kind
// is set only for items and uses the same compact vocabulary as
// ItemTimeline.Kind ("rl","lg","ra","mh",…).
type MapEntity struct {
	Type       string  `json:"type"`           // "item"|"spawn"|"teleportDst"|"teleportSrc"|"button"|"door"
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
	Bounds     *Bounds `json:"bounds,omitempty"` // brush entities: trigger/door volume in world coords
}

// Bounds is an axis-aligned bounding box (world coords) carried by brush
// entities (teleportSrc/button/door) so consumers have the volume, not
// just the centre point in X/Y/Z.
type Bounds struct {
	Min [3]float32 `json:"min"`
	Max [3]float32 `json:"max"`
}

// Category maps an item Kind to the coarse class used by the
// /map-entities and /items filters. Mirrors ItemTimeline.Category().
// Returns "" for non-item entities and unknown kinds.
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
