// Package democache is a three-tier disk cache for QuakeWorld demos.
//
//	tier 1: raw MVD bytes (gzip), keyed by SHA-256          → mvd/<sha[:2]>/<sha>.mvd.gz
//	tier 2: parsed *result.Result (gob), keyed by SHA + ver → results/v<N>/<sha[:2]>/<sha>.gob
//	tier 3: lazy artifact side-gobs, keyed by SHA + EV       → artifacts/<sha[:2]>/<sha>/<name>@v<EV>.gob
//
// A schema bump invalidates tier 2 only — tier 1 survives, so the next
// access reparses from the cached bytes without re-fetching from
// hub.quakeworld.nu. An in-process LRU (default size 4) sits in front
// of tier 2 to absorb the gob-decode cost during a session of related
// queries.
//
// Tier 3 holds the lazily-materialised artifacts (los, shot-streams) so a
// lazy compute — the ~10s LOS raycast or the full MVD re-parse — survives a
// process restart or an LRU eviction: after the base Result is served from
// tier 2, EnsureLOS / EnsureShotStreams splice the artifact from tier 3
// instead of recomputing it (PLAN-api F8b). Like tier 2 it lives under a
// version-keyed path (the effective version EV = CurrentSchemaVersion), so a
// stale version is simply never read; there is no GC (a hosting-prep phase).
//
// The cache is consumed by mvd-api (the REST host); it is not part of
// the public mvd-analytics API.
package democache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/hubfetch"
	"github.com/mvd-analyzer/mvd-analytics/result"
)

// Sentinel errors. Use errors.Is to detect.
var (
	ErrInvalidDemoID = errors.New("invalid demo id")
	ErrDemoNotFound  = errors.New("demo not found")
	ErrHubUpstream   = errors.New("hub upstream error")
)

// DemoID identifies a demo. Exactly one of GameID (with Kind="gameId")
// or SHA (with Kind="sha256") must be set.
type DemoID struct {
	Kind   string // "gameId" or "sha256"
	GameID int
	SHA    string
}

// CacheMeta reports which tier served a GetResult call.
type CacheMeta struct {
	SHA256        string
	FromCache     bool // true when neither the parser nor the hub was invoked
	FromMVDTier   bool // true when MVD bytes were on disk but the parser ran
	SchemaVersion int
	// ShotStreamsUnavailable is set by EnsureShotStreams when the opt-in
	// weapon-fire streams could not be built because the tier-1 MVD bytes
	// were missing (evicted after the base Result was cached). The returned
	// Result is the lean one — its stream-derived parts (rl/gl splash, the
	// LG whiff split, projectile/beam/nail streams) are absent. Surfaced so
	// /shots, /aim and /streams/* can signal the degrade rather than serve
	// silently incomplete data.
	ShotStreamsUnavailable bool
}

// ParseFunc parses MVD bytes (gzip or plain) into a Result.
// Injectable so tests don't need a real demo on disk.
type ParseFunc func(ctx context.Context, mvdBytes []byte, filename string) (*result.Result, error)

// defaultParse runs the standard analyzer pipeline.
func defaultParse(_ context.Context, mvdBytes []byte, filename string) (*result.Result, error) {
	registry := analyzer.NewDefaultRegistry()
	return registry.AnalyzeReader(bytes.NewReader(mvdBytes), filename)
}

// Cache is a two-tier on-disk cache for demos. Safe for concurrent
// use; per-SHA singleflight guarantees a cold demo is parsed at most
// once even under fan-out fetch.
type Cache struct {
	Root      string
	Hub       *hubfetch.Client
	MemoryLRU int
	Parse     ParseFunc

	once         sync.Once
	mem          *resultLRU
	inflight     sync.Map // sha → *inflightEntry
	lastResolved sync.Map // sha → *hubfetch.GameInfo (drained by loadResult)

	// shotLocks / losLocks serialize the on-demand lazy-artifact
	// materialisation per SHA, so a build for demo B does not queue behind
	// demo A's multi-second work (the shot-stream re-parse; the LOS raycast),
	// and two callers for one demo cannot both compute and race the splice
	// onto the shared Result. The need-check sits inside the lock (no TOCTOU).
	shotLocks KeyedMutex
	losLocks  KeyedMutex
}

