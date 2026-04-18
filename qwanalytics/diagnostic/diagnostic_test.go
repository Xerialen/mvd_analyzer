package diagnostic

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"testing"

	"github.com/mvd-analyzer/qwanalytics/analyzer"
	"github.com/mvd-analyzer/qwdemo/mvd"
	"github.com/mvd-analyzer/qwdemo/parser"
	"github.com/mvd-analyzer/qwdemo/mvdfile"
)

// TestDiagnosticParseDemos runs every demo in testdata/ through the parser in
// diagnostic mode (warnings collected instead of silently dropped) and then
// checks data quality on the analysis result.
//
// Usage:
//
//	go test -v -run TestDiagnosticParseDemos ./internal/diagnostic/
//
// To test a larger collection, drop .mvd / .mvd.gz files into
// internal/diagnostic/testdata/.
func TestDiagnosticParseDemos(t *testing.T) {
	demos, _ := filepath.Glob("testdata/*.mvd*")
	if len(demos) == 0 {
		t.Skip("no demos in testdata/")
	}

	for _, demo := range demos {
		t.Run(filepath.Base(demo), func(t *testing.T) {
			// --- Pass 1: diagnostic parse (collect warnings) ---
			parseWarnings := diagnosticParse(t, demo)

			// Summarize parse warnings by type+message (deduplicated)
			type warnKey struct{ typ, msg string }
			parseCounts := map[warnKey]int{}
			parseFirst := map[warnKey]float64{}
			for _, w := range parseWarnings {
				k := warnKey{w.Type, w.Message}
				parseCounts[k]++
				if _, seen := parseFirst[k]; !seen {
					parseFirst[k] = w.Time
				}
			}
			for k, count := range parseCounts {
				t.Logf("PARSE  [first@%.1fs] %s: %s (x%d)", parseFirst[k], k.typ, k.msg, count)
			}

			// --- Pass 2: full analysis + data quality checks ---
			reg := analyzer.NewDefaultRegistry()
			result, err := reg.Analyze(demo)
			if err != nil {
				t.Fatalf("analysis failed: %v", err)
			}

			qualityWarnings := checkDataQuality(result)
			for _, w := range qualityWarnings {
				t.Logf("QUALITY  %s", w)
			}

			t.Logf("--- summary: %d parse warnings (%d unique), %d quality warnings ---",
				len(parseWarnings), len(parseCounts), len(qualityWarnings))
		})
	}
}

// diagnosticParse runs the parser in diagnostic mode and returns collected warnings.
func diagnosticParse(t *testing.T, path string) []parser.Warning {
	t.Helper()

	f, err := mvdfile.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	decoder := mvd.NewDecoder(f)
	p := parser.NewParser(decoder)
	p.SetDiagnosticMode(true)

	// Register a no-op handler so the parser fully processes events
	p.OnEvent(func(event parser.Event) error { return nil })

	if err := p.Parse(); err != nil && err != io.EOF {
		t.Logf("parse ended with error: %v", err)
	}

	return p.DiagnosticWarnings()
}

// checkDataQuality runs coherence checks on the analysis result.
func checkDataQuality(r *analyzer.Result) []string {
	var warnings []string
	warn := func(format string, args ...interface{}) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}

	if r.DemoInfo == nil {
		warn("no demoInfo (no embedded KTX stats)")
		return warnings
	}

	// demoInfo vs match player coverage
	if r.Match != nil {
		demoNames := map[string]bool{}
		for _, p := range r.DemoInfo.Players {
			demoNames[p.Name] = true
		}
		matchNames := map[string]bool{}
		for _, p := range r.Match.Players {
			matchNames[p.Name] = true
		}
		for name := range matchNames {
			if !demoNames[name] {
				warn("match player %q not found in demoInfo", name)
			}
		}
		for name := range demoNames {
			if !matchNames[name] {
				warn("demoInfo player %q not found in match result", name)
			}
		}
	}

	// Frag totals: timeline event sum vs demoInfo
	if r.TimelineAnalysis != nil {
		fragEventTotals := map[string]int{}
		for _, fe := range r.TimelineAnalysis.FragEvents {
			fragEventTotals[fe.Player] += fe.Delta
		}
		for _, dp := range r.DemoInfo.Players {
			if dp.Stats == nil {
				continue
			}
			eventFrags := fragEventTotals[dp.Name]
			demoFrags := dp.Stats.Frags
			diff := int(math.Abs(float64(eventFrags - demoFrags)))
			if diff > 2 {
				warn("frag mismatch for %q: timeline events sum=%d, demoInfo=%d (diff=%d)",
					dp.Name, eventFrags, demoFrags, diff)
			}
		}
	}

	// Players with frags but no team
	if r.Match != nil {
		for _, p := range r.Match.Players {
			if p.Frags > 0 && p.Team == "" {
				warn("player %q has %d frags but no team", p.Name, p.Frags)
			}
		}
	}

	// Timeline player names ⊆ demoInfo player names
	if r.TimelineAnalysis != nil {
		demoNames := map[string]bool{}
		for _, p := range r.DemoInfo.Players {
			demoNames[p.Name] = true
		}
		seen := map[string]bool{}
		for _, fe := range r.TimelineAnalysis.FragEvents {
			if !seen[fe.Player] && !demoNames[fe.Player] {
				warn("timeline frag event player %q not in demoInfo", fe.Player)
				seen[fe.Player] = true
			}
		}
		bucketNames := map[string]bool{}
		for _, b := range r.TimelineAnalysis.HighResBuckets {
			for name := range b.P {
				bucketNames[name] = true
			}
		}
		for name := range bucketNames {
			if !demoNames[name] {
				warn("timeline bucket player %q not in demoInfo", name)
			}
		}
	}

	// Impossible stat values in high-res timeline buckets
	if r.TimelineAnalysis != nil {
		for _, b := range r.TimelineAnalysis.HighResBuckets {
			for name, pd := range b.P {
				if pd.H > 250 {
					warn("player %q has health=%d at t=%.1f (max 250)", name, pd.H, b.T)
				}
				if pd.A > 200 {
					warn("player %q has armor=%d at t=%.1f (max 200)", name, pd.A, b.T)
				}
			}
		}
	}

	// Negative frags
	for _, p := range r.DemoInfo.Players {
		if p.Stats != nil && p.Stats.Frags < -5 {
			warn("player %q has suspicious negative frags: %d", p.Name, p.Stats.Frags)
		}
	}

	// Duplicate player names in demoInfo
	nameCount := map[string]int{}
	for _, p := range r.DemoInfo.Players {
		nameCount[p.Name]++
	}
	for name, count := range nameCount {
		if count > 1 {
			warn("duplicate player name in demoInfo: %q appears %d times", name, count)
		}
	}

	return warnings
}
