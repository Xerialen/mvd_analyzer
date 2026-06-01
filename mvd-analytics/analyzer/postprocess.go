package analyzer

import (
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-analytics/view"
)

// Default post-processors for the registry. Each one is registered by
// NewDefaultRegistry; callers building a registry from scratch can
// pick which ones they want via RegisterPostProcessor.

// normalizeMatchRelativeTimes shifts every time-stamped field in
// Result so that t=0 is the moment the match started. The original
// match-start offset is preserved in TimelineAnalysis.DemoOffset so
// the frontend can map back to demo-time when needed (e.g. building
// hub viewer URLs).
//
// Warmup entries (those with negative t after the shift) are dropped
// from result.Streams — they would otherwise produce garbage pre-match
// samples that downstream consumers have no use for.
//
// All time fields are integer milliseconds at schema v8; the shift is
// a single int32 subtraction per value.
func normalizeMatchRelativeTimes(res *Result, _ *CoreOutputs) {
	matchStartMs := int32(0)
	if res.TimelineAnalysis != nil {
		matchStartMs = res.TimelineAnalysis.MatchStartTime
	}
	if matchStartMs <= 0 {
		return
	}

	if ta := res.TimelineAnalysis; ta != nil {
		for i := range ta.FragEvents {
			ta.FragEvents[i].Time -= matchStartMs
		}
		for i := range ta.PowerupEvents {
			ta.PowerupEvents[i].Time -= matchStartMs
			ta.PowerupEvents[i].EndTime -= matchStartMs
		}
		for i := range ta.FragStreaks {
			ta.FragStreaks[i].Time -= matchStartMs
			ta.FragStreaks[i].EndTime -= matchStartMs
		}
		ta.DemoOffset = matchStartMs
		ta.MatchStartTime = 0
	}

	// Shift every per-player stream's timestamps and drop warmup
	// entries. The match-window anchors on Streams.Global also rebase.
	if streams := res.Streams; streams != nil {
		streams.Global.MatchStart -= matchStartMs
		streams.Global.MatchEnd -= matchStartMs
		if streams.Global.MatchStart < 0 {
			streams.Global.MatchStart = 0
		}
		for pi := range streams.Players {
			p := &streams.Players[pi]
			p.Health = shiftAndFilterChangeI16(p.Health, matchStartMs)
			p.Armor = shiftAndFilterChangeI16(p.Armor, matchStartMs)
			p.ArmorType = shiftAndFilterChangeStr(p.ArmorType, matchStartMs)
			p.Loc = shiftAndFilterChangeI16(p.Loc, matchStartMs)
			p.Shells = shiftAndFilterChangeI16(p.Shells, matchStartMs)
			p.Nails = shiftAndFilterChangeI16(p.Nails, matchStartMs)
			p.Rockets = shiftAndFilterChangeI16(p.Rockets, matchStartMs)
			p.Cells = shiftAndFilterChangeI16(p.Cells, matchStartMs)

			p.RL = shiftAndFilterIntervals(p.RL, matchStartMs)
			p.LG = shiftAndFilterIntervals(p.LG, matchStartMs)
			p.GL = shiftAndFilterIntervals(p.GL, matchStartMs)
			p.SSG = shiftAndFilterIntervals(p.SSG, matchStartMs)
			p.SNG = shiftAndFilterIntervals(p.SNG, matchStartMs)
			p.Quad = shiftAndFilterIntervals(p.Quad, matchStartMs)
			p.Pent = shiftAndFilterIntervals(p.Pent, matchStartMs)
			p.Ring = shiftAndFilterIntervals(p.Ring, matchStartMs)

			p.Spawns = shiftAndFilterInts(p.Spawns, matchStartMs)
			p.Deaths = shiftAndFilterInts(p.Deaths, matchStartMs)

			if p.Position != nil {
				shiftAndFilterPosition(p.Position, matchStartMs)
			}
		}
	}

	if res.Messages != nil {
		for i := range res.Messages.Events {
			res.Messages.Events[i].Time -= matchStartMs
		}
	}

	if res.Frags != nil {
		for i := range res.Frags.Frags {
			res.Frags.Frags[i].Time -= matchStartMs
		}
	}

	if res.Match != nil {
		res.Match.StartTime -= matchStartMs
		res.Match.EndTime -= matchStartMs
	}

	if res.Items != nil {
		for i := range res.Items.Items {
			ph := res.Items.Items[i].Phases
			for j := range ph {
				// AvailableFrom=0 is the synthetic "match start" marker
				// for initial phases; leave it alone. All real
				// timestamps get shifted.
				if ph[j].AvailableFrom > 0 {
					ph[j].AvailableFrom -= matchStartMs
				}
				if ph[j].TakenAt > 0 {
					ph[j].TakenAt -= matchStartMs
				}
				if ph[j].RespawnAt > 0 {
					ph[j].RespawnAt -= matchStartMs
				}
			}
		}
	}

	if res.Damage != nil {
		// Rebase per-hit timestamps to match-relative ms so they join the
		// (already-rebased) per-player armor / RL / LG intervals. Clamp
		// rather than drop: the analyzer already print-gated to the match
		// window, so any residual sub-match-start hit is a boundary artifact;
		// dropping it would desync Hits from the byPlayer totals.
		for i := range res.Damage.Hits {
			res.Damage.Hits[i].TimeMs -= matchStartMs
			if res.Damage.Hits[i].TimeMs < 0 {
				res.Damage.Hits[i].TimeMs = 0
			}
		}
	}

	for i := range res.Backpacks {
		res.Backpacks[i].Time -= matchStartMs
	}

	for i := range res.WeaponPickups {
		res.WeaponPickups[i].Time -= matchStartMs
		if res.WeaponPickups[i].NextDeathTime > 0 {
			res.WeaponPickups[i].NextDeathTime -= matchStartMs
		}
		if res.WeaponPickups[i].DropTime > 0 {
			res.WeaponPickups[i].DropTime -= matchStartMs
		}
	}
}

