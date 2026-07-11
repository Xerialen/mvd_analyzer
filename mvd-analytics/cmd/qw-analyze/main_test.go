package main

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func TestParseViewOptionsAcceptsClosedDiagnosticBucketsView(t *testing.T) {
	v, err := parseViewOptions(
		"diagnostic-buckets",
		"1s",
		"",
		nil,
		"",
		"",
		"",
		"",
		"0",
		"",
		false,
		"positions",
	)
	if err != nil {
		t.Fatalf("parseViewOptions: %v", err)
	}
	if v.view != "diagnostic-buckets" || v.bucketDur.Milliseconds() != 1000 {
		t.Fatalf("view = %q bucket = %s", v.view, v.bucketDur)
	}
}

func TestDiagnosticBucketsUseCompleteDemoWindowIncludingQuietTail(t *testing.T) {
	r := &result.Result{Streams: &result.Streams{
		Global: result.GlobalStream{MatchStart: 0, MatchEnd: 101},
		Players: []result.PlayerStream{{
			Name: "cand-1",
			Team: "red",
			Position: &result.PositionTrack{
				T: []int32{100}, X: []float32{10}, Y: []float32{20}, Z: []float32{30},
			},
		}},
	}}

	buckets, err := diagnosticBuckets(r, 1000, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets.Buckets) != 3 {
		t.Fatalf("bucket count = %d, want 3 from concrete 3s demo end", len(buckets.Buckets))
	}
	if got := buckets.Buckets[0].Players["cand-1"]["pos"]; got == nil {
		t.Fatalf("first bucket missing diagnostic identity/position: %+v", buckets.Buckets[0])
	}
	if len(buckets.Buckets[2].Players) != 0 {
		t.Fatalf("quiet tail bucket should remain present and empty: %+v", buckets.Buckets[2])
	}
}

func TestDiagnosticBucketsIncludePositionExactlyAtSourceEnd(t *testing.T) {
	r := &result.Result{Streams: &result.Streams{
		// Diagnostic capture closes its half-open stream window 1ms after the
		// last position. The concrete MVD source itself ends exactly at 3s.
		Global: result.GlobalStream{MatchStart: 0, MatchEnd: 3001},
		Players: []result.PlayerStream{{
			Name: "cand-1",
			Position: &result.PositionTrack{
				T: []int32{3000}, X: []float32{40}, Y: []float32{50}, Z: []float32{60},
			},
		}},
	}}

	buckets, err := diagnosticBuckets(r, 1000, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets.Buckets) != 4 {
		t.Fatalf("bucket count = %d, want final partial bucket at source end", len(buckets.Buckets))
	}
	got := buckets.Buckets[3].Players["cand-1"]["pos"]
	if got == nil {
		t.Fatalf("position at exact source end was excluded: %+v", buckets.Buckets[3])
	}
}
