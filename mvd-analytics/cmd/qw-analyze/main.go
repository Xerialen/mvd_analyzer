// qw-analyze is a command-line consumer of the qwanalytics pipeline.
// It reads an MVD demo file and writes the analysis result in one of
// several formats — full JSON (the stable result-schema contract),
// markdown (a human-readable summary suitable as a seed for an AI
// review agent), line-delimited event JSON (the raw qwdemo event
// stream, useful for debugging and for driving alternative
// analytics), or one of the query-API views.
//
// At schema v7 the parse-time HighResBuckets field is gone; bucketed
// data is produced on demand by view.Buckets, accessible via the
// -view buckets flag below. Other views (events, stream-slice,
// state-at, trails, region-control) are also available.
//
// Example invocations:
//
//	qw-analyze demo.mvd.gz                              # full JSON to stdout
//	qw-analyze -include positions demo.mvd.gz           # full JSON with native x/y/z track
//	qw-analyze -include positions,view,height demo.mvd.gz # also view angles + floor height
//	qw-analyze -format md demo.mvd.gz > report.md       # markdown summary
//	qw-analyze -format events demo.mvd.gz | jq .        # event stream
//	qw-analyze -view buckets -bucket 1s demo.mvd.gz     # 1s buckets
//	qw-analyze -view events -event-types frag demo.mvd.gz
//	qw-analyze -view stream-slice -fields h,a -from 432.0 -to 442.0 demo.mvd.gz
//	qw-analyze -view state-at -time 432.5 demo.mvd.gz
//	qw-analyze -view trails -min-dwell 500ms demo.mvd.gz
//	qw-analyze -view region-control -bucket 1s demo.mvd.gz
//	qw-analyze -bulk demos/ -out-dir analyses/          # batch mode
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/config"
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-analytics/view"
	mvdsource "github.com/mvd-analyzer/mvd-reader/source/mvd"
)

// viewOptions bundles every flag that's meaningful only for the
// non-full views. Parsed once in main().
type viewOptions struct {
	view       string
	bucketDur  time.Duration
	fields     []string
	reducers   map[string]string
	from, to   time.Duration
	players    []string
	eventTypes []string
	minDwell   time.Duration
	timeAt     time.Duration
	includeTeam bool
	include    map[string]bool // -include positions etc. for -view full
}

func main() {
	format := flag.String("format", "json", "output format: json | md | events")
	outDir := flag.String("out-dir", "", "bulk mode: write <demo>.<ext> into this directory")
	bulk := flag.Bool("bulk", false, "treat the input path as a directory and analyze every demo in it")
	indent := flag.Bool("pretty", false, "pretty-print JSON output (single-demo mode only); pipe to `jq .` for human reading")
	regionsPath := flag.String("regions", "", "path to a regions JSON ({\"regions\":[{\"name\":...,\"locs\":[...]}]}) to override the embedded per-map regions for the analyzed demo")

	viewName := flag.String("view", "full", "view: full | buckets | events | trails | stream-slice | state-at | region-control")
	bucketStr := flag.String("bucket", "50ms", "bucket duration for -view buckets / region-control (e.g. 50ms, 1s, 10s)")
	fieldsStr := flag.String("fields", "", "comma-separated field codes (see qwanalytics/view docs)")
	reducerArgs := stringListFlag("reducer", "field=name reducer override; repeatable (e.g. -reducer h=min)")
	fromStr := flag.String("from", "", "start time (match-relative; e.g. 30s, 1m30s)")
	toStr := flag.String("to", "", "end time")
	playersStr := flag.String("players", "", "comma-separated player names")
	eventTypesStr := flag.String("event-types", "", "comma-separated event types (frag, powerup, streak, spawn, death, weapon, item, chat, loc, health, armor); empty = default discrete set")
	minDwellStr := flag.String("min-dwell", "0", "drop transitions shorter than this for -view trails")
	timeStr := flag.String("time", "", "time for -view state-at (required)")
	includeTeam := flag.Bool("include-team", false, "emit per-team aggregates on -view buckets")
	includeStr := flag.String("include", "", "comma-separated position-track columns for -view full: positions (x/y/z+loc), view (pitch/yaw), height, liquid, velocity")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: qw-analyze [options] <demo.mvd | demo.mvd.gz | directory>\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	input := flag.Arg(0)

	var regionsOverride []config.MapRegionOverride
	if *regionsPath != "" {
		loaded, err := loadRegionsOverride(*regionsPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "qw-analyze:", err)
			os.Exit(1)
		}
		regionsOverride = loaded
	}

	vopts, err := parseViewOptions(*viewName, *bucketStr, *fieldsStr, *reducerArgs, *fromStr, *toStr, *playersStr, *eventTypesStr, *minDwellStr, *timeStr, *includeTeam, *includeStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qw-analyze:", err)
		os.Exit(2)
	}

	if *bulk || *outDir != "" {
		if *outDir == "" {
			fmt.Fprintln(os.Stderr, "qw-analyze: -bulk requires -out-dir")
			os.Exit(2)
		}
		if err := runBulk(input, *outDir, *format, regionsOverride, vopts); err != nil {
			fmt.Fprintln(os.Stderr, "qw-analyze:", err)
			os.Exit(1)
		}
		return
	}

	if err := runOne(input, os.Stdout, *format, *indent, regionsOverride, vopts); err != nil {
		fmt.Fprintln(os.Stderr, "qw-analyze:", err)
		os.Exit(1)
	}
}

