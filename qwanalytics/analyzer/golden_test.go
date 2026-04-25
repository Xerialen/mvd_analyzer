package analyzer_test

// Golden test harness for the full analyzer pipeline. Reads
// qwanalytics/testdata/corpus.json (a list of hub.quakeworld.nu game
// IDs), pulls each demo into the local cache (downloading on first run,
// reusing on subsequent runs), runs the default registry, and pins
// the JSON-serialised Result against a checked-in golden file.
//
// Usage:
//   make test                            # normal run; downloads on cache miss
//   go test ./qwanalytics/... -run TestGoldenCorpus -args -update-golden
//                                        # regenerate golden files after an
//                                        # intentional change
//
// The corpus.json manifest is committed; the cache/ directory is
// gitignored. A demo's golden file lives at testdata/golden/<label>.json.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mvd-analyzer/qwanalytics/analyzer"
	"github.com/mvd-analyzer/qwanalytics/internal/hubfetch"
)

// updateGolden regenerates every golden file from the current pipeline
// output instead of comparing. Activate with `go test ... -args -update-golden`.
var updateGolden = flag.Bool("update-golden", false, "regenerate golden files instead of comparing")

// corpusEntry mirrors one row in qwanalytics/testdata/corpus.json.
//
// gameId  → hub.quakeworld.nu game ID; resolved via hubfetch.
// label   → stable filename slug for the cache + golden file. Choose
//           something descriptive (e.g. "duel_dm6_2024-01") so a
//           regression diff makes sense without cross-referencing the ID.
// mode    → free-text human label ("1on1", "2on2", "4on4", …) — not
//           checked, just there so a reader of corpus.json can see
//           coverage at a glance.
type corpusEntry struct {
	GameID int    `json:"gameId"`
	Label  string `json:"label"`
	Mode   string `json:"mode,omitempty"`
}

func TestGoldenCorpus(t *testing.T) {
	corpus := loadCorpus(t)
	if len(corpus) == 0 {
		t.Skip("qwanalytics/testdata/corpus.json has no entries — add hub gameIds to enable golden coverage")
	}

	cacheDir := filepath.Join("..", "testdata", "cache")
	goldenDir := filepath.Join("..", "testdata", "golden")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("create cache dir: %v", err)
	}
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		t.Fatalf("create golden dir: %v", err)
	}

	for _, entry := range corpus {
		t.Run(entry.Label, func(t *testing.T) {
			mvdPath := ensureCached(t, cacheDir, entry)

			result, err := analyzer.NewDefaultRegistry().Analyze(mvdPath)
			if err != nil {
				t.Fatalf("analyze %s: %v", entry.Label, err)
			}

			actual, err := canonicalJSON(result)
			if err != nil {
				t.Fatalf("canonicalise: %v", err)
			}

			goldenPath := filepath.Join(goldenDir, entry.Label+".json")
			if *updateGolden {
				if err := os.WriteFile(goldenPath, actual, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				t.Logf("updated %s", goldenPath)
				return
			}

			expected, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v\nrun with -args -update-golden to create it", goldenPath, err)
			}
			if !bytes.Equal(expected, actual) {
				t.Errorf("%s differs from golden — run with -args -update-golden if intended.\nfirst diff line: %s",
					entry.Label, firstDiffLine(expected, actual))
			}
		})
	}
}

// loadCorpus reads testdata/corpus.json. Missing or empty file is
// treated as "skip" — see the t.Skip in the caller.
func loadCorpus(t *testing.T) []corpusEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "corpus.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read corpus.json: %v", err)
	}
	var out []corpusEntry
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse corpus.json: %v", err)
	}
	return out
}

