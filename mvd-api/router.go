package main

import (
	"log/slog"
	"net/http"

	"github.com/mvd-analyzer/mvd-api/internal/portal"
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
// Logging + panic recovery wrap the mux. auth may be nil (no-auth /
// localhost mode); when non-nil the auth + rate-limit middleware is inserted
// between accessLog and recover. p may be nil (portal disabled); when non-nil
// its /portal routes are registered on the mux (and are auth-exempt, so they
// are reachable without an API key even in auth mode — see authExempt).
func newRouter(store demoStore, logger *slog.Logger, mapsDir string, auth *authenticator, p *portal.Portal) http.Handler {
	s := &server{store: store, logger: logger, mapsDir: mapsDir}
	mux := http.NewServeMux()

	// Portal routes are registered ONLY when -portal is set (p != nil). When
	// off, /portal is not a route at all — it 404s — so today's behaviour is
	// unchanged. When on, the routes sit under the phase-14 /portal exemption
	// and do their own Discord-cookie auth.
	if p != nil {
		p.Register(mux)
	}

	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/version", s.handleVersion)
	mux.HandleFunc("GET /v1/auth/check", s.handleAuthCheck)

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
	// that id (and seeds the reqInfo auth writes its identity into); auth
	// validates the key + rate-limits; recover catches handler panics closest
	// to the mux so the request is still logged.
	//
	// Chain: requestID → cors → accessLog → auth → recover → mux.
	//
	// auth sits INSIDE cors so an OPTIONS preflight is answered (204, no key)
	// before auth runs, and INSIDE accessLog so 401s/429s are logged. When
	// auth is nil (localhost mode) it is omitted entirely and the chain is
	// byte-identical to before phase 14.
	inner := http.Handler(recoverMiddleware(logger, mux))
	if auth != nil {
		inner = auth.middleware(inner)
	}
	return requestIDMiddleware(
		corsMiddleware(
			accessLogMiddleware(logger, inner)))
}
