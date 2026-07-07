package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/result"
)

// getHeader does a GET and returns the decoded JSON, status, and a named
// response header — for asserting ETag / cache semantics on the new surface.
func getWithHeaders(t *testing.T, url string) (map[string]any, *http.Response) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	return m, resp
}

// --- GET /v1/artifacts (manifest) ---

func TestArtifactsManifest(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()

	body, resp := getWithHeaders(t, srv.URL+"/v1/artifacts")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	if int(body["schemaVersion"].(float64)) != result.CurrentSchemaVersion {
		t.Errorf("schemaVersion = %v", body["schemaVersion"])
	}
	arts, _ := body["artifacts"].([]any)
	if len(arts) != len(analyzer.ArtifactManifest()) {
		t.Fatalf("manifest has %d entries; want %d", len(arts), len(analyzer.ArtifactManifest()))
	}

	// Servable/non-servable + resultKey flags survive the JSON round-trip.
	byName := map[string]map[string]any{}
	for _, a := range arts {
		m := a.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if frag := byName["frag"]; frag["servable"] != true || frag["resultKey"] != "frags" {
		t.Errorf("frag entry = %v; want servable true resultKey frags", frag)
	}
	if clock := byName["clock"]; clock["servable"] != false || clock["resultKey"] != "" {
		t.Errorf("clock entry = %v; want non-servable, empty resultKey", clock)
	}
	// los is the only lazy artifact since phase 12 folded shot-streams into the
	// eager always-full parse — the latter must be gone from the manifest.
	if los := byName["los"]; los["lazy"] != true || los["cost"] != "heavy" || los["servable"] != true {
		t.Errorf("los entry = %v; want lazy heavy servable", los)
	}
	if _, present := byName["shot-streams"]; present {
		t.Error("shot-streams should no longer appear in the manifest (folded into the eager parse)")
	}

	// Static ETag keyed on the schema version → cheap 304.
	wantETag := fmt.Sprintf(`"artifacts-v%d"`, result.CurrentSchemaVersion)
	if resp.Header.Get("ETag") != wantETag {
		t.Errorf("ETag = %q; want %q", resp.Header.Get("ETag"), wantETag)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/v1/artifacts", nil)
	req.Header.Set("If-None-Match", wantETag)
	r2, _ := http.DefaultClient.Do(req)
	r2.Body.Close()
	if r2.StatusCode != 304 {
		t.Errorf("If-None-Match status = %d; want 304", r2.StatusCode)
	}
}

// --- GET /v1/graph ---

func TestGraphEndpoint(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()

	body, resp := getWithHeaders(t, srv.URL+"/v1/graph")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	nodes, _ := body["nodes"].([]any)
	if len(nodes) != len(analyzer.ArtifactManifest()) {
		t.Errorf("graph nodes = %d; want %d (manifest node count)", len(nodes), len(analyzer.ArtifactManifest()))
	}
	if edges, _ := body["edges"].([]any); len(edges) == 0 {
		t.Error("graph has no edges")
	}
	wantETag := fmt.Sprintf(`"graph-v%d"`, result.CurrentSchemaVersion)
	if resp.Header.Get("ETag") != wantETag {
		t.Errorf("ETag = %q; want %q", resp.Header.Get("ETag"), wantETag)
	}
}

// --- GET /v1/demos/{id}/artifacts/{name} ---

func fragOnlyStore() *fakeStore {
	return &fakeStore{byID: map[string]*result.Result{"gameId:42": {
		SchemaVersion: result.CurrentSchemaVersion,
		Frags: &result.FragResult{
			TotalFrags: 3,
			ByPlayer:   map[string]*result.PlayerFrags{"bps": {Kills: 3}},
		},
	}}}
}

func TestArtifact_EagerFrag(t *testing.T) {
	srv := newTestServer(t, fragOnlyStore())
	defer srv.Close()

	body, resp := getWithHeaders(t, srv.URL+"/v1/demos/gameId:42/artifacts/frag")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d; want 200 (%v)", resp.StatusCode, body)
	}
	// Body is {"frags": FragResult} — the resultKey, not the node name.
	frags, ok := body["frags"].(map[string]any)
	if !ok {
		t.Fatalf("expected frags object, got %v", body)
	}
	if int(frags["totalFrags"].(float64)) != 3 {
		t.Errorf("totalFrags = %v; want 3", frags["totalFrags"])
	}
	// Per-artifact ETag "<sha>-<name>@v<n>" (finer than the global "<sha>-v<n>").
	etag := resp.Header.Get("ETag")
	if !containsAll(etag, "-frag@v", fmt.Sprintf("v%d", result.CurrentSchemaVersion)) {
		t.Errorf("ETag = %q; want the <sha>-frag@v%d form", etag, result.CurrentSchemaVersion)
	}

	// Revalidation with that ETag → 304.
	req, _ := http.NewRequest("GET", srv.URL+"/v1/demos/gameId:42/artifacts/frag", nil)
	req.Header.Set("If-None-Match", etag)
	r2, _ := http.DefaultClient.Do(req)
	r2.Body.Close()
	if r2.StatusCode != 304 {
		t.Errorf("If-None-Match status = %d; want 304", r2.StatusCode)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// An object-shaped section the demo lacks → 422 with the section's code,
// matching the curated endpoint's convention.
func TestArtifact_EagerAbsent422(t *testing.T) {
	srv := newTestServer(t, fragOnlyStore()) // no DemoInfo
	defer srv.Close()
	body, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/artifacts/demoinfo")
	if status != 422 {
		t.Fatalf("status = %d; want 422 (%s)", status, body)
	}
	var env errorEnvelope
	_ = json.Unmarshal(body, &env)
	if env.Error.Code != "demoinfo_unavailable" {
		t.Errorf("code = %q; want demoinfo_unavailable", env.Error.Code)
	}
}

// An always-computable / list-shaped section absent → 200 with a raw
// (possibly null/empty) section, never 422.
func TestArtifact_EagerAbsent200(t *testing.T) {
	srv := newTestServer(t, fragOnlyStore()) // no Match
	defer srv.Close()
	body, resp := getWithHeaders(t, srv.URL+"/v1/demos/gameId:42/artifacts/match")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	if v, present := body["match"]; !present || v != nil {
		t.Errorf("expected {\"match\": null}, got %v", body)
	}
}

func TestArtifact_UnknownName404(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	for _, name := range []string{"bogus", "clock", "roster", "region-control", "shot-streams"} {
		body, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/artifacts/"+name)
		if status != 404 {
			t.Errorf("%s: status = %d; want 404 (%s)", name, status, body)
			continue
		}
		var env errorEnvelope
		_ = json.Unmarshal(body, &env)
		if env.Error.Code != "artifact_unknown" {
			t.Errorf("%s: code = %q; want artifact_unknown", name, env.Error.Code)
		}
	}
}

func TestArtifact_ParamsRejected400(t *testing.T) {
	srv := newTestServer(t, fragOnlyStore())
	defer srv.Close()
	body, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/artifacts/frag?players=bps")
	if status != 400 {
		t.Fatalf("status = %d; want 400 (%s)", status, body)
	}
	var env errorEnvelope
	_ = json.Unmarshal(body, &env)
	if env.Error.Code != "invalid_param" {
		t.Errorf("code = %q; want invalid_param", env.Error.Code)
	}
}

func TestArtifact_LazyLOS(t *testing.T) {
	store := storeWithStub()
	srv := newTestServer(t, store)
	defer srv.Close()

	body, resp := getWithHeaders(t, srv.URL+"/v1/demos/gameId:42/artifacts/los")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d; want 200 (%v)", resp.StatusCode, body)
	}
	players, ok := body["players"].([]any)
	if !ok || len(players) == 0 {
		t.Fatalf("expected players array, got %v", body["players"])
	}
	if players[0].(map[string]any)["name"] != "bps" {
		t.Errorf("players[0].name = %v; want bps", players[0])
	}
	// Same body shape as the curated /los endpoint (reused losBody).
	if !store.byID["gameId:42"].Streams.LOSComputed {
		t.Error("LOSComputed should latch after the first los artifact request")
	}
	etag := resp.Header.Get("ETag")
	if !containsAll(etag, "-los@v", fmt.Sprintf("v%d", result.CurrentSchemaVersion)) {
		t.Errorf("ETag = %q; want the <sha>-los@v%d form", etag, result.CurrentSchemaVersion)
	}
}
