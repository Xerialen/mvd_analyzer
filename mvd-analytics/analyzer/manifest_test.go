package analyzer

import (
	"os"
	"testing"
)

// TestArtifactManifestMirrorsGraph: the manifest carries exactly the graph's
// node set (eager registration nodes + the two lazy artifacts), so the two
// encodings can never drift.
func TestArtifactManifestMirrorsGraph(t *testing.T) {
	m := ArtifactManifest()
	if len(m) != len(registrationOrder)+2 { // +los +shot-streams
		t.Fatalf("manifest has %d nodes, want %d", len(m), len(registrationOrder)+2)
	}
	for i, name := range registrationOrder {
		if m[i].Name != name {
			t.Errorf("manifest[%d] = %q, want %q (registration order)", i, m[i].Name, name)
		}
	}
	// The lazy artifacts trail the eager set, by name.
	if m[len(registrationOrder)].Name != "los" || m[len(registrationOrder)+1].Name != "shot-streams" {
		t.Errorf("lazy tail = %q, %q; want los, shot-streams",
			m[len(registrationOrder)].Name, m[len(registrationOrder)+1].Name)
	}
}

// TestArtifactManifestServability pins the servable/non-servable split and
// the resultKey mapping the generic endpoint depends on.
func TestArtifactManifestServability(t *testing.T) {
	byName := map[string]ArtifactMeta{}
	for _, m := range ArtifactManifest() {
		byName[m.Name] = m
	}

	// Servable eager artifacts and the Result key they land in.
	wantKey := map[string]string{
		"demoinfo": "demoInfo", "frag": "frags", "metadata": "metadata",
		"match": "match", "messages": "messages", "timeline": "timelineAnalysis",
		"items": "items", "damage": "damage", "shots": "shots",
		"map-entities": "mapEntities", "backpacks": "backpacks",
		"weapon-pickups": "weaponPickups", "aim": "aim", "loc-graph": "locGraph",
	}
	for name, key := range wantKey {
		m := byName[name]
		if m.ResultKey != key {
			t.Errorf("%s resultKey = %q, want %q", name, m.ResultKey, key)
		}
		if !m.Servable {
			t.Errorf("%s should be servable", name)
		}
		if m.Cost != costLight {
			t.Errorf("%s cost = %q, want light", name, m.Cost)
		}
	}

	// Lazy artifacts are servable and heavy, with no resultKey.
	for _, name := range []string{"los", "shot-streams"} {
		m := byName[name]
		if !m.Servable || !m.Lazy || m.Cost != costHeavy || m.ResultKey != "" {
			t.Errorf("%s = %+v; want servable lazy heavy no-resultKey", name, m)
		}
	}

	// Pseudo/internal nodes are never servable.
	for _, name := range []string{
		"clock", "roster", "identity",
		"recover-telefrag-teamkills", "airgibs", "scoreboard-stats", "region-control",
	} {
		m := byName[name]
		if m.Servable || m.ResultKey != "" {
			t.Errorf("%s should be non-servable with no resultKey, got %+v", name, m)
		}
	}
}

// TestServableArtifactLookup is the closed-registry gate mvd-api uses: a
// servable name resolves, an internal one and an unknown one do not.
func TestServableArtifactLookup(t *testing.T) {
	// The URL segment is the DAG node name ("frag"), which lands in Result
	// under its resultKey ("frags").
	if _, ok := ServableArtifact("frag"); !ok {
		t.Error("frag should resolve as servable")
	}
	if _, ok := ServableArtifact("shot-streams"); !ok {
		t.Error("shot-streams should resolve as servable")
	}
	if _, ok := ServableArtifact("clock"); ok {
		t.Error("clock is internal; must not resolve as servable")
	}
	if _, ok := ServableArtifact("nope"); ok {
		t.Error("unknown name must not resolve")
	}
}

// TestArtifactsMarkdownDeterministic: two generations are byte-identical, so
// the catalog can be committed and drift-checked.
func TestArtifactsMarkdownDeterministic(t *testing.T) {
	if ArtifactsMarkdown() != ArtifactsMarkdown() {
		t.Fatal("ArtifactsMarkdown is not deterministic")
	}
}

// TestArtifactsMarkdownCommittedIsCurrent fails if mvd-analytics/ARTIFACTS.md
// is stale — regenerate with `make artifacts-md` (like a golden test). This
// keeps the contributor-facing catalog honest against the DAG metadata.
func TestArtifactsMarkdownCommittedIsCurrent(t *testing.T) {
	const path = "../ARTIFACTS.md"
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(committed) != ArtifactsMarkdown() {
		t.Fatalf("%s is stale — run `make artifacts-md` and commit the result", path)
	}
}
