// Package democache is a two-tier disk cache for QuakeWorld demos.
//
//	tier 1: raw MVD bytes (gzip), keyed by SHA-256          → mvd/<sha[:2]>/<sha>.mvd.gz
//	tier 2: parsed *result.Result (gob), keyed by SHA + ver → results/v<N>/<sha[:2]>/<sha>.gob
//
// A schema bump invalidates tier 2 only — tier 1 survives, so the next
// access reparses from the cached bytes without re-fetching from
// hub.quakeworld.nu. An in-process LRU (default size 4) sits in front
// of tier 2 to absorb the gob-decode cost during a session of related
// queries.
//
// The cache is consumed by mvd-api (the REST host); it is not part of
// the public mvd-analytics API.
package democache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

	// shotLocks serializes the on-demand shot-stream rebuild per SHA, so a
	// rebuild for demo B does not queue behind demo A's multi-second
	// re-parse (EnsureShotStreams).
	shotLocks KeyedMutex
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
// weapon-fire streams built: rockets/grenades + LG beams, and (when nails is
// true) the nail-flight stream. These are off in the default parse to keep
// the cache lean and — unlike LOS — cannot be recomputed from the cached
// Result, so the first request re-parses the cached MVD bytes with the build
// flags on and splices the streams onto the in-memory Result. The rebuilt
// Shots and Aim blocks ride along: their stream-derived parts (RL/GL
// direct/splash, the LG whiff split, nail fires) only exist in the enriched
// parse, so /shots and /aim serve complete data. The ShotStreamsComputed /
// NailsComputed latches make repeat requests free.
//
// This serializes concurrent calls for one demo internally via a per-SHA
// lock (shotLocks): a rebuild for demo B does not queue behind demo A's,
// and two callers for the same demo cannot both re-parse and race the
// splice onto the shared Result. A demo with no Streams (no player tracks)
// is returned unchanged.
func (c *Cache) EnsureShotStreams(ctx context.Context, id DemoID, nails bool) (*result.Result, CacheMeta, error) {
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

	needBase := !res.Streams.ShotStreamsComputed
	needNails := nails && !res.Streams.NailsComputed
	if !needBase && !needNails {
		return res, meta, nil
	}

	mvdBytes, err := os.ReadFile(mvdPath(c.Root, sha))
	if err != nil {
		// The tier-1 bytes are gone (evicted after the base Result was
		// cached), so the opt-in streams cannot be rebuilt. Surface the lean
		// Result rather than failing the request, but flag the degrade so the
		// handler signals it instead of serving silently-incomplete data
		// (the "surface authoritative data" rule).
		meta.ShotStreamsUnavailable = true
		return res, meta, nil
	}

	reg := analyzer.NewDefaultRegistry()
	// Always rebuild with the base streams on: the grafted Shots/Aim blocks
	// below only carry their stream-derived parts when the pipeline saw the
	// streams. Nails stay opt-in, but once computed they are kept on so a
	// later base-only rebuild cannot drop them from the grafts.
	reg.BuildShotStreams = true
	reg.BuildNails = needNails || res.Streams.NailsComputed
	built, err := reg.AnalyzeReader(bytes.NewReader(mvdBytes), fmt.Sprintf("%s.mvd.gz", sha))
	if err != nil {
		return nil, meta, fmt.Errorf("rebuild shot streams: %w", err)
	}
	if built.Streams != nil {
		if needBase {
			res.Streams.Projectiles = built.Streams.Projectiles
			res.Streams.Beams = built.Streams.Beams
			res.Streams.ShotStreamsComputed = true
		}
		if needNails {
			res.Streams.Nails = built.Streams.Nails
			res.Streams.NailsComputed = true
		}
	}
	if built.Shots != nil {
		res.Shots = built.Shots
	}
	if built.Aim != nil {
		res.Aim = built.Aim
	}
	return res, meta, nil
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
