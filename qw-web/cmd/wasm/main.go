//go:build js && wasm

package main

import (
	"bytes"
	"encoding/json"
	"syscall/js"

	"github.com/mvd-analyzer/qwanalytics/analyzer"
	"github.com/mvd-analyzer/qwanalytics/config"
	"github.com/mvd-analyzer/qwdemo/mvdfile"
)

// lastResult retains the most recently analysed demo so JS can call
// recomputeRegionControl with edited regions and get fresh stats
// without re-parsing the demo. Cleared/replaced by each analyze call.
var lastResult *analyzer.Result

func analyze(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return errorJSON("missing data argument")
	}

	filename := "demo.mvd"
	if len(args) >= 2 {
		filename = args[1].String()
	}

	// Copy Uint8Array from JS to Go
	jsData := args[0]
	length := jsData.Get("length").Int()
	data := make([]byte, length)
	js.CopyBytesToGo(data, jsData)

	// Handle gzip decompression
	reader, err := mvdfile.NewReader(bytes.NewReader(data))
	if err != nil {
		return errorJSON(err.Error())
	}
	defer reader.Close()

	// Run analysis pipeline
	registry := analyzer.NewDefaultRegistry()
	result, err := registry.AnalyzeReader(reader, filename)
	if err != nil {
		return errorJSON(err.Error())
	}

	lastResult = result

	jsonBytes, err := json.Marshal(result)
	if err != nil {
		return errorJSON(err.Error())
	}

	return string(jsonBytes)
}

// recomputeRegionControl is the JS-callable region recompute hook. It
// reads the cached lastResult (set by analyze) and re-runs
// analyzer.ComputeRegionControl with caller-supplied regions, then
// returns a fresh RegionControlResult JSON. The caller passes a JSON
// string of {"regions":[{"name":...,"locs":[...]}]} — same shape as the
// embedded per-map regions JSON the analyzer ships with.
//
// Returns an error envelope when no demo has been analysed yet, the
// JSON is malformed, or the cached match has no two-team layout (FFA
// region control is out of scope).
func recomputeRegionControl(this js.Value, args []js.Value) interface{} {
	if lastResult == nil || lastResult.TimelineAnalysis == nil {
		return errorJSON("no demo analyzed yet")
	}
	if len(args) < 1 {
		return errorJSON("missing regions argument")
	}
	var ov config.MapRegionOverrides
	if err := json.Unmarshal([]byte(args[0].String()), &ov); err != nil {
		return errorJSON("bad regions JSON: " + err.Error())
	}

	ta := lastResult.TimelineAnalysis
	if ta.RegionControl == nil || ta.RegionControl.TeamA == "" || ta.RegionControl.TeamB == "" {
		return errorJSON("region control unavailable (non-binary team layout)")
	}

	// Build name -> team from the existing buckets (the analyser already
	// populated them with the canonical demoinfo-resolved names). We
	// learn each player's team from any bucket they appear in by joining
	// the team aggregations: if TD has both teams, fall back to the
	// match.teams ordering. Simpler: read RegionControl.TeamA/B and rely
	// on per-bucket player.* (no team field per player) — so build the
	// map by walking every bucket once and recording the first team a
	// player is seen on. Player presence in TD requires team membership,
	// which the buckets export filters by.
	nameToTeam := buildNameToTeam(lastResult)

	// Reshape the override list into ControlRegion records so
	// ComputeRegionControl can use them directly. Points/Centroid are
	// optional for the classifier — leave them empty here; the frontend
	// already has the geometry from the analyse-time regions.
	regions := make([]analyzer.ControlRegion, 0, len(ov.Regions))
	for _, r := range ov.Regions {
		regions = append(regions, analyzer.ControlRegion{
			Name: r.Name,
			Locs: append([]string(nil), r.Locs...),
		})
	}
	teamOf := func(name string) string { return nameToTeam[name] }
	bucketStates, stats := analyzer.ComputeRegionControl(
		ta.HighResBuckets, ta.LocTable, regions,
		ta.RegionControl.TeamA, ta.RegionControl.TeamB, teamOf,
	)

	out := analyzer.RegionControlResult{
		Regions:      regions,
		TeamA:        ta.RegionControl.TeamA,
		TeamB:        ta.RegionControl.TeamB,
		BucketStates: bucketStates,
		Stats:        stats,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return errorJSON(err.Error())
	}
	return string(b)
}

// buildNameToTeam derives a player-name -> team mapping from the
// analysed result. Prefers Match.Players (the canonical scoreboard
// view); falls back to demoinfo if Match is absent.
func buildNameToTeam(r *analyzer.Result) map[string]string {
	m := make(map[string]string)
	if r.Match != nil {
		for _, p := range r.Match.Players {
			if p.Name != "" && p.Team != "" {
				m[p.Name] = p.Team
			}
		}
	}
	if r.DemoInfo != nil {
		for _, p := range r.DemoInfo.Players {
			if p.Name != "" && p.Team != "" {
				if _, ok := m[p.Name]; !ok {
					m[p.Name] = p.Team
				}
			}
		}
	}
	return m
}

func errorJSON(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

// Set at build time via -ldflags.
var (
	GitHash   = "dev"
	GitTag    = "dev"
	BuildDate = "unknown"
)

func main() {
	js.Global().Set("analyzeMVD", js.FuncOf(analyze))
	js.Global().Set("recomputeRegionControl", js.FuncOf(recomputeRegionControl))
	js.Global().Set("wasmVersion", map[string]interface{}{
		"hash": GitHash,
		"tag":  GitTag,
		"date": BuildDate,
	})
	// Block forever to keep WASM instance alive
	select {}
}
