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
// Tier 3 holds the lazily-materialised los artifact so its ~2.5s raycast
// survives a process restart or an LRU eviction: after the base Result is
// served from tier 2, EnsureLOS splices the artifact from tier 3 instead of
// recomputing it (PLAN-api F8b). Like tier 2 it lives under a version-keyed
// path (the effective version EV = CurrentSchemaVersion), so a stale version
// is simply never read.
//
// All three tiers are bounded by an optional byte budget (Cache.MaxBytes):
// a background sweep (gc.go) evicts oldest-mtime-first, with mtime bumped on
// every hit as the recency signal. Startup cleanup removes version trees and
// artifact gobs orphaned by schema/format bumps (CleanupOnStartup).
//
// The spatial weapon-fire streams (projectiles/beams/nails) used to be a
// second lazy artifact ("shot-streams") behind a full re-parse, but phase 12
// folded them into the always-full tier-2 parse (defaultParse sets
// BuildShotStreams+BuildNails): they cost only a few percent of parse time and
// cache size, and mvd-api has served the enriched /shots and /aim on every
// request since phase 5.3, so serving them from the base parse changes no
// response body while deleting the whole lazy machinery.
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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
}

// ParseFunc parses MVD bytes (gzip or plain) into a Result.
// Injectable so tests don't need a real demo on disk.
type ParseFunc func(ctx context.Context, mvdBytes []byte, filename string) (*result.Result, error)

// BuildLOSFunc materialises the los artifact onto res in place. Injectable
// (mirroring ParseFunc) so a test can prove the raycast goes through the
// parse-semaphore acquire without a real BSP. Default: art.Build.
type BuildLOSFunc func(art *analyzer.LazyArtifact, res *result.Result) error

func defaultBuildLOS(art *analyzer.LazyArtifact, res *result.Result) error {
	return art.Build(res, analyzer.MaterializeDeps{})
}

// defaultParse runs the standard analyzer pipeline with the spatial
// weapon-fire streams and nails built (BuildShotStreams+BuildNails). Since
// phase 12 the mvd-api cache is always-full: the +3–4% parse cost and ~+5%
// cache size buy an enriched Result on every request, which is what /shots,
// /aim and /streams/* have effectively served since phase 5.3 — so baking the
// streams into tier 2 deletes the lazy re-parse machinery without changing any
// response body. The CLI/WASM registries are configured by their own callers;
// only this API parse turns both flags on.
func defaultParse(_ context.Context, mvdBytes []byte, filename string) (*result.Result, error) {
	registry := analyzer.NewDefaultRegistry()
	registry.BuildShotStreams = true
	registry.BuildNails = true
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
	BuildLOS  BuildLOSFunc // nil → defaultBuildLOS (art.Build)

	// MaxBytes is the tier-1 + tier-2 + tier-3 disk budget. When exceeded, a
	// background sweep (maybeGC) evicts oldest-mtime-first down to it.
	// <= 0 disables eviction (local dev default via New; serve.go sets it).
	MaxBytes int64
	// MaxParses bounds concurrent cold download+parse operations. <= 0 →
	// max(1, NumCPU/2), resolved in ensureInit.
	MaxParses int
	// Logger receives GC lines; nil → slog.Default().
	Logger *slog.Logger

	once         sync.Once
	mem          *resultLRU
	inflight     sync.Map // sha → *inflightEntry
	lastResolved sync.Map // sha → *hubfetch.GameInfo (drained by loadResult)

	// parseSem is a counting semaphore (buffered channel) bounding the
	// number of concurrent heavy cold operations — a hub-download+full-parse
	// (loadResult) or an on-demand LOS raycast (EnsureLOS). The per-SHA
	// singleflight / losLocks already dedupe requests for the *same* demo;
	// this caps a storm of *distinct* cold demos from spawning unbounded
	// parallel heavy work. Cache hits never touch it.
	parseSem chan struct{}

	// gcRunning guards maybeGC so at most one background sweep runs at a
	// time; concurrent triggers after tier writes are dropped, not queued.
	gcRunning atomic.Bool

	// losLocks serializes the on-demand LOS materialisation per SHA, so a
	// raycast for demo B does not queue behind demo A's, and two callers for
	// one demo cannot both compute and race the splice onto the shared Result.
	// The need-check sits inside the lock (no TOCTOU).
	losLocks KeyedMutex
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
		if c.BuildLOS == nil {
			c.BuildLOS = defaultBuildLOS
		}
		if c.MemoryLRU <= 0 {
			c.MemoryLRU = 4
		}
		if c.MaxParses <= 0 {
			c.MaxParses = runtime.NumCPU() / 2
			if c.MaxParses < 1 {
				c.MaxParses = 1
			}
		}
		c.parseSem = make(chan struct{}, c.MaxParses)
		c.mem = newResultLRU(c.MemoryLRU)
	})
}