func parseViewOptions(viewName, bucketStr, fieldsStr string, reducerArgs []string, fromStr, toStr, playersStr, eventTypesStr, minDwellStr, timeStr string, includeTeam bool, includeStr string) (*viewOptions, error) {
	v := &viewOptions{view: viewName, includeTeam: includeTeam, include: map[string]bool{}}

	if bucketStr != "" {
		d, err := time.ParseDuration(bucketStr)
		if err != nil {
			return nil, fmt.Errorf("bad -bucket: %w", err)
		}
		v.bucketDur = d
	}
	if fieldsStr != "" {
		v.fields = splitCSV(fieldsStr)
	}
	if len(reducerArgs) > 0 {
		v.reducers = map[string]string{}
		for _, kv := range reducerArgs {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("bad -reducer %q (want field=name)", kv)
			}
			v.reducers[parts[0]] = parts[1]
		}
	}
	if fromStr != "" {
		d, err := time.ParseDuration(fromStr)
		if err != nil {
			return nil, fmt.Errorf("bad -from: %w", err)
		}
		v.from = d
	}
	if toStr != "" {
		d, err := time.ParseDuration(toStr)
		if err != nil {
			return nil, fmt.Errorf("bad -to: %w", err)
		}
		v.to = d
	}
	if playersStr != "" {
		v.players = splitCSV(playersStr)
	}
	if eventTypesStr != "" {
		v.eventTypes = splitCSV(eventTypesStr)
	}
	if minDwellStr != "" {
		d, err := time.ParseDuration(minDwellStr)
		if err != nil {
			return nil, fmt.Errorf("bad -min-dwell: %w", err)
		}
		v.minDwell = d
	}
	if timeStr != "" {
		d, err := time.ParseDuration(timeStr)
		if err != nil {
			return nil, fmt.Errorf("bad -time: %w", err)
		}
		v.timeAt = d
	}
	for _, opt := range splitCSV(includeStr) {
		v.include[opt] = true
	}

	switch v.view {
	case "full", "buckets", "events", "trails", "stream-slice", "state-at", "region-control":
	default:
		return nil, fmt.Errorf("unknown -view %q", v.view)
	}
	return v, nil
}

