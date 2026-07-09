package main

import (
	"context"
	"errors"
	"fmt"
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

// apiKeyPrefix mirrors authkeys.KeyPrefix in mvd-api (not imported — the shim
// deliberately depends only on the wire contract). A client-supplied bearer is
// forwarded upstream only when it carries this prefix; anything else (e.g. an
// OAuth token a chat platform attaches on its own) is ignored so it cannot
// break an otherwise anonymous session.
const apiKeyPrefix = "qwmvd_"

// newStreamableHandler builds the MCP streamable-HTTP handler with the options
// this server requires behind a reverse proxy. Shared by runHTTP and the tests
// so both exercise the same configuration.
//
// Stateless: each POST is self-contained (no Mcp-Session-Id continuity), the
// natural fit for a shim that holds no per-session state — getServer runs per
// request, so per-request key selection is automatic. The SDK rejects GET
// (standalone SSE) with 405 in this mode; MCP clients fall back to
// request/response POSTs, which is all the proxy needs.
//
// CrossOriginProtection is deliberately left off (nil): the server sits behind
// Caddy on a real TLS domain, MCP clients are non-browser, and every tool is
// read-only against public demo data — there is no cookie/ambient-credential
// surface for a cross-origin request to abuse.
//
// DisableLocalhostProtection is REQUIRED for a proxied deployment. The SDK's
// DNS-rebinding guard rejects any request that arrives over the loopback
// interface with a non-loopback Host header — and a reverse proxy (Caddy)
// forwards every request over loopback (localhost:8081) while preserving the
// public Host (e.g. "example.com"), so without this every proxied call 403s
// "invalid Host header". That guard protects a *local* dev server reached
// directly by a browser; this HTTP mode is designed to run behind a trusted
// proxy, so the guard is the wrong fit. (If you ever expose -http directly
// to browsers without a proxy, re-enable it and set a Host allowlist instead.)
func newStreamableHandler(getServer func(*http.Request) *mcp.Server, logger *slog.Logger) *mcp.StreamableHTTPHandler {
	return mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		DisableLocalhostProtection: true,
		Logger:                     logger,
	})
}

// runHTTP serves MCP over streamable HTTP on addr, proxying to the mvd-api at
// apiURL. MCP itself is UNAUTHENTICATED — requiring a bearer key per MCP
// request proved too cumbersome for the clients this transport exists for
// (web AI chat connectors), and every tool is read-only over public demo
// data. Instead the shim authenticates ITSELF to mvd-api: apiKey (from
// MVD_API_KEY, operator-issued service key) is forwarded on every proxied
// REST call, so mvd-api keeps full key auth and its per-key rate limit on
// that one key acts as the global throttle for anonymous MCP traffic. A
// request that does carry its own qwmvd_ bearer overrides the service key
// (per-key attribution still works). Blocks until SIGINT/SIGTERM.
func runHTTP(addr, apiURL, apiKey string, timeout time.Duration, logger *slog.Logger) error {
	logger.Info("mvd-mcp starting (http)",
		"addr", addr, "api", apiURL, "path", mcpPath, "serviceKeySet", apiKey != "")
	if apiKey == "" {
		logger.Warn("MVD_API_KEY is not set; proxied calls carry no key and will 401 against an auth-enabled mvd-api")
	}

	// One searcher shared across all sessions: it holds no per-request state
	// (the hub anon key is public), so there is nothing per-key to isolate.
	search := newSupabaseClient(timeout)

	handler := newStreamableHandler(newGetServer(apiURL, apiKey, timeout, search), logger)

	mux := http.NewServeMux()
	// Serve both "/mcp" and "/mcp/" so a client that appends a slash still hits
	// the handler (ServeMux's "/mcp/" subtree does not match the bare "/mcp").
	mux.Handle(mcpPath, handler)
	mux.Handle(mcpPath+"/", handler)
	// Liveness probe for the fronting proxy / systemd.
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

// newGetServer returns the per-request server factory for HTTP mode: each
// request gets a fresh mcp.Server whose proxy backend forwards the service
// key — or the caller's own qwmvd_ bearer when one is presented — to mvd-api,
// which is the single point of key validation. Shared by runHTTP and the
// tests so both exercise the same key selection.
func newGetServer(apiURL, apiKey string, timeout time.Duration, search searcher) func(*http.Request) *mcp.Server {
	return func(r *http.Request) *mcp.Server {
		key := apiKey
		if t := bearerToken(r); strings.HasPrefix(t, apiKeyPrefix) {
			key = t
		}
		backend := newProxyBackend(apiURL, key, timeout)
		return newMCPServer(backend, search)
	}
}

// handleHealthz is the liveness endpoint. It does no auth and touches no
// backend — it only proves the process is up and serving.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// bearerToken returns the raw token from an "Authorization: Bearer <token>"
// header, or "" if absent/malformed. Scheme match is case-insensitive per RFC
// 7235; the token is returned verbatim (it may be a secret key). Mirrors
// mvd-api's bearerToken so the two ends agree on what a key looks like.
func bearerToken(r *http.Request) string {
	const prefix = "bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}
