package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/loc"
	"github.com/mvd-analyzer/mvd-analytics/mapents"
	"github.com/mvd-analyzer/mvd-analytics/result"
)

// handleMapEntitiesByMap: GET /v1/maps/{map}/entities — the map's static
// designed layout (item spawns, spawnpoints, teleport destinations/sources,
// buttons) addressed by map name directly (no demo needed). Reads the
// embedded corpus; identical for every demo on the map. Callers holding a
// demo id get the map name from /overview first.
//
// Query params (case-insensitive):
//
//	types  csv — restrict to entity types: item, spawn, teleportDst,
//	             teleportSrc, button, door
//	kinds  csv — restrict to item categories (armor, mega, health,
//	             powerup, weapon, ammo) or a raw kind token (ra, quad)
func (s *server) handleMapEntitiesByMap(w http.ResponseWriter, r *http.Request) {
	base := loc.NormalizeMapName(r.PathValue("map"))
	me, err := mapents.LoadForMap(base)
	if err != nil {
		writeError(w, http.StatusNotFound, "map_unavailable", "no map-entities for map "+base)
		return
	}
	etag := fmt.Sprintf(`"ents-%s-v%d"`, base, mapents.CorpusVersion)
	if !writeStaticHeaders(w, r, etag) {
		return
	}
	writeJSON(w, http.StatusOK, filterMapEntities(corpusToResult(me), r))
}

// handleMapGeometry: GET /v1/maps/{map}/geometry — streams the per-map
// floor-polygon geometry JSON (mapgeom.MapRegions) from the maps
// directory. REST-only (the payload is large; not an MCP tool).
func (s *server) handleMapGeometry(w http.ResponseWriter, r *http.Request) {
	if s.mapsDir == "" {
		writeError(w, http.StatusNotFound, "map_unavailable",
			"map geometry is not configured on this server (no -maps-dir)")
		return
	}
	base := sanitizeMapName(r.PathValue("map"))
	if base == "" {
		writeError(w, http.StatusBadRequest, "invalid_param", "bad map name")
		return
	}
	data, err := os.ReadFile(filepath.Join(s.mapsDir, base+".json"))
	if err != nil {
		writeError(w, http.StatusNotFound, "map_unavailable", "no geometry for map "+base)
		return
	}
	// The geometry is immutable for a given map content; len(data) is a
	// cheap content-version proxy that avoids parsing the (large) file.
	etag := fmt.Sprintf(`"geo-%s-%d"`, base, len(data))
	if !writeStaticHeaders(w, r, etag) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// filterMapEntities applies optional types= / kinds= filters, returning
// the input unchanged when neither is present.
func filterMapEntities(in *result.MapEntitiesResult, r *http.Request) *result.MapEntitiesResult {
	q := r.URL.Query()
	typeSet := csvSetLower(q.Get("types"))
	kindSet := csvSetLower(q.Get("kinds"))
	if len(typeSet) == 0 && len(kindSet) == 0 {
		return in
	}
	out := &result.MapEntitiesResult{Map: in.Map, Entities: make([]result.MapEntity, 0, len(in.Entities))}
	for _, e := range in.Entities {
		if len(typeSet) > 0 && !typeSet[strings.ToLower(e.Type)] {
			continue
		}
		if len(kindSet) > 0 && !kindSet[e.Category()] && !kindSet[strings.ToLower(e.Kind)] {
			continue
		}
		out.Entities = append(out.Entities, e)
	}
	return out
}

// corpusToResult converts a mapents corpus entry to the Result-contract
// shape returned by both map-entities endpoints.
func corpusToResult(me *mapents.MapEntities) *result.MapEntitiesResult {
	out := &result.MapEntitiesResult{Map: me.Map, Entities: make([]result.MapEntity, 0, len(me.Entities))}
	for _, e := range me.Entities {
		re := result.MapEntity{
			Type: e.Type, Class: e.Class, Kind: e.Kind, Name: e.Name,
			X: e.X, Y: e.Y, Z: e.Z, Loc: e.Loc,
			Target: e.Target, TargetName: e.TargetName, Spawnflags: e.Spawnflags,
		}
		if e.Bounds != nil {
			re.Bounds = &result.Bounds{Min: e.Bounds.Min, Max: e.Bounds.Max}
		}
		out.Entities = append(out.Entities, re)
	}
	return out
}

// writeStaticHeaders sets immutable cache headers + ETag for per-map
// static responses and honours If-None-Match. Returns false when it has
// already written a 304 and the caller should stop.
func writeStaticHeaders(w http.ResponseWriter, r *http.Request, etag string) bool {
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return false
	}
	return true
}

// sanitizeMapName reduces a path param to a safe map basename (no
// directory traversal) for on-disk geometry lookup.
func sanitizeMapName(s string) string {
	base := strings.ToLower(filepath.Base(s))
	base = strings.TrimSuffix(base, ".bsp")
	base = strings.TrimSuffix(base, ".json")
	if base == "." || base == ".." {
		return ""
	}
	return base
}
