package diagnostic

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-reader/mvd"
	"github.com/mvd-analyzer/mvd-reader/parser"
	"github.com/mvd-analyzer/mvd-reader/mvdfile"
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
		streamNames := map[string]bool{}
		if r.Streams != nil {
			for _, p := range r.Streams.Players {
				streamNames[p.Name] = true
			}
		}
		for name := range streamNames {
			if !demoNames[name] {
				warn("stream player %q not in demoInfo", name)
			}
		}
	}

	// Impossible stat values in per-player streams.
	if r.Streams != nil {
		for _, p := range r.Streams.Players {
			for _, c := range p.Health {
				if c.V > 250 {
					warn("player %q has health=%d at t=%.1fs (max 250)", p.Name, c.V, float64(c.T)*0.001)
				}
			}
			for _, c := range p.Armor {
				if c.V > 200 {
					warn("player %q has armor=%d at t=%.1fs (max 200)", p.Name, c.V, float64(c.T)*0.001)
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

	// Item pickup attribution coverage. Every phase that closed (TakenAt
	// > 0) should name a picker; an empty TakenBy is the layered-signal
	// pipeline's "no candidate" outcome (source="none") and indicates
	// either a degenerate scenario or a gap in our signal coverage.
	if r.Items != nil {
		var taken, unattributed int
		for _, it := range r.Items.Items {
			for _, ph := range it.Phases {
				if ph.TakenAt > 0 {
					taken++
					if ph.TakenBy == "" {
						unattributed++
					}
				}
			}
		}
		if taken > 0 {
			warn("items: %d closed phases, %d unattributed (TakenBy=\"\")", taken, unattributed)
		}
	}

	// Death reconciliation: every authoritative DeathEvent must be
	// explained by an obituary. Named kills, suicides, and recovered
	// killer-named teamkills land in Frags.Frags (victim set); victim-named
	// teamkills ("X was telefragged by his teammate") name only the victim
	// and so appear only as frag messages. A death with neither is a
	// genuine parse gap — an obituary pattern we don't recognize at all.
	if r.TimelineAnalysis != nil && r.Frags != nil {
		deathsByPlayer := map[string]int{}
		for _, de := range r.TimelineAnalysis.DeathEvents {
			deathsByPlayer[de.Player]++
		}
		fragVictims := map[string]int{}
		for _, f := range r.Frags.Frags {
			fragVictims[f.Victim]++
		}
		victimNamedTK := map[string]int{}
		if r.Messages != nil {
			for _, ev := range r.Messages.Events {
				if ev.Type != "frag" {
					continue
				}
				text := ev.MessageClean
				if text == "" {
					text = ev.Message
				}
				if v := victimNamedTeamkillVictim(text); v != "" {
					victimNamedTK[v]++
				}
			}
		}
		for player, deaths := range deathsByPlayer {
			orphans := deaths - fragVictims[player] - victimNamedTK[player]
			if orphans > 1 {
				warn("unexplained deaths for %q: %d (deaths=%d, frag-victim=%d, victim-named-tk=%d) — likely unparsed obituaries",
					player, orphans, deaths, fragVictims[player], victimNamedTK[player])
			}
		}
	}

	return warnings
}

// victimNamedTeamkillVictim returns the victim name when msg is a
// victim-named teamkill obituary (killer is the generic "teammate", so the
// frag never enters Frags.Frags), else "". Mirrors the tkVictimPatterns in
// the frag analyzer; kept local so the diagnostic harness has no analyzer
// internals dependency.
func victimNamedTeamkillVictim(msg string) string {
	patterns := []string{
		" was telefragged by his teammate",
		" was telefragged by her teammate",
		" was crushed by his teammate",
		" was crushed by her teammate",
		" was jumped by his teammate",
		" was jumped by her teammate",
	}
	for _, p := range patterns {
		if i := strings.Index(msg, p); i > 0 {
			return strings.TrimSpace(msg[:i])
		}
	}
	return ""
}
