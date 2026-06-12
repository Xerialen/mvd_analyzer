package view

import (
	"github.com/mvd-analyzer/mvd-analytics/result"
)

// StreamSliceOptions specifies a window and player/field filter for a
// raw, unreduced stream slice. Right shape for AI agents inspecting a
// short event — they get every transition that occurred, not a
// reduced bucket value.
//
// StartTime / EndTime are required (zero values mean match start /
// match end); Players empty → all; Fields empty → AllStandardFields.
type StreamSliceOptions struct {
	StartTime float64
	EndTime   float64
	Players   []string
	Fields    []string
	// LocIndex selects the loc representation: false (default) resolves
	// to loc names (PlayerSlice.Loc); true emits the raw LocTable index
	// stream (PlayerSlice.Li). Decode the index via /loc-table.
	LocIndex bool
}

// StreamSliceView is the response shape: per-player slices over the
// requested window with the same JSON keys as result.PlayerStream so
// consumers can treat a slice as a mini stream.
type StreamSliceView struct {
	StartTime float64       `json:"startTime"`
	EndTime   float64       `json:"endTime"`
	Players   []PlayerSlice `json:"players"`
}

// PlayerSlice mirrors result.PlayerStream with one notable
// addition: each change-stream gets a synthetic carry-forward entry
// at StartTime so consumers don't have to scan back into the rest of
// the stream to figure out the entering state. Intervals are clamped
// to [StartTime, EndTime). Position samples in the window are
// included as-is (no carry-forward).
type PlayerSlice struct {
	Name string `json:"name"`

	Position *result.PositionTrack `json:"pos,omitempty"`

	Health    []result.ChangeI16 `json:"h,omitempty"`
	Armor     []result.ChangeI16 `json:"a,omitempty"`
	ArmorType []result.ChangeStr `json:"at,omitempty"`
	// Loc / Li carry the loc change stream, one or the other depending
	// on StreamSliceOptions.LocIndex. Loc (default) holds resolved names
	// in V (e.g. "RA") so consumers never need the table; Li holds the
	// raw LocTable index stream (opt-in, for index math; decode via
	// /loc-table).
	Loc []result.ChangeStr `json:"loc,omitempty"`
	Li  []result.ChangeI16 `json:"li,omitempty"`

	RL  []result.Interval `json:"rl,omitempty"`
	LG  []result.Interval `json:"lg,omitempty"`
	GL  []result.Interval `json:"gl,omitempty"`
	SSG []result.Interval `json:"ssg,omitempty"`
	SNG []result.Interval `json:"sng,omitempty"`

	Quad []result.Interval `json:"q,omitempty"`
	Pent []result.Interval `json:"pe,omitempty"`
	Ring []result.Interval `json:"r,omitempty"`

	Shells  []result.ChangeI16 `json:"sh,omitempty"`
	Nails   []result.ChangeI16 `json:"nl,omitempty"`
	Rockets []result.ChangeI16 `json:"rk,omitempty"`
	Cells   []result.ChangeI16 `json:"cl,omitempty"`

	// Spawns / Deaths inherit the schema-v8 int32-ms representation
	// from result.PlayerStream — same JSON keys, same unit.
	Spawns []int32 `json:"sp,omitempty"`
	Deaths []int32 `json:"d,omitempty"`
}

// StreamSlice walks each player's streams and returns the entries
// that fall in [StartTime, EndTime). For change streams a
// carry-forward entry is prepended at StartTime; intervals overlapping
// the window are clamped to fit.
func StreamSlice(r *result.Result, opts StreamSliceOptions) (*StreamSliceView, error) {
	if r == nil || r.Streams == nil {
		return &StreamSliceView{StartTime: opts.StartTime, EndTime: opts.EndTime}, nil
	}
	fields := opts.Fields
	if len(fields) == 0 {
		fields = AllStandardFields
	}
	if err := validateFields(fields); err != nil {
		return nil, err
	}
	requested := make(map[string]bool, len(fields))
	for _, f := range fields {
		requested[f] = true
	}
	start := opts.StartTime
	end := opts.EndTime
	// Global.Match* is int32 ms (schema v8); public view API is
	// float64 seconds — convert once at the entry.
	if end == 0 {
		end = float64(r.Streams.Global.MatchEnd) * 0.001
	}
	if start == 0 {
		start = float64(r.Streams.Global.MatchStart) * 0.001
	}
	pf := newPlayerFilter(opts.Players)
	locTable := locTableOf(r)

	out := &StreamSliceView{StartTime: start, EndTime: end}
	for _, p := range r.Streams.Players {
		if !pf.accepts(p.Name) {
			continue
		}
		ps := PlayerSlice{Name: p.Name}
		if requested[FieldHealth] {
			ps.Health = sliceI16(p.Health, start, end)
		}
		if requested[FieldArmor] {
			ps.Armor = sliceI16(p.Armor, start, end)
		}
		if requested[FieldArmorType] {
			ps.ArmorType = sliceStr(p.ArmorType, start, end)
		}
		if requested[FieldLoc] {
			if opts.LocIndex {
				ps.Li = sliceI16(p.Loc, start, end)
			} else {
				ps.Loc = sliceLocNames(p.Loc, locTable, start, end)
			}
		}
		if requested[FieldShells] {
			ps.Shells = sliceI16(p.Shells, start, end)
		}
		if requested[FieldNails] {
			ps.Nails = sliceI16(p.Nails, start, end)
		}
		if requested[FieldRockets] {
			ps.Rockets = sliceI16(p.Rockets, start, end)
		}
		if requested[FieldCells] {
			ps.Cells = sliceI16(p.Cells, start, end)
		}

		if requested[FieldRL] {
			ps.RL = sliceInterval(p.RL, start, end)
		}
		if requested[FieldLG] {
			ps.LG = sliceInterval(p.LG, start, end)
		}
		if requested[FieldGL] {
			ps.GL = sliceInterval(p.GL, start, end)
		}
		if requested[FieldSSG] {
			ps.SSG = sliceInterval(p.SSG, start, end)
		}
		if requested[FieldSNG] {
			ps.SNG = sliceInterval(p.SNG, start, end)
		}
		if requested[FieldQuad] {
			ps.Quad = sliceInterval(p.Quad, start, end)
		}
		if requested[FieldPent] {
			ps.Pent = sliceInterval(p.Pent, start, end)
		}
		if requested[FieldRing] {
			ps.Ring = sliceInterval(p.Ring, start, end)
		}
		if requested[FieldSpawns] {
			ps.Spawns = sliceInts(p.Spawns, start, end)
		}
		if requested[FieldDeaths] {
			ps.Deaths = sliceInts(p.Deaths, start, end)
		}
		if requested[FieldPosition] && p.Position != nil {
			ps.Position = slicePosition(p.Position, start, end)
		}

		out.Players = append(out.Players, ps)
	}
	return out, nil
}