func loadRegionsOverride(path string) ([]config.MapRegionOverride, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read regions %s: %w", path, err)
	}
	var ov config.MapRegionOverrides
	if err := json.Unmarshal(data, &ov); err != nil {
		return nil, fmt.Errorf("parse regions %s: %w", path, err)
	}
	return ov.Regions, nil
}

func runOne(path string, w io.Writer, format string, pretty bool, regionsOverride []config.MapRegionOverride, vopts *viewOptions) error {
	switch format {
	case "events":
		return dumpEvents(path, w)
	case "json":
		if vopts != nil && vopts.view != "full" {
			return dumpView(path, w, regionsOverride, vopts, pretty)
		}
		return dumpJSON(path, w, pretty, regionsOverride, vopts)
	case "md":
		return dumpMarkdown(path, w, regionsOverride)
	default:
		return fmt.Errorf("unknown format %q (want json | md | events)", format)
	}
}

func dumpJSON(path string, w io.Writer, pretty bool, regionsOverride []config.MapRegionOverride, vopts *viewOptions) error {
	src, err := mvdsource.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer src.Close()

	reg := analyzer.NewDefaultRegistry()
	if regionsOverride != nil {
		reg.SetRegionsOverride(regionsOverride)
	}
	res, err := reg.AnalyzeSource(src, filepath.Base(path))
	if err != nil {
		return err
	}

	// Position-track columns are opt-in: by default strip the whole
	// native-rate track from JSON to keep the file small (~12 MB per 4on4
	// match). -include positions/view/height/liquid keeps each column set.
	sel := streamColumnSelection{}
	if vopts != nil {
		sel.positions = vopts.include["positions"]
		sel.view = vopts.include["view"]
		sel.height = vopts.include["height"]
		sel.liquid = vopts.include["liquid"]
		sel.velocity = vopts.include["velocity"]
	}
	stripStreamColumns(res, sel)

	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(res)
}

// dumpView analyses the demo, runs the requested view function on the
// finalised Result, and writes its JSON to w.
func dumpView(path string, w io.Writer, regionsOverride []config.MapRegionOverride, vopts *viewOptions, pretty bool) error {
	src, err := mvdsource.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer src.Close()

	reg := analyzer.NewDefaultRegistry()
	if regionsOverride != nil {
		reg.SetRegionsOverride(regionsOverride)
	}
	res, err := reg.AnalyzeSource(src, filepath.Base(path))
	if err != nil {
		return err
	}

	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}

	switch vopts.view {
	case "buckets":
		bv, err := view.Buckets(res, view.BucketsOptions{
			WindowMs:    int(vopts.bucketDur / time.Millisecond),
			StartTime:   vopts.from.Seconds(),
			EndTime:     vopts.to.Seconds(),
			Players:     vopts.players,
			Fields:      vopts.fields,
			Reducers:    vopts.reducers,
			IncludeTeam: vopts.includeTeam,
		})
		if err != nil {
			return err
		}
		return enc.Encode(bv)

	case "events":
		ev, err := view.Events(res, view.EventsFilter{
			StartTime: vopts.from.Seconds(),
			EndTime:   vopts.to.Seconds(),
			Players:   vopts.players,
			Types:     vopts.eventTypes,
		})
		if err != nil {
			return err
		}
		return enc.Encode(ev)

	case "stream-slice":
		ssv, err := view.StreamSlice(res, view.StreamSliceOptions{
			StartTime: vopts.from.Seconds(),
			EndTime:   vopts.to.Seconds(),
			Players:   vopts.players,
			Fields:    vopts.fields,
		})
		if err != nil {
			return err
		}
		return enc.Encode(ssv)

	case "state-at":
		if vopts.timeAt == 0 {
			return fmt.Errorf("-view state-at requires -time")
		}
		v, err := view.StateAt(res, view.StateAtOptions{
			Time:    vopts.timeAt.Seconds(),
			Players: vopts.players,
			Fields:  vopts.fields,
		})
		if err != nil {
			return err
		}
		return enc.Encode(v)

	case "trails":
		tv, err := view.LocTrails(res, view.LocTrailsOptions{
			Players:    vopts.players,
			MinDwellMs: int(vopts.minDwell / time.Millisecond),
			StartTime:  vopts.from.Seconds(),
			EndTime:    vopts.to.Seconds(),
		})
		if err != nil {
			return err
		}
		return enc.Encode(tv)

	case "region-control":
		ta := res.TimelineAnalysis
		if ta == nil || ta.RegionControl == nil {
			return fmt.Errorf("region-control unavailable for this demo")
		}
		rcv, err := view.RegionControl(res, view.RegionControlOptions{
			WindowMs: int(vopts.bucketDur / time.Millisecond),
		})
		if err != nil {
			return err
		}
		return enc.Encode(rcv)
	}
	return fmt.Errorf("unhandled view %q", vopts.view)
}

