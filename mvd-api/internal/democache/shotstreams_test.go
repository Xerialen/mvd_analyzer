package democache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// corpusDemo returns a real demo from the analytics test cache, or skips when
// it is absent (the cache is gitignored — present after a golden run, offline
// otherwise).
func corpusDemo(t *testing.T) (sha string, bytes []byte) {
	t.Helper()
	const rel = "../../../mvd-analytics/testdata/cache/211161.mvd.gz"
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Skipf("corpus demo not present (%v); run the golden corpus first", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b
}

// TestEnsureShotStreams re-parses a real demo to build the opt-in spatial
// streams on demand in ONE variant — projectiles, beams and nails together
// (F12: a separate nails latch made /shots and /aim bodies depend on
// request history under an immutable ETag) — latches everything, and
// serves repeat requests from the same Result.
func TestEnsureShotStreams(t *testing.T) {
	sha, demo := corpusDemo(t)
	root := t.TempDir()
	mp := mvdPath(root, sha)
	if err := os.MkdirAll(filepath.Dir(mp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mp, demo, 0o644); err != nil {
		t.Fatal(err)
	}

	c := New(root, nil)
	id := DemoID{Kind: "sha256", SHA: sha}
	ctx := context.Background()

	// First request builds and latches everything in one rebuild.
	res, _, err := c.EnsureShotStreams(ctx, id)
	if err != nil {
		t.Fatalf("EnsureShotStreams: %v", err)
	}
	if res.Streams == nil || res.Streams.Projectiles == nil {
		t.Fatal("first request did not build the projectile stream")
	}
	if !res.Streams.ShotStreamsComputed {
		t.Error("ShotStreamsComputed not latched")
	}
	if !res.Streams.NailsComputed {
		t.Error("NailsComputed not latched — the one-variant rebuild must build nails too (F12)")
	}

	// The rebuilt Shots/Aim ride along, carrying the stream-derived blocks
	// the lean parse cannot compute: with projectiles linked every RL/GL
	// fire splits into direct+splash+missed == shots, and with beams every
	// missed LG fire is classified (blocked+miss+far+unresolved == misses).
	// In the lean parse those fields are all zero, so the sums cannot match.
	// (The corpus demo is schloss — no LG on the map — so RL/GL carries the
	// check and the LG branch is exercised only if the demo changes.)
	if res.Shots == nil || res.Aim == nil {
		t.Fatalf("Shots/Aim not grafted: shots=%v aim=%v", res.Shots != nil, res.Aim != nil)
	}
	streamDerived := false
	for _, pa := range res.Aim.Players {
		for _, wa := range pa.Weapons {
			if wa.Shots == 0 {
				continue
			}
			switch wa.Weapon {
			case "rl", "gl":
				streamDerived = true
				if got := wa.Direct + wa.Splash + wa.Missed; got != wa.Shots {
					t.Errorf("%s %s: direct+splash+missed = %d; want shots = %d",
						pa.Player, wa.Weapon, got, wa.Shots)
				}
			case "lg":
				streamDerived = true
				if got := wa.Blocked + wa.Miss + wa.OutOfRange + wa.Unresolved; got != wa.Shots-wa.Hits {
					t.Errorf("%s lg whiffs: blocked+miss+far+unresolved = %d; want shots-hits = %d",
						pa.Player, got, wa.Shots-wa.Hits)
				}
			}
		}
	}
	if !streamDerived {
		t.Error("no RL/GL/LG fires in corpus demo — stream-derived aim graft not exercised")
	}

	// Repeat request: same cached Result pointer, no rebuild.
	res2, _, err := c.EnsureShotStreams(ctx, id)
	if err != nil {
		t.Fatalf("EnsureShotStreams repeat: %v", err)
	}
	if res2 != res {
		t.Error("expected the cached Result pointer to be reused")
	}
}

// TestEnsureShotStreams_MissingTier1_FlagsUnavailable covers the quiet-degrade
// path: when the tier-1 MVD bytes are gone (evicted after the base Result was
// cached), EnsureShotStreams serves the lean Result and sets
// CacheMeta.ShotStreamsUnavailable so the handlers can signal the degrade
// (X-Shot-Streams: unavailable) instead of serving silently-incomplete data.
// The flag is per-call meta, never persisted, so it cannot stick once the
// bytes are back.
func TestEnsureShotStreams_MissingTier1_FlagsUnavailable(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()
	hub.addGame(42, testSHA, testMVD)

	c, root := newTestCache(t, hub.hubClient(), &stubParser{})
	// The stub parse must yield a Streams block (EnsureShotStreams returns
	// early on Streams == nil) with the latches unset, so the rebuild is
	// attempted and hits the missing tier-1 file.
	c.Parse = func(_ context.Context, _ []byte, filename string) (*result.Result, error) {
		return &result.Result{
			SchemaVersion: result.CurrentSchemaVersion,
			FilePath:      filename,
			Streams:       &result.Streams{},
		}, nil
	}
	ctx := context.Background()
	id := DemoID{Kind: "gameId", GameID: 42}

	// Cold fetch caches the Result in memory and the bytes at tier 1.
	if _, _, err := c.GetResult(ctx, id); err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	// Simulate tier-1 eviction.
	if err := os.Remove(mvdPath(root, testSHA)); err != nil {
		t.Fatalf("remove tier-1: %v", err)
	}

	res, meta, err := c.EnsureShotStreams(ctx, id)
	if err != nil {
		t.Fatalf("EnsureShotStreams: %v", err)
	}
	if !meta.ShotStreamsUnavailable {
		t.Error("meta.ShotStreamsUnavailable = false; want true when tier-1 bytes are gone")
	}
	if res.Streams.ShotStreamsComputed {
		t.Error("ShotStreamsComputed latched without a rebuild")
	}
}
