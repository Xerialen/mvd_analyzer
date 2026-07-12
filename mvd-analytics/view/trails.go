package view

import (
	"github.com/mvd-analyzer/mvd-analytics/result"
)

// LocTrailsOptions narrows a LocTrails query. MinDwellMs drops
// transitions shorter than the threshold (the per-player dwell
// summed into the surrounding stable loc) — useful for filtering
// nearest-loc flicker without re-running the analyzer's blip filter.
type LocTrailsOptions struct {
	Players    []string
	MinDwellMs int
	StartTime  float64
	EndTime    float64
	// LocIndex selects the residence representation: false (default)
	// names each residence (TrailEntry.Loc); true emits the raw
	// LocTable index (TrailEntry.Li). Decode the index via /loc-table.
	LocIndex bool
}

// LocTrailsView is the response shape: per-player loc-name sequence
// with dwell durations.
type LocTrailsView struct {
	Players []PlayerTrail `json:"players"`
}

// PlayerTrail is one player's loc journey within the requested
// window.
type PlayerTrail struct {
	Name     string       `json:"name"`
	Sequence []TrailEntry `json:"sequence"`
}

// TrailEntry is one continuous residence in a single loc. Loc (name)
// or Li (raw LocTable index) is set per LocTrailsOptions.LocIndex; the
// unexported li always holds the index so grouping/merging stay
// name-agnostic and the index render is a final relabel.
type TrailEntry struct {
	Start float64 `json:"s"`
	End   float64 `json:"e"`
	Loc   string  `json:"loc,omitempty"`
	Li    *int16  `json:"li,omitempty"`

	li int16
}

// LocTrails derives per-player loc residences from
// PlayerStream.Loc + TimelineAnalysis.LocTable. Walks each player's
// loc-change list, pairing consecutive entries into [Start, End)
// intervals, then optionally folds short dwells into their neighbour.
func LocTrails(r *result.Result, opts LocTrailsOptions) (*LocTrailsView, error) {
	if r == nil || r.Streams == nil || r.TimelineAnalysis == nil {
		return &LocTrailsView{}, nil
	}
	locTable := r.TimelineAnalysis.LocTable
	if len(locTable) == 0 {
		return &LocTrailsView{}, nil
	}
	end := opts.EndTime
	if end == 0 {
		end = secs(r.Streams.Global.MatchEnd)
	}
	pf := newPlayerFilter(opts.Players)
	out := &LocTrailsView{}
	minDwell := float64(opts.MinDwellMs) / 1000.0

	for _, p := range r.Streams.Players {
		if !pf.accepts(p.Name) {
			continue
		}
		raw := buildTrailRaw(p.Loc, opts.StartTime, end, locTable)
		seq := raw
		if minDwell > 0 {
			seq = mergeShortDwells(seq, minDwell)
		}
		if len(seq) == 0 {
			continue
		}
		if opts.LocIndex {
			for j := range seq {
				li := seq[j].li
				seq[j].Li = &li
				seq[j].Loc = ""
			}
		}
		out.Players = append(out.Players, PlayerTrail{Name: p.Name, Sequence: seq})
	}
	return out, nil
}

// buildTrailRaw walks the loc-change stream and emits a [Start, End)
// entry per residence. The final entry is closed at windowEnd (or
// match end). Entries entirely outside the window are dropped.
//
// The loc-change stream T is int32 ms (schema v8); windowStart and
// windowEnd are float64 seconds (public view API). Convert windows
// once and keep the inner walk in int32 ms; TrailEntry.Start/End are
// the public float64-seconds shape.
func buildTrailRaw(stream []result.ChangeI16, windowStart, windowEnd float64, locTable []string) []TrailEntry {
	if len(stream) == 0 {
		return nil
	}
	windowStartMs := int32(windowStart * 1000)
	windowEndMs := int32(windowEnd * 1000)
	out := make([]TrailEntry, 0, len(stream))
	for i, c := range stream {
		segStart := c.T
		var segEnd int32
		if i+1 < len(stream) {
			segEnd = stream[i+1].T
		} else {
			segEnd = windowEndMs
		}
		if segEnd <= windowStartMs {
			continue
		}
		if windowEndMs > 0 && segStart >= windowEndMs {
			break
		}
		if segStart < windowStartMs {
			segStart = windowStartMs
		}
		if windowEndMs > 0 && segEnd > windowEndMs {
			segEnd = windowEndMs
		}
		idx := int(c.V)
		locName := ""
		if idx >= 0 && idx < len(locTable) {
			locName = locTable[idx]
		}
		if locName == "" {
			continue
		}
		out = append(out, TrailEntry{
			Start: secs(segStart),
			End:   secs(segEnd),
			Loc:   locName,
			li:    c.V,
		})
	}
	return out
}

// mergeShortDwells folds entries shorter than minDwell into their
// preceding entry. Keeps the earlier loc name (its dwell extends to
// cover the dropped span), which matches the analyzer's blip-filter
// behaviour.
func mergeShortDwells(seq []TrailEntry, minDwell float64) []TrailEntry {
	if len(seq) <= 1 {
		return seq
	}
	out := make([]TrailEntry, 0, len(seq))
	out = append(out, seq[0])
	for i := 1; i < len(seq); i++ {
		dwell := seq[i].End - seq[i].Start
		if dwell < minDwell {
			out[len(out)-1].End = seq[i].End
			continue
		}
		// Coalesce identical-loc adjacent entries (rare, but the
		// merge above can produce them).
		last := &out[len(out)-1]
		if last.Loc == seq[i].Loc {
			last.End = seq[i].End
			continue
		}
		out = append(out, seq[i])
	}
	return out
}
