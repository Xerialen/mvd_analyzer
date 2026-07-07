package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-analytics/view"
	"github.com/mvd-analyzer/mvd-api/internal/democache"
)

// This file adds the automatic API surface over the analyzer DAG (Stage 4 of
// PLAN-improve-analytics.md §7): a manifest of every servable artifact, a
// generic materialise-and-serve endpoint keyed on the artifact name, and the
// DAG as JSON. The manifest and graph are static per binary (a pure function
// of the registered specs), so they cache under an ETag keyed on the schema
// version; the per-demo generic endpoint carries a finer per-artifact ETag.
//
// The generic endpoint is a thin generic accessor, not a re-implementation of
// the curated filters: it resolves the name through the closed
// analyzer.ServableArtifact registry (no user input reaches the filesystem
// beyond a validated name), accepts no query parameters (parameterised reads
// are the view endpoints), and serves the artifact's Result section as-is.

// eagerArtifact describes how the generic endpoint extracts one servable
// eager artifact's section from the base (cached) Result. extract returns the
// raw section; when it can return view.ErrUnavailable (the object-shaped
// sections that need a demo capability), code/msg name the 422. Sections that
// are always computable / list-shaped leave code empty and never error.
type eagerArtifact struct {
	extract func(*result.Result) (any, error)
	code    string
	msg     string
}

// eagerArtifacts maps each servable eager artifact (by DAG node name) to its
// section accessor. It routes through the view availability accessors where
// one exists (so the 422-vs-200 convention matches the curated endpoints) and
// otherwise returns the raw section at 200. The shot-streams-enriched parts of
// shots/aim are NOT rebuilt here — this serves the lean eager section; the
// enriched blocks come from the `shot-streams` lazy artifact (or /shots, /aim).
var eagerArtifacts = map[string]eagerArtifact{
	"demoinfo": {extract: func(r *result.Result) (any, error) { return view.DemoInfo(r) },
		code: "demoinfo_unavailable", msg: "this demo has no KTX demoinfo block (likely non-KTX or pre-match abort)"},
	"frag": {extract: func(r *result.Result) (any, error) { return view.Frags(r, view.FragOptions{}) },
		code: "frags_unavailable", msg: "this demo has no frag log"},
	"metadata": {extract: func(r *result.Result) (any, error) { return view.Metadata(r) },
		code: "metadata_unavailable", msg: "this demo has no metadata (no fullserverinfo / no countdown centerprint)"},
	"damage": {extract: func(r *result.Result) (any, error) { return view.Damage(r, view.DamageOptions{}) },
		code: "damage_unavailable", msg: "this demo has no damage data (no KTX mvdhidden_dmgdone stream)"},
	"shots": {extract: func(r *result.Result) (any, error) { return view.Shots(r) },
		code: "shots_unavailable", msg: "this demo has no shot data (no weapon fires decoded)"},
	"aim": {extract: func(r *result.Result) (any, error) { return view.Aim(r) },
		code: "aim_unavailable", msg: "this demo has no aim data (needs shots + position/view streams)"},
	"loc-graph": {extract: func(r *result.Result) (any, error) { return view.LocGraph(r) },
		code: "locgraph_unavailable", msg: "this demo has no loc graph (probably no position track was emitted)"},

	// Always-computable / list-shaped sections: 200 with the raw section (which
	// may be null/empty), never 422 — the same convention the curated endpoints
	// use for these.
	"match":          {extract: func(r *result.Result) (any, error) { return r.Match, nil }},
	"messages":       {extract: func(r *result.Result) (any, error) { return r.Messages, nil }},
	"timeline":       {extract: func(r *result.Result) (any, error) { return r.TimelineAnalysis, nil }},
	"items":          {extract: func(r *result.Result) (any, error) { return r.Items, nil }},
	"map-entities":   {extract: func(r *result.Result) (any, error) { return r.MapEntities, nil }},
	"backpacks":      {extract: func(r *result.Result) (any, error) { return r.Backpacks, nil }},
	"weapon-pickups": {extract: func(r *result.Result) (any, error) { return r.WeaponPickups, nil }},
}

// handleArtifactsManifest: GET /v1/artifacts — the manifest of every DAG node
// (name, tier, requires, provides, mutates, lazy, cost, resultKey, servable,
// description). Static per binary; ETag keyed on the schema version.
func (s *server) handleArtifactsManifest(w http.ResponseWriter, r *http.Request) {
	s.writeStaticCacheHeaders(w, "artifacts")
	if staticRevalidated(w, r, "artifacts") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": result.CurrentSchemaVersion,
		"artifacts":     analyzer.ArtifactManifest(),
	})
}

