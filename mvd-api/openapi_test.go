package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-analytics/view"
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

// --- Drift tests: pin openapi/openapi.yaml to the code -------------------
//
// House pattern: TestArtifactsMarkdownCommittedIsCurrent (regenerate-and-
// compare) and the README mermaid block (marker-delimited compare) in
// mvd-analytics/analyzer/manifest_test.go. The spec is hand-authored, so
// these tests scan it line-wise (no YAML dependency): path items are
// "  /path:" (2-space indent), methods "    get:" (4-space), and the
// marker comments delimit pinned enum blocks. openapi.yaml declares this
// formatting load-bearing in its header.

// specPaths parses "METHOD path" operations out of the embedded spec text.
func specPaths(t *testing.T) map[string]bool {
	t.Helper()
	ops := map[string]bool{}
	inPaths := false
	var curPath string
	pathRe := regexp.MustCompile(`^  (/[^:]*):\s*$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|delete|patch|head|options):\s*$`)
	for _, line := range strings.Split(string(openapiSpec), "\n") {
		switch {
		case line == "paths:":
			inPaths = true
			continue
		case inPaths && len(line) > 0 && line[0] != ' ' && line[0] != '#':
			inPaths = false // next top-level key (components:)
		}
		if !inPaths {
			continue
		}
		if m := pathRe.FindStringSubmatch(line); m != nil {
			curPath = m[1]
			continue
		}
		if m := methodRe.FindStringSubmatch(line); m != nil && curPath != "" {
			ops[strings.ToUpper(m[1])+" "+curPath] = true
		}
	}
	if len(ops) == 0 {
		t.Fatal("specPaths parsed no operations — has the paths: formatting changed?")
	}
	return ops
}

// routerOps parses the "METHOD /path" mux registrations out of router.go
// source. go test runs with cwd = the package dir, the same convention
// manifest_test.go uses to read ../ARTIFACTS.md.
func routerOps(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	re := regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]+)"`)
	ops := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		ops[m[1]+" "+m[2]] = true
	}
	// Floor guard against regex rot: 33 v1 routes + the 4 spec/docs
	// registrations existed when this test was written.
	if len(ops) < 40 {
		t.Fatalf("routerOps found only %d registrations — has the HandleFunc pattern changed?", len(ops))
	}
	return ops
}

// TestOpenAPICoversAllRoutes pins the spec's path+method set to router.go,
// both directions.
func TestOpenAPICoversAllRoutes(t *testing.T) {
	// Viewer plumbing intentionally undocumented: /docs is the documented
	// entry point; the trailing-slash alias and the JS asset are not API.
	excluded := map[string]bool{
		"GET /docs/{$}":              true,
		"GET /docs/rapidoc-min.js":   true,
		"GET /docs/result-schema.md": true, // raw-markdown sibling, described on the page's op
		"GET /docs/marked.min.js":    true,
	}
	router := routerOps(t)
	spec := specPaths(t)
	for op := range router {
		if excluded[op] {
			continue
		}
		if !spec[op] {
			t.Errorf("route %q is registered but missing from openapi/openapi.yaml — document it", op)
		}
	}
	for op := range spec {
		if !router[op] {
			t.Errorf("spec documents %q but router.go does not register it — remove or fix the path", op)
		}
	}
}

// markerBlock extracts the "- value" entries between the begin/end marker
// comments for the named enum block.
func markerBlock(t *testing.T, name string) []string {
	t.Helper()
	begin, end := "# "+name+":begin", "# "+name+":end"
	var out []string
	in := false
	for _, line := range strings.Split(string(openapiSpec), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, begin):
			if in {
				t.Fatalf("duplicate %s marker", begin)
			}
			in = true
		case strings.HasPrefix(trimmed, end):
			if !in {
				t.Fatalf("%s before %s", end, begin)
			}
			return out
		case in:
			v, ok := strings.CutPrefix(trimmed, "- ")
			if !ok {
				t.Fatalf("non-enum line %q inside %s block", line, name)
			}
			out = append(out, v)
		}
	}
	t.Fatalf("marker block %s not found in openapi.yaml", name)
	return nil
}

// diffSets reports both directions of a set mismatch with actionable names.
func diffSets(t *testing.T, what string, got, want []string) {
	t.Helper()
	gotSet, wantSet := map[string]bool{}, map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	for _, w := range want {
		wantSet[w] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("%s: %q is missing from openapi/openapi.yaml — add it to the marker block", what, w)
		}
	}
	for _, g := range got {
		if !wantSet[g] {
			t.Errorf("%s: %q is in openapi/openapi.yaml but not in the code — remove it", what, g)
		}
	}
}

