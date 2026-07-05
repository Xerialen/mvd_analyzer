package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// cannedAPI returns a small httptest.Server that mimics mvd-api just
// enough for the proxy backend's HTTP wiring to be exercised. Each
// endpoint returns a tiny canned JSON body.
func cannedAPI(t *testing.T, recordAuth *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/demos/{id}", func(w http.ResponseWriter, r *http.Request) {
		if recordAuth != nil {
			*recordAuth = r.Header.Get("Authorization")
		}
		id := r.PathValue("id")
		if id == "gameId:9999" {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"demo_not_found","message":"unknown gameId"}}`))
			return
		}
		fmt.Fprintf(w, `{"demoId":"sha:%s","sha256":"%s","fromCache":false,"schemaVersion":7}`,
			strings.Repeat("a", 64), strings.Repeat("a", 64))
	})
	mux.HandleFunc("GET /v1/demos/{id}/overview", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"schemaVersion":7,"map":"dm6","duration":600,"matchStart":0,"matchEnd":600}`))
	})
	mux.HandleFunc("GET /v1/demos/{id}/buckets", func(w http.ResponseWriter, r *http.Request) {
		ms := r.URL.Query().Get("windowMs")
		fmt.Fprintf(w, `{"windowMs":%s,"buckets":[]}`, defaultIfEmpty(ms, "50"))
	})
	mux.HandleFunc("GET /v1/demos/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"events":[]}`))
	})
	mux.HandleFunc("GET /v1/demos/{id}/stream-slice", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"startTime":0,"endTime":0,"players":[]}`))
	})
	mux.HandleFunc("GET /v1/demos/{id}/state-at", func(w http.ResponseWriter, r *http.Request) {
		t := r.URL.Query().Get("time")
		fmt.Fprintf(w, `{"t":%s,"players":{}}`, defaultIfEmpty(t, "0"))
	})
	mux.HandleFunc("GET /v1/demos/{id}/loc-trails", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"players":[]}`))
	})
	mux.HandleFunc("GET /v1/demos/{id}/region-control", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"regions":[],"stats":{}}`))
	})
	// These three mvd-api endpoints return top-level JSON arrays; the
	// proxy must wrap them so the MCP SDK accepts the structuredContent.
	mux.HandleFunc("GET /v1/demos/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"type":"chat","player":"bps"}]`))
	})
	mux.HandleFunc("GET /v1/demos/{id}/backpacks", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"weapon":"rl","player":"bps"}]`))
	})
	mux.HandleFunc("GET /v1/demos/{id}/weapon-pickups", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func defaultIfEmpty(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func TestProxy_LoadDemo(t *testing.T) {
	srv := cannedAPI(t, nil)
	b := newProxyBackend(srv.URL, "", 5*time.Second)
	out, err := b.LoadDemo(context.Background(), LoadDemoInput{GameID: 42})
	if err != nil {
		t.Fatalf("LoadDemo: %v", err)
	}
	if out.SHA256 == "" || !strings.HasPrefix(out.DemoID, "sha:") {
		t.Errorf("unexpected output %+v", out)
	}
}

func TestProxy_LoadDemo_NotFound(t *testing.T) {
	srv := cannedAPI(t, nil)
	b := newProxyBackend(srv.URL, "", 5*time.Second)
	_, err := b.LoadDemo(context.Background(), LoadDemoInput{GameID: 9999})
	if err == nil {
		t.Fatalf("expected error")
	}
	pe, ok := err.(*proxyError)
	if !ok {
		t.Fatalf("expected *proxyError, got %T: %v", err, err)
	}
	if pe.Status != 404 || pe.Code != "demo_not_found" {
		t.Errorf("status=%d code=%q; want 404 demo_not_found", pe.Status, pe.Code)
	}
}

func TestProxy_GetOverview(t *testing.T) {
	srv := cannedAPI(t, nil)
	b := newProxyBackend(srv.URL, "", 5*time.Second)
	out, err := b.GetOverview(context.Background(), GetOverviewInput{DemoID: "gameId:42"})
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", out)
	}
	if m["map"] != "dm6" {
		t.Errorf("map=%v; want dm6", m["map"])
	}
}

func TestProxy_GetBuckets_WindowMs(t *testing.T) {
	srv := cannedAPI(t, nil)
	b := newProxyBackend(srv.URL, "", 5*time.Second)
	out, err := b.GetBuckets(context.Background(), GetBucketsInput{DemoID: "gameId:42", WindowMs: 1000})
	if err != nil {
		t.Fatalf("GetBuckets: %v", err)
	}
	m := out.(map[string]any)
	if m["windowMs"].(float64) != 1000 {
		t.Errorf("windowMs=%v; want 1000", m["windowMs"])
	}
}

// TestProxy_GetBuckets_MCPDefaultIs1s verifies that omitting
// WindowMs in the MCP input forwards windowMs=1000 to the REST API
// (not 50, which is what the API itself defaults to). This is the
// MCP-side ergonomic default to keep buckets responses
// LLM-readable.
func TestProxy_GetBuckets_MCPDefaultIs1s(t *testing.T) {
	srv := cannedAPI(t, nil)
	b := newProxyBackend(srv.URL, "", 5*time.Second)
	out, err := b.GetBuckets(context.Background(), GetBucketsInput{DemoID: "gameId:42"})
	if err != nil {
		t.Fatalf("GetBuckets: %v", err)
	}
	m := out.(map[string]any)
	if m["windowMs"].(float64) != 1000 {
		t.Errorf("MCP default windowMs=%v; want 1000 (proxy must inject)", m["windowMs"])
	}
}

// TestProxy_GetBuckets_LayoutForwarded verifies that the layout input
// reaches the REST query (and that omitting it sends no layout, leaving
// the API default).
func TestProxy_GetBuckets_LayoutForwarded(t *testing.T) {
	var seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.RawQuery
		w.Write([]byte(`{"windowMs":1000,"count":0}`))
	}))
	defer srv.Close()
	b := newProxyBackend(srv.URL, "", 5*time.Second)

	if _, err := b.GetBuckets(context.Background(), GetBucketsInput{DemoID: "gameId:42", Layout: "column"}); err != nil {
		t.Fatalf("GetBuckets: %v", err)
	}
	if !strings.Contains(seenQuery, "layout=column") {
		t.Errorf("expected layout=column in query; got %q", seenQuery)
	}

	if _, err := b.GetBuckets(context.Background(), GetBucketsInput{DemoID: "gameId:42"}); err != nil {
		t.Fatalf("GetBuckets: %v", err)
	}
	if strings.Contains(seenQuery, "layout=") {
		t.Errorf("omitted layout should not be forwarded; got %q", seenQuery)
	}
}

func TestProxy_GetRegionControl_MCPDefaultIs1s(t *testing.T) {
	var seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.RawQuery
		w.Write([]byte(`{"regions":[],"stats":{}}`))
	}))
	defer srv.Close()
	b := newProxyBackend(srv.URL, "", 5*time.Second)

	if _, err := b.GetRegionControl(context.Background(), GetRegionControlInput{DemoID: "gameId:42"}); err != nil {
		t.Fatalf("GetRegionControl: %v", err)
	}
	if !strings.Contains(seenQuery, "windowMs=1000") {
		t.Errorf("expected windowMs=1000 in query; got %q", seenQuery)
	}
}

func TestProxy_GetStateAt(t *testing.T) {
	srv := cannedAPI(t, nil)
	b := newProxyBackend(srv.URL, "", 5*time.Second)
	out, err := b.GetStateAt(context.Background(), GetStateAtInput{DemoID: "gameId:42", Time: 15})
	if err != nil {
		t.Fatalf("GetStateAt: %v", err)
	}
	m := out.(map[string]any)
	if m["t"].(float64) != 15 {
		t.Errorf("t=%v; want 15", m["t"])
	}
}

func TestProxy_LabelForwardedAsBearer(t *testing.T) {
	var seenAuth string
	srv := cannedAPI(t, &seenAuth)
	b := newProxyBackend(srv.URL, "mcp-test", 5*time.Second)
	if _, err := b.LoadDemo(context.Background(), LoadDemoInput{GameID: 42}); err != nil {
		t.Fatalf("LoadDemo: %v", err)
	}
	if seenAuth != "Bearer mcp-test" {
		t.Errorf("Authorization=%q; want Bearer mcp-test", seenAuth)
	}
}

func TestProxy_EmptyLabel_NoAuthHeader(t *testing.T) {
	var seenAuth string
	srv := cannedAPI(t, &seenAuth)
	b := newProxyBackend(srv.URL, "", 5*time.Second)
	if _, err := b.LoadDemo(context.Background(), LoadDemoInput{GameID: 42}); err != nil {
		t.Fatalf("LoadDemo: %v", err)
	}
	if seenAuth != "" {
		t.Errorf("Authorization=%q; want empty", seenAuth)
	}
}

// TestProxy_DemoPathValidation covers F5: a model-supplied demoId that
// isn't a canonical gameId:N / sha:HEX — in particular one carrying '/' or
// '?' — is rejected before any HTTP call, so it can't reroute the request.
func TestProxy_DemoPathValidation(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	b := newProxyBackend(srv.URL, "", 5*time.Second)
	ctx := context.Background()

	bad := []string{
		"",                          // empty
		"gameId:42/frags?players=x", // path-splice reroute
		"gameId:42#frag",            // fragment
		"sha:xyz",                   // not 64 hex
		"../secrets",                // traversal
		"gameId:",                   // missing number
		"gameId:0x10",               // non-decimal
	}
	for _, id := range bad {
		if _, err := b.GetOverview(ctx, GetOverviewInput{DemoID: id}); err == nil {
			t.Errorf("GetOverview(%q): expected validation error, got nil", id)
		}
	}
	if hits != 0 {
		t.Errorf("invalid demoIds reached the backend %d times; want 0", hits)
	}

	// Canonical ids pass through untouched.
	for _, id := range []string{"gameId:42", "sha:" + strings.Repeat("a", 64), "sha:" + strings.Repeat("A", 64)} {
		if _, err := b.GetOverview(ctx, GetOverviewInput{DemoID: id}); err != nil {
			t.Errorf("GetOverview(%q): valid id rejected: %v", id, err)
		}
	}
	if hits != 3 {
		t.Errorf("valid ids reached backend %d times; want 3", hits)
	}
}

// TestProxy_GetBackpacks_WeaponCSV covers F7a: Weapon is a []string set
// forwarded as CSV, matching REST /backpacks (was a single string).
func TestProxy_GetBackpacks_WeaponCSV(t *testing.T) {
	var seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.RawQuery
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	b := newProxyBackend(srv.URL, "", 5*time.Second)

	if _, err := b.GetBackpacks(context.Background(), GetBackpacksInput{
		DemoID: "gameId:42", Weapon: []string{"rl", "lg"},
	}); err != nil {
		t.Fatalf("GetBackpacks: %v", err)
	}
	vals, _ := url.ParseQuery(seenQuery)
	if vals.Get("weapon") != "rl,lg" {
		t.Errorf("weapon=%q; want rl,lg (CSV set)", vals.Get("weapon"))
	}
}

// Smoke test that all the view endpoints decode without error.
func TestProxy_AllView(t *testing.T) {
	srv := cannedAPI(t, nil)
	b := newProxyBackend(srv.URL, "", 5*time.Second)
	ctx := context.Background()

	if _, err := b.GetEvents(ctx, GetEventsInput{DemoID: "gameId:42"}); err != nil {
		t.Errorf("GetEvents: %v", err)
	}
	if _, err := b.GetStreamSlice(ctx, GetStreamSliceInput{DemoID: "gameId:42"}); err != nil {
		t.Errorf("GetStreamSlice: %v", err)
	}
	if _, err := b.GetLocTrails(ctx, GetLocTrailsInput{DemoID: "gameId:42"}); err != nil {
		t.Errorf("GetLocTrails: %v", err)
	}
	if _, err := b.GetRegionControl(ctx, GetRegionControlInput{DemoID: "gameId:42"}); err != nil {
		t.Errorf("GetRegionControl: %v", err)
	}
}

// The array-bodied endpoints must come back wrapped in an object under a
// named key — a bare array fails the MCP SDK's structuredContent check.
func TestProxy_ListEndpointsWrapped(t *testing.T) {
	srv := cannedAPI(t, nil)
	b := newProxyBackend(srv.URL, "", 5*time.Second)
	ctx := context.Background()

	cases := []struct {
		name string
		key  string
		want int // expected element count under key
		call func() (any, error)
	}{
		{"chat", "messages", 1, func() (any, error) {
			return b.GetChat(ctx, GetChatInput{DemoID: "gameId:42"})
		}},
		{"backpacks", "backpacks", 1, func() (any, error) {
			return b.GetBackpacks(ctx, GetBackpacksInput{DemoID: "gameId:42"})
		}},
		{"weapon-pickups", "pickups", 0, func() (any, error) {
			return b.GetWeaponPickups(ctx, GetWeaponPickupsInput{DemoID: "gameId:42"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.call()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			m, ok := out.(map[string]any)
			if !ok {
				t.Fatalf("%s: structuredContent must be an object, got %T", tc.name, out)
			}
			arr, ok := m[tc.key].([]any)
			if !ok {
				t.Fatalf("%s: missing %q array, got %+v", tc.name, tc.key, m)
			}
			if len(arr) != tc.want {
				t.Errorf("%s: len(%s)=%d; want %d", tc.name, tc.key, len(arr), tc.want)
			}
		})
	}
}
