package main

import (
	"flag"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-api/internal/democache"
)

// runCache dispatches the `mvd-api cache <stats|prune>` ops subcommands.
// These operate directly on the cache directory (no hub, no server) and
// reuse the same sweep code as the online GC.
func runCache(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mvd-api cache <stats|prune> [flags]")
	}
	switch args[0] {
	case "stats":
		return runCacheStats(args[1:])
	case "prune":
		return runCachePrune(args[1:])
	default:
		return fmt.Errorf("unknown cache subcommand %q (want stats|prune)", args[0])
	}
}

func runCacheStats(args []string) error {
	fs := flag.NewFlagSet("cache stats", flag.ContinueOnError)
	cacheDir := fs.String("cache-dir", democache.DefaultRoot(), "on-disk cache root")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rep := democache.Stats(*cacheDir, result.CurrentSchemaVersion)
	fmt.Printf("cache root: %s\n", *cacheDir)
	fmt.Printf("tier-1 (mvd):       %5d files  %s\n", rep.Tier1Count, humanBytes(rep.Tier1Bytes))
	fmt.Printf("tier-2 (results):   %5d files  %s\n", rep.Tier2Count, humanBytes(rep.Tier2Bytes))
	fmt.Printf("tier-3 (artifacts): %5d files  %s\n", rep.Tier3Count, humanBytes(rep.Tier3Bytes))
	fmt.Printf("index (gameId):     %5d files  %s\n", rep.IndexCount, humanBytes(rep.IndexBytes))
	if rep.TempCount > 0 {
		fmt.Printf("temp (.tmp-*):      %5d files  %s\n", rep.TempCount, humanBytes(rep.TempBytes))
	}
	fmt.Printf("total tiers:              %s\n", humanBytes(rep.Tier1Bytes+rep.Tier2Bytes+rep.Tier3Bytes))
	fmt.Printf("current schema tree: %s\n", rep.CurrentVersion)

	names := make([]string, 0, len(rep.VersionTrees))
	for name := range rep.VersionTrees {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tag := ""
		if name != rep.CurrentVersion {
			tag = "  (orphaned — reclaim with `cache prune`)"
		}
		fmt.Printf("  results/%s: %s%s\n", name, humanBytes(rep.VersionTrees[name]), tag)
	}
	return nil
}

func runCachePrune(args []string) error {
	fs := flag.NewFlagSet("cache prune", flag.ContinueOnError)
	cacheDir := fs.String("cache-dir", democache.DefaultRoot(), "on-disk cache root")
	maxBytes := fs.Int64("max-bytes", -1, "evict oldest files until tiers fit this many bytes (use -all to wipe everything)")
	olderThan := fs.String("older-than", "", "remove files older than this age (e.g. 30d, 720h)")
	all := fs.Bool("all", false, "remove all cache tiers (mvd/ + results/ + artifacts/); keeps the gameId index")
	dryRun := fs.Bool("dry-run", false, "log exactly what would be removed and delete nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Exactly one action must be selected.
	actions := 0
	if *maxBytes >= 0 {
		actions++
	}
	if *olderThan != "" {
		actions++
	}
	if *all {
		actions++
	}
	if actions != 1 {
		return fmt.Errorf("choose exactly one of -max-bytes, -older-than, -all")
	}
	// -max-bytes 0 would select the sweep but evict nothing (0 disables the
	// budget) — a silent no-op that reads like "wipe the cache". Reject it and
	// point at the real wipe.
	if *maxBytes == 0 {
		return fmt.Errorf("-max-bytes 0 evicts nothing; use -all to wipe everything")
	}

	logger := newLogger("text")
	// Orphaned schema trees and stale-version artifact gobs are reclaimable
	// regardless of the chosen action.
	democache.CleanOldVersionTrees(*cacheDir, result.CurrentSchemaVersion, *dryRun, logger)
	democache.CleanStaleArtifacts(*cacheDir, *dryRun, logger)

	switch {
	case *all:
		democache.PruneAll(*cacheDir, *dryRun, logger)
	case *maxBytes > 0:
		democache.SweepToBudgetDryRun(*cacheDir, *maxBytes, *dryRun, logger)
	case *olderThan != "":
		age, err := parseAge(*olderThan)
		if err != nil {
			return err
		}
		democache.PruneOlderThan(*cacheDir, age, *dryRun, logger)
	}
	return nil
}

// parseAge parses a duration that additionally accepts a trailing 'd' (days)
// or 'w' (weeks), which time.ParseDuration does not support.
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty age")
	}
	switch unit := s[len(s)-1]; unit {
	case 'd', 'w':
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid age %q", s)
		}
		per := 24 * time.Hour
		if unit == 'w' {
			per = 7 * 24 * time.Hour
		}
		return time.Duration(n) * per, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid age %q: %v", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("age must not be negative: %q", s)
	}
	return d, nil
}

// humanBytes formats a byte count as a compact human-readable string.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
