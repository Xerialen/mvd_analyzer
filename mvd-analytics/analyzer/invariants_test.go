package analyzer_test

// A1 structural invariant over the committed golden corpus.
//
// The pipeline rewrites every timestamped field to the match-relative clock
// (normalizeMatchRelativeTimes) and every team-labelled field to the duel
// player name in 1v1s (normalizeDuelTeams). Both post-processors enumerate
// their target fields by hand, so a newly added TimelineAnalysis event stream
// that forgets one of them ships silently wrong — which is exactly what F1
// was: killEvents left on the demo clock (~10 s late) and carrying raw
// pre-normalize team labels ("red") instead of the player name.
//
// This test converts "remember to edit two post-processors" into a failing
// test. It reads the checked-in golden JSON directly (offline, no
// re-analysis — the goldens are the normalized output) and walks
// timelineAnalysis generically: any array field whose element objects carry a
// "time"/"endTime" is bounds-checked, and (on duel-normalized results) any
// "team"/"attackerTeam"/"victimTeam" must be a participant name. A future
// stream is covered automatically as long as it follows those field-name
// conventions (which every event type here does — see result/timeline.go).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// invariantSlackMs bounds how far a legitimately timestamped TimelineAnalysis
// event may fall outside the [demo open, match end] window. The upper edge
// tolerates an event landing a few ms past the detected match end (e.g. a frag
// at intermission) plus integer-ms rounding at the boundary; the lower edge, a
// warmup-countdown event down to demo open (match-time -demoOffset). It is
// deliberately an order of magnitude below the ~10 s demoOffset that a missed
// match-relative time shift (F1) introduces, so the invariant still fails
// loudly on a stream left on the demo clock. Measured headroom on the current
// corpus is 0 ms over matchEnd; 1 s is generous.
const invariantSlackMs = 1000

// timeKeys / teamKeys are the JSON field-name conventions every timestamped /
// team-labelled TimelineAnalysis event type follows (result/timeline.go).
var (
	timeKeys = []string{"time", "endTime"}
	teamKeys = []string{"team", "attackerTeam", "victimTeam"}
)

func TestTimelineInvariants(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "testdata", "golden", "*.json"))
	if err != nil {
		t.Fatalf("glob goldens: %v", err)
	}
	if len(files) == 0 {
		t.Skip("no golden files — run TestGoldenCorpus -args -update-golden first")
	}

	for _, path := range files {
		label := strings.TrimSuffix(filepath.Base(path), ".json")
		t.Run(label, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}

			ta, _ := doc["timelineAnalysis"].(map[string]any)
			if ta == nil {
				return // no timeline events on this demo; nothing to assert
			}

			// (a) Every timestamped event lies in the match-relative window.
			demoOffset := jsonNum(doc, "streams", "global", "demoOffset")
			matchEnd := jsonNum(doc, "streams", "global", "matchEnd")
			lo := -demoOffset - invariantSlackMs
			hi := matchEnd + invariantSlackMs
			forEachEvent(ta, func(stream string, i int, obj map[string]any) {
				for _, tk := range timeKeys {
					tv, ok := obj[tk].(float64)
					if !ok {
						continue
					}
					if tv < lo || tv > hi {
						t.Errorf("timelineAnalysis.%s[%d].%s = %.0f outside match-relative window [%.0f, %.0f] (demoOffset=%.0f matchEnd=%.0f) — did a post-processor skip this stream's time shift? (F1/A1)",
							stream, i, tk, tv, lo, hi, demoOffset, matchEnd)
					}
				}
			})

			// (b) On a duel-normalized result every team label is a player name.
			names, dueled := duelNames(doc)
			if !dueled {
				return
			}
			forEachEvent(ta, func(stream string, i int, obj map[string]any) {
				for _, teamKey := range teamKeys {
					tv, ok := obj[teamKey].(string)
					if !ok || tv == "" {
						continue
					}
					if !names[tv] {
						t.Errorf("timelineAnalysis.%s[%d].%s = %q is not a duel participant name %v — did normalizeDuelTeams skip this stream's team rewrite? (F1/A1)",
							stream, i, teamKey, tv, sortedKeys(names))
					}
				}
			})
		})
	}
}

// forEachEvent invokes fn for every object element of every array-valued field
// of the timelineAnalysis map. Non-array fields (locTable, playerUserIDs,
// regionControl) and non-object elements (locTable strings) are skipped.
func forEachEvent(ta map[string]any, fn func(stream string, i int, obj map[string]any)) {
	// Iterate in key order for deterministic failure output.
	streams := make([]string, 0, len(ta))
	for k := range ta {
		streams = append(streams, k)
	}
	sort.Strings(streams)
	for _, k := range streams {
		arr, ok := ta[k].([]any)
		if !ok {
			continue
		}
		for i, e := range arr {
			if obj, ok := e.(map[string]any); ok {
				fn(k, i, obj)
			}
		}
	}
}

// jsonNum walks a nested JSON object by keys and returns the terminal value as
// float64 (0 if any hop is absent or non-numeric — demoOffset is omitempty
// when the match starts at demo open).
func jsonNum(doc map[string]any, path ...string) float64 {
	var cur any = doc
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return 0
		}
		cur = m[k]
	}
	f, _ := cur.(float64)
	return f
}

// duelNames returns the set of match-participant names and whether the result
// was duel-normalized — match.players non-empty and every player's team equal
// to its own name, the shape normalizeDuelTeams produces. Only then does the
// "team label must be a player name" invariant apply; a team game (teams are
// clan tags, never equal to a player name) returns dueled=false.
func duelNames(doc map[string]any) (names map[string]bool, dueled bool) {
	match, _ := doc["match"].(map[string]any)
	if match == nil {
		return nil, false
	}
	players, _ := match["players"].([]any)
	if len(players) == 0 {
		return nil, false
	}
	names = make(map[string]bool, len(players))
	dueled = true
	for _, p := range players {
		pm, ok := p.(map[string]any)
		if !ok {
			return nil, false
		}
		name, _ := pm["name"].(string)
		team, _ := pm["team"].(string)
		if name == "" || team != name {
			dueled = false
		}
		names[name] = true
	}
	return names, dueled
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
