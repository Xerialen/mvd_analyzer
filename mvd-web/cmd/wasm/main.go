//go:build js && wasm

package main

import (
	"bytes"
	"encoding/json"
	"syscall/js"
	"time"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/config"
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-analytics/view"
	"github.com/mvd-analyzer/mvd-reader/mvdfile"
)

// lastResult retains the most recently analysed demo so JS can call
// recomputeRegionControl with edited regions and get fresh stats
// without re-parsing the demo. Cleared/replaced by each analyze call.
var lastResult *analyzer.Result

// lastTimingsJSON holds the per-phase pipeline timings (plus the JSON
// marshal cost) from the most recent analyze call, surfaced to the
// browser console via getAnalysisTimings. Replaced by each analyze call.
var lastTimingsJSON string

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
	res, err := registry.AnalyzeReader(reader, filename)
	if err != nil {
		return errorJSON(err.Error())
	}

	lastResult = res

	marshalStart := time.Now()
	jsonBytes, err := json.Marshal(res)
	if err != nil {
		return errorJSON(err.Error())
	}
	marshalMs := float64(time.Since(marshalStart).Microseconds()) / 1000

	if tb, err := json.Marshal(map[string]interface{}{
		"phases":    registry.PhaseTimings,
		"marshalMs": marshalMs,
	}); err == nil {
		lastTimingsJSON = string(tb)
	}

	return string(jsonBytes)
}

// getAnalysisTimings returns the per-phase pipeline timings (init, event
// pass, each analyzer Finalize, each post-processor) plus the JSON
// marshal cost from the most recent analyzeMVD call, as a JSON string.
// Kept separate from analyzeMVD's return value so the frontend's
// Result-parsing path is unaffected.
func getAnalysisTimings(this js.Value, args []js.Value) interface{} {
	if lastTimingsJSON == "" {
		return "{}"
	}
	return lastTimingsJSON
}

// getDemoInfo returns just the KTX demoinfo summary (result.DemoInfo —
// map, players, teams, scores, date) from the most recent analyzeMVD call
// as a JSON string, or "null" if unavailable. Zero extra cost: the data is
// already computed and pinned in lastResult, so a consumer that only wants
// the match summary can read it without re-marshalling the full Result.
func getDemoInfo(this js.Value, args []js.Value) interface{} {
	if lastResult == nil || lastResult.DemoInfo == nil {
		return "null"
	}
	b, err := json.Marshal(lastResult.DemoInfo)
	if err != nil {
		return errorJSON(err.Error())
	}
	return string(b)
}

// getDefaultBuckets returns the column-major ColumnarBuckets the
// frontend's Timeline/Map panels read (50 ms, all fields, legacy
// "first-sample-of-bucket" reducers, team aggregates). Columnar always
// emits the raw loc index "li". See mvd-analytics/view/columnar.go for
// the shape and the app.js accessors that consume it.
func getDefaultBuckets(this js.Value, args []js.Value) interface{} {
	if lastResult == nil {
		return errorJSON("no demo analyzed yet")
	}
	cb, err := view.BucketsColumnar(lastResult, view.BucketsOptions{
		WindowMs:    50,
		Fields:      view.AllStandardFields,
		Reducers:    view.LegacyReducerSet,
		IncludeTeam: true,
	})
	if err != nil {
		return errorJSON(err.Error())
	}
	b, err := json.Marshal(cb)
	if err != nil {
		return errorJSON(err.Error())
	}
	return string(b)
}

// getBuckets is the query API surface. Argument is a JSON string of
// view.BucketsOptions; Layout ("row" default | "column") selects the
// output shape. Returns BucketsView JSON (row) or ColumnarBuckets JSON
// (column).
func getBuckets(this js.Value, args []js.Value) interface{} {
	if lastResult == nil {
		return errorJSON("no demo analyzed yet")
	}
	if len(args) < 1 {
		return errorJSON("missing options argument")
	}
	var opts view.BucketsOptions
	if err := json.Unmarshal([]byte(args[0].String()), &opts); err != nil {
		return errorJSON("bad options JSON: " + err.Error())
	}
	if opts.Layout == "column" {
		cb, err := view.BucketsColumnar(lastResult, opts)
		if err != nil {
			return errorJSON(err.Error())
		}
		b, err := json.Marshal(cb)
		if err != nil {
			return errorJSON(err.Error())
		}
		return string(b)
	}
	bv, err := view.Buckets(lastResult, opts)
	if err != nil {
		return errorJSON(err.Error())
	}
	b, err := json.Marshal(bv)
	if err != nil {
		return errorJSON(err.Error())
	}
	return string(b)
}

// getEvents returns a tagged event list. Argument is a JSON string of
// view.EventsFilter. Returns EventsView JSON.
func getEvents(this js.Value, args []js.Value) interface{} {
	if lastResult == nil {
		return errorJSON("no demo analyzed yet")
	}
	if len(args) < 1 {
		return errorJSON("missing filter argument")
	}
	var filter view.EventsFilter
	if err := json.Unmarshal([]byte(args[0].String()), &filter); err != nil {
		return errorJSON("bad filter JSON: " + err.Error())
	}
	v, err := view.Events(lastResult, filter)
	if err != nil {
		return errorJSON(err.Error())
	}
	b, err := json.Marshal(v)
	if err != nil {
		return errorJSON(err.Error())
	}
	return string(b)
}

