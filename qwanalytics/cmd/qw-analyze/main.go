// qw-analyze is a command-line consumer of the qwanalytics pipeline.
// It reads an MVD demo file and writes the analysis result in one of
// three formats — JSON (the stable result-schema contract), markdown
// (a human-readable summary suitable as a seed for an AI review
// agent), or line-delimited event JSON (the raw qwdemo event stream,
// useful for debugging and for driving alternative analytics).
//
// Example invocations:
//
//	qw-analyze demo.mvd.gz                         # JSON to stdout
//	qw-analyze -format md demo.mvd.gz > report.md  # markdown summary
//	qw-analyze -format events demo.mvd.gz | jq .   # event stream
//	qw-analyze -bulk demos/ -out-dir analyses/     # batch mode
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mvd-analyzer/qwanalytics/analyzer"
	"github.com/mvd-analyzer/qwanalytics/config"
	mvdsource "github.com/mvd-analyzer/qwdemo/source/mvd"
)

func main() {
	format := flag.String("format", "json", "output format: json | md | events")
	outDir := flag.String("out-dir", "", "bulk mode: write <demo>.<ext> into this directory")
	bulk := flag.Bool("bulk", false, "treat the input path as a directory and analyze every demo in it")
	indent := flag.Bool("pretty", false, "pretty-print JSON output (single-demo mode only); pipe to `jq .` for human reading")
	regionsPath := flag.String("regions", "", "path to a regions JSON ({\"regions\":[{\"name\":...,\"locs\":[...]}]}) to override the embedded per-map regions for the analyzed demo")
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

	if *bulk || *outDir != "" {
		if *outDir == "" {
			fmt.Fprintln(os.Stderr, "qw-analyze: -bulk requires -out-dir")
			os.Exit(2)
		}
		if err := runBulk(input, *outDir, *format, regionsOverride); err != nil {
			fmt.Fprintln(os.Stderr, "qw-analyze:", err)
			os.Exit(1)
		}
		return
	}

	if err := runOne(input, os.Stdout, *format, *indent, regionsOverride); err != nil {
		fmt.Fprintln(os.Stderr, "qw-analyze:", err)
		os.Exit(1)
	}
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

func runOne(path string, w io.Writer, format string, pretty bool, regionsOverride []config.MapRegionOverride) error {
	switch format {
	case "events":
		return dumpEvents(path, w)
	case "json":
		return dumpJSON(path, w, pretty, regionsOverride)
	case "md":
		return dumpMarkdown(path, w, regionsOverride)
	default:
		return fmt.Errorf("unknown format %q (want json | md | events)", format)
	}
}

func dumpJSON(path string, w io.Writer, pretty bool, regionsOverride []config.MapRegionOverride) error {
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
	return enc.Encode(res)
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
			Kind int         `json:"kind"`
			Time float64     `json:"time"`
			Data interface{} `json:"data"`
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
		fmt.Fprintf(&b, "- duration: %.1fs\n", res.Match.Duration)
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
				fmt.Fprintf(&b, "| %s | %s | %d | %.1fs | %s |\n", s.PlayerName, s.Team, s.Frags, s.Duration, s.Ewep)
			}
		}
		if n := len(ta.PowerupEvents); n > 0 {
			show := n
			if show > 5 {
				show = 5
			}
			fmt.Fprintf(&b, "\n## Top powerup runs\n\n| Player | Team | Powerup | Duration | Frags |\n|---|---|---|---:|---:|\n")
			for _, p := range ta.PowerupEvents[:show] {
				fmt.Fprintf(&b, "| %s | %s | %s | %.1fs | %d |\n", p.PlayerName, p.Team, p.PowerupType, p.Duration, p.Frags)
			}
		}
	}

	_, err = io.WriteString(w, b.String())
	return err
}

func runBulk(demosDir, outDir, format string, regionsOverride []config.MapRegionOverride) error {
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
		err = runOne(filepath.Join(demosDir, name), f, format, false, regionsOverride)
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