// New constructs a Cache rooted at the given directory.
func New(root string, hub *hubfetch.Client) *Cache {
	if hub == nil {
		hub = hubfetch.NewClient()
	}
	return &Cache{Root: root, Hub: hub, MemoryLRU: 4, Parse: defaultParse}
}

func (c *Cache) ensureInit() {
	c.once.Do(func() {
		if c.Parse == nil {
			c.Parse = defaultParse
		}
		if c.MemoryLRU <= 0 {
			c.MemoryLRU = 4
		}
		c.mem = newResultLRU(c.MemoryLRU)
	})
}

// GetResult resolves the demo, fetches/parses/caches as needed, and
// returns a read-only *Result along with cache metadata.
//
// The ctx is accepted for call-site symmetry (and to satisfy the
// demoStore interface the API handlers use), but a cold GetResult runs
// its hub download and parse *to completion even if ctx is cancelled*:
// the per-SHA singleflight shares one computation across every waiter, so
// honoring the first caller's cancellation would poison all the others.
// Cancellation is therefore deliberately not threaded into the hub client
// or ParseFunc; ctx is passed through unused (see defaultParse).
func (c *Cache) GetResult(ctx context.Context, id DemoID) (*result.Result, CacheMeta, error) {
	c.ensureInit()

	// resolveSHA for a cold gameId hits the hub once. It runs outside the
	// SHA-keyed singleflight (the SHA is what keys it — chicken/egg), so N
	// concurrent cold requests for the same new gameId issue N parallel
	// Hub.Resolve calls before any of them has a SHA to dedupe on. We accept
	// that stampede: a resolve is a single cheap Supabase GET, the result is
	// persisted to the gameId index on first success, and only the
	// download+parse — the expensive part — needs deduping, which the
	// singleflight below does. (F9: revisit with a gameId-keyed singleflight
	// only if resolve cost ever shows up.)
	sha, err := c.resolveSHA(id)
	if err != nil {
		return nil, CacheMeta{}, err
	}

	return c.getOrCompute(sha, func() (*result.Result, CacheMeta, error) {
		return c.loadResult(ctx, sha, id)
	})
}

// resolveSHA converts a DemoID to a canonical lowercased SHA-256 hex.
// For Kind="gameId" it consults the on-disk index first, falling back
// to hubfetch.Resolve and persisting the index entry on success. The
// resolved GameInfo is stashed on lastResolved so loadResult can
// download without re-resolving.
func (c *Cache) resolveSHA(id DemoID) (string, error) {
	switch id.Kind {
	case "sha256":
		if !isValidSHA(id.SHA) {
			return "", fmt.Errorf("%w: sha must be 64 hex chars", ErrInvalidDemoID)
		}
		return strings.ToLower(id.SHA), nil

	case "gameId":
		if id.GameID <= 0 {
			return "", fmt.Errorf("%w: gameId must be positive", ErrInvalidDemoID)
		}
		if data, err := os.ReadFile(gameIndexPath(c.Root, id.GameID)); err == nil {
			sha := strings.ToLower(strings.TrimSpace(string(data)))
			if isValidSHA(sha) {
				return sha, nil
			}
		}
		info, err := c.Hub.Resolve(id.GameID)
		if err != nil {
			return "", classifyHubError(err, id.GameID)
		}
		if !isValidSHA(info.DemoSHA256) {
			return "", fmt.Errorf("%w: hub returned invalid sha for gameId %d", ErrHubUpstream, id.GameID)
		}
		sha := strings.ToLower(info.DemoSHA256)
		_ = writeFileAtomic(gameIndexPath(c.Root, id.GameID), []byte(sha+"\n"), 0o644)
		c.lastResolved.Store(sha, info)
		return sha, nil

	default:
		return "", fmt.Errorf("%w: unknown kind %q", ErrInvalidDemoID, id.Kind)
	}
}

