package view

import (
	"reflect"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// sampleStream builds one PlayerStream exercising every field kind the
// fast path specialises: change-i16, change-str, interval, position and
// event-list. Values are arbitrary but cover carry-forward, in-window
// transitions, boundary hits and gaps.
func sampleStream() result.PlayerStream {
	return result.PlayerStream{
		Name: "p", Team: "red",
		Health:    []result.ChangeI16{{T: 0, V: 100}, {T: 320, V: 75}, {T: 1010, V: 40}},
		Armor:     []result.ChangeI16{{T: 100, V: 200}, {T: 900, V: 0}},
		ArmorType: []result.ChangeStr{{T: 100, V: "ra"}, {T: 900, V: ""}},
		Loc:       []result.ChangeI16{{T: 0, V: 1}, {T: 500, V: 2}, {T: 1300, V: 3}},
		Shells:    []result.ChangeI16{{T: 0, V: 25}, {T: 700, V: 10}},
		Nails:     []result.ChangeI16{{T: 50, V: 100}},
		Rockets:   []result.ChangeI16{{T: 0, V: 5}, {T: 1010, V: 0}},
		Cells:     []result.ChangeI16{{T: 200, V: 30}},
		RL:        []result.Interval{{Start: 0, End: 1010}},
		LG:        []result.Interval{{Start: 600, End: 2000}},
		GL:        []result.Interval{{Start: 300, End: 350}},
		Quad:      []result.Interval{{Start: 450, End: 480}},
		Position: &result.PositionTrack{
			T:  []int32{0, 120, 260, 410, 980, 1300},
			X:  []int32{10, 11, 12, 13, 14, 15},
			Y:  []int32{20, 21, 22, 23, 24, 25},
			Z:  []int32{30, 31, 32, 33, 34, 35},
			Li: []int16{1, 1, 2, 2, 2, 3},
		},
		Spawns: []int32{0, 1005},
		Deaths: []int32{1000},
	}
}

// TestFastReduceParity asserts the allocation-free fast path returns
// exactly what the general collectSamples + Reducer.Apply path returns,
// for every standard field under its default reducer, across a sweep of
// windows (including before/after the data and on bucket boundaries).
// This is the correctness lock for the buckets.go optimisation.
func TestFastReduceParity(t *testing.T) {
	p := sampleStream()
	step := 0.05 // 50ms windows, matching the default bucketing
	for _, f := range AllStandardFields {
		red, err := resolveReducerName(f, nil) // default reducer for this field
		if err != nil {
			t.Fatalf("resolveReducerName(%q): %v", f, err)
		}
		name := red.Name()
		for bs := -0.1; bs < 1.6; bs += step {
			be := bs + step
			got, ok := fastReduce(&p, f, name, bs, be)
			if !ok {
				t.Fatalf("field %q reducer %q: fast path declined a default field", f, name)
			}
			want := red.Apply(collectSamples(&p, f, bs, be))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("field %q reducer %q window [%.3f,%.3f): fast=%#v slow=%#v",
					f, name, bs, be, got, want)
			}
		}
	}
}

// TestFastReduceFallback confirms reducers the fast path does not
// specialise fall back to the general path (ok=false), so custom
// getBuckets reducers keep exact semantics.
func TestFastReduceFallback(t *testing.T) {
	p := sampleStream()
	for _, rn := range []string{"mean", "min", "max", "last", "dominant", "held-any", "majority"} {
		if _, ok := fastReduce(&p, FieldHealth, rn, 0, 0.05); ok {
			t.Fatalf("reducer %q should not be fast-pathed", rn)
		}
	}
}