// getStreamSlice returns raw change entries in a window — right shape
// for AI agents inspecting a short event.
func getStreamSlice(this js.Value, args []js.Value) interface{} {
	if lastResult == nil {
		return errorJSON("no demo analyzed yet")
	}
	if len(args) < 1 {
		return errorJSON("missing options argument")
	}
	var opts view.StreamSliceOptions
	if err := json.Unmarshal([]byte(args[0].String()), &opts); err != nil {
		return errorJSON("bad options JSON: " + err.Error())
	}
	v, err := view.StreamSlice(lastResult, opts)
	if err != nil {
		return errorJSON(err.Error())
	}
	b, err := json.Marshal(v)
	if err != nil {
		return errorJSON(err.Error())
	}
	return string(b)
}

// getStateAt resolves each requested field's value at a specific time.
func getStateAt(this js.Value, args []js.Value) interface{} {
	if lastResult == nil {
		return errorJSON("no demo analyzed yet")
	}
	if len(args) < 1 {
		return errorJSON("missing options argument")
	}
	var opts view.StateAtOptions
	if err := json.Unmarshal([]byte(args[0].String()), &opts); err != nil {
		return errorJSON("bad options JSON: " + err.Error())
	}
	v, err := view.StateAt(lastResult, opts)
	if err != nil {
		return errorJSON(err.Error())
	}
	b, err := json.Marshal(v)
	if err != nil {
		return errorJSON(err.Error())
	}
	return string(b)
}

// getLocTrails returns per-player loc residences.
func getLocTrails(this js.Value, args []js.Value) interface{} {
	if lastResult == nil {
		return errorJSON("no demo analyzed yet")
	}
	var opts view.LocTrailsOptions
	if len(args) >= 1 && args[0].String() != "" {
		if err := json.Unmarshal([]byte(args[0].String()), &opts); err != nil {
			return errorJSON("bad options JSON: " + err.Error())
		}
	}
	v, err := view.LocTrails(lastResult, opts)
	if err != nil {
		return errorJSON(err.Error())
	}
	b, err := json.Marshal(v)
	if err != nil {
		return errorJSON(err.Error())
	}
	return string(b)
}

// recomputeRegionControl is the JS-callable region recompute hook.
// Walks result.Streams via view.RegionControl with caller-supplied
// region overrides (the user edits region definitions in the map
// tab UI).
//
// The caller passes a JSON string of {"regions":[{"name":...,"locs":[...]}]}.
// Returns an error envelope when no demo has been analysed yet, the
// JSON is malformed, or the cached match has no two-team layout.
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

	regions := make([]result.ControlRegion, 0, len(ov.Regions))
	for _, r := range ov.Regions {
		regions = append(regions, result.ControlRegion{
			Name: r.Name,
			Locs: append([]string(nil), r.Locs...),
		})
	}
	// view.RegionControl's default teamOf already handles the
	// disambiguation suffix via Match.Players lookup, so we don't need
	// to pass TeamOf explicitly. Regions are caller-edited and must be
	// passed via the override.
	rcv, err := view.RegionControl(lastResult, view.RegionControlOptions{
		WindowMs: 50,
		Regions:  regions,
		TeamA:    ta.RegionControl.TeamA,
		TeamB:    ta.RegionControl.TeamB,
	})
	if err != nil {
		return errorJSON(err.Error())
	}

	b, err := json.Marshal(rcv)
	if err != nil {
		return errorJSON(err.Error())
	}
	return string(b)
}

// computeLineOfSight is the JS-callable lazy line-of-sight hook. LOS is not
// computed during analyze() — it is the heaviest position-derived pass and has
// no other consumer — so the map overlay requests it on first toggle.
// analyzer.ComputeLOS is idempotent (Streams.LOSComputed), so repeat calls are
// cheap. Returns the per-player visibility tracks (name + los/pvs, each
// [{o,iv}]) aligned with streams.players: los is the clear-raycast sightline,
// pvs the potentially-visible-set superset. An empty los/pvs on every player
// means the map has no BSP.
func computeLineOfSight(this js.Value, args []js.Value) interface{} {
	if lastResult == nil || lastResult.Streams == nil {
		return errorJSON("no demo analyzed yet")
	}
	analyzer.ComputeLOS(lastResult)
	players := lastResult.Streams.Players
	out := make([]struct {
		Name string            `json:"name"`
		LOS  []result.LosTrack `json:"los,omitempty"`
		PVS  []result.LosTrack `json:"pvs,omitempty"`
	}, len(players))
	for i := range players {
		out[i].Name = players[i].Name
		out[i].LOS = players[i].LOS
		out[i].PVS = players[i].PVS
	}
	b, err := json.Marshal(out)
	if err != nil {
		return errorJSON(err.Error())
	}
	return string(b)
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
	js.Global().Set("computeLineOfSight", js.FuncOf(computeLineOfSight))
	js.Global().Set("getDefaultBuckets", js.FuncOf(getDefaultBuckets))
	js.Global().Set("getBuckets", js.FuncOf(getBuckets))
	js.Global().Set("getEvents", js.FuncOf(getEvents))
	js.Global().Set("getStreamSlice", js.FuncOf(getStreamSlice))
	js.Global().Set("getStateAt", js.FuncOf(getStateAt))
	js.Global().Set("getLocTrails", js.FuncOf(getLocTrails))
	js.Global().Set("getAnalysisTimings", js.FuncOf(getAnalysisTimings))
	js.Global().Set("getDemoInfo", js.FuncOf(getDemoInfo))
	js.Global().Set("wasmVersion", map[string]interface{}{
		"hash": GitHash,
		"tag":  GitTag,
		"date": BuildDate,
	})
	// Block forever to keep WASM instance alive
	select {}
}