// Slice helpers operate in int32 ms (schema v8). The public view API
// takes float64 seconds; convert once at the entry of each helper.

func sliceI16(stream []result.ChangeI16, start, end float64) []result.ChangeI16 {
	startMs := int32(start * 1000)
	endMs := int32(end * 1000)
	out := make([]result.ChangeI16, 0, 4)
	if idx := indexI16AtOrBefore(stream, startMs); idx >= 0 {
		out = append(out, result.ChangeI16{T: startMs, V: stream[idx].V})
	}
	for _, c := range stream {
		if c.T < startMs {
			continue
		}
		if c.T >= endMs {
			break
		}
		if len(out) == 1 && c.T == startMs && c.V == out[0].V {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sliceLocNames is sliceI16 for the loc stream, resolving each index to
// its loc name through locTable as it goes. Carry-forward and dedup
// happen on the resolved name so two indices mapping to the same name
// (or to "") collapse like any other no-op transition.
func sliceLocNames(stream []result.ChangeI16, locTable []string, start, end float64) []result.ChangeStr {
	startMs := int32(start * 1000)
	endMs := int32(end * 1000)
	out := make([]result.ChangeStr, 0, 4)
	if idx := indexI16AtOrBefore(stream, startMs); idx >= 0 {
		out = append(out, result.ChangeStr{T: startMs, V: locNameAt(locTable, stream[idx].V)})
	}
	for _, c := range stream {
		if c.T < startMs {
			continue
		}
		if c.T >= endMs {
			break
		}
		name := locNameAt(locTable, c.V)
		if len(out) == 1 && c.T == startMs && name == out[0].V {
			continue
		}
		out = append(out, result.ChangeStr{T: c.T, V: name})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sliceStr(stream []result.ChangeStr, start, end float64) []result.ChangeStr {
	startMs := int32(start * 1000)
	endMs := int32(end * 1000)
	out := make([]result.ChangeStr, 0, 4)
	if idx := indexStrAtOrBefore(stream, startMs); idx >= 0 {
		out = append(out, result.ChangeStr{T: startMs, V: stream[idx].V})
	}
	for _, c := range stream {
		if c.T < startMs {
			continue
		}
		if c.T >= endMs {
			break
		}
		if len(out) == 1 && c.T == startMs && c.V == out[0].V {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sliceInterval(stream []result.Interval, start, end float64) []result.Interval {
	startMs := int32(start * 1000)
	endMs := int32(end * 1000)
	out := make([]result.Interval, 0, 4)
	for _, iv := range stream {
		if iv.End <= startMs || iv.Start >= endMs {
			continue
		}
		s := iv.Start
		e := iv.End
		if s < startMs {
			s = startMs
		}
		if e > endMs {
			e = endMs
		}
		out = append(out, result.Interval{Start: s, End: e})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sliceInts is the int32-ms variant of sliceFloats, used for the
// schema-v8 Spawns / Deaths streams. Window bounds are float64 seconds
// (public view API); convert once and stay in int32 for the loop.
func sliceInts(stream []int32, start, end float64) []int32 {
	startMs := int32(start * 1000)
	endMs := int32(end * 1000)
	out := make([]int32, 0, 4)
	for _, t := range stream {
		if t < startMs || t >= endMs {
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func slicePosition(pt *result.PositionTrack, start, end float64) *result.PositionTrack {
	if pt == nil {
		return nil
	}
	// pt.T is int32 ms (schema v8); convert window once.
	startMs := int32(start * 1000)
	endMs := int32(end * 1000)
	// Optional sample-aligned columns come along when present — a slice
	// without them would silently drop the loc / floor-height data the
	// full result carries for the same samples.
	hasLi := len(pt.Li) == len(pt.T)
	hasH := len(pt.H) == len(pt.T)
	hasLq := len(pt.Lq) == len(pt.T)
	out := &result.PositionTrack{}
	for i := range pt.T {
		t := pt.T[i]
		if t < startMs {
			continue
		}
		if t >= endMs {
			break
		}
		out.T = append(out.T, pt.T[i])
		out.X = append(out.X, pt.X[i])
		out.Y = append(out.Y, pt.Y[i])
		out.Z = append(out.Z, pt.Z[i])
		if hasLi {
			out.Li = append(out.Li, pt.Li[i])
		}
		if hasH {
			out.H = append(out.H, pt.H[i])
		}
		if hasLq {
			out.Lq = append(out.Lq, pt.Lq[i])
		}
	}
	if len(out.T) == 0 {
		return nil
	}
	return out
}
