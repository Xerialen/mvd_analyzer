package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type mapEntitiesResp struct {
	Map      string `json:"map"`
	Entities []struct {
		Type string `json:"type"`
		Kind string `json:"kind"`
	} `json:"entities"`
}

func getMapEntities(t *testing.T, url string) (mapEntitiesResp, int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out mapEntitiesResp
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out, resp.StatusCode
}

// dm2 is in the embedded mapents corpus, so the by-map endpoint resolves
// it without any demo or maps-dir.
func TestMapEntitiesByMap(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()

	got, status := getMapEntities(t, srv.URL+"/v1/maps/dm2/entities")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if got.Map != "dm2" {
		t.Errorf("map = %q, want dm2", got.Map)
	}
	if len(got.Entities) == 0 {
		t.Fatal("expected entities, got none")
	}

	// types= filter narrows to one type.
	spawns, status := getMapEntities(t, srv.URL+"/v1/maps/dm2/entities?types=spawn")
	if status != 200 {
		t.Fatalf("filtered status = %d, want 200", status)
	}
	if len(spawns.Entities) == 0 || len(spawns.Entities) >= len(got.Entities) {
		t.Errorf("spawn filter = %d entities, want >0 and < %d", len(spawns.Entities), len(got.Entities))
	}
	for _, e := range spawns.Entities {
		if e.Type != "spawn" {
			t.Errorf("types=spawn returned a %q entity", e.Type)
		}
	}

	// Param names are case-insensitive (nit): ?Types= must behave like
	// ?types=, per the API.md promise.
	ciSpawns, status := getMapEntities(t, srv.URL+"/v1/maps/dm2/entities?Types=spawn")
	if status != 200 {
		t.Fatalf("?Types= status = %d, want 200", status)
	}
	if len(ciSpawns.Entities) != len(spawns.Entities) {
		t.Errorf("?Types=spawn = %d entities; want same as ?types=spawn (%d)",
			len(ciSpawns.Entities), len(spawns.Entities))
	}
}

func TestMapEntitiesByMapUnknown(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()
	_, status := getMapEntities(t, srv.URL+"/v1/maps/nosuchmapxyz/entities")
	if status != http.StatusNotFound {
		t.Errorf("unknown map status = %d, want 404", status)
	}
}

func TestMapGeometry(t *testing.T) {
	dir := t.TempDir()
	body := `{"map":"dm2","version":1,"bounds":{},"locs":[]}`
	if err := os.WriteFile(filepath.Join(dir, "dm2.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newTestServerMaps(t, &fakeStore{}, dir)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/maps/dm2/geometry")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Error("missing ETag")
	}

	// If-None-Match → 304.
	req, _ := http.NewRequest("GET", srv.URL+"/v1/maps/dm2/geometry", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match status = %d, want 304", resp2.StatusCode)
	}

	// Missing map → 404.
	miss, _ := http.Get(srv.URL + "/v1/maps/nope/geometry")
	if miss.StatusCode != http.StatusNotFound {
		t.Errorf("missing geometry status = %d, want 404", miss.StatusCode)
	}
	miss.Body.Close()
}

// Without -maps-dir the geometry endpoint is disabled.
func TestMapGeometryDisabled(t *testing.T) {
	srv := newTestServer(t, &fakeStore{}) // mapsDir ""
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/maps/dm2/geometry")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("disabled geometry status = %d, want 404", resp.StatusCode)
	}
}
