package democache

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeFileWithMtime creates path (with parents) holding size bytes and sets
// its mtime, so GC-ordering tests are deterministic.
func writeFileWithMtime(t *testing.T, path string, size int, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSweepToBudget_EvictsOldestFirstAndExemptsIndex(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	ver := result.CurrentSchemaVersion

	// Six 100-byte eviction units across all three tiers, staggered by age
	// (oldest first). tier-3 files must be evictable like the rest.
	type f struct {
		path string
		age  time.Duration
	}
	units := []f{
		{mvdPath(root, "aa"+repeat("0", 62)), 60 * time.Minute},
		{resultPath(root, ver, "ab"+repeat("0", 62)), 50 * time.Minute},
		{artifactPath(root, "los", "ac"+repeat("0", 62)), 40 * time.Minute},
		{resultPath(root, ver, "ad"+repeat("0", 62)), 30 * time.Minute},
		{mvdPath(root, "ae"+repeat("0", 62)), 20 * time.Minute},
		{artifactPath(root, "los", "af"+repeat("0", 62)), 10 * time.Minute},
	}
	for _, u := range units {
		writeFileWithMtime(t, u.path, 100, now.Add(-u.age))
	}
	// Index file — must survive any budget.
	idx := gameIndexPath(root, 42)
	writeFileWithMtime(t, idx, 100, now.Add(-90*time.Minute))

	// Budget = 250 bytes → only 2 of the six 100-byte units may remain;
	// the four oldest are evicted.
	SweepToBudget(root, 250, quietLogger())

	// Four oldest gone.
	for _, u := range units[:4] {
		if exists(u.path) {
			t.Errorf("expected oldest unit evicted: %s", u.path)
		}
	}
	// Two newest kept.
	for _, u := range units[4:] {
		if !exists(u.path) {
			t.Errorf("expected newest unit kept: %s", u.path)
		}
	}
	// Index exempt.
	if !exists(idx) {
		t.Errorf("index file must be exempt from GC eviction")
	}
}

func TestSweepToBudget_DryRunDeletesNothing(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	ver := result.CurrentSchemaVersion
	paths := []string{
		mvdPath(root, "aa"+repeat("0", 62)),
		resultPath(root, ver, "ab"+repeat("0", 62)),
		artifactPath(root, "los", "ac"+repeat("0", 62)),
	}
	for i, p := range paths {
		writeFileWithMtime(t, p, 100, now.Add(-time.Duration(i+1)*time.Minute))
	}
	// Budget forces eviction, but dry-run must leave every file in place.
	SweepToBudgetDryRun(root, 1, true, quietLogger())
	for _, p := range paths {
		if !exists(p) {
			t.Errorf("dry-run must not delete %s", p)
		}
	}
}

func TestSweepToBudget_UnderBudgetNoOp(t *testing.T) {
	root := t.TempDir()
	p := mvdPath(root, "ba"+repeat("0", 62))
	writeFileWithMtime(t, p, 100, time.Now())
	SweepToBudget(root, 1<<20, quietLogger())
	if !exists(p) {
		t.Errorf("no file should be evicted when under budget")
	}
}

func TestSweepToBudget_DisabledStillCleansTemps(t *testing.T) {
	root := t.TempDir()
	// A large file plus a stale temp; budget 0 disables eviction but temps
	// must still be reaped.
	keep := mvdPath(root, "ca"+repeat("0", 62))
	writeFileWithMtime(t, keep, 1000, time.Now())

	staleTemp := filepath.Join(mvdRoot(root), "ca", ".tmp-abandoned")
	writeFileWithMtime(t, staleTemp, 500, time.Now().Add(-2*time.Hour))
	freshTemp := filepath.Join(mvdRoot(root), "ca", ".tmp-inflight")
	writeFileWithMtime(t, freshTemp, 500, time.Now())

	SweepToBudget(root, 0, quietLogger())

	if !exists(keep) {
		t.Errorf("budget 0 must not evict tier files")
	}
	if exists(staleTemp) {
		t.Errorf("stale temp (>1h) should be cleaned")
	}
	if !exists(freshTemp) {
		t.Errorf("fresh temp (<1h) must be left for the in-flight writer")
	}
}

func TestCleanOldVersionTrees(t *testing.T) {
	root := t.TempDir()
	cur := result.CurrentSchemaVersion
	now := time.Now()

	// Current tree (v<N>f<F>), an orphaned previous-schema tree, a legacy
	// suffix-less pre-phase-12 tree, and a stray non-version dir.
	curFile := resultPath(root, cur, "da"+repeat("0", 62))
	oldFile := resultPath(root, cur-1, "db"+repeat("0", 62))
	writeFileWithMtime(t, curFile, 10, now)
	writeFileWithMtime(t, oldFile, 10, now)
	legacy := filepath.Join(resultsRoot(root), "v49", "dc", "dc.gob")
	writeFileWithMtime(t, legacy, 10, now)
	stray := filepath.Join(resultsRoot(root), "notes", "readme.txt")
	writeFileWithMtime(t, stray, 10, now)

	CleanOldVersionTrees(root, cur, false, quietLogger())

	if !exists(curFile) {
		t.Errorf("current schema tree must be kept")
	}
	if exists(oldFile) {
		t.Errorf("orphaned %s tree must be removed", resultsVersionName(cur-1))
	}
	if exists(legacy) {
		t.Errorf("legacy suffix-less v49 tree must be removed")
	}
	if !exists(stray) {
		t.Errorf("non-version directory must not be touched")
	}
}

func TestCleanStaleArtifacts(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	sha := "ea" + repeat("0", 62)

	// Current-version los gob, a previous-version los gob, and an orphaned
	// shot-streams gob from before phase 12 — only the current one survives.
	current := artifactPath(root, "los", sha)
	staleVer := filepath.Join(artifactsRoot(root), sha[:2], sha,
		"los@v"+strconv.Itoa(result.CurrentSchemaVersion-1)+".gob")
	retired := filepath.Join(artifactsRoot(root), sha[:2], sha,
		"shot-streams@v"+strconv.Itoa(result.CurrentSchemaVersion-1)+".gob")
	writeFileWithMtime(t, current, 10, now)
	writeFileWithMtime(t, staleVer, 10, now)
	writeFileWithMtime(t, retired, 10, now)

	CleanStaleArtifacts(root, false, quietLogger())

	if !exists(current) {
		t.Errorf("current-version artifact must be kept")
	}
	if exists(staleVer) {
		t.Errorf("stale-version artifact must be removed")
	}
	if exists(retired) {
		t.Errorf("retired shot-streams artifact must be removed")
	}
}

func TestStats(t *testing.T) {
	root := t.TempDir()
	cur := result.CurrentSchemaVersion
	now := time.Now()

	writeFileWithMtime(t, mvdPath(root, "fa"+repeat("0", 62)), 100, now)
	writeFileWithMtime(t, mvdPath(root, "fb"+repeat("0", 62)), 200, now)
	writeFileWithMtime(t, resultPath(root, cur, "fc"+repeat("0", 62)), 50, now)
	writeFileWithMtime(t, resultPath(root, cur-1, "fd"+repeat("0", 62)), 70, now)
	writeFileWithMtime(t, artifactPath(root, "los", "fe"+repeat("0", 62)), 30, now)
	writeFileWithMtime(t, gameIndexPath(root, 7), 5, now)

	rep := Stats(root, cur)
	if rep.Tier1Count != 2 || rep.Tier1Bytes != 300 {
		t.Errorf("tier1 = %d files / %d bytes; want 2 / 300", rep.Tier1Count, rep.Tier1Bytes)
	}
	if rep.Tier2Count != 2 || rep.Tier2Bytes != 120 {
		t.Errorf("tier2 = %d files / %d bytes; want 2 / 120", rep.Tier2Count, rep.Tier2Bytes)
	}
	if rep.Tier3Count != 1 || rep.Tier3Bytes != 30 {
		t.Errorf("tier3 = %d files / %d bytes; want 1 / 30", rep.Tier3Count, rep.Tier3Bytes)
	}
	if rep.IndexCount != 1 || rep.IndexBytes != 5 {
		t.Errorf("index = %d files / %d bytes; want 1 / 5", rep.IndexCount, rep.IndexBytes)
	}
	if rep.CurrentVersion != resultsVersionName(cur) {
		t.Errorf("CurrentVersion = %q; want %q", rep.CurrentVersion, resultsVersionName(cur))
	}
	if got := rep.VersionTrees[resultsVersionName(cur)]; got != 50 {
		t.Errorf("current tree bytes = %d; want 50", got)
	}
	if got := rep.VersionTrees[resultsVersionName(cur-1)]; got != 70 {
		t.Errorf("orphan tree bytes = %d; want 70", got)
	}
}

// repeat is strings.Repeat without importing strings just for the SHA
// filler; SHA path segments only need 64 hex chars, any hex works.
func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
