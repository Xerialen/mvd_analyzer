package democache

import (
	"context"
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
