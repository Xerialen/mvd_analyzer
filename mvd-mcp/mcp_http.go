package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpPath is the path the streamable-HTTP handler is mounted at. The 16b
// Caddyfile routes `/mcp*` to this server, so both "/mcp" and "/mcp/" reach
// the same handler (no prefix strip — the handler is registered at the exact
// path and the fronting proxy passes the path through unchanged).
const mcpPath = "/mcp"

// authCheckTimeout bounds the per-request key check against mvd-api. It is
// short because /v1/auth/check is a trivial 204 and we do it inline on the
// request's critical path; keep-alive (the shared http.Client below) makes the
// steady-state cost a single round trip on a warm connection.
const authCheckTimeout = 10 * time.Second

// runHTTP serves MCP over streamable HTTP on addr, proxying to the mvd-api at
// apiURL. Each request must carry `Authorization: Bearer <key>`; the outer
// gate validates it against mvd-api /v1/auth/check, and getServer forwards the
// same key on every proxied REST call (D7). Blocks until SIGINT/SIGTERM.
func runHTTP(addr, apiURL string, timeout time.Duration, logger *slog.Logger) error {
	logger.Info("mvd-mcp starting (http)", "addr", addr, "api", apiURL, "path", mcpPath)

	// One searcher shared across all sessions: it holds no per-request state
	// (the hub anon key is public), so there is nothing per-key to isolate. The
	// outer auth gate is what stops an unauthenticated caller from driving it.
	search := newSupabaseClient(timeout)

	gate := &authGate{
		apiURL: strings.TrimRight(apiURL, "/"),
		// Dedicated short-timeout client with keep-alive (stdlib default
		// transport) so the per-request check reuses connections to mvd-api.
		http:   &http.Client{Timeout: authCheckTimeout},
		logger: logger,
	}

	// getServer builds a fresh mcp.Server per request whose proxy backend
	// forwards THIS request's bearer key to mvd-api. The token is already known
	// non-empty and validated by the outer gate before getServer runs, so
	// mvd-api is the single point of key validation for every proxied REST call.
	getServer := func(r *http.Request) *mcp.Server {
		key := bearerToken(r)
		backend := newProxyBackend(apiURL, key, timeout)
		return newMCPServer(backend, search)
	}

	// Stateless: each POST is self-contained (no Mcp-Session-Id continuity),
	// which is the natural fit for a shim that holds no per-session state and
	// sits behind a proxy — getServer runs per request, so per-request auth is
	// automatic. The SDK rejects GET (standalone SSE) with 405 in this mode; MCP
	// clients fall back to request/response POSTs, which is all the proxy needs.
	//
	// CrossOriginProtection is deliberately left off (nil): the server sits
	// behind Caddy on a real TLS domain, MCP clients are non-browser, and the
	// bearer-key gate — not an Origin check — is the security boundary. The
	// SDK's localhost DNS-rebinding guard still applies automatically when bound
	// to a loopback address; in production the bind is behind Caddy.
	handler := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless: true,
		Logger:    logger,
	})

	mux := http.NewServeMux()
	// Serve both "/mcp" and "/mcp/" so a client that appends a slash still hits
	// the handler (ServeMux's "/mcp/" subtree does not match the bare "/mcp").
	mux.Handle(mcpPath, gate.wrap(handler))
	mux.Handle(mcpPath+"/", gate.wrap(handler))
	// Unauthenticated liveness probe for the fronting proxy / systemd.
	mux.HandleFunc("GET /healthz", handleHealthz)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// ReadHeaderTimeout guards against slow-header DoS. We deliberately do
		// NOT set WriteTimeout: a streamed MCP response can outlive any fixed
		// deadline, and cutting it mid-stream would corrupt the response. Idle
		// keep-alives are bounded by IdleTimeout instead.
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("mvd-mcp shutting down", "signal", sig.String())
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

// handleHealthz is the liveness endpoint. It does no auth and touches no
// backend — it only proves the process is up and serving.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// authGate is the OUTER HTTP middleware. It is the single gate that protects
// every MCP tool — including the Supabase search tools, which do NOT transit
// mvd-api and would otherwise be reachable by a keyless session. It extracts
// the bearer key and validates it against mvd-api /v1/auth/check; only on 204
// does the request reach the streamable handler (and getServer).
type authGate struct {
	apiURL string
	http   *http.Client
	logger *slog.Logger
}

// wrap returns next guarded by the bearer-key check.
func (g *authGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := bearerToken(r)
		if key == "" {
			g.unauthorized(w)
			return
		}
		if !g.check(r.Context(), key) {
			g.unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// check calls mvd-api GET /v1/auth/check with the presented key. Returns true
// only on 204 (mvd-api's "valid key" signal). Any other status — 401, 429, a
// transport error — returns false, so the request is rejected. A transient
// mvd-api outage therefore reads as "unauthorized"; that is the safe default
// for an auth gate (fail closed).
func (g *authGate) check(ctx context.Context, key string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.apiURL+"/v1/auth/check", nil)
	if err != nil {
		g.logger.Error("auth-check build request", "err", err.Error())
		return false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := g.http.Do(req)
	if err != nil {
		// Do not log the key; log only that the check failed to reach mvd-api.
		g.logger.Error("auth-check request", "err", err.Error())
		return false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	return resp.StatusCode == http.StatusNoContent
}

// unauthorized writes a generic 401 with WWW-Authenticate: Bearer. The body
// never reveals whether the key was absent, malformed, or revoked (parity with
// mvd-api's own 401), so it leaks nothing about the key store.
func (g *authGate) unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"missing or invalid API key"}}`))
}

// bearerToken returns the raw token from an "Authorization: Bearer <token>"
// header, or "" if absent/malformed. Scheme match is case-insensitive per RFC
// 7235; the token is returned verbatim (it is the secret key). Mirrors
// mvd-api's bearerToken so the two ends agree on what a key looks like.
func bearerToken(r *http.Request) string {
	const prefix = "bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}
