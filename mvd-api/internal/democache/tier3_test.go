package democache

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/result"
)

// streamsWithPlayers is a stub parse producing a Result with a Streams block
// carrying named players but no DemoInfo — enough for EnsureLOS to run (and
// compute to empty, since there is no BSP) without needing a real demo.
func streamsWithPlayers(_ context.Context, _ []byte, filename string) (*result.Result, error) {
	return &result.Result{
		SchemaVersion: result.CurrentSchemaVersion,
		FilePath:      filename,
		Streams: &result.Streams{
			Players: []result.PlayerStream{{Name: "A"}, {Name: "B"}},
		},
	}, nil
}

// --- los tier-3 ---

// TestEnsureLOS_Tier3_ColdComputeWritesArtifact: the first EnsureLOS computes
// the pass (empty here — no BSP) and persists it to tier 3, latching the base
// Result.
func TestEnsureLOS_Tier3_ColdComputeWritesArtifact(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()
	hub.addGame(42, testSHA, testMVD)

	c, root := newTestCache(t, hub.hubClient(), &stubParser{})
	c.Parse = streamsWithPlayers
	ctx := context.Background()
	id := DemoID{Kind: "gameId", GameID: 42}

	res, _, err := c.EnsureLOS(ctx, id)
	if err != nil {
		t.Fatalf("EnsureLOS: %v", err)
	}
	if !res.Streams.LOSComputed {
		t.Error("LOSComputed not latched after compute")
	}
	mustExist(t, artifactPath(root, "los", testSHA), "tier-3 los artifact")
}

// TestEnsureLOS_Tier3_WarmLoadSplices: a fresh process (new Cache instance,
// empty memory LRU) with a tier-3 los artifact on disk splices it onto the
// base Result WITHOUT recomputing — proven by a sentinel interval a fresh
// (BSP-less) compute could never produce.
func TestEnsureLOS_Tier3_WarmLoadSplices(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()
	hub.addGame(42, testSHA, testMVD)

	c, root := newTestCache(t, hub.hubClient(), &stubParser{})
	c.Parse = streamsWithPlayers
	ctx := context.Background()
	id := DemoID{Kind: "gameId", GameID: 42}

	// Warm the base Result (tier-1 + tier-2), then craft a distinctive tier-3
	// artifact by hand via the analyzer codec.
	base, _, err := c.GetResult(ctx, id)
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	sentinel := []result.LosTrack{{Other: 1, Iv: []result.Interval{{Start: 111, End: 222}}}}
	base.Streams.Players[0].LOS = sentinel
	base.Streams.LOSComputed = true
	art, _ := analyzer.LazyArtifactByName("los")
	data, ok, err := art.EncodeTier3(base)
	if err != nil || !ok {
		t.Fatalf("EncodeTier3: ok=%v err=%v", ok, err)
	}
	if err := writeFileAtomic(artifactPath(root, "los", testSHA), data, 0o644); err != nil {
		t.Fatalf("write tier-3: %v", err)
	}

	// Fresh Cache on the same root: no in-memory latch, no LRU. The base
	// Result comes from tier 2 (LOS empty, LOSComputed=false); EnsureLOS must
	// serve the sentinel from tier 3.
	c2 := New(root, hub.hubClient())
	c2.Parse = streamsWithPlayers
	res, _, err := c2.EnsureLOS(ctx, id)
	if err != nil {
		t.Fatalf("EnsureLOS warm: %v", err)
	}
	if !res.Streams.LOSComputed {
		t.Error("LOSComputed not latched after warm load")
	}
	if len(res.Streams.Players[0].LOS) != 1 || res.Streams.Players[0].LOS[0].Iv[0].Start != 111 {
		t.Errorf("warm load did not splice the tier-3 sentinel: %+v", res.Streams.Players[0].LOS)
	}
}

// TestEnsureLOS_Tier3_CorruptFallsBack: a garbage tier-3 gob is discarded (not
// spliced) and EnsureLOS recomputes, still succeeding.
func TestEnsureLOS_Tier3_CorruptFallsBack(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()
	hub.addGame(42, testSHA, testMVD)

	c, root := newTestCache(t, hub.hubClient(), &stubParser{})
	c.Parse = streamsWithPlayers
	ctx := context.Background()
	id := DemoID{Kind: "gameId", GameID: 42}

	if _, _, err := c.GetResult(ctx, id); err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if err := writeFileAtomic(artifactPath(root, "los", testSHA), []byte("not a gob"), 0o644); err != nil {
		t.Fatalf("write corrupt tier-3: %v", err)
	}

	c2 := New(root, hub.hubClient())
	c2.Parse = streamsWithPlayers
	res, _, err := c2.EnsureLOS(ctx, id)
	if err != nil {
		t.Fatalf("EnsureLOS with corrupt tier-3: %v", err)
	}
	if !res.Streams.LOSComputed {
		t.Error("expected recompute (latched) after discarding corrupt tier-3")
	}
	// The recompute (empty LOS) overwrites the corrupt file with a valid gob.
	if data, err := os.ReadFile(artifactPath(root, "los", testSHA)); err != nil {
		t.Errorf("tier-3 not rewritten: %v", err)
	} else if err := art0LOS().DecodeTier3(&result.Result{Streams: &result.Streams{Players: []result.PlayerStream{{Name: "A"}, {Name: "B"}}}}, data); err != nil {
		t.Errorf("rewritten tier-3 is not a valid los gob: %v", err)
	}
}