// handleGraph: GET /v1/graph — the analyzer DAG as JSON (nodes with cost /
// resultKey / lazy + the artifact edges), exactly analyzer.ExportGraph("json").
// Static per binary; ETag keyed on the schema version.
func (s *server) handleGraph(w http.ResponseWriter, r *http.Request) {
	s.writeStaticCacheHeaders(w, "graph")
	if staticRevalidated(w, r, "graph") {
		return
	}
	body, err := analyzer.ExportGraph("json")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

// handleArtifact: GET /v1/demos/{id}/artifacts/{name} — materialise and serve
// any servable artifact by name. The name is resolved through the closed
// registry; unknown / non-servable names 404. No query params are accepted.
func (s *server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	meta, ok := analyzer.ServableArtifact(name)
	if !ok {
		writeError(w, http.StatusNotFound, "artifact_unknown",
			fmt.Sprintf("no servable artifact %q (see GET /v1/artifacts)", name))
		return
	}
	// Artifacts are parameter-free; parameterised reads are the view endpoints
	// (plan §3.4/§7). Reject any query params rather than silently ignoring them.
	if len(r.URL.Query()) > 0 {
		writeError(w, http.StatusBadRequest, "invalid_param",
			"artifact endpoints take no query parameters (parameterised reads are the view endpoints)")
		return
	}
	id, err := democache.ParseDemoID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_demo_id", err.Error())
		return
	}

	if meta.Lazy {
		s.serveLazyArtifact(w, r, id, name)
		return
	}

	res, cm, err := s.store.GetResult(r.Context(), id)
	if err != nil {
		mapStoreError(w, err)
		return
	}
	setArtifactCacheHeaders(w, cm, name)
	if artifactRevalidated(w, r, cm, name) {
		return
	}
	ea, known := eagerArtifacts[name]
	if !known {
		// Manifest says servable but no accessor is wired — a programmer error.
		writeError(w, http.StatusInternalServerError, "internal",
			fmt.Sprintf("no accessor for servable artifact %q", name))
		return
	}
	section, err := ea.extract(res)
	if err != nil {
		writeUnavailable(w, err, ea.code, ea.msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{meta.ResultKey: section})
}

// serveLazyArtifact materialises los / shot-streams through the same store
// hooks the curated /los and /shots|/aim|/streams endpoints use, and serves the
// artifact's own body (reusing their serialization helpers — no forked shapes).
func (s *server) serveLazyArtifact(w http.ResponseWriter, r *http.Request, id democache.DemoID, name string) {
	switch name {
	case "los":
		res, meta, err := s.store.EnsureLOS(r.Context(), id)
		if err != nil {
			mapStoreError(w, err)
			return
		}
		setArtifactCacheHeaders(w, meta, name)
		if artifactRevalidated(w, r, meta, name) {
			return
		}
		writeJSON(w, http.StatusOK, losBody(res))
	case "shot-streams":
		res, meta, err := s.store.EnsureShotStreams(r.Context(), id)
		if err != nil {
			mapStoreError(w, err)
			return
		}
		setArtifactCacheHeaders(w, meta, name)
		if meta.ShotStreamsUnavailable {
			// Degrade exactly like /shots: tier-1 bytes gone, stream-derived parts
			// absent; flag it and don't let clients cache the lean body as immutable.
			w.Header().Set("X-Shot-Streams", "unavailable")
			w.Header().Set("Cache-Control", "no-store")
		}
		if artifactRevalidated(w, r, meta, name) {
			return
		}
		writeJSON(w, http.StatusOK, shotStreamsBody(res))
	default:
		// Unreachable: only los / shot-streams are marked lazy in the manifest.
		writeError(w, http.StatusInternalServerError, "internal", "unhandled lazy artifact "+name)
	}
}

// shotStreamsBody is the `shot-streams` artifact body: the three spatial
// streams plus the stream-enriched shots / aim blocks — the exact set
// EnsureShotStreams materialises. Null fields for a demo with none.
func shotStreamsBody(res *result.Result) any {
	var pr, nl *result.ProjectileStreams
	var bm *result.BeamStreams
	if res.Streams != nil {
		pr = res.Streams.Projectiles
		bm = res.Streams.Beams
		nl = res.Streams.Nails
	}
	return struct {
		Projectiles *result.ProjectileStreams `json:"projectiles"`
		Beams       *result.BeamStreams       `json:"beams"`
		Nails       *result.ProjectileStreams `json:"nails"`
		Shots       *result.ShotsResult       `json:"shots"`
		Aim         *result.AimResult         `json:"aim"`
	}{pr, bm, nl, res.Shots, res.Aim}
}

// --- per-artifact and static cache headers ---

// artifactETag is the finer per-artifact ETag (plan §7): "<sha>-<name>@v<n>".
func artifactETag(meta democache.CacheMeta, name string) string {
	return fmt.Sprintf(`"%s-%s@v%d"`, meta.SHA256, name, meta.SchemaVersion)
}

func setArtifactCacheHeaders(w http.ResponseWriter, meta democache.CacheMeta, name string) {
	setCacheHeaders(w, meta)
	w.Header().Set("ETag", artifactETag(meta, name)) // override the global "<sha>-v<n>" form
}

func artifactRevalidated(w http.ResponseWriter, r *http.Request, meta democache.CacheMeta, name string) bool {
	etag := artifactETag(meta, name)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// staticETag keys the binary-static endpoints (/v1/artifacts, /v1/graph) on
// the schema version — the only thing that changes their bodies.
func staticETag(kind string) string {
	return fmt.Sprintf(`"%s-v%d"`, kind, result.CurrentSchemaVersion)
}

func (s *server) writeStaticCacheHeaders(w http.ResponseWriter, kind string) {
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("X-Schema-Version", fmt.Sprintf("%d", result.CurrentSchemaVersion))
	w.Header().Set("ETag", staticETag(kind))
}

func staticRevalidated(w http.ResponseWriter, r *http.Request, kind string) bool {
	etag := staticETag(kind)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}
