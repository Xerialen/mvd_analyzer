package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const goodKey = "qwmvd_goodkey"

// stubAPI is a fake mvd-api: it answers /v1/auth/check (204 for goodKey, 401
// otherwise) and one tool endpoint (/v1/demos/{id}/overview). It records the
// Authorization header seen on the LAST proxied overview call so a test can
// assert the user's key was forwarded.
type stubAPI struct {
	authChecks   atomic.Int64 // number of /v1/auth/check calls served
	overviewAuth atomic.Value // string: Authorization on the last overview call
}

func newStubAPI() *stubAPI {
	s := &stubAPI{}
	s.overviewAuth.Store("")
	return s
}

func (s *stubAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/auth/check", func(w http.ResponseWriter, r *http.Request) {
		s.authChecks.Add(1)
		if bearerToken(r) == goodKey {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("GET /v1/demos/{id}/overview", func(w http.ResponseWriter, r *http.Request) {
		s.overviewAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"map":"dm6","schemaVersion":49}`))
	})
	return mux
}

// bearerRoundTripper injects a fixed Authorization header on every client
// request, which is how a hosted MCP client would carry its key.
type bearerRoundTripper struct {
	key  string
	base http.RoundTripper
}

func (b *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if b.key != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+b.key)
	}
	return b.base.RoundTrip(req)
}

func bearerClient(key string) *http.Client {
	return &http.Client{Transport: &bearerRoundTripper{key: key, base: http.DefaultTransport}}
}

// newMCPTestServer wires the real streamable handler + auth gate against the
// stubbed mvd-api and returns an httptest.Server plus the stub for assertions.
func newMCPTestServer(t *testing.T) (*httptest.Server, *stubAPI) {
	t.Helper()
	api := newStubAPI()
	apiSrv := httptest.NewServer(api.handler())
	t.Cleanup(apiSrv.Close)

	logger := slog.New(slog.NewTextHandler(&testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError}))
	search := &fakeSearcher{}
	gate := &authGate{
		apiURL: strings.TrimRight(apiSrv.URL, "/"),
		http:   &http.Client{Timeout: authCheckTimeout},
		logger: logger,
	}
	getServer := func(r *http.Request) *mcp.Server {
		backend := newProxyBackend(apiSrv.URL, bearerToken(r), 5*time.Second)
		return newMCPServer(backend, search)
	}
	handler := newStreamableHandler(getServer, logger)

	mux := http.NewServeMux()
	mux.Handle(mcpPath, gate.wrap(handler))
	mux.Handle(mcpPath+"/", gate.wrap(handler))
	mux.HandleFunc("GET /healthz", handleHealthz)

	mcpSrv := httptest.NewServer(mux)
	t.Cleanup(mcpSrv.Close)
	return mcpSrv, api
}

// connectHTTP drives the real SDK client over streamable HTTP with the given
// bearer key. err is the Initialize error (nil on success).
func connectHTTP(t *testing.T, url, key string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "mvd-mcp-http-test", Version: "test"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   url + mcpPath,
		HTTPClient: bearerClient(key),
		// Non-browser client; we only need request/response, not the standalone
		// SSE stream (which stateless mode rejects with 405 anyway).
		DisableStandaloneSSE: true,
		MaxRetries:           -1, // fail fast on a 401 rather than retrying
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess, nil
}

// TestHTTP_EndToEnd_ValidKey proves the full path: Initialize + ListTools +
// CallTool(getOverview) all succeed with a good key, and the stubbed mvd-api
// saw the USER's key forwarded on the proxied overview call.
func TestHTTP_EndToEnd_ValidKey(t *testing.T) {
	srv, api := newMCPTestServer(t)

	sess, err := connectHTTP(t, srv.URL, goodKey)
	if err != nil {
		t.Fatalf("connect with good key: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tools, err := sess.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatalf("ListTools returned no tools")
	}

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "getOverview",
		Arguments: map[string]any{"demoId": "gameId:42"},
	})
	if err != nil {
		t.Fatalf("CallTool getOverview: %v", err)
	}
	if res.IsError {
		t.Fatalf("getOverview isError=true: %+v", res.Content)
	}
	var out map[string]any
	mustDecodeStructured(t, res, &out)
	if out["map"] != "dm6" {
		t.Errorf("overview map = %v; want dm6", out["map"])
	}

	// The load-bearing assertion: mvd-api saw the USER's key on the proxied call.
	gotAuth, _ := api.overviewAuth.Load().(string)
	if want := "Bearer " + goodKey; gotAuth != want {
		t.Errorf("overview Authorization = %q; want %q", gotAuth, want)
	}
	if api.authChecks.Load() == 0 {
		t.Errorf("auth-check gate never fired")
	}
}

// TestHTTP_MissingKey_401 asserts a keyless client cannot initialize and no
// auth-check is even attempted against mvd-api (the gate rejects on empty).
func TestHTTP_MissingKey_401(t *testing.T) {
	srv, api := newMCPTestServer(t)

	if _, err := connectHTTP(t, srv.URL, ""); err == nil {
		t.Fatalf("expected connect to fail with missing key")
	}
	// A missing key is rejected before we ever call mvd-api.
	if n := api.authChecks.Load(); n != 0 {
		t.Errorf("auth-check called %d times for missing key; want 0", n)
	}
	// Sanity: the gate returns a 401 to a raw POST as well.
	assertRawPOST401(t, srv.URL, "")
}

// TestHTTP_BadKey_401 asserts a bad key is rejected at init; the gate DID call
// mvd-api (which answered 401), and no tool ran.
func TestHTTP_BadKey_401(t *testing.T) {
	srv, api := newMCPTestServer(t)

	if _, err := connectHTTP(t, srv.URL, "qwmvd_wrong"); err == nil {
		t.Fatalf("expected connect to fail with bad key")
	}
	if n := api.authChecks.Load(); n == 0 {
		t.Errorf("auth-check gate never fired for bad key; want >=1")
	}
	// No overview call could have happened.
	if a, _ := api.overviewAuth.Load().(string); a != "" {
		t.Errorf("overview was reached with a bad key: auth=%q", a)
	}
}

// TestHTTP_KeylessSearchBlocked proves the Supabase search tool — which does
// NOT transit mvd-api — is unreachable without a valid key, because the OUTER
// gate blocks the session before getServer (and thus any tool) runs.
func TestHTTP_KeylessSearchBlocked(t *testing.T) {
	srv, _ := newMCPTestServer(t)

	// Even a well-formed tools/call for searchGames cannot get past init.
	if _, err := connectHTTP(t, srv.URL, ""); err == nil {
		t.Fatalf("expected keyless session to be rejected before search could run")
	}

	// Direct proof at the HTTP layer: a raw MCP tools/call POST for searchGames
	// without a key gets a 401 from the gate, never reaching the handler.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"searchGames","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+mcpPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("raw POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("keyless searchGames POST status = %d; want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("missing WWW-Authenticate: Bearer on 401")
	}
}

// TestHTTP_Healthz asserts the liveness endpoint is unauthenticated and 200.
func TestHTTP_Healthz(t *testing.T) {
	srv, _ := newMCPTestServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d; want 200", resp.StatusCode)
	}
}

// TestHTTP_NonLoopbackHostAccepted proves a proxy-forwarded request is not
// rejected by the SDK's DNS-rebinding guard: the connection arrives over
// loopback (httptest listens on 127.0.0.1, exactly as Caddy reaches the
// backend) while the Host header is the public domain. Without
// DisableLocalhostProtection (set in newStreamableHandler) this returns
// 403 "invalid Host header" — the exact failure seen behind Caddy in
// production. With it, the initialize succeeds.
func TestHTTP_NonLoopbackHostAccepted(t *testing.T) {
	srv, _ := newMCPTestServer(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+mcpPath, strings.NewReader(body))
	req.Host = "mvdanalyzer.example.com" // non-loopback, as a reverse proxy forwards it
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+goodKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("raw POST: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden || strings.Contains(string(rb), "invalid Host header") {
		t.Fatalf("non-loopback Host rejected (localhost guard not disabled): status=%d body=%s", resp.StatusCode, rb)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize with non-loopback Host: status=%d body=%s", resp.StatusCode, rb)
	}
}

// assertRawPOST401 does a raw MCP initialize POST with the given key and
// asserts a 401 from the gate.
func assertRawPOST401(t *testing.T, base, key string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`
	req, _ := http.NewRequest(http.MethodPost, base+mcpPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("raw POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("raw POST status = %d; want 401", resp.StatusCode)
	}
}

// testWriter adapts *testing.T to io.Writer for slog in tests.
type testWriter struct{ t *testing.T }

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
