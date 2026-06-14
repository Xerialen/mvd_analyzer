// Package view provides time-parameterised queries over a finalised
// result.Result. Every function here takes at least one time-related
// option (window resolution, time range, point-in-time) that the
// caller controls and re-walks result.Streams at the requested
// resolution. Six entry points: Buckets, Events, StreamSlice,
// StateAt, LocTrails, RegionControl.
//
// Static derivations the analyzer baked at parse time — FragResult,
// MessagesResult, LocGraphResult, BackpackDrop, WeaponPickup,
// MetadataResult, DemoInfoResult, MatchResult — don't belong here.
// They have no caller-tunable parameters, so they're served directly
// from result fields by the transport layer (mvd-api routes them
// straight to JSON). The rule keeps view's surface coherent: if a
// query has a time/window knob, it belongs in this package; if it
// doesn't, it's a result-field passthrough elsewhere.
//
// All view functions read result.Streams (the canonical event-rate
// storage) and return derived shapes — bucketed timelines, raw
// stream slices, point-in-time state, tagged event lists, loc
// trails, region-control bucket states. No function mutates its
// input. Given the same Result and the same options they always
// produce the same output; none of them re-parse the demo or do
// I/O.
package view

import (
	"fmt"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// Sample is a single (time, value) point fed into a Reducer. Time is
// not used by every reducer (last/mean/min/max only need values), but
// majority and held-any need to know window bounds, so the
// constructors carry that context separately.
type Sample struct {
	T float64
	V any
}

// Reducer collapses the samples that fall in a single bucket into one
// value. Concrete reducers are stateless — Apply must be safe to call
// concurrently across buckets.
//
// For interval / boolean fields the slice carries Sample values whose V
// is bool; for numeric fields V is int / int8 / int16; categorical
// fields use string. Reducers that don't make sense for a given V type
// (e.g. mean over strings) return nil.
type Reducer interface {
	Apply(samples []Sample) any
	// Name is the registry key (e.g. "last").
	Name() string
}

// Registry maps reducer names to implementations. View functions look
// up reducers by name; unknown names return an error from the caller
// (no silent fallback).
var Registry = map[string]Reducer{
	"last":     LastReducer{},
	"first":    FirstReducer{},
	"mean":     MeanReducer{},
	"min":      MinReducer{},
	"max":      MaxReducer{},
	"dominant": DominantReducer{},
	"held-any": HeldAnyReducer{},
	"majority": MajorityReducer{},
	"any":      AnyReducer{},
}

// LookupReducer resolves a name to its Reducer or returns an error.
func LookupReducer(name string) (Reducer, error) {
	r, ok := Registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown reducer %q", name)
	}
	return r, nil
}

// LastReducer returns the value at the end of the window. If no
// samples fell in the window callers should pre-fill via carry-forward
// before calling Apply, otherwise nil is returned.
type LastReducer struct{}

func (LastReducer) Name() string { return "last" }
func (LastReducer) Apply(samples []Sample) any {
	if len(samples) == 0 {
		return nil
	}
	return samples[len(samples)-1].V
}

// FirstReducer returns the first sample's value.
type FirstReducer struct{}

func (FirstReducer) Name() string { return "first" }
func (FirstReducer) Apply(samples []Sample) any {
	if len(samples) == 0 {
		return nil
	}
	return samples[0].V
}

// MeanReducer returns the arithmetic mean of numeric samples. Returns
// nil for empty input or non-numeric values.
type MeanReducer struct{}

func (MeanReducer) Name() string { return "mean" }
func (MeanReducer) Apply(samples []Sample) any {
	if len(samples) == 0 {
		return nil
	}
	var sum float64
	for _, s := range samples {
		f, ok := numericToFloat(s.V)
		if !ok {
			return nil
		}
		sum += f
	}
	return sum / float64(len(samples))
}

// MinReducer / MaxReducer return the extrema of numeric samples.
type MinReducer struct{}

func (MinReducer) Name() string { return "min" }
func (MinReducer) Apply(samples []Sample) any {
	if len(samples) == 0 {
		return nil
	}
	var (
		best   float64
		hasMin bool
	)
	for _, s := range samples {
		f, ok := numericToFloat(s.V)
		if !ok {
			return nil
		}
		if !hasMin || f < best {
			best = f
			hasMin = true
		}
	}
	return best
}

type MaxReducer struct{}

func (MaxReducer) Name() string { return "max" }
func (MaxReducer) Apply(samples []Sample) any {
	if len(samples) == 0 {
		return nil
	}
	var (
		best   float64
		hasMax bool
	)
	for _, s := range samples {
		f, ok := numericToFloat(s.V)
		if !ok {
			return nil
		}
		if !hasMax || f > best {
			best = f
			hasMax = true
		}
	}
	return best
}

// DominantReducer returns the mode (most common value). Ties are
// resolved by the latest occurrence — i.e. when two values share the
// top count, the one that appears last in the slice wins. Works on any
// comparable type; non-comparable values short-circuit to nil.
type DominantReducer struct{}

func (DominantReducer) Name() string { return "dominant" }
func (DominantReducer) Apply(samples []Sample) any {
	if len(samples) == 0 {
		return nil
	}
	counts := make(map[any]int, len(samples))
	for _, s := range samples {
		if !isComparable(s.V) {
			return nil
		}
		counts[s.V]++
	}
	var (
		best     any
		bestN    int
		bestIdx  int
	)
	for idx := len(samples) - 1; idx >= 0; idx-- {
		v := samples[idx].V
		c := counts[v]
		if c > bestN || (c == bestN && idx > bestIdx) {
			best = v
			bestN = c
			bestIdx = idx
		}
	}
	return best
}

// HeldAnyReducer is the OR-fold over a boolean stream — true iff any
// sample is true. Mirrors current frontend semantics for "did the
// player have RL during this window."
type HeldAnyReducer struct{}

func (HeldAnyReducer) Name() string { return "held-any" }
func (HeldAnyReducer) Apply(samples []Sample) any {
	if len(samples) == 0 {
		return false
	}
	for _, s := range samples {
		if b, ok := s.V.(bool); ok && b {
			return true
		}
	}
	return false
}

// MajorityReducer returns true when the boolean stream is true for at
// least half of the window. Samples are taken to span equal slices —
// this is a coarse approximation; a more accurate version would weigh
// by interval length, but the sample-based form matches what most
// callers expect from a 50 ms / 1 s bucket.
type MajorityReducer struct{}

func (MajorityReducer) Name() string { return "majority" }
func (MajorityReducer) Apply(samples []Sample) any {
	if len(samples) == 0 {
		return false
	}
	t := 0
	for _, s := range samples {
		if b, ok := s.V.(bool); ok && b {
			t++
		}
	}
	return t*2 >= len(samples)
}

// AnyReducer returns true iff at least one sample is present in the
// window — useful for spawn / death / event-list streams where the
// presence of an entry is itself the signal.
type AnyReducer struct{}

func (AnyReducer) Name() string { return "any" }
func (AnyReducer) Apply(samples []Sample) any {
	return len(samples) > 0
}

// --- helpers ---

// numericToFloat coerces a sample value to float64 if it's any of the
// numeric kinds we emit. Returns ok=false otherwise.
func numericToFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case result.Coord:
		return float64(n), true
	case float64:
		return n, true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// isComparable mirrors Go's comparability rules for the value types we
// stash in Sample.V. Only types we know are safe to use as map keys
// pass.
func isComparable(v any) bool {
	switch v.(type) {
	case nil, bool, int, int8, int16, int32, int64,
		float32, float64, string:
		return true
	}
	return false
}