// loadResult walks the cache tiers. Runs once per SHA via singleflight.
func (c *Cache) loadResult(ctx context.Context, sha string, id DemoID) (*result.Result, CacheMeta, error) {
	meta := CacheMeta{SHA256: sha, SchemaVersion: result.CurrentSchemaVersion}

	// Drain any GameInfo stashed by resolveSHA unconditionally, regardless
	// of which tier ends up serving this call. Only the download path below
	// consumes it; a mem/tier-2/tier-1 hit never downloads, so without this
	// the entry would leak in lastResolved for the life of the process
	// (F9).
	var stashed *hubfetch.GameInfo
	if v, ok := c.lastResolved.LoadAndDelete(sha); ok {
		stashed = v.(*hubfetch.GameInfo)
	}

	if r := c.mem.get(sha); r != nil {
		meta.FromCache = true
		return r, meta, nil
	}

	rp := resultPath(c.Root, result.CurrentSchemaVersion, sha)
	if data, err := os.ReadFile(rp); err == nil {
		if r, decErr := decodeResult(data); decErr == nil {
			c.mem.put(sha, r)
			meta.FromCache = true
			return r, meta, nil
		}
	}

	mp := mvdPath(c.Root, sha)
	var mvdBytes []byte
	if data, err := os.ReadFile(mp); err == nil {
		mvdBytes = data
		meta.FromMVDTier = true
	} else {
		info, err := c.resolveDownloadInfo(id, stashed)
		if err != nil {
			return nil, CacheMeta{}, err
		}
		data, err := c.Hub.Download(info)
		if err != nil {
			return nil, CacheMeta{}, fmt.Errorf("%w: %v", ErrHubUpstream, err)
		}
		// Verify the downloaded bytes hash to the SHA that keys everything:
		// the tier-1/tier-2 path, the sha: public address, and the ETag. A
		// corrupted CDN object or a wrong demo_source_url would otherwise
		// poison the cache permanently under this sha and make every
		// "immutable" promise built on it false. Reject and do not cache on
		// mismatch. (One hash per cold download — negligible.)
		if got := sha256Hex(data); got != sha {
			return nil, CacheMeta{}, fmt.Errorf("%w: downloaded bytes hash to %s, expected %s", ErrHubUpstream, got, sha)
		}
		if err := writeFileAtomic(mp, data, 0o644); err != nil {
			return nil, CacheMeta{}, fmt.Errorf("write tier-1: %w", err)
		}
		mvdBytes = data
	}

	filename := fmt.Sprintf("%s.mvd.gz", sha)
	r, err := c.Parse(ctx, mvdBytes, filename)
	if err != nil {
		return nil, CacheMeta{}, fmt.Errorf("parse: %w", err)
	}
	if data, err := encodeResult(r); err == nil {
		_ = writeFileAtomic(rp, data, 0o644)
	}
	c.mem.put(sha, r)
	return r, meta, nil
}

