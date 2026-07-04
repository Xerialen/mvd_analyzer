package main

import (
	"log/slog"
	"net/http"
	"sync"
)

// server bundles the per-request dependencies.
type server struct {
	store   demoStore
	logger  *slog.Logger
	mapsDir string // directory of per-map geometry JSON; "" disables /geometry

	// losMu serializes the lazy line-of-sight pass so concurrent /los
	// requests don't race on the shared cached Result (ComputeLOS mutates
	// Streams.Players[].LOS in place and is idempotent, so only the first
	// holder under the lock does the work).
	losMu sync.Mutex

	// streamsMu serializes the on-demand shot-stream rebuild (EnsureShotStreams
	// re-parses and splices Projectiles/Beams/Nails onto the shared cached
	// Result, latched, so only the first holder does the re-parse).
	streamsMu sync.Mutex
}

// newRouter returns an http.Handler with every endpoint registered.
// Logging + panic recovery wrap the mux.
func newRouter(store demoStore, logger *slog.Logger, mapsDir string) http.Handler {
	s := &server{store: store, logger: logger, mapsDir: mapsDir}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/version", s.handleVersion)

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

	return recoverMiddleware(logger, accessLogMiddleware(logger, mux))
}
