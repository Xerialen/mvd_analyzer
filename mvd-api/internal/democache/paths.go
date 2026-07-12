package democache

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// DefaultRoot returns the conventional on-disk cache root.
// Honors XDG_CACHE_HOME; falls back to ~/.cache/qw-mvd.
func DefaultRoot() string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "qw-mvd")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "qw-mvd")
	}
	return filepath.Join(home, ".cache", "qw-mvd")
}

// mvdRoot is the tier-1 subtree (raw MVD gz bytes).
func mvdRoot(root string) string { return filepath.Join(root, "mvd") }

// resultsRoot is the tier-2 subtree; it holds one version tree per
// schema/format generation ever written under this cache.
func resultsRoot(root string) string { return filepath.Join(root, "results") }

// artifactsRoot is the tier-3 subtree (lazy artifact side-gobs).
func artifactsRoot(root string) string { return filepath.Join(root, "artifacts") }

// indexRoot is the small gameId → sha map subtree. It is exempt from GC
// eviction (tiny, and losing it would force a hub re-resolve).
func indexRoot(root string) string { return filepath.Join(root, "index") }

// mvdPath returns the on-disk path for tier-1 (raw MVD gz bytes).
//
//	<root>/mvd/<sha[:2]>/<sha>.mvd.gz
func mvdPath(root, sha string) string {
	return filepath.Join(mvdRoot(root), sha[:2], sha+".mvd.gz")
}

// resultCacheFormat is the tier-2 gob layout generation, an internal counter
// independent of the wire schema version. It is folded into the tier-2 path so
// a change to WHAT the cache stores (not the JSON wire shape) invalidates old
// gobs without a schema bump.
//
// Format 1 was the pre-phase-12 lean layout (the suffix-less `results/v<N>/…`
// path, whose cached Result had the spatial shot streams and nails absent).
// Phase 12 made the mvd-api parse always-full — projectile/beam/nail streams
// and the enriched shots/aim are baked into tier 2 — so a lean format-1 gob
// must never be served as if it were full. Bumping to 2 moves the tier-2 path
// (`results/v<N>f2/…`), so old lean gobs are simply never read and get
// re-parsed on next touch. The wire schema (v49) is unchanged: served bodies
// are byte-identical (mvd-api has served the enriched /shots and /aim on every
// request since phase 5.3), so this is a cache-locality bump, not a schema one.
//
// Bump this whenever the cached Result's populated-ness changes under a fixed
// schema version (the "which optional passes are baked in" contract).
const resultCacheFormat = 2

// resultsVersionName is the directory name of the tier-2 tree for a schema
// version + the current cache-format generation — the single source of truth
// for the "v<N>f<F>" naming that both resultPath and the orphan-tree GC sweep
// (CleanOldVersionTrees) depend on.
func resultsVersionName(schemaVersion int) string {
	return fmt.Sprintf("v%df%d", schemaVersion, resultCacheFormat)
}

// resultPath returns the on-disk path for tier-2 (parsed *Result gob),
// keyed by schema version AND the cache-format generation so both a schema
// bump and a cache-format bump invalidate this tier without touching tier-1.
//
//	<root>/results/v<N>f<F>/<sha[:2]>/<sha>.gob
func resultPath(root string, schemaVersion int, sha string) string {
	return filepath.Join(resultsRoot(root), resultsVersionName(schemaVersion), sha[:2], sha+".gob")
}

// gameIndexPath returns the on-disk path for the gameId → sha map.
//
//	<root>/index/games/<gameId>.txt
func gameIndexPath(root string, gameID int) string {
	return filepath.Join(indexRoot(root), "games", fmt.Sprintf("%d.txt", gameID))
}

// artifactVersionSuffix is the filename tail of a tier-3 gob at the current
// effective version ("@v<EV>.gob"). The GC's stale-artifact sweep removes any
// artifacts/ gob NOT carrying it: per-file versioning means a stale artifact
// is never read again (artifactPath simply points elsewhere), so it is pure
// garbage — this includes the shot-streams@* gobs orphaned by phase 12.
func artifactVersionSuffix() string {
	return fmt.Sprintf("@v%d.gob", result.CurrentSchemaVersion)
}

// artifactPath returns the on-disk path for tier-3 (a lazily-materialised
// artifact side-gob — only "los" since phase 12 folded shot-streams into the
// always-full parse), keyed by the artifact's effective version so a stale
// version is simply never read.
//
//	<root>/artifacts/<sha[:2]>/<sha>/<name>@v<EV>.gob
//
// With one live artifact and no per-node Version field yet, the effective
// version EV is pragmatically result.CurrentSchemaVersion: the artifact is
// derived from the v-N parse, so a schema bump must invalidate it exactly as
// it invalidates tier 2. Per-node effective versions (hash of the node's
// Version + its Requires') arrive with Stage 4's manifest if node versions
// ever diverge from the schema (PLAN-improve-analytics.md §3.5).
//
// Orphaned shot-streams@* gobs written by pre-phase-12 processes are never
// read anymore (nothing resolves the "shot-streams" artifact); the GC's
// stale-artifact sweep (CleanStaleArtifacts) reaps them along with any other
// gob whose version suffix is not current.
func artifactPath(root, name, sha string) string {
	return filepath.Join(artifactsRoot(root), sha[:2], sha, name+artifactVersionSuffix())
}
