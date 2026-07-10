package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// --- Served-asset behaviour: /openapi.yaml, /docs, /docs/rapidoc-min.js ---

func TestOpenAPISpecServed(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /openapi.yaml = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
	if !strings.Contains(string(body), "openapi: 3.1.0") {
		t.Error("spec body does not contain the openapi 3.1.0 version line")
	}
	etag := resp.Header.Get("ETag")
	if etag == "" || !strings.HasPrefix(etag, `"openapi-`) {
		t.Errorf("ETag = %q, want content-hash form \"openapi-…\"", etag)
	}

	// Conditional GET revalidates to 304.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/openapi.yaml", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", resp2.StatusCode)
	}
}

func TestDocsServed(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()

	for _, p := range []string{"/docs", "/docs/"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", p, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s Content-Type = %q, want text/html", p, ct)
		}
		// The shell must reference the spec and the vendored viewer — the
		// only two things that make the page work offline.
		if !strings.Contains(string(body), `spec-url="/openapi.yaml"`) {
			t.Errorf("GET %s body does not point RapiDoc at /openapi.yaml", p)
		}
		if !strings.Contains(string(body), "/docs/rapidoc-min.js") {
			t.Errorf("GET %s body does not load the vendored viewer", p)
		}
	}

	resp, err := http.Get(srv.URL + "/docs/rapidoc-min.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /docs/rapidoc-min.js = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("viewer Content-Type = %q, want text/javascript", ct)
	}
	if resp.ContentLength == 0 {
		t.Error("viewer bundle is empty")
	}
}