// streamColumnSelection records which position-track columns the
// -include flag asked to keep. The whole track is dropped when none are
// selected.
type streamColumnSelection struct {
	positions bool // x/y/z (+ the per-sample loc label li)
	view      bool // vp/vya — view direction
	height    bool // h — height above floor
	liquid    bool // lq — liquid state
	velocity  bool // vx/vy/vz — velocity
}

func (s streamColumnSelection) any() bool {
	return s.positions || s.view || s.height || s.liquid || s.velocity
}

// stripStreamColumns drops position-track data the consumer did not ask
// for. With no position-family -include token the whole track is nil'd
// (the default — it is ~12 MB per 4on4). Otherwise the track is kept and
// each optional column is nil'd unless its token was given, so a consumer
// can include just view, just height, etc. without the rest. x/y/z are
// the track's base and always remain when the track is kept.
func stripStreamColumns(r *result.Result, sel streamColumnSelection) {
	if r.Streams == nil {
		return
	}
	for i := range r.Streams.Players {
		pt := r.Streams.Players[i].Position
		if pt == nil {
			continue
		}
		if !sel.any() {
			r.Streams.Players[i].Position = nil
			continue
		}
		if !sel.positions {
			pt.Li = nil
		}
		if !sel.view {
			pt.VP = nil
			pt.VYa = nil
		}
		if !sel.height {
			pt.H = nil
		}
		if !sel.liquid {
			pt.Lq = nil
		}
		if !sel.velocity {
			pt.VX = nil
			pt.VY = nil
			pt.VZ = nil
		}
	}
}

func dumpEvents(path string, w io.Writer) error {
	src, err := mvdsource.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer src.Close()

	enc := json.NewEncoder(w)
	for {
		ev, err := src.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// Wrap in a small envelope so consumers always see kind+time at
		// the top even for events whose own fields clash with those names.
		envelope := struct {
			Kind int     `json:"kind"`
			Time float64 `json:"time"`
			Data any     `json:"data"`
		}{int(ev.EventType()), ev.EventTime(), ev}
		if err := enc.Encode(envelope); err != nil {
			return err
		}
	}
}

