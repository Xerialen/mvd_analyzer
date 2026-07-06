package analyzer

import "github.com/mvd-analyzer/mvd-analytics/result"

// Shared match-relative rebasing helpers. Producers (the timeline and shots
// nodes) call these at Finalize to convert their own stream columns from the
// demo clock to match-relative time, dropping warmup samples — the "born
// correct" replacement for the old whole-Result normalizeMatchRelativeTimes
// pass. All time arithmetic is int32 milliseconds (schema v8); the shift is a
// single subtraction per value.

// shiftAndFilterChanges subtracts matchStartMs from each entry's
// timestamp and drops entries with negative T. The latest entry at or
// before matchStartMs is kept, clamped to T=0, as the carry-forward
// "value at t=0"; getT reads an entry's timestamp and withT returns a
// copy with a new timestamp (value preserved). All times are integer
// milliseconds. Instantiated for result.ChangeI16 and result.ChangeStr,
// whose JSON shapes are unchanged.
func shiftAndFilterChanges[C any](stream []C, matchStartMs int32, getT func(C) int32, withT func(C, int32) C) []C {
	if len(stream) == 0 {
		return nil
	}
	// Find the latest entry at or before matchStartMs — it becomes the
	// carry-forward "value at t=0" entry.
	carryIdx := -1
	for i, c := range stream {
		if getT(c) <= matchStartMs {
			carryIdx = i
			continue
		}
		break
	}
	out := make([]C, 0, len(stream))
	if carryIdx >= 0 {
		out = append(out, withT(stream[carryIdx], 0))
	}
	for _, c := range stream {
		if getT(c) <= matchStartMs {
			continue
		}
		out = append(out, withT(c, getT(c)-matchStartMs))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func shiftAndFilterChangeI16(stream []result.ChangeI16, matchStartMs int32) []result.ChangeI16 {
	return shiftAndFilterChanges(stream, matchStartMs,
		func(c result.ChangeI16) int32 { return c.T },
		func(c result.ChangeI16, t int32) result.ChangeI16 { c.T = t; return c })
}

func shiftAndFilterChangeStr(stream []result.ChangeStr, matchStartMs int32) []result.ChangeStr {
	return shiftAndFilterChanges(stream, matchStartMs,
		func(c result.ChangeStr) int32 { return c.T },
		func(c result.ChangeStr, t int32) result.ChangeStr { c.T = t; return c })
}

// shiftAndFilterIntervals shifts each interval and clamps to t >= 0.
// Intervals entirely before matchStartMs are dropped; intervals
// straddling are clamped to start at 0. Times are integer milliseconds.
func shiftAndFilterIntervals(stream []result.Interval, matchStartMs int32) []result.Interval {
	if len(stream) == 0 {
		return nil
	}
	out := make([]result.Interval, 0, len(stream))
	for _, iv := range stream {
		if iv.End <= matchStartMs {
			continue
		}
		s := iv.Start - matchStartMs
		if s < 0 {
			s = 0
		}
		out = append(out, result.Interval{Start: s, End: iv.End - matchStartMs})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// shiftAndFilterInts subtracts matchStartMs from each entry and drops
// entries that fall before the match start. Used for the int32-ms
// schema-v8 streams (Spawns, Deaths).
func shiftAndFilterInts(stream []int32, matchStartMs int32) []int32 {
	if len(stream) == 0 {
		return nil
	}
	out := make([]int32, 0, len(stream))
	for _, t := range stream {
		if t < matchStartMs {
			continue
		}
		out = append(out, t-matchStartMs)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// shiftAndFilterPosition trims pre-match position samples and shifts
// the survivors. Mutates pt in place. Every column that is sample-
// aligned with T (X/Y/Z always, Li/H/Lq/VP/VYa/VX/VY/VZ when present)
// must be trimmed by the same keepFrom — consumers (BuildLocGraph,
// view.RegionControl, airgibsPost) guard on `len(col) == len(pt.T)` and
// will silently skip the player if the lengths drift. All time
// arithmetic is int32 ms.
//
// PositionTrack column checklist site 5 (match-relative trim); see the
// checklist in result/coord.go (PositionTrack.MarshalJSON).
func shiftAndFilterPosition(pt *result.PositionTrack, matchStartMs int32) {
	if pt == nil || len(pt.T) == 0 {
		return
	}
	oldLen := len(pt.T)
	keepFrom := 0
	for keepFrom < oldLen && pt.T[keepFrom] < matchStartMs {
		keepFrom++
	}
	if keepFrom > 0 {
		pt.T = pt.T[keepFrom:]
		pt.X = pt.X[keepFrom:]
		pt.Y = pt.Y[keepFrom:]
		pt.Z = pt.Z[keepFrom:]
		if len(pt.Li) == oldLen {
			pt.Li = pt.Li[keepFrom:]
		}
		if len(pt.H) == oldLen {
			pt.H = pt.H[keepFrom:]
		}
		if len(pt.Lq) == oldLen {
			pt.Lq = pt.Lq[keepFrom:]
		}
		if len(pt.VP) == oldLen {
			pt.VP = pt.VP[keepFrom:]
		}
		if len(pt.VYa) == oldLen {
			pt.VYa = pt.VYa[keepFrom:]
		}
		if len(pt.VX) == oldLen {
			pt.VX = pt.VX[keepFrom:]
		}
		if len(pt.VY) == oldLen {
			pt.VY = pt.VY[keepFrom:]
		}
		if len(pt.VZ) == oldLen {
			pt.VZ = pt.VZ[keepFrom:]
		}
	}
	for i := range pt.T {
		pt.T[i] -= matchStartMs
	}
}

// shiftAndClampMoverStream rebases a mover's pose timeline to match time,
// mutating m in place. Unlike player positions (whole pre-match samples
// are dropped), a mover's pre-match poses must NOT all be discarded — a
// parked lift's only wire state is its baseline at demo open, and dropping
// it would leave the mover pose-less for the whole match. Instead the
// latest pre-match state is kept and clamped to T=0 (the pose held at
// match start); earlier pre-match states are dropped as superseded, and
// in-match states shift normally. A mover first seen mid-match keeps all
// its states. All columns stay index-aligned with T.
func shiftAndClampMoverStream(m *result.MoverStream, matchStartMs int32) {
	if m == nil || len(m.T) == 0 {
		return
	}
	// Latest index at or before match start — the pose held at t=0.
	carry := -1
	for i, t := range m.T {
		if t <= matchStartMs {
			carry = i
		} else {
			break
		}
	}
	start := carry
	if start < 0 {
		start = 0 // mover first appeared after match start; keep everything
	}
	if start > 0 {
		m.T = m.T[start:]
		m.X = m.X[start:]
		m.Y = m.Y[start:]
		m.Z = m.Z[start:]
		m.Vis = m.Vis[start:]
	}
	for i := range m.T {
		m.T[i] -= matchStartMs
		if m.T[i] < 0 {
			m.T[i] = 0 // the carried pre-match pose anchors at t=0
		}
	}
}

// Plausible wall-clock window for a demo-open anchor, in Unix epoch
// milliseconds: [2000-01-01, 2100-01-01). QuakeWorld demos carrying a
// wall-clock source are 2026+, so this generous window accepts every real
// value while rejecting the non-timestamp 0x000B payloads some demos carry
// (e.g. 61, 11701 — see the DemoStartTimestampEvent handling in clock.go).
const (
	minDemoStartUnixMs = 946684800000  // 2000-01-01T00:00:00Z
	maxDemoStartUnixMs = 4102444800000 // 2100-01-01T00:00:00Z
)

// plausibleDemoStartUnixMs reports whether v could be a real demo-open
// wall clock rather than a garbage / non-timestamp value.
func plausibleDemoStartUnixMs(v int64) bool {
	return v >= minDemoStartUnixMs && v < maxDemoStartUnixMs
}
