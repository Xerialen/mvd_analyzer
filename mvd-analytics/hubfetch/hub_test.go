package hubfetch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// resolveSrv stands in for Supabase. Returns a single-row payload
// when the query carries `id=eq.<wantID>`, an empty array otherwise.
func resolveSrv(t *testing.T, wantID string, demoSHA, sourceURL string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity-check headers — Resolve must send the anon-key + Authorization.
		if r.Header.Get("apikey") == "" || r.Header.Get("Authorization") == "" {
			t.Errorf("missing auth headers: %v", r.Header)
		}
		q := r.URL.Query()
		if q.Get("id") != "eq."+wantID {
			t.Errorf("unexpected id filter: %q", q.Get("id"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":` + wantID + `,"demo_sha256":"` + demoSHA + `","demo_source_url":"` + sourceURL + `"}]`))
	}))
}

func TestResolve_HappyPath(t *testing.T) {
	srv := resolveSrv(t, "212111", "abc123def456", "https://example.com/demo.mvd.gz")
	defer srv.Close()

	c := NewClient()
	c.SupabaseURL = srv.URL

	info, err := c.Resolve(212111)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if info.ID != 212111 || info.DemoSHA256 != "abc123def456" || info.DemoSourceURL != "https://example.com/demo.mvd.gz" {
		t.Errorf("got %+v", info)
	}
}

func TestResolve_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient()
	c.SupabaseURL = srv.URL

	_, err := c.Resolve(99999999)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestResolve_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	c := NewClient()
	c.SupabaseURL = srv.URL

	_, err := c.Resolve(1)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 error, got %v", err)
	}
}

// Download must hit CDN first when sha256 is set; only fall back to
// demo_source_url when the CDN copy is unavailable.
func TestDownload_CDNHit(t *testing.T) {
	const sha = "abcdef0123456789"
	cdnHits, srcHits := 0, 0

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnHits++
		want := "/" + sha[:3] + "/" + sha + ".mvd.gz"
		if r.URL.Path != want {
			t.Errorf("CDN path = %q, want %q", r.URL.Path, want)
		}
		w.Write([]byte("FROM_CDN"))
	}))
	defer cdn.Close()

	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srcHits++
		w.Write([]byte("FROM_SOURCE"))
	}))
	defer src.Close()

	c := NewClient()
	c.CDNBase = cdn.URL

	data, err := c.Download(&GameInfo{DemoSHA256: sha, DemoSourceURL: src.URL})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(data) != "FROM_CDN" {
		t.Errorf("got %q, want FROM_CDN", data)
	}
	if cdnHits != 1 || srcHits != 0 {
		t.Errorf("cdnHits=%d srcHits=%d, want 1/0", cdnHits, srcHits)
	}
}

func TestDownload_FallbackOnCDNMiss(t *testing.T) {
	const sha = "deadbeefcafe1234"

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer cdn.Close()

	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("FROM_SOURCE"))
	}))
	defer src.Close()

	c := NewClient()
	c.CDNBase = cdn.URL

	data, err := c.Download(&GameInfo{DemoSHA256: sha, DemoSourceURL: src.URL})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(data) != "FROM_SOURCE" {
		t.Errorf("fallback returned %q, want FROM_SOURCE", data)
	}
}

func TestDownload_NoSHAUsesSourceDirectly(t *testing.T) {
	srcHits := 0
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srcHits++
		w.Write([]byte("X"))
	}))
	defer src.Close()

	c := NewClient()
	c.CDNBase = "http://invalid.invalid" // would fail if attempted

	data, err := c.Download(&GameInfo{DemoSourceURL: src.URL})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(data) != "X" || srcHits != 1 {
		t.Errorf("data=%q hits=%d", data, srcHits)
	}
}

func TestDownload_NoURLsAtAll(t *testing.T) {
	c := NewClient()
	_, err := c.Download(&GameInfo{})
	if err == nil {
		t.Errorf("expected error for empty GameInfo")
	}
}

// TestDownload_BodySizeCap covers F16: an oversized upstream body is
// rejected, while a body exactly at the cap is accepted (the fetch reader
// reads cap+1 to tell the two apart). Shrinks the package-level cap so the
// test is cheap.
func TestDownload_BodySizeCap(t *testing.T) {
	const sha = "abcdef0123456789"

	orig := maxDownloadBytes
	maxDownloadBytes = 1024
	defer func() { maxDownloadBytes = orig }()

	serveN := func(n int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write(make([]byte, n))
		}))
	}

	// Over the cap → error.
	over := serveN(int(maxDownloadBytes) + 1)
	defer over.Close()
	c := NewClient()
	c.CDNBase = over.URL
	if _, err := c.Download(&GameInfo{DemoSHA256: sha}); err == nil {
		t.Errorf("expected error for over-cap body")
	}

	// Exactly at the cap → success.
	atCap := serveN(int(maxDownloadBytes))
	defer atCap.Close()
	c2 := NewClient()
	c2.CDNBase = atCap.URL
	data, err := c2.Download(&GameInfo{DemoSHA256: sha})
	if err != nil {
		t.Fatalf("body at cap should succeed: %v", err)
	}
	if int64(len(data)) != maxDownloadBytes {
		t.Errorf("got %d bytes; want %d", len(data), maxDownloadBytes)
	}
}