// EnsureShotStreams returns the demo's Result with the opt-in spatial
// weapon-fire streams built: rocket/grenade flights, LG beams AND nail
// flights, in one rebuild. These are off in the default parse to keep the
// cache lean and — unlike LOS — cannot be recomputed from the cached
// Result, so the first request re-parses the cached MVD bytes with the
// build flags on and splices the streams onto the in-memory Result. The
// rebuilt Shots and Aim blocks ride along: their stream-derived parts
// (RL/GL direct/splash, the LG whiff split, ng/sng linking + accuracy)
// only exist in the enriched parse, so /shots and /aim serve complete
// data. The ShotStreamsComputed / NailsComputed latches make repeat
// requests free within a process; the tier-3 artifact cache (below) makes
// them free across restarts and LRU evictions too — a warm process splices
// the streams from disk instead of re-parsing (PLAN-api F8b).
//
// There is deliberately only ONE variant (F12): nails used to be a
// separate opt-in latch, which made /shots and /aim bodies depend on
// whether any earlier client had asked for nails — same URL, same strong
// ETag, different body before/after the latch (and again after an LRU
// eviction reverted it). Folding nails into the base rebuild makes every
// response a pure function of the URL, which the immutable cache headers
// require. The extra nail decode is a one-time per-demo cost on a path
// that already re-parses the whole MVD, and the spliced nail stream keeps
// only per-flight endpoints.
//
// This serializes concurrent calls for one demo internally via a per-SHA
// lock (shotLocks): a rebuild for demo B does not queue behind demo A's,
// and two callers for the same demo cannot both re-parse and race the
// splice onto the shared Result. A demo with no Streams (no player tracks)
// is returned unchanged.
func (c *Cache) EnsureShotStreams(ctx context.Context, id DemoID) (*result.Result, CacheMeta, error) {
	res, meta, err := c.GetResult(ctx, id)
	if err != nil {
		return nil, meta, err
	}
	if res.Streams == nil {
		return res, meta, nil
	}

	// meta.SHA256 is the resolved cache key from GetResult; use it to lock
	// the rebuild per-demo. The lock covers the need-check so the re-parse
	// decision is not a TOCTOU race between two callers (F8).
	sha := meta.SHA256
	unlock := c.shotLocks.Lock(sha)
	defer unlock()

	art := mustLazyArtifact("shot-streams")
	if art.Computed(res) {
		return res, meta, nil
	}
	// Tier-3 warm path: splice the artifact from disk without a re-parse. A
	// hit here works even after the tier-1 bytes were evicted — that is the
	// F8b win (no re-parse, no degrade). A corrupt/partial gob is treated as a
	// miss (tier3Load slog-warns and returns false).
	if c.tier3Load(sha, art, res) {
		return res, meta, nil
	}

	mvdBytes, err := os.ReadFile(mvdPath(c.Root, sha))
	if err != nil {
		// The tier-1 bytes are gone (evicted after the base Result was cached)
		// and there is no tier-3 artifact, so the opt-in streams cannot be
		// rebuilt. Surface the lean Result rather than failing the request, but
		// flag the degrade so the handler signals it instead of serving
		// silently-incomplete data (the "surface authoritative data" rule).
		meta.ShotStreamsUnavailable = true
		return res, meta, nil
	}

	// One rebuild builds everything (the single F12 variant): the re-parse
	// runs BuildShotStreams+BuildNails, and the artifact's Build grafts the
	// streams plus the rebuilt Shots/Aim onto res and latches.
	deps := analyzer.MaterializeDeps{Reparse: func() (*result.Result, error) {
		reg := analyzer.NewDefaultRegistry()
		reg.BuildShotStreams = true
		reg.BuildNails = true
		return reg.AnalyzeReader(bytes.NewReader(mvdBytes), fmt.Sprintf("%s.mvd.gz", sha))
	}}
	if err := art.Build(res, deps); err != nil {
		return nil, meta, fmt.Errorf("rebuild shot streams: %w", err)
	}
	c.tier3Store(sha, art, res)
	return res, meta, nil
}

