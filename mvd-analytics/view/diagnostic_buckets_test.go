package view

import (
	"reflect"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func TestDiagnosticBucketsMaterializeEmptyDemoAxis(t *testing.T) {
	got, err := DiagnosticBuckets(&result.Result{}, DiagnosticBucketsOptions{
		WindowMs:    1000,
		SourceEndMs: 3000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Buckets) != 3 {
		t.Fatalf("bucket count = %d, want 3", len(got.Buckets))
	}
	for i, bucket := range got.Buckets {
		if bucket.Players == nil || len(bucket.Players) != 0 {
			t.Fatalf("bucket %d players = %#v, want a concrete empty map", i, bucket.Players)
		}
	}
}

func TestDiagnosticBucketsUseOnlyNativePositions(t *testing.T) {
	base := result.PlayerStream{
		Name:   "cand-1",
		Spawns: []int32{0},
		Deaths: []int32{500},
		Position: &result.PositionTrack{
			T: []int32{100, 2100},
			X: []float32{10, 40},
			Y: []float32{20, 50},
			Z: []float32{30, 60},
		},
	}
	withoutLiveness := base
	withoutLiveness.Spawns = nil
	withoutLiveness.Deaths = nil

	build := func(player result.PlayerStream) *result.Result {
		return &result.Result{Streams: &result.Streams{
			Players: []result.PlayerStream{player},
		}}
	}
	withSignals, err := DiagnosticBuckets(build(base), DiagnosticBucketsOptions{
		WindowMs:    1000,
		SourceEndMs: 3000,
	})
	if err != nil {
		t.Fatal(err)
	}
	withoutSignals, err := DiagnosticBuckets(build(withoutLiveness), DiagnosticBucketsOptions{
		WindowMs:    1000,
		SourceEndMs: 3000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withSignals, withoutSignals) {
		t.Fatalf("spawn/death signals changed a position-only diagnostic view:\nwith=%#v\nwithout=%#v", withSignals, withoutSignals)
	}
	if got := withSignals.Buckets[0].Players["cand-1"][FieldPosition]; got == nil {
		t.Fatalf("first bucket missing native position: %#v", withSignals.Buckets[0])
	}
	if len(withSignals.Buckets[1].Players) != 0 {
		t.Fatalf("quiet bucket carried a stale position: %#v", withSignals.Buckets[1])
	}
	if got := withSignals.Buckets[2].Players["cand-1"][FieldPosition]; got == nil {
		t.Fatalf("third bucket missing native position: %#v", withSignals.Buckets[2])
	}
}

func TestDiagnosticBucketsIncludePositionExactlyAtSourceEnd(t *testing.T) {
	r := &result.Result{Streams: &result.Streams{
		Global: result.GlobalStream{MatchEnd: 3001},
		Players: []result.PlayerStream{{
			Name: "cand-1",
			Position: &result.PositionTrack{
				T: []int32{3000}, X: []float32{40}, Y: []float32{50}, Z: []float32{60},
			},
		}},
	}}

	got, err := DiagnosticBuckets(r, DiagnosticBucketsOptions{
		WindowMs:    1000,
		SourceEndMs: 3000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Buckets) != 4 || !got.Buckets[3].Partial {
		t.Fatalf("buckets = %#v, want a fourth partial bucket", got.Buckets)
	}
	if pos := got.Buckets[3].Players["cand-1"][FieldPosition]; pos == nil {
		t.Fatalf("position at exact source end was excluded: %#v", got.Buckets[3])
	}
}

func TestDiagnosticBucketsRejectDuplicateDisplayNames(t *testing.T) {
	r := &result.Result{Streams: &result.Streams{
		Players: []result.PlayerStream{
			{Name: "same", Position: &result.PositionTrack{T: []int32{1}, X: []float32{1}, Y: []float32{2}, Z: []float32{3}}},
			{Name: "same", Position: &result.PositionTrack{T: []int32{2}, X: []float32{4}, Y: []float32{5}, Z: []float32{6}}},
		},
	}}

	if _, err := DiagnosticBuckets(r, DiagnosticBucketsOptions{WindowMs: 1000, SourceEndMs: 10}); err == nil {
		t.Fatal("duplicate display names were silently collapsed")
	}
}

func TestDiagnosticBucketsAllowSameMillisecondSamplesInStableOrder(t *testing.T) {
	r := &result.Result{Streams: &result.Streams{
		Players: []result.PlayerStream{{
			Name: "paused-player",
			Position: &result.PositionTrack{
				T: []int32{100, 100, 2100},
				X: []float32{10, 40, 70},
				Y: []float32{20, 50, 80},
				Z: []float32{30, 60, 90},
			},
		}},
	}}

	got, err := DiagnosticBuckets(r, DiagnosticBucketsOptions{WindowMs: 1000, SourceEndMs: 3000})
	if err != nil {
		t.Fatal(err)
	}
	want := [3]result.Coord{10, 20, 30}
	if pos := got.Buckets[0].Players["paused-player"][FieldPosition]; !reflect.DeepEqual(pos, want) {
		t.Fatalf("first paused position = %#v, want stable first sample %#v", pos, want)
	}
}

func TestDiagnosticBucketsRejectTimestampRegression(t *testing.T) {
	r := &result.Result{Streams: &result.Streams{
		Players: []result.PlayerStream{{
			Name: "regressed-player",
			Position: &result.PositionTrack{
				T: []int32{101, 100},
				X: []float32{10, 40},
				Y: []float32{20, 50},
				Z: []float32{30, 60},
			},
		}},
	}}

	if _, err := DiagnosticBuckets(r, DiagnosticBucketsOptions{WindowMs: 1000, SourceEndMs: 1000}); err == nil {
		t.Fatal("timestamp regression was accepted")
	}
}
