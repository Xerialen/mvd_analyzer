// mapgen is a developer tool that parses Quake 1 BSP files and writes
// two kinds of per-map JSON:
//
//   - -out-dir: per-loc walkable-floor polygon geometry (the viewer
//     renders real floor geometry instead of the loc convex-hull blob).
//   - -entities-out: the static map-entity corpus (mapents) — item
//     spawns, player spawnpoints, teleport destinations/sources, buttons
//     — classified from the BSP entity lump and named by nearest loc.
//
// Either output is optional; set the flags for what you want. Missing
// files degrade silently downstream.
//
// Usage (developer machine — point at your own local Quake install):
//
//	go build ./cmd/mapgen
//	./mapgen -bsp-dir ~/quake/id1/maps -verbose                 # geometry (default out-dir)
//	./mapgen -bsp-dir ~/quake/id1/maps -out-dir "" \
//	    -entities-out mvd-analytics/mapents/data -verbose        # entity corpus only
//	./mapgen -bsp-dir /path/to/maps -map dm2 -verbose
//
// This tool is NOT part of CI and is not run during normal builds; only
// the generated JSON files are committed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/mapbsp"
	"github.com/mvd-analyzer/mvd-analytics/mapclip"
	"github.com/mvd-analyzer/mvd-analytics/mapgen/bsp"
	"github.com/mvd-analyzer/mvd-analytics/loc"
	"github.com/mvd-analyzer/mvd-analytics/mapgen/mapgeom"
	"github.com/mvd-analyzer/mvd-analytics/result"
)

