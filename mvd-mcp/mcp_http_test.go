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

// serviceKey is the operator-issued key the shim itself holds (MVD_API_KEY in
// production); userKey is a portal key a client may present per request.
const (
	serviceKey = "qwmvd_servicekey"
	userKey    = "qwmvd_userkey"
)

// stubAPI is a fake auth-enabled mvd-api: it answers one tool endpoint
// (/v1/demos/{id}/overview), 401ing unless the request carries a known key.
// It records the Authorization header seen on the LAST proxied overview call
// so a test can assert which key was forwarded.
type stubAPI struct {
	overviewAuth atomic.Value // string: Authorization on the last overview call
}

func newStubAPI() *stubAPI {
	s := &stubAPI{}
	s.overviewAuth.Store("")
	return s
}

func (s *stubAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/demos/{id}/overview", func(w http.ResponseWriter, r *http.Request) {
		s.overviewAuth.Store(r.Header.Get("Authorization"))
		if k := bearerToken(r); k != serviceKey && k != userKey {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"map":"dm6","schemaVersion":50}`))
	})
	return mux
}

// bearerRoundTripper injects a fixed Authorization header on every client
// request, for tests that present a per-request key.
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

// newMCPTestServer wires the real streamable handler — with the same
// key-selection logic runHTTP installs — against the stubbed mvd-api.
func newMCPTestServer(t *testing.T) (*httptest.Server, *stubAPI) {
	t.Helper()
	api := newStubAPI()
	apiSrv := httptest.NewServer(api.handler())
	t.Cleanup(apiSrv.Close)

	logger := slog.New(slog.NewTextHandler(&testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError}))
	search := &fakeSearcher{}
	handler := newStreamableHandler(newGetServer(apiSrv.URL, serviceKey, 5*time.Second, search), logger)

	mux := http.NewServeMux()
	mux.Handle(mcpPath, handler)
	mux.Handle(mcpPath+"/", handler)
	mux.HandleFunc("GET /healthz", handleHealthz)

	mcpSrv := httptest.NewServer(mux)
	t.Cleanup(mcpSrv.Close)
	return mcpSrv, api
}

// connectHTTP drives the real SDK client over streamable HTTP with the given
// bearer key ("" = anonymous). err is the Initialize error (nil on success).
func connectHTTP(t *testing.T, url, key string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "mvd-mcp-http-test", Version: "test"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   url + mcpPath,
		HTTPClient: bearerClient(key),
		// Non-browser client; we only need request/response, not the standalone
		// SSE stream (which stateless mode rejects with 405 anyway).
		DisableStandaloneSSE: true,
		MaxRetries:           -1, // fail fast rather than retrying
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

// callOverview runs Initialize + ListTools + CallTool(getOverview) on a fresh
// session with the given client key and returns the Authorization the stub
// API saw on the proxied call.
func callOverview(t *testing.T, srv *httptest.Server, api *stubAPI, clientKey string) string {
	t.Helper()
	sess, err := connectHTTP(t, srv.URL, clientKey)
	if err != nil {
		t.Fatalf("connect (key=%q): %v", clientKey, err)
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
	auth, _ := api.overviewAuth.Load().(string)
	return auth
}

// TestHTTP_Anonymous_UsesServiceKey proves the core of the no-auth MCP model:
// a keyless client can Initialize + ListTools + CallTool, and the proxied
// REST call reaches mvd-api carrying the shim's SERVICE key.
func TestHTTP_Anonymous_UsesServiceKey(t *testing.T) {
	srv, api := newMCPTestServer(t)
	if got, want := callOverview(t, srv, api, ""), "Bearer "+serviceKey; got != want {
		t.Errorf("overview Authorization = %q; want %q (service key)", got, want)
	}
}

// TestHTTP_ClientKey_Overrides proves a client presenting its own qwmvd_ key
// has THAT key forwarded upstream instead of the service key.
func TestHTTP_ClientKey_Overrides(t *testing.T) {
	srv, api := newMCPTestServer(t)
	if got, want := callOverview(t, srv, api, userKey), "Bearer "+userKey; got != want {
		t.Errorf("overview Authorization = %q; want %q (client key)", got, want)
	}
}

// TestHTTP_ForeignBearer_Ignored proves a non-qwmvd_ bearer (e.g. an OAuth
// token a chat platform attaches on its own) does not break the session: it
// is ignored and the service key is used upstream.
func TestHTTP_ForeignBearer_Ignored(t *testing.T) {
	srv, api := newMCPTestServer(t)
	if got, want := callOverview(t, srv, api, "eyJhbGciOi.notaqwmvdkey"), "Bearer "+serviceKey; got != want {
		t.Errorf("overview Authorization = %q; want %q (service key)", got, want)
	}
}

// TestHTTP_AnonymousSearch proves the Supabase search tool — which does not
// transit mvd-api — is callable without any Authorization header.
func TestHTTP_AnonymousSearch(t *testing.T) {
	srv, _ := newMCPTestServer(t)

	sess, err := connectHTTP(t, srv.URL, "")
	if err != nil {
		t.Fatalf("anonymous connect: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "searchGames",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool searchGames: %v", err)
	}
	if res.IsError {
		t.Fatalf("searchGames isError=true: %+v", res.Content)
	}
}

// TestHTTP_Healthz asserts the liveness endpoint is 200.
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

// testWriter adapts *testing.T to io.Writer for slog in tests.
type testWriter struct{ t *testing.T }

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// TestHTTP_ErrorSurfacesAPIMessage: same guard as the in-memory
// transport test, but through the streamable HTTP handler the hosted
// deployment uses — a REST 4xx body must arrive verbatim in the
// isError text content, not as an opaque protocol error.
func TestHTTP_ErrorSurfacesAPIMessage(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_param","message":"unknown field code loc; valid codes: li (location), h (health)"}}`))
	}))
	defer api.Close()

	logger := slog.New(slog.NewTextHandler(&testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := newStreamableHandler(newGetServer(api.URL, serviceKey, 5*time.Second, nil), logger)
	mux := http.NewServeMux()
	mux.Handle(mcpPath, handler)
	mux.Handle(mcpPath+"/", handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sess, err := connectHTTP(t, srv.URL, "")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "getStateAt",
		Arguments: map[string]any{"demoId": "gameId:42", "time": 0.3, "fields": []string{"loc"}},
	})
	if err != nil {
		t.Fatalf("CallTool must not fail at the protocol level: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected isError=true, got %+v", res)
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "unknown field code loc") || !strings.Contains(text, "valid codes") {
		t.Errorf("error text must carry the API message verbatim, got %q", text)
	}
}