// shiftAndFilterChangeI16 subtracts matchStartMs from each entry's T
// and drops entries with negative T. The first surviving entry is
// the carry-forward state at t=0. All times are integer milliseconds.
func shiftAndFilterChangeI16(stream []result.ChangeI16, matchStartMs int32) []result.ChangeI16 {
	if len(stream) == 0 {
		return nil
	}
	// Find the latest entry at or before matchStartMs — it becomes the
	// carry-forward "value at t=0" entry.
	carryIdx := -1
	for i, c := range stream {
		if c.T <= matchStartMs {
			carryIdx = i
			continue
		}
		break
	}
	out := make([]result.ChangeI16, 0, len(stream))
	if carryIdx >= 0 {
		out = append(out, result.ChangeI16{T: 0, V: stream[carryIdx].V})
	}
	for _, c := range stream {
		if c.T <= matchStartMs {
			continue
		}
		out = append(out, result.ChangeI16{T: c.T - matchStartMs, V: c.V})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func shiftAndFilterChangeStr(stream []result.ChangeStr, matchStartMs int32) []result.ChangeStr {
	if len(stream) == 0 {
		return nil
	}
	carryIdx := -1
	for i, c := range stream {
		if c.T <= matchStartMs {
			carryIdx = i
			continue
		}
		break
	}
	out := make([]result.ChangeStr, 0, len(stream))
	if carryIdx >= 0 {
		out = append(out, result.ChangeStr{T: 0, V: stream[carryIdx].V})
	}
	for _, c := range stream {
		if c.T <= matchStartMs {
			continue
		}
		out = append(out, result.ChangeStr{T: c.T - matchStartMs, V: c.V})
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
// the survivors. Mutates pt in place. Must keep all five columns
// (T/X/Y/Z/Li) aligned — BuildLocGraph and view.RegionControl both
// guard on `len(pt.Li) == len(pt.T)` and will silently skip the
// player if the lengths drift. All time arithmetic is int32 ms.
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
	}
	for i := range pt.T {
		pt.T[i] -= matchStartMs
	}
}

// duelTeamNormalize is the post-processor wrapper around
// normalizeDuelTeams (defined in duel_normalize.go).
func duelTeamNormalize(res *Result, _ *CoreOutputs) {
	normalizeDuelTeams(res)
}

// locGraphPost runs BuildLocGraph on the assembled Result. Has to run
// after the time and duel normalisations so the loc nodes/edges use
// the same time base and team labels as the rest of the result.
func locGraphPost(res *Result, _ *CoreOutputs) {
	res.LocGraph = BuildLocGraph(res)
}

// regionControlPost runs view.RegionControl on the assembled Result to
// fill in TimelineAnalysisResult.RegionControl.BucketStates and Stats.
// The analyzer's Finalize has already populated Regions/TeamA/TeamB
// from analyzer-internal state (locFinder, slotToTeam, region
// auto-detection); the view function reads those plus result.Streams
// and emits the classified bucket states + percentages.
//
// Must run after normalizeMatchRelativeTimes (so MatchStart=0) and
// after duelTeamNormalize (so per-player team labels are stable).
func regionControlPost(res *Result, _ *CoreOutputs) {
	if res == nil || res.TimelineAnalysis == nil {
		return
	}
	existing := res.TimelineAnalysis.RegionControl
	if existing == nil || len(existing.Regions) == 0 {
		return
	}
	rc, err := view.RegionControl(res, view.RegionControlOptions{})
	if err != nil || rc == nil {
		return
	}
	// Finalize wrote Regions + tentative TeamA/TeamB (computed pre-
	// duel-normalize). The view recomputes TeamA/TeamB from the now-
	// canonical Match.Players and fills BucketStates/Stats. Overwrite
	// both so external view-time callers see the same labels the
	// classifier used.
	if rc.TeamA != "" {
		existing.TeamA = rc.TeamA
	}
	if rc.TeamB != "" {
		existing.TeamB = rc.TeamB
	}
	existing.BucketStates = rc.BucketStates
	existing.Stats = rc.Stats
}