// log returns the configured logger or the process default.
func (c *Cache) log() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// touch bumps a cache file's mtime so the GC's oldest-first eviction treats
// it as recently used. atime is deliberately not used for LRU: most
// filesystems mount relatime/noatime, so read access does not reliably
// advance atime — an explicit mtime bump on every hit is the portable
// signal. Errors (a concurrently-evicted file) are ignored.
func touch(path string) {
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

// acquireParse blocks until a heavy-operation slot is free, honouring ctx
// while queued. Cancellation is respected only *while waiting* for the slot —
// once acquired, the parse/raycast itself runs to completion (the
// singleflight / losLock shares one computation across every same-SHA
// waiter). Distinct demos have distinct SHAs, so a cancel here only affects
// the cancelling client's own fan-out, never an unrelated demo.
func (c *Cache) acquireParse(ctx context.Context) (func(), error) {
	select {
	case c.parseSem <- struct{}{}:
		return func() { <-c.parseSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// maybeGC launches a background sweep after a tier write, guarded so only one
// sweep runs at a time; it never blocks the request path. It runs even when
// eviction is disabled (MaxBytes <= 0): SweepToBudget still reaps stale
// atomic-write temp files in that mode, so a crashed writer's leftovers are
// cleaned online, not only at the next startup.
func (c *Cache) maybeGC() {
	if !c.gcRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer c.gcRunning.Store(false)
		SweepToBudget(c.Root, c.MaxBytes, c.log())
	}()
}

// CleanupOnStartup removes tier-2 trees orphaned by past schema/format
// bumps, deletes stale-version tier-3 artifact gobs, clears stale
// atomic-write temp files, and enforces the byte budget once. Call before
// serving.
func (c *Cache) CleanupOnStartup() {
	c.ensureInit()
	CleanOldVersionTrees(c.Root, result.CurrentSchemaVersion, false, c.log())
	CleanStaleArtifacts(c.Root, false, c.log())
	SweepToBudget(c.Root, c.MaxBytes, c.log())
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

	rp := resultPath(c.Root, result.CurrentSchemaVersion, sha)
	mp := mvdPath(c.Root, sha)

	if r := c.mem.get(sha); r != nil {
		// Keep both on-disk tiers hot so an actively-queried demo is not the
		// GC's eviction target even while it lives only in the mem LRU.
		touch(rp)
		touch(mp)
		meta.FromCache = true
		return r, meta, nil
	}

	if data, err := os.ReadFile(rp); err == nil {
		if r, decErr := decodeResult(data); decErr == nil {
			touch(rp)
			touch(mp)
			c.mem.put(sha, r)
			meta.FromCache = true
			return r, meta, nil
		}
	}

	// Past the mem + tier-2 hits: everything below either reparses cached
	// tier-1 bytes or downloads then parses — the expensive cold path. Bound
	// its concurrency (F15); cache hits above never reach here.
	release, err := c.acquireParse(ctx)
	if err != nil {
		return nil, CacheMeta{}, err
	}
	defer release()

	var mvdBytes []byte
	if data, err := os.ReadFile(mp); err == nil {
		touch(mp)
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
		c.maybeGC()
	}

	filename := fmt.Sprintf("%s.mvd.gz", sha)
	r, err := c.Parse(ctx, mvdBytes, filename)
	if err != nil {
		return nil, CacheMeta{}, fmt.Errorf("parse: %w", err)
	}
	if data, err := encodeResult(r); err == nil {
		if writeErr := writeFileAtomic(rp, data, 0o644); writeErr == nil {
			c.maybeGC()
		}
	}
	c.mem.put(sha, r)
	return r, meta, nil
}

// EnsureLOS returns the demo's Result with the per-player line-of-sight / PVS
// interval sets materialised (Streams.Players[].LOS/PVS). LOS is the heaviest
// position-derived pass and has no in-pipeline consumer, so it is computed on
// demand: latch check → tier-3 load+splice → else compute (analyzer.ComputeLOS,
// which loads its own visibility BSP) → write tier-3. It needs no raw bytes, so
// there is no degrade — a map with no provisioned BSP computes to an empty LOS,
// latches, and is cached as such (computed once).
//
// Serialised per SHA (losLocks) with the need-check inside the lock, so /los
// for demo B does not queue behind demo A's raycast and two callers for one
// demo cannot race the splice onto the shared Result. The raycast itself
// (the ~2.5s heavy compute) is additionally bounded across distinct demos by
// the same parse semaphore as the cold parse — see the acquire below.
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
		// Keep the on-disk artifact hot for the GC even when the latch on the
		// in-memory Result short-circuits the disk read.
		touch(artifactPath(c.Root, art.Name(), sha))
		return res, meta, nil
	}
	if c.tier3Load(sha, art, res) {
		return res, meta, nil
	}

	// The raycast (~2.5s + BSP load) is per-SHA-serialised by losLocks but
	// otherwise unbounded across distinct demos — an unauthenticated
	// CPU-exhaustion path (N cold /los = N parallel raycasts), which is what
	// F15's semaphore exists to close. Bound it with the SAME parse semaphore
	// as the cold parse: one "max concurrent heavy cold operations" knob.
	//
	// Deadlock-free: by here EnsureLOS's own GetResult has fully released
	// parseSem (loadResult acquires and releases it around the parse, never
	// holding it across the return), so no goroutine holds parseSem while
	// taking a losLock. losLocks are per-SHA, so two raycast holders never
	// contend on the same losLock; and a cold parse waits on parseSem but
	// never on any losLock. No wait-cycle can form.
	//
	// Ordering matters: the losLock and the need-checks above run WITHOUT a
	// semaphore slot, so same-SHA callers dedupe on the losLock cheaply and
	// never occupy a slot while blocked. The semaphore is acquired only around
	// Build, so it bounds exactly the genuine distinct-demo raycasts.
	release, err := c.acquireParse(ctx)
	if err != nil {
		return nil, meta, err
	}
	defer release()

	if err := c.BuildLOS(art, res); err != nil {
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
	ap := artifactPath(c.Root, art.Name(), sha)
	data, err := os.ReadFile(ap)
	if err != nil {
		return false
	}
	if err := art.DecodeTier3(res, data); err != nil {
		slog.Warn("tier-3 artifact discarded", "artifact", art.Name(), "sha", sha, "err", err)
		return false
	}
	touch(ap)
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
		return
	}
	c.maybeGC()
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