func dumpMarkdown(path string, w io.Writer, regionsOverride []config.MapRegionOverride) error {
	src, err := mvdsource.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer src.Close()

	reg := analyzer.NewDefaultRegistry()
	if regionsOverride != nil {
		reg.SetRegionsOverride(regionsOverride)
	}
	res, err := reg.AnalyzeSource(src, filepath.Base(path))
	if err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", filepath.Base(path))
	if res.Match != nil {
		fmt.Fprintf(&b, "- duration: %.1fs\n", float64(res.Match.Duration)*0.001)
		fmt.Fprintf(&b, "- map: %s\n", res.Match.Map)
		fmt.Fprintf(&b, "- game dir: %s\n", res.Match.GameDir)
	}
	if res.Metadata != nil && res.Metadata.MatchSettings != nil {
		ms := res.Metadata.MatchSettings
		if ms.Mode != "" {
			fmt.Fprintf(&b, "- mode: %s\n", ms.Mode)
		}
		if ms.Timelimit > 0 {
			fmt.Fprintf(&b, "- timelimit: %d min\n", ms.Timelimit)
		}
		if ms.Matchtag != "" {
			fmt.Fprintf(&b, "- matchtag: %s\n", ms.Matchtag)
		}
	}

	if res.Match != nil && len(res.Match.Players) > 0 {
		fmt.Fprintf(&b, "\n## Players\n\n| Name | Team | Frags | Kills | Deaths |\n|---|---|---:|---:|---:|\n")
		for _, p := range res.Match.Players {
			var kills, deaths int
			if res.Frags != nil {
				if pf, ok := res.Frags.ByPlayer[p.Name]; ok {
					kills = pf.Kills
					deaths = pf.Deaths
				}
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %d |\n", p.Name, p.Team, p.Frags, kills, deaths)
		}
	}

	if res.Match != nil && len(res.Match.Teams) > 1 {
		fmt.Fprintf(&b, "\n## Teams\n\n| Team | Frags |\n|---|---:|\n")
		for _, t := range res.Match.Teams {
			fmt.Fprintf(&b, "| %s | %d |\n", t.Name, t.Frags)
		}
	}

	if res.TimelineAnalysis != nil {
		ta := res.TimelineAnalysis
		if n := len(ta.FragStreaks); n > 0 {
			show := n
			if show > 5 {
				show = 5
			}
			fmt.Fprintf(&b, "\n## Top frag streaks\n\n| Player | Team | Frags | Duration | Weapon |\n|---|---|---:|---:|---|\n")
			for _, s := range ta.FragStreaks[:show] {
				fmt.Fprintf(&b, "| %s | %s | %d | %.1fs | %s |\n", s.PlayerName, s.Team, s.Frags, float64(s.Duration)*0.001, s.Ewep)
			}
		}
		if n := len(ta.PowerupEvents); n > 0 {
			show := n
			if show > 5 {
				show = 5
			}
			fmt.Fprintf(&b, "\n## Top powerup runs\n\n| Player | Team | Powerup | Duration | Frags |\n|---|---|---|---:|---:|\n")
			for _, p := range ta.PowerupEvents[:show] {
				fmt.Fprintf(&b, "| %s | %s | %s | %.1fs | %d |\n", p.PlayerName, p.Team, p.PowerupType, float64(p.Duration)*0.001, p.Frags)
			}
		}
	}

	_, err = io.WriteString(w, b.String())
	return err
}

func runBulk(demosDir, outDir, format string, regionsOverride []config.MapRegionOverride, vopts *viewOptions) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(demosDir)
	if err != nil {
		return err
	}
	ext := outputExt(format)
	var processed, failed int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !isDemoFile(name) {
			continue
		}
		processed++
		outPath := filepath.Join(outDir, name+ext)
		f, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "create:", err)
			failed++
			continue
		}
		err = runOne(filepath.Join(demosDir, name), f, format, false, regionsOverride, vopts)
		f.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, name+":", err)
			failed++
			continue
		}
		fmt.Fprintln(os.Stderr, "wrote", outPath)
	}
	fmt.Fprintf(os.Stderr, "processed=%d failed=%d\n", processed, failed)
	if failed > 0 {
		return fmt.Errorf("%d demo(s) failed", failed)
	}
	return nil
}

func outputExt(format string) string {
	switch format {
	case "md":
		return ".md"
	case "events":
		return ".events.jsonl"
	default:
		return ".json"
	}
}

func isDemoFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".mvd") || strings.HasSuffix(lower, ".mvd.gz")
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// stringListFlag is a tiny helper for repeatable string flags. The
// returned pointer's []string accumulates one entry per occurrence.
type stringList []string

func (s *stringList) String() string         { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error     { *s = append(*s, v); return nil }
func stringListFlag(name, usage string) *stringList {
	var sl stringList
	flag.Var(&sl, name, usage)
	return &sl
}