func art0LOS() *analyzer.LazyArtifact { a, _ := analyzer.LazyArtifactByName("los"); return a }

// --- shot-streams tier-3 ---

// TestEnsureShotStreams_Tier3_SurvivesTier1Eviction is the F8b win: once the
// shot-streams artifact is on tier 3, a fresh process serves it even after the
// tier-1 MVD bytes were evicted — where a rebuild would have hit the degrade
// path. Cold-vs-warm equivalence is asserted on the spliced blocks.
func TestEnsureShotStreams_Tier3_SurvivesTier1Eviction(t *testing.T) {
	sha, demo := corpusDemo(t)
	root := t.TempDir()
	if err := writeFileAtomic(mvdPath(root, sha), demo, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	id := DemoID{Kind: "sha256", SHA: sha}

	// Cold: compute + write tier 3.
	c1 := New(root, nil)
	cold, meta, err := c1.EnsureShotStreams(ctx, id)
	if err != nil {
		t.Fatalf("cold EnsureShotStreams: %v", err)
	}
	if meta.ShotStreamsUnavailable {
		t.Fatal("cold build should not be unavailable")
	}
	if cold.Streams == nil || cold.Streams.Projectiles == nil {
		t.Fatal("cold build produced no projectile stream")
	}
	mustExist(t, artifactPath(root, "shot-streams", sha), "tier-3 shot-streams artifact")
	coldJSON := shotStreamsJSON(t, cold)

	// Evict tier 1 and start a fresh process.
	if err := os.Remove(mvdPath(root, sha)); err != nil {
		t.Fatalf("evict tier-1: %v", err)
	}
	c2 := New(root, nil)
	warm, meta2, err := c2.EnsureShotStreams(ctx, id)
	if err != nil {
		t.Fatalf("warm EnsureShotStreams: %v", err)
	}
	if meta2.ShotStreamsUnavailable {
		t.Error("warm request degraded despite a tier-3 artifact (F8b regression)")
	}
	if warm.Streams == nil || warm.Streams.Projectiles == nil {
		t.Fatal("warm load produced no projectile stream")
	}
	if !warm.Streams.ShotStreamsComputed || !warm.Streams.NailsComputed {
		t.Error("warm load did not latch the shot-stream flags")
	}
	if warmJSON := shotStreamsJSON(t, warm); warmJSON != coldJSON {
		t.Errorf("warm tier-3 load differs from cold compute:\n cold: %s\n warm: %s", coldJSON, warmJSON)
	}
}

// TestEnsureShotStreams_Tier3_CorruptFallsBack: a garbage tier-3 gob is
// discarded and the streams are rebuilt from the (still present) tier-1 bytes.
func TestEnsureShotStreams_Tier3_CorruptFallsBack(t *testing.T) {
	sha, demo := corpusDemo(t)
	root := t.TempDir()
	if err := writeFileAtomic(mvdPath(root, sha), demo, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := DemoID{Kind: "sha256", SHA: sha}

	c1 := New(root, nil)
	if _, _, err := c1.EnsureShotStreams(ctx, id); err != nil {
		t.Fatalf("cold EnsureShotStreams: %v", err)
	}
	if err := writeFileAtomic(artifactPath(root, "shot-streams", sha), []byte("not a gob"), 0o644); err != nil {
		t.Fatalf("write corrupt tier-3: %v", err)
	}

	c2 := New(root, nil)
	res, meta, err := c2.EnsureShotStreams(ctx, id)
	if err != nil {
		t.Fatalf("EnsureShotStreams with corrupt tier-3: %v", err)
	}
	if meta.ShotStreamsUnavailable {
		t.Error("should have rebuilt from tier-1, not degraded")
	}
	if res.Streams == nil || res.Streams.Projectiles == nil {
		t.Error("rebuild after corrupt tier-3 produced no projectile stream")
	}
}

// shotStreamsJSON marshals the stream-derived blocks of a Result to compare
// cold-compute vs warm-load for equivalence.
func shotStreamsJSON(t *testing.T, r *result.Result) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Projectiles *result.ProjectileStreams
		Beams       *result.BeamStreams
		Nails       *result.ProjectileStreams
		Shots       *result.ShotsResult
		Aim         *result.AimResult
	}{r.Streams.Projectiles, r.Streams.Beams, r.Streams.Nails, r.Shots, r.Aim})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
