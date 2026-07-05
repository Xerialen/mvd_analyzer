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
// The cache is exclusively consumed by qwanalytics/cmd/qw-mvd; it does
// not appear on the public qwanalytics API.
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
func (c *Cache) GetResult(ctx context.Context, id DemoID) (*result.Result, CacheMeta, error) {
	c.ensureInit()

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
			if strings.Contains(err.Error(), "not found") {
				return "", fmt.Errorf("%w: gameId %d", ErrDemoNotFound, id.GameID)
			}
			return "", fmt.Errorf("%w: %v", ErrHubUpstream, err)
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
		info, err := c.resolveDownloadInfo(sha, id)
		if err != nil {
			return nil, CacheMeta{}, err
		}
		data, err := c.Hub.Download(info)
		if err != nil {
			return nil, CacheMeta{}, fmt.Errorf("%w: %v", ErrHubUpstream, err)
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
// Callers serialize concurrent calls for one demo (see the API's streamsMu),
// matching the ComputeLOS pattern. A demo with no Streams (no player tracks)
// is returned unchanged.
func (c *Cache) EnsureShotStreams(ctx context.Context, id DemoID, nails bool) (*result.Result, CacheMeta, error) {
	res, meta, err := c.GetResult(ctx, id)
	if err != nil {
		return nil, meta, err
	}
	if res.Streams == nil {
		return res, meta, nil
	}
	needBase := !res.Streams.ShotStreamsComputed
	needNails := nails && !res.Streams.NailsComputed
	if !needBase && !needNails {
		return res, meta, nil
	}

	sha, err := c.resolveSHA(id)
	if err != nil {
		return nil, meta, err
	}
	mvdBytes, err := os.ReadFile(mvdPath(c.Root, sha))
	if err != nil {
		// Bytes should be on disk after GetResult; if not, surface the lean
		// Result rather than failing the request.
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

func (c *Cache) resolveDownloadInfo(sha string, id DemoID) (*hubfetch.GameInfo, error) {
	if v, ok := c.lastResolved.LoadAndDelete(sha); ok {
		return v.(*hubfetch.GameInfo), nil
	}
	if id.Kind == "sha256" {
		return nil, fmt.Errorf("%w: sha not in local cache and no gameId to resolve source", ErrDemoNotFound)
	}
	info, err := c.Hub.Resolve(id.GameID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, fmt.Errorf("%w: gameId %d", ErrDemoNotFound, id.GameID)
		}
		return nil, fmt.Errorf("%w: %v", ErrHubUpstream, err)
	}
	return info, nil
}
