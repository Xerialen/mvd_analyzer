package analyzer

import (
	"reflect"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// TestLazyArtifactRegistry: the lazy artifact resolves by name; unknown names
// do not (a closed registry). Since phase 12 los is the only lazy artifact —
// shot-streams folded into the eager always-full parse.
func TestLazyArtifactRegistry(t *testing.T) {
	for _, name := range []string{"los"} {
		a, ok := LazyArtifactByName(name)
		if !ok {
			t.Fatalf("LazyArtifactByName(%q) not found", name)
		}
		if a.Name() != name {
			t.Errorf("artifact name = %q, want %q", a.Name(), name)
		}
	}
	if _, ok := LazyArtifactByName("shot-streams"); ok {
		t.Error("LazyArtifactByName(shot-streams) should not resolve — folded into the eager parse (phase 12)")
	}
	if _, ok := LazyArtifactByName("nope"); ok {
		t.Error("LazyArtifactByName(nope) should not resolve")
	}
}

// TestLOSTier3RoundTrip: encode a computed los artifact and decode it onto a
// fresh Result with the same players, asserting the per-player LOS/PVS splice
// and the latch.
func TestLOSTier3RoundTrip(t *testing.T) {
	art, _ := LazyArtifactByName("los")

	mk := func() *result.Result {
		return &result.Result{Streams: &result.Streams{Players: []result.PlayerStream{
			{Name: "alpha"}, {Name: "bravo"},
		}}}
	}
	src := mk()
	src.Streams.LOSComputed = true
	src.Streams.Players[0].LOS = []result.LosTrack{{Other: 1, Iv: []result.Interval{{Start: 0, End: 100}}}}
	src.Streams.Players[0].PVS = []result.LosTrack{{Other: 1, Iv: []result.Interval{{Start: 0, End: 200}}}}

	data, ok, err := art.EncodeTier3(src)
	if err != nil || !ok {
		t.Fatalf("EncodeTier3: ok=%v err=%v", ok, err)
	}

	dst := mk()
	if err := art.DecodeTier3(dst, data); err != nil {
		t.Fatalf("DecodeTier3: %v", err)
	}
	if !dst.Streams.LOSComputed {
		t.Error("LOSComputed latch not set after decode")
	}
	if !reflect.DeepEqual(dst.Streams.Players[0].LOS, src.Streams.Players[0].LOS) {
		t.Errorf("LOS not spliced: %+v", dst.Streams.Players[0].LOS)
	}
	if !reflect.DeepEqual(dst.Streams.Players[0].PVS, src.Streams.Players[0].PVS) {
		t.Errorf("PVS not spliced: %+v", dst.Streams.Players[0].PVS)
	}
}

// TestLOSTier3DriftDiscarded: a cached los gob whose player set does not match
// the live Result is rejected (so the caller recomputes), not spliced blindly.
func TestLOSTier3DriftDiscarded(t *testing.T) {
	art, _ := LazyArtifactByName("los")

	src := &result.Result{Streams: &result.Streams{
		LOSComputed: true,
		Players:     []result.PlayerStream{{Name: "alpha"}, {Name: "bravo"}},
	}}
	data, ok, err := art.EncodeTier3(src)
	if err != nil || !ok {
		t.Fatalf("EncodeTier3: ok=%v err=%v", ok, err)
	}

	// Different player set (name drift): decode must error.
	drift := &result.Result{Streams: &result.Streams{
		Players: []result.PlayerStream{{Name: "alpha"}, {Name: "charlie"}},
	}}
	if err := art.DecodeTier3(drift, data); err == nil {
		t.Error("expected drift error decoding onto a mismatched player set")
	}
	if drift.Streams.LOSComputed {
		t.Error("latch should not be set on a rejected decode")
	}

	// Count mismatch too.
	fewer := &result.Result{Streams: &result.Streams{Players: []result.PlayerStream{{Name: "alpha"}}}}
	if err := art.DecodeTier3(fewer, data); err == nil {
		t.Error("expected error decoding onto a smaller player set")
	}
}

// TestLOSBuildNoBSP: Build through the artifact latches even with no BSP, so
// the compute is attempted once (matching ComputeLOS).
func TestLOSBuildNoBSP(t *testing.T) {
	art, _ := LazyArtifactByName("los")
	res := &result.Result{
		DemoInfo: &result.DemoInfoResult{Map: "zzz_no_such_map_xyz"},
		Streams: &result.Streams{Players: []result.PlayerStream{
			{Name: "A", Position: &result.PositionTrack{T: []int32{0, 50}}},
			{Name: "B", Position: &result.PositionTrack{T: []int32{0, 50}}},
		}},
	}
	if err := art.Build(res, MaterializeDeps{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !art.Computed(res) {
		t.Error("los Build should latch even with no BSP")
	}
}
