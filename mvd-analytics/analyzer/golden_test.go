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

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/hubfetch"
	"github.com/mvd-analyzer/mvd-analytics/mapbsp"
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

			// The golden includes BSP-derived columns (pos.h floor height,
			// pos.lq liquid) and floor-polygon loc resolution. When the
			// map's BSP can't be resolved (no MVDA_BSP_DIR, no ./bsps) those
			// degrade or vanish, so a comparison fails for the wrong reason
			// and an -update-golden would overwrite a good golden with
			// gutted data. Skip the comparison and refuse to regenerate.
			if result.DemoInfo != nil && result.DemoInfo.Map != "" &&
				mapbsp.LoadBytes(result.DemoInfo.Map) == nil {
				msg := fmt.Sprintf("%s: BSP for map %q not resolvable — set MVDA_BSP_DIR to the full map set (lookup order: MVDA_BSP_DIR, ./bsps)",
					entry.Label, result.DemoInfo.Map)
				if *updateGolden {
					t.Fatalf("refusing to regenerate %s — %s; would write a golden missing height/liquid/loc", entry.Label, msg)
				}
				t.Skip(msg)
			}

			actual, err := canonicalJSON(result, densePosDemos[entry.Label])
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
// comparison. Three transforms are applied:
//
//  1. filePath is stripped — it's a per-machine cache path that would
//     force a diff on every developer machine.
//  2. streams.players[].* time series are sliced down to three 15 s
//     windows (start, 1:00–1:15, last 15 s). The full native-rate
//     position track is ~18 MB per 4on4 demo; sparse change streams
//     and intervals are smaller but together still bloat the corpus.
//     The three windows are enough sampling to catch bucketer /
//     stream-emitter drift while keeping committed goldens around 1 MB.
//  3. unless keepPos, the dense per-sample position/view track
//     (streams.players[].pos) is dropped entirely — see
//     dropPositionTracks. It is ~60 % of every file but exercises the
//     same emitter / BSP-trace code on every demo, so it is pinned on
//     just densePosDemos; the rest still verify the light change
//     streams, intervals, locGraph, region-control and aggregates.
//
// Everything else (locGraph, schemaVersion, durations, weapon stats,
// items, frags, …) is pinned in full; changes to those should be
// deliberate, and -update-golden makes the intent explicit.
func canonicalJSON(v interface{}, keepPos bool) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	delete(m, "filePath")
	sampleStreams(m)
	if !keepPos {
		dropPositionTracks(m)
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// densePosDemos are the labels whose golden keeps the full per-sample
// position/view track (streams.players[].pos: x/y/z, vp/vya, h/lq/li,
// velocity). One full 4on4 (obsidian — eight players, exercises the
// height + liquid BSP traces under load) and one duel (dm2 — the
// two-player case) are enough to catch drift in the position / view /
// height / liquid pipeline, which runs identically across demos.
// Pinning it on all ten would roughly double the committed corpus for
// no extra coverage.
var densePosDemos = map[string]bool{
	"4on4_ahoy_bhb_240426_obsidian":      true,
	"1on1_bananfalco_betowen_240426_dm2": true,
}

// dropPositionTracks removes streams.players[].pos — the dense
// per-sample position/view/height/liquid track that dominates the
// corpus size — from every player. Called for demos outside
// densePosDemos; the rest of streams.players (light change streams,
// intervals, spawn/death timestamps) and all aggregate sections are
// kept, so each demo still verifies loc graph, region control, items,
// frags, damage and the sparse timelines.
func dropPositionTracks(m map[string]interface{}) {
	streams, ok := m["streams"].(map[string]interface{})
	if !ok {
		return
	}
	players, ok := streams["players"].([]interface{})
	if !ok {
		return
	}
	for _, pi := range players {
		if p, ok := pi.(map[string]interface{}); ok {
			delete(p, "pos")
		}
	}
}

// goldenWindows is the three 15-second windows used to slice every
// per-player time series in the golden corpus. Schema v8: all stream
// timestamps are int32 ms in the JSON, so the window bounds are
// expressed in ms (0..15000, 60000..75000). Match ends at
// global.matchEnd; the trailing window starts at matchEnd - 15000.
var goldenWindows = []struct{ start, end float64 }{
	{0, 15000},
	{60000, 75000},
	// (end - 15000, end) appended dynamically per demo.
}

// sampleStreams replaces every per-player time series in
// streams.players[] with the concatenation of three 15-second windows.
// The global matchEnd is read from streams.global.matchEnd (ms).
func sampleStreams(m map[string]interface{}) {
	streams, ok := m["streams"].(map[string]interface{})
	if !ok {
		return
	}
	matchEnd := 0.0
	if g, ok := streams["global"].(map[string]interface{}); ok {
		if t, ok := g["matchEnd"].(float64); ok {
			matchEnd = t
		}
	}
	windows := append([]struct{ start, end float64 }(nil), goldenWindows...)
	if matchEnd > 15000 {
		windows = append(windows, struct{ start, end float64 }{matchEnd - 15000, matchEnd})
	}

	players, ok := streams["players"].([]interface{})
	if !ok {
		return
	}
	for _, pi := range players {
		p, ok := pi.(map[string]interface{})
		if !ok {
			continue
		}
		// Change streams: []{t, v}.
		for _, key := range []string{"h", "a", "at", "li", "sh", "nl", "rk", "cl"} {
			if arr, ok := p[key].([]interface{}); ok {
				p[key] = filterChangeStream(arr, windows)
			}
		}
		// Intervals: []{s, e}. Clamp to overlapping window slice.
		for _, key := range []string{"rl", "lg", "gl", "ssg", "sng", "q", "pe", "r"} {
			if arr, ok := p[key].([]interface{}); ok {
				p[key] = filterIntervalStream(arr, windows)
			}
		}
		// Discrete event timestamps.
		for _, key := range []string{"sp", "d"} {
			if arr, ok := p[key].([]interface{}); ok {
				p[key] = filterTimestamps(arr, windows)
			}
		}
		// Position track (columnar) — slice every sample-aligned column
		// by the same kept indices. A column left at full length would
		// ship misaligned against the sliced t (and bloat the golden).
		if pos, ok := p["pos"].(map[string]interface{}); ok {
			ts, _ := pos["t"].([]interface{})
			keepIdx := indicesInWindows(ts, windows)
			for _, key := range []string{"t", "x", "y", "z", "li", "h", "lq", "vp", "vya", "vx", "vy", "vz"} {
				if arr, ok := pos[key].([]interface{}); ok {
					pos[key] = pickByIndex(arr, keepIdx)
				}
			}
		}
	}
}

func filterChangeStream(arr []interface{}, windows []struct{ start, end float64 }) []interface{} {
	out := arr[:0:0]
	for _, e := range arr {
		em, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		t, ok := em["t"].(float64)
		if !ok {
			continue
		}
		if inGoldenWindows(t, windows) {
			out = append(out, e)
		}
	}
	return out
}

func filterIntervalStream(arr []interface{}, windows []struct{ start, end float64 }) []interface{} {
	out := arr[:0:0]
	for _, e := range arr {
		em, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		s, _ := em["s"].(float64)
		ee, _ := em["e"].(float64)
		// Keep intervals overlapping any window.
		for _, w := range windows {
			if ee > w.start && s < w.end {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

func filterTimestamps(arr []interface{}, windows []struct{ start, end float64 }) []interface{} {
	out := arr[:0:0]
	for _, e := range arr {
		t, ok := e.(float64)
		if !ok {
			continue
		}
		if inGoldenWindows(t, windows) {
			out = append(out, e)
		}
	}
	return out
}

func indicesInWindows(ts []interface{}, windows []struct{ start, end float64 }) []int {
	out := make([]int, 0, len(ts))
	for i, e := range ts {
		t, ok := e.(float64)
		if !ok {
			continue
		}
		if inGoldenWindows(t, windows) {
			out = append(out, i)
		}
	}
	return out
}

func pickByIndex(arr []interface{}, idx []int) []interface{} {
	out := make([]interface{}, 0, len(idx))
	for _, i := range idx {
		if i < len(arr) {
			out = append(out, arr[i])
		}
	}
	return out
}

func inGoldenWindows(t float64, windows []struct{ start, end float64 }) bool {
	for _, w := range windows {
		if t >= w.start && t <= w.end {
			return true
		}
	}
	return false
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