// EnsureLOS returns the demo's Result with the per-player line-of-sight / PVS
// interval sets materialised (Streams.Players[].LOS/PVS). LOS is the heaviest
// position-derived pass and has no in-pipeline consumer, so it is computed on
// demand: latch check → tier-3 load+splice → else compute (analyzer.ComputeLOS,
// which loads its own visibility BSP) → write tier-3. Unlike shot-streams it
// needs no raw bytes, so there is no degrade — a map with no provisioned BSP
// computes to an empty LOS, latches, and is cached as such (computed once).
//
// Serialised per SHA (losLocks) with the need-check inside the lock, so /los
// for demo B does not queue behind demo A's raycast and two callers for one
// demo cannot race the splice onto the shared Result.
func (c *Cache) EnsureLOS(ctx context.Context, id DemoID) (*result.Result, CacheMeta, error) {
	res, meta, err := c.GetResult(ctx, id)
	if err != nil {
		return nil, meta, err
	}
	if res.Streams == nil {
		return res, meta, nil
	}

	sha := meta.SHA256
	unlock := c.losLocks.Lock(sha)
	defer unlock()

	art := mustLazyArtifact("los")
	if art.Computed(res) {
		return res, meta, nil
	}
	if c.tier3Load(sha, art, res) {
		return res, meta, nil
	}
	if err := art.Build(res, analyzer.MaterializeDeps{}); err != nil {
		return nil, meta, fmt.Errorf("compute los: %w", err)
	}
	c.tier3Store(sha, art, res)
	return res, meta, nil
}

// mustLazyArtifact resolves a lazy artifact by name, panicking on an unknown
// name — the names are compile-time constants in this package, so a miss is a
// programmer error, not a runtime condition.
func mustLazyArtifact(name string) *analyzer.LazyArtifact {
	art, ok := analyzer.LazyArtifactByName(name)
	if !ok {
		panic("democache: unknown lazy artifact " + name)
	}
	return art
}

// tier3Load splices the named artifact from disk onto res and reports whether
// it did (the latch is now set). A read miss is a plain false; a decode/splice
// failure (corrupt or drifted gob) is slog-warned and also treated as a miss,
// so the caller recomputes rather than serving mismatched data.
func (c *Cache) tier3Load(sha string, art *analyzer.LazyArtifact, res *result.Result) bool {
	data, err := os.ReadFile(artifactPath(c.Root, art.Name(), sha))
	if err != nil {
		return false
	}
	if err := art.DecodeTier3(res, data); err != nil {
		slog.Warn("tier-3 artifact discarded", "artifact", art.Name(), "sha", sha, "err", err)
		return false
	}
	return true
}

// tier3Store writes the freshly-built artifact to disk (atomic write). Write
// failures are non-fatal: the artifact stays on the in-memory Result for this
// process, and the next process recomputes. Nothing to persist (ok=false) is a
// silent no-op.
func (c *Cache) tier3Store(sha string, art *analyzer.LazyArtifact, res *result.Result) {
	data, ok, err := art.EncodeTier3(res)
	if err != nil {
		slog.Warn("tier-3 artifact encode failed", "artifact", art.Name(), "sha", sha, "err", err)
		return
	}
	if !ok {
		return
	}
	if err := writeFileAtomic(artifactPath(c.Root, art.Name(), sha), data, 0o644); err != nil {
		slog.Warn("tier-3 artifact write failed", "artifact", art.Name(), "sha", sha, "err", err)
	}
}

func (c *Cache) resolveDownloadInfo(id DemoID, stashed *hubfetch.GameInfo) (*hubfetch.GameInfo, error) {
	if stashed != nil {
		return stashed, nil
	}
	if id.Kind == "sha256" {
		return nil, fmt.Errorf("%w: sha not in local cache and no gameId to resolve source", ErrDemoNotFound)
	}
	info, err := c.Hub.Resolve(id.GameID)
	if err != nil {
		return nil, classifyHubError(err, id.GameID)
	}
	return info, nil
}

// classifyHubError maps a hubfetch.Resolve error to the democache error
// taxonomy by identity, not by message substring: a genuine "no such
// game" (hubfetch.ErrNotFound) becomes ErrDemoNotFound (404); anything
// else — network failure, 5xx, decode error, even a 5xx whose body text
// happens to contain "not found" — becomes ErrHubUpstream (502) (F2).
func classifyHubError(err error, gameID int) error {
	if errors.Is(err, hubfetch.ErrNotFound) {
		return fmt.Errorf("%w: gameId %d", ErrDemoNotFound, gameID)
	}
	return fmt.Errorf("%w: %v", ErrHubUpstream, err)
}