func main() {
	bspDir := flag.String("bsp-dir", "", "directory containing .bsp files (required)")
	outDir := flag.String("out-dir", "mvd-web/static/maps", "output directory for geometry JSON; empty to skip geometry")
	entitiesOut := flag.String("entities-out", "", "output directory for per-map entity JSON (mapents corpus); empty to skip entities")
	locDir := flag.String("loc-dir", "", "directory of .loc files; empty uses the embedded loc corpus (mvd-analytics/loc/data)")
	mapFilter := flag.String("map", "", "process only the BSP whose basename (no extension) matches")
	verbose := flag.Bool("verbose", false, "print per-map progress and stats")
	demosDir := flag.String("demos", "", "directory of .mvd/.mvd.gz demos for usage-based floor pruning (geometry only); empty to skip")
	pruneXYTol := flag.Float64("prune-xy-tol", mapclip.FootprintReach, "usage pruning: max XY distance (world units) from a floor-contact sample to a floor polygon")
	pruneZTol := flag.Float64("prune-z-tol", 16.0, "usage pruning: max |faceZ - sampleZ| (world units); raise for slope-heavy maps")
	flag.Parse()

	if *outDir == "" && *entitiesOut == "" {
		fmt.Fprintln(os.Stderr, "mapgen: nothing to do — set -out-dir and/or -entities-out")
		flag.Usage()
		os.Exit(2)
	}

	loc.SetLocDir(*locDir)

	if *bspDir == "" {
		fmt.Fprintln(os.Stderr, "mapgen: -bsp-dir is required")
		flag.Usage()
		os.Exit(2)
	}

	info, err := os.Stat(*bspDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mapgen: %v\n", err)
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "mapgen: %s is not a directory\n", *bspDir)
		os.Exit(1)
	}

	for _, dir := range []string{*outDir, *entitiesOut} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mapgen: create output dir %s: %v\n", dir, err)
			os.Exit(1)
		}
	}

	bspPaths, err := findBSPs(*bspDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mapgen: walk bsp-dir: %v\n", err)
		os.Exit(1)
	}
	sort.Strings(bspPaths)

	if *verbose {
		fmt.Fprintf(os.Stderr, "mapgen: found %d BSP files under %s\n", len(bspPaths), *bspDir)
	}

	// Optional usage-based floor pruning: analyze the demo set once and
	// group floor-contact points per normalized map name. Geometry-only;
	// a no-op without -out-dir.
	var usageByMap map[string]*mapgeom.FloorUsage
	if *demosDir != "" {
		if *outDir == "" {
			fmt.Fprintln(os.Stderr, "mapgen: -demos has no effect without -out-dir (pruning only touches geometry)")
		} else {
			mapbsp.SetDir(*bspDir) // so the analyzer computes per-sample H/Lq
			usageByMap = collectUsage(*demosDir, *verbose)
		}
	}
	params := mapgeom.DefaultParams()
	params.PruneXYTol = float32(*pruneXYTol)
	params.PruneZTol = float32(*pruneZTol)

	var processed, failed int
	for _, path := range bspPaths {
		name := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		if *mapFilter != "" && name != strings.ToLower(*mapFilter) {
			continue
		}

		var usage *mapgeom.FloorUsage
		if usageByMap != nil {
			usage = usageByMap[loc.NormalizeMapName(name)]
		}
		if err := processOne(path, name, *outDir, *entitiesOut, *verbose, usage, params); err != nil {
			fmt.Fprintf(os.Stderr, "  fail %s: %v\n", name, err)
			failed++
			continue
		}
		processed++
	}

	fmt.Fprintf(os.Stderr, "mapgen: processed=%d failed=%d\n", processed, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func findBSPs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".bsp") {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func processOne(path, name, outDir, entitiesOut string, verbose bool, usage *mapgeom.FloorUsage, params mapgeom.Params) error {
	// Loc file is optional: without it, geometry routes every floor face
	// into the unnamed backdrop bucket and entities fall back to
	// kind/type names instead of loc names.
	finder, locErr := loc.LoadForMap(name)
	if locErr != nil {
		finder = nil
		if verbose {
			fmt.Fprintf(os.Stderr, "  note %s: no loc file, emitting unnamed data only\n", name)
		}
	}

	if outDir != "" {
		if err := emitGeometry(path, name, finder, outDir, verbose, usage, params); err != nil {
			return err
		}
	}
	if entitiesOut != "" {
		if err := emitEntities(path, name, finder, entitiesOut, verbose); err != nil {
			return err
		}
	}
	return nil
}

func emitGeometry(path, name string, finder *loc.Finder, outDir string, verbose bool, usage *mapgeom.FloorUsage, params mapgeom.Params) error {
	parsed, err := bsp.Parse(path)
	if err != nil {
		return fmt.Errorf("parse bsp: %w", err)
	}

	// Prune only when this map has recorded usage; otherwise emit the full
	// geometry (a map with no demos must not lose all its floors).
	var (
		regions *mapgeom.MapRegions
		stats   mapgeom.Stats
	)
	if usage != nil {
		regions, stats = mapgeom.BuildPruned(name, parsed, finder, params, usage)
	} else {
		regions, stats = mapgeom.Build(name, parsed, finder)
	}
	if len(regions.Locs) == 0 {
		return fmt.Errorf("no floor geometry extracted (faces total=%d kept=%d dropped=%d)",
			stats.FacesTotal, stats.FacesKept, stats.FacesDropped)
	}

	outPath := filepath.Join(outDir, name+".json")
	data, err := json.Marshal(regions)
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	if verbose {
		prune := ""
		if regions.Pruned != nil {
			prune = fmt.Sprintf(" pruned=%d/%d demos=%d points=%d",
				regions.Pruned.FacesDropped, stats.FacesKept+regions.Pruned.FacesDropped,
				regions.Pruned.Demos, regions.Pruned.Points)
		}
		fmt.Fprintf(os.Stderr, "  ok   %s: locs=%d tris=%d walls=%d liquids=%d/%d submodels=%d/%d faces=%d/%d unnamed=%d ceiling=%d dropped=%d degen=%d%s bytes=%d\n",
			name, stats.Locs, stats.Triangles, stats.WallTris,
			stats.LiquidFaces, stats.LiquidTris, stats.SubModelMeshes, stats.SubModelTris,
			stats.FacesKept, stats.FacesTotal,
			stats.FacesUnnamed, stats.FacesCeiling, stats.FacesDropped, stats.DegenerateTris, prune, len(data))
	}
	return nil
}

// collectUsage analyzes every demo under demosDir and groups the resulting
// floor-contact points by normalized map name. The supporting surface
// beneath a player at sample i is exactly (X, Y, Z − PlayerFeetOffset − H);
// samples with no floor (H == NoFloor) or where the player is in a liquid
// (Lq level ≥ 1 — liquid-supported, not floor) are skipped. A demo that
// fails to analyze is logged and skipped rather than aborting the run.
func collectUsage(demosDir string, verbose bool) map[string]*mapgeom.FloorUsage {
	paths := findDemos(demosDir)
	if verbose {
		fmt.Fprintf(os.Stderr, "mapgen: pruning from %d demos under %s\n", len(paths), demosDir)
	}
	out := make(map[string]*mapgeom.FloorUsage)
	for _, p := range paths {
		// A fresh registry per demo: analyzer state is not safe to reuse
		// across Analyze calls (matches the golden harness).
		res, err := analyzer.NewDefaultRegistry().Analyze(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn demo %s: %v\n", filepath.Base(p), err)
			continue
		}
		if res.DemoInfo == nil || res.Streams == nil {
			continue
		}
		key := loc.NormalizeMapName(res.DemoInfo.Map)
		if key == "" {
			continue
		}
		u := out[key]
		if u == nil {
			u = mapgeom.NewFloorUsage()
			out[key] = u
		}
		u.AddDemo()
		for _, pl := range res.Streams.Players {
			pt := pl.Position
			if pt == nil {
				continue
			}
			n := len(pt.T)
			// H is required and aligned with T; coordinates likewise.
			if len(pt.H) != n || len(pt.X) != n || len(pt.Y) != n || len(pt.Z) != n {
				continue
			}
			hasLq := len(pt.Lq) == n
			for i := 0; i < n; i++ {
				if pt.H[i] == result.NoFloor {
					continue
				}
				if hasLq && result.LqLevel(pt.Lq[i]) >= 1 {
					continue // swimmer: liquid-supported, not standing on floor
				}
				fz := float32(pt.Z[i]) - mapclip.PlayerFeetOffset - float32(pt.H[i])
				u.AddPoint(float32(pt.X[i]), float32(pt.Y[i]), fz)
			}
		}
	}
	return out
}

// findDemos returns every .mvd / .mvd.gz path under root.
func findDemos(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		lower := strings.ToLower(d.Name())
		if strings.HasSuffix(lower, ".mvd") || strings.HasSuffix(lower, ".mvd.gz") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

