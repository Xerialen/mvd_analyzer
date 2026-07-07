package analyzer

import (
	"reflect"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// TestLazyArtifactRegistry: the two lazy artifacts resolve by name; unknown
// names do not (a closed registry).
func TestLazyArtifactRegistry(t *testing.T) {
	for _, name := range []string{"los", "shot-streams"} {
		a, ok := LazyArtifactByName(name)
		if !ok {
			t.Fatalf("LazyArtifactByName(%q) not found", name)
		}
		if a.Name() != name {
			t.Errorf("artifact name = %q, want %q", a.Name(), name)
		}
	}
	if _, ok := LazyArtifactByName("nope"); ok {
		t.Error("LazyArtifactByName(nope) should not resolve")
	}
}

// TestShotStreamsTier3RoundTrip: encode a built shot-streams artifact, decode
// it onto a fresh lean Result, and assert the spliced blocks + latches match
// (the warm tier-3 path splices without a re-parse).
func TestShotStreamsTier3RoundTrip(t *testing.T) {
	art, _ := LazyArtifactByName("shot-streams")

	built := &result.Result{
		Streams: &result.Streams{
			ShotStreamsComputed: true,
			NailsComputed:       true,
			Projectiles:         &result.ProjectileStreams{Weapon: []string{"rl"}, Spawn: []int32{100}, End: []int32{200}, Sx: []float32{1}, Sy: []float32{2}, Sz: []float32{3}, Ex: []float32{4}, Ey: []float32{5}, Ez: []float32{6}},
			Beams:               &result.BeamStreams{T: []int32{50}, Sx: []float32{7}},
			Nails:               &result.ProjectileStreams{Weapon: []string{"nail"}, Spawn: []int32{10}},
		},
		Shots: &result.ShotsResult{},
		Aim:   &result.AimResult{},
	}

	data, ok, err := art.EncodeTier3(built)
	if err != nil || !ok {
		t.Fatalf("EncodeTier3: ok=%v err=%v", ok, err)
	}

	lean := &result.Result{Streams: &result.Streams{}}
	if art.Computed(lean) {
		t.Fatal("lean result should not be Computed before decode")
	}
	if err := art.DecodeTier3(lean, data); err != nil {
		t.Fatalf("DecodeTier3: %v", err)
	}
	if !art.Computed(lean) {
		t.Error("shot-streams latch not set after decode")
	}
	if !reflect.DeepEqual(lean.Streams.Projectiles, built.Streams.Projectiles) {
		t.Errorf("projectiles not spliced: %+v", lean.Streams.Projectiles)
	}
	if !reflect.DeepEqual(lean.Streams.Beams, built.Streams.Beams) {
		t.Errorf("beams not spliced: %+v", lean.Streams.Beams)
	}
	if !reflect.DeepEqual(lean.Streams.Nails, built.Streams.Nails) {
		t.Errorf("nails not spliced: %+v", lean.Streams.Nails)
	}
	if lean.Shots == nil || lean.Aim == nil {
		t.Error("Shots/Aim not spliced")
	}
}

// TestShotStreamsEncodeSkipsUnbuilt: a lean Result (latch unset) has nothing
// to persist.
func TestShotStreamsEncodeSkipsUnbuilt(t *testing.T) {
	art, _ := LazyArtifactByName("shot-streams")
	_, ok, err := art.EncodeTier3(&result.Result{Streams: &result.Streams{}})
	if err != nil {
		t.Fatalf("EncodeTier3: %v", err)
	}
	if ok {
		t.Error("EncodeTier3 ok=true for an unbuilt artifact")
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