// ensureCached returns the local path to the demo for entry.GameID,
// downloading via hubfetch on cache miss. The cache key is the gameId
// itself (label-derived would invalidate every time the user renames a
// label without changing the underlying demo).
func ensureCached(t *testing.T, cacheDir string, entry corpusEntry) string {
	t.Helper()
	cachePath := filepath.Join(cacheDir, fmt.Sprintf("%d.mvd.gz", entry.GameID))
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath
	}

	if !networkAllowed() {
		t.Fatalf("demo %d (%s) not in cache and network probe failed — populate %s manually or run online once",
			entry.GameID, entry.Label, cachePath)
	}

	t.Logf("cache miss for game %d (%s) — fetching from hub", entry.GameID, entry.Label)
	client := hubfetch.NewClient()
	info, err := client.Resolve(entry.GameID)
	if err != nil {
		t.Fatalf("resolve gameId %d: %v", entry.GameID, err)
	}
	data, err := client.Download(info)
	if err != nil {
		t.Fatalf("download gameId %d: %v", entry.GameID, err)
	}
	if err := os.WriteFile(cachePath, data, 0o644); err != nil {
		t.Fatalf("write cache %s: %v", cachePath, err)
	}
	return cachePath
}

// networkAllowed does a 2-second HEAD against the Supabase host. If
// that fails, we treat the environment as offline so the test can give
// a clean "populate the cache" message instead of a generic timeout.
func networkAllowed() bool {
	u, _ := url.Parse(hubfetch.SupabaseURL)
	if u == nil {
		return false
	}
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Head("https://" + u.Host)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// canonicalJSON marshals Result to deterministic JSON for golden
// comparison. Two transforms are applied:
//
//  1. filePath is stripped — it's a per-machine cache path that would
//     force a diff on every developer machine.
//  2. timelineAnalysis.highResBuckets is sliced down to three 15 s
//     windows (start, 1:00–1:15, end). The full 50 ms position track
//     is ~20 MB per 4on4 demo and most of it is redundant for
//     regression detection; the three windows are enough to catch
//     bucketer / position-extractor drift while keeping committed
//     goldens around 1 MB.
//
// Everything else (locGraph, schemaVersion, durations, weapon stats,
// items, frags, …) is pinned in full; changes to those should be
// deliberate, and -update-golden makes the intent explicit.
func canonicalJSON(v interface{}) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	delete(m, "filePath")
	sampleHighResBuckets(m)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// sampleHighResBuckets replaces timelineAnalysis.highResBuckets with a
// concatenation of three 15 s windows: [0, 15], [60, 75], and the
// trailing 15 s. Buckets are kept in order. Demos shorter than the
// middle window simply contribute no middle samples — the slice is
// taken with bounds checks only, no synthetic padding.
func sampleHighResBuckets(m map[string]interface{}) {
	ta, ok := m["timelineAnalysis"].(map[string]interface{})
	if !ok {
		return
	}
	raw, ok := ta["highResBuckets"].([]interface{})
	if !ok || len(raw) == 0 {
		return
	}
	lastT := 0.0
	if t, ok := raw[len(raw)-1].(map[string]interface{})["t"].(float64); ok {
		lastT = t
	}
	endStart := lastT - 15
	keep := raw[:0:0]
	for _, b := range raw {
		bm, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		t, ok := bm["t"].(float64)
		if !ok {
			continue
		}
		if t <= 15 || (t >= 60 && t <= 75) || t >= endStart {
			keep = append(keep, b)
		}
	}
	ta["highResBuckets"] = keep
}

// firstDiffLine returns a short summary of where two byte slices first
// disagree. Just enough context to point a developer at the right
// area in the golden file — the full diff is reproducible by writing
// the actual bytes and running `diff`.
func firstDiffLine(want, got []byte) string {
	line, col := 1, 1
	for i := 0; i < len(want) && i < len(got); i++ {
		if want[i] != got[i] {
			return fmt.Sprintf("line %d col %d: want %q got %q",
				line, col, snippet(want, i), snippet(got, i))
		}
		if want[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	if len(want) != len(got) {
		return fmt.Sprintf("length differs: want %d got %d (likely missing/extra trailing field)", len(want), len(got))
	}
	return "(no difference)"
}

func snippet(b []byte, i int) string {
	start := i
	end := i + 40
	if end > len(b) {
		end = len(b)
	}
	return string(b[start:end])
}
