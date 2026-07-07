package democache

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// staleTempAge is how old a leftover atomic-write temp file (.tmp-*) must be
// before the sweep treats it as abandoned by a crashed writer and removes
// it. Fresh temps belong to an in-flight writeFileAtomic and are left alone.
const staleTempAge = time.Hour

// versionDirRe matches a tier-2 version directory name: the current
// format-suffixed form (v<N>f<F>) or the pre-phase-12 suffix-less form
// (v<N>). Used so the old-version-tree sweep only ever removes directories
// the cache created, never a stray file an operator dropped under results/.
var versionDirRe = regexp.MustCompile(`^v\d+(f\d+)?$`)

// cacheFile is one GC eviction unit: a single tier-1 MVD, tier-2 gob, or
// tier-3 artifact gob, with the mtime the sweep orders by.
type cacheFile struct {
	path  string
	size  int64
	mtime time.Time
}

// tierScan is the result of walking the cache root's subtrees once. tier1,
// tier2 and tier3 are the evictable units; temps are .tmp-* leftovers (any
// subtree); the index subtree is counted but never evicted.
type tierScan struct {
	tier1 []cacheFile
	tier2 []cacheFile
	tier3 []cacheFile
	temps []cacheFile

	indexCount int
	indexBytes int64

	// versionDirs maps each results/<ver> directory name (v<N>f<F>, or a
	// legacy v<N>) to its byte total, so Stats can distinguish the current
	// tree from orphaned ones.
	versionDirs map[string]int64
}

func isTempName(path string) bool {
	return strings.HasPrefix(filepath.Base(path), ".tmp-")
}

// walkRegular calls fn for every regular file under dir. A missing dir is
// not an error (a fresh cache has no tiers yet).
func walkRegular(dir string, fn func(path string, size int64, mtime time.Time)) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries rather than abort the sweep
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fn(path, info.Size(), info.ModTime())
		return nil
	})
}

// scanCache walks the four cache subtrees once and classifies every file.
func scanCache(root string) tierScan {
	sc := tierScan{versionDirs: map[string]int64{}}

	walkRegular(mvdRoot(root), func(p string, size int64, mtime time.Time) {
		if isTempName(p) {
			sc.temps = append(sc.temps, cacheFile{p, size, mtime})
			return
		}
		sc.tier1 = append(sc.tier1, cacheFile{p, size, mtime})
	})

	resDir := resultsRoot(root)
	walkRegular(resDir, func(p string, size int64, mtime time.Time) {
		if isTempName(p) {
			sc.temps = append(sc.temps, cacheFile{p, size, mtime})
			return
		}
		sc.tier2 = append(sc.tier2, cacheFile{p, size, mtime})
		if v := versionComponent(resDir, p); v != "" {
			sc.versionDirs[v] += size
		}
	})

	walkRegular(artifactsRoot(root), func(p string, size int64, mtime time.Time) {
		if isTempName(p) {
			sc.temps = append(sc.temps, cacheFile{p, size, mtime})
			return
		}
		sc.tier3 = append(sc.tier3, cacheFile{p, size, mtime})
	})

	walkRegular(indexRoot(root), func(p string, size int64, mtime time.Time) {
		if isTempName(p) {
			sc.temps = append(sc.temps, cacheFile{p, size, mtime})
			return
		}
		sc.indexCount++
		sc.indexBytes += size
	})

	return sc
}

// versionComponent returns the first path element of p relative to the
// results root (the "v<N>" tree name), or "" if p is not under it.
func versionComponent(resultsDir, p string) string {
	rel, err := filepath.Rel(resultsDir, p)
	if err != nil {
		return ""
	}
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		return rel[:i]
	}
	return ""
}

// cleanTemps removes atomic-write temp files older than staleTempAge. A
// fresh temp is an in-flight writer's, so it is left in place.
func cleanTemps(temps []cacheFile, logger *slog.Logger) {
	cutoff := time.Now().Add(-staleTempAge)
	for _, t := range temps {
		if t.mtime.After(cutoff) {
			continue
		}
		if err := os.Remove(t.path); err == nil {
			logger.Debug("cache gc: removed stale temp", "path", t.path)
		}
	}
}

// SweepToBudget cleans stale temp files and, when maxBytes > 0, evicts
// evictable cache files oldest-mtime-first until the tier-1 + tier-2 +
// tier-3 total fits the budget. maxBytes <= 0 disables eviction (temps are
// still cleaned). This is the single sweep used by the online GC
// (Cache.maybeGC), startup cleanup, and `mvd-api cache prune -max-bytes`.
//
// Eviction unit: each tier-1 MVD, tier-2 gob and tier-3 artifact gob is
// treated independently by its own mtime — the sweep never pairs a gob with
// its MVD. Every unit is reconstructible: dropping a tier-2 gob reparses
// from the cached bytes (no re-download); dropping a tier-1 MVD still
// serves everything from its gob (since phase 12 the tier-2 Result is
// always-full, so no endpoint needs the raw bytes back); dropping a tier-3
// artifact recomputes it on the next /los. The gameId index is exempt
// entirely (tiny; losing it forces a hub re-resolve).
//
// Safe to run concurrently with request serving. On Linux, unlinking a file
// another goroutine already opened only drops the directory entry; the open
// fd keeps the bytes alive until closed, so an in-flight reader never
// faults on a file this sweep removes.
func SweepToBudget(root string, maxBytes int64, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	sc := scanCache(root)
	cleanTemps(sc.temps, logger)
	if maxBytes <= 0 {
		return
	}

	files := make([]cacheFile, 0, len(sc.tier1)+len(sc.tier2)+len(sc.tier3))
	files = append(files, sc.tier1...)
	files = append(files, sc.tier2...)
	files = append(files, sc.tier3...)

	var total int64
	for _, f := range files {
		total += f.size
	}
	if total <= maxBytes {
		return
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })

	var freed int64
	var removed int
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err != nil {
			if !os.IsNotExist(err) {
				logger.Warn("cache gc: remove failed", "path", f.path, "err", err)
			}
			continue
		}
		total -= f.size
		freed += f.size
		removed++
	}
	logger.Info("cache gc swept",
		"removed", removed, "freed_bytes", freed,
		"remaining_bytes", total, "budget_bytes", maxBytes)
}

