package main

import (
	"log/slog"
	"net/http"
)

// server bundles the per-request dependencies.
//
// The lazy line-of-sight pass is serialised per demo SHA inside the cache
// (EnsureLOS), where the SHA is resolved and the tier-3 artifact is
// read/written, so the server holds no per-demo lock of its own.
type server struct {
	store   demoStore
	logger  *slog.Logger
	mapsDir string // directory of per-map geometry JSON; "" disables /geometry
}

// newRouter returns an http.Handler with every endpoint registered.
// Logging + panic recovery wrap the mux.
func newRouter(store demoStore, logger *slog.Logger, mapsDir string) http.Handler {
	s := &server{store: store, logger: logger, mapsDir: mapsDir}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/version", s.handleVersion)

	// Automatic DAG surface (Stage 4): the artifact manifest, the generic
	// per-artifact endpoint, and the graph as JSON.
	mux.HandleFunc("GET /v1/artifacts", s.handleArtifactsManifest)
	mux.HandleFunc("GET /v1/graph", s.handleGraph)
	mux.HandleFunc("GET /v1/demos/{id}/artifacts/{name}", s.handleArtifact)

	mux.HandleFunc("POST /v1/demos/{id}", s.handleLoad)
	mux.HandleFunc("GET /v1/demos/{id}/overview", s.handleOverview)
	mux.HandleFunc("GET /v1/demos/{id}/demoinfo", s.handleDemoInfo)
	mux.HandleFunc("GET /v1/demos/{id}/metadata", s.handleMetadata)
	mux.HandleFunc("GET /v1/demos/{id}/frags", s.handleFrags)
	mux.HandleFunc("GET /v1/demos/{id}/damage", s.handleDamage)
	mux.HandleFunc("GET /v1/demos/{id}/shots", s.handleShots)
	mux.HandleFunc("GET /v1/demos/{id}/aim", s.handleAim)
	mux.HandleFunc("GET /v1/demos/{id}/loc-graph", s.handleLocGraph)
	mux.HandleFunc("GET /v1/demos/{id}/chat", s.handleChat)
	mux.HandleFunc("GET /v1/demos/{id}/backpacks", s.handleBackpacks)
	mux.HandleFunc("GET /v1/demos/{id}/items", s.handleItems)
	mux.HandleFunc("GET /v1/demos/{id}/weapon-pickups", s.handleWeaponPickups)
	mux.HandleFunc("GET /v1/demos/{id}/buckets", s.handleBuckets)
	mux.HandleFunc("GET /v1/demos/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /v1/demos/{id}/stream-slice", s.handleStreamSlice)
	mux.HandleFunc("GET /v1/demos/{id}/state-at", s.handleStateAt)
	mux.HandleFunc("GET /v1/demos/{id}/los", s.handleLOS)
	mux.HandleFunc("GET /v1/demos/{id}/streams/projectiles", s.handleProjectiles)
	mux.HandleFunc("GET /v1/demos/{id}/streams/beams", s.handleBeams)
	mux.HandleFunc("GET /v1/demos/{id}/streams/nails", s.handleNails)
	mux.HandleFunc("GET /v1/demos/{id}/loc-trails", s.handleLocTrails)
	mux.HandleFunc("GET /v1/demos/{id}/loc-table", s.handleLocTable)
	mux.HandleFunc("GET /v1/demos/{id}/region-control", s.handleRegionControl)
	mux.HandleFunc("GET /v1/demos/{id}/airgibs", s.handleAirgibs)

	// Per-map static data (no demo needed).
	mux.HandleFunc("GET /v1/maps/{map}/entities", s.handleMapEntitiesByMap)
	mux.HandleFunc("GET /v1/maps/{map}/geometry", s.handleMapGeometry)

	// Middleware order (outer → inner): request-id runs first so every
	// response — including a CORS preflight short-circuit — carries an
	// X-Request-Id; CORS then answers preflight and stamps Allow-Origin on
	// every response (incl. panics); access log records the final status with
	// that id; recover catches handler panics closest to the mux so the
	// request is still logged. (CORS stays outside auth in phase 14 so
	// preflight is never auth-blocked.)
	return requestIDMiddleware(
		corsMiddleware(
			accessLogMiddleware(logger,
				recoverMiddleware(logger, mux))))
}