// TestOpenAPIArtifactEnumCurrent pins the artifact-name enum to the
// servable subset of analyzer.ArtifactManifest(). On failure the expected
// block is printed ready to paste (the README-mermaid re-embed precedent).
func TestOpenAPIArtifactEnumCurrent(t *testing.T) {
	var want []string
	for _, m := range analyzer.ArtifactManifest() {
		if m.Servable {
			want = append(want, m.Name)
		}
	}
	got := markerBlock(t, "artifact-enum")
	diffSets(t, "artifact enum", got, want)
	if t.Failed() {
		var b strings.Builder
		for _, w := range want {
			fmt.Fprintf(&b, "          - %s\n", w)
		}
		t.Logf("expected artifact-enum block (registration order):\n%s", b.String())
	}
}

// TestOpenAPIFieldCodesCurrent pins the fields= selector enum to the view
// registry: the default set plus the deliberate opt-ins. A new opt-in field
// added to view/fields.go must be added both there and here.
func TestOpenAPIFieldCodesCurrent(t *testing.T) {
	want := append([]string{}, view.AllStandardFields...)
	want = append(want, view.FieldView, view.FieldHeight, view.FieldLiquid, view.FieldVelocity)
	got := markerBlock(t, "field-code-enum")
	diffSets(t, "field-code enum", got, want)
}

// TestOpenAPIErrorCodesCurrent pins the ErrorCode enum to the writeError /
// writeUnavailable call sites across the package (plus the eagerArtifacts
// code table). Scanning source beats a canonical slice refactor: the codes
// are call-site literals today and gain nothing from indirection.
func TestOpenAPIErrorCodesCurrent(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	res := []*regexp.Regexp{
		regexp.MustCompile(`writeError\(w,\s*[^,]+,\s*"([a-z_]+)"`),
		regexp.MustCompile(`writeUnavailable\(w, r, err,\s*"([a-z_]+)"`),
		regexp.MustCompile(`code:\s*"([a-z_]+_unavailable)"`), // the eagerArtifacts table
	}
	found := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, re := range res {
			for _, m := range re.FindAllStringSubmatch(string(src), -1) {
				found[m[1]] = true
			}
		}
	}
	// Floor guard against regex rot: 20 distinct codes existed when this
	// test was written.
	if len(found) < 20 {
		t.Fatalf("error-code scan found only %d codes — have the writeError call shapes changed?", len(found))
	}
	want := make([]string, 0, len(found))
	for c := range found {
		want = append(want, c)
	}
	sort.Strings(want)
	got := markerBlock(t, "error-code-enum")
	diffSets(t, "error-code enum", got, want)
	if t.Failed() {
		t.Logf("expected error-code-enum block (alphabetical):\n        - %s", strings.Join(want, "\n        - "))
	}
}

// TestOpenAPIVersionMatchesSchemaVersion pins info.version to
// result.CurrentSchemaVersion — same UX as an ARTIFACTS.md staleness
// failure: bump the one line and recommit.
func TestOpenAPIVersionMatchesSchemaVersion(t *testing.T) {
	wantLine := fmt.Sprintf("  version: %q", fmt.Sprintf("%d", result.CurrentSchemaVersion))
	if !strings.Contains(string(openapiSpec), wantLine+"\n") {
		t.Fatalf("openapi/openapi.yaml info.version is stale — set the line %s (schema version %d)",
			wantLine, result.CurrentSchemaVersion)
	}
}

// TestResultSchemaServed: /docs/result-schema renders RESULT_SCHEMA.md
// standalone — the shell, the raw markdown, and the vendored renderer.
func TestResultSchemaServed(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/docs/result-schema")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /docs/result-schema = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	// The shell must reference the markdown and the vendored renderer.
	if !strings.Contains(string(body), "/docs/result-schema.md") ||
		!strings.Contains(string(body), "/docs/marked.min.js") {
		t.Error("shell does not reference the markdown + renderer assets")
	}

	resp, err = http.Get(srv.URL + "/docs/result-schema.md")
	if err != nil {
		t.Fatal(err)
	}
	md, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /docs/result-schema.md = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(md), "# Result schema") && !strings.Contains(string(md), "RESULT_SCHEMA") &&
		!strings.Contains(string(md), "schemaVersion") {
		t.Errorf("markdown body does not look like RESULT_SCHEMA.md (%d bytes)", len(md))
	}

	resp, err = http.Get(srv.URL + "/docs/marked.min.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /docs/marked.min.js = %d, want 200", resp.StatusCode)
	}
}