// PruneOlderThan removes tier-1, tier-2 and tier-3 files whose mtime is
// older than age, and cleans stale temps. CLI-only (`cache prune
// -older-than`); the index subtree is exempt, same as SweepToBudget.
func PruneOlderThan(root string, age time.Duration, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	sc := scanCache(root)
	cleanTemps(sc.temps, logger)
	cutoff := time.Now().Add(-age)

	evictable := append(append(append([]cacheFile(nil), sc.tier1...), sc.tier2...), sc.tier3...)
	var freed int64
	var removed int
	for _, f := range evictable {
		if !f.mtime.Before(cutoff) {
			continue
		}
		if err := os.Remove(f.path); err != nil {
			if !os.IsNotExist(err) {
				logger.Warn("cache prune: remove failed", "path", f.path, "err", err)
			}
			continue
		}
		freed += f.size
		removed++
	}
	logger.Info("cache pruned by age",
		"removed", removed, "freed_bytes", freed, "older_than", age.String())
}

// PruneAll removes all three cache tiers (mvd/, results/, artifacts/)
// wholesale, keeping only the small gameId index so a re-warm skips the hub
// re-resolve. CLI-only (`cache prune -all`).
func PruneAll(root string, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	for _, dir := range []string{mvdRoot(root), resultsRoot(root), artifactsRoot(root)} {
		if err := os.RemoveAll(dir); err != nil {
			logger.Warn("cache prune -all: remove failed", "path", dir, "err", err)
		}
	}
	logger.Info("cache pruned (all tiers removed; index kept)")
}

// CleanOldVersionTrees deletes every results/* tree whose directory name is
// not the current schema+format version's (resultsVersionName), reclaiming
// the disk a schema or cache-format bump orphans — including the legacy
// suffix-less v<N> trees written before the format suffix existed. Only
// directories matching the version-name shape are ever removed.
func CleanOldVersionTrees(root string, currentVersion int, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	resDir := resultsRoot(root)
	entries, err := os.ReadDir(resDir)
	if err != nil {
		return // no results tree yet — nothing to clean
	}
	keep := resultsVersionName(currentVersion)
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep || !versionDirRe.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(resDir, e.Name())
		if err := os.RemoveAll(p); err != nil {
			logger.Warn("cache gc: remove orphaned schema tree failed", "path", p, "err", err)
			continue
		}
		logger.Info("cache gc: removed orphaned schema tree", "path", p, "kept", keep)
	}
}

// CleanStaleArtifacts deletes every tier-3 gob whose filename does not carry
// the current effective-version suffix ("@v<EV>.gob"). Tier-3 versioning is
// per-file, not per-tree: a stale gob is simply never read again
// (artifactPath points at the current-version name), so it is pure garbage —
// this reaps both old-version los gobs after a schema bump and the
// shot-streams@* gobs orphaned when phase 12 retired that artifact. Temp
// files are left to cleanTemps.
func CleanStaleArtifacts(root string, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	keep := artifactVersionSuffix()
	var removed int
	walkRegular(artifactsRoot(root), func(p string, _ int64, _ time.Time) {
		if isTempName(p) || strings.HasSuffix(filepath.Base(p), keep) {
			return
		}
		if err := os.Remove(p); err != nil {
			logger.Warn("cache gc: remove stale artifact failed", "path", p, "err", err)
			return
		}
		removed++
	})
	if removed > 0 {
		logger.Info("cache gc: removed stale artifacts", "removed", removed, "kept_suffix", keep)
	}
}

// StatsReport summarises on-disk cache contents for `mvd-api cache stats`.
type StatsReport struct {
	Tier1Count int
	Tier1Bytes int64
	Tier2Count int
	Tier2Bytes int64
	Tier3Count int
	Tier3Bytes int64
	IndexCount int
	IndexBytes int64
	TempCount  int
	TempBytes  int64

	// CurrentVersion is the tier-2 tree name for the running schema+format
	// version.
	CurrentVersion string
	// VersionTrees maps every results/* version-tree name to its byte total.
	// Any key != CurrentVersion is orphaned and reclaimable.
	VersionTrees map[string]int64
}

// Stats walks the cache once and reports per-tier counts and bytes plus the
// current-vs-orphaned version-tree split.
func Stats(root string, currentVersion int) StatsReport {
	sc := scanCache(root)
	rep := StatsReport{
		Tier1Count:     len(sc.tier1),
		Tier2Count:     len(sc.tier2),
		Tier3Count:     len(sc.tier3),
		IndexCount:     sc.indexCount,
		IndexBytes:     sc.indexBytes,
		TempCount:      len(sc.temps),
		CurrentVersion: resultsVersionName(currentVersion),
		VersionTrees:   sc.versionDirs,
	}
	for _, f := range sc.tier1 {
		rep.Tier1Bytes += f.size
	}
	for _, f := range sc.tier2 {
		rep.Tier2Bytes += f.size
	}
	for _, f := range sc.tier3 {
		rep.Tier3Bytes += f.size
	}
	for _, f := range sc.temps {
		rep.TempBytes += f.size
	}
	return rep
}
