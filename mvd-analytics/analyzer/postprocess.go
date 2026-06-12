package analyzer

import (
	"strconv"
	"strings"

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
	// The match-start shift is carried, pre-normalize, on
	// Streams.Global.MatchStart (the demo-relative match start the analyzer
	// recorded). Schema v23 dropped the duplicate timelineAnalysis.matchStartTime
	// that previously held it.
	matchStartMs := int32(0)
	if res.Streams != nil {
		matchStartMs = res.Streams.Global.MatchStart
	}
	if matchStartMs <= 0 {
		return
	}

	if ta := res.TimelineAnalysis; ta != nil {
		for i := range ta.FragEvents {
			ta.FragEvents[i].Time -= matchStartMs
		}
		for i := range ta.DeathEvents {
			ta.DeathEvents[i].Time -= matchStartMs
		}
		for i := range ta.PowerupEvents {
			ta.PowerupEvents[i].Time -= matchStartMs
			ta.PowerupEvents[i].EndTime -= matchStartMs
		}
		for i := range ta.FragStreaks {
			ta.FragStreaks[i].Time -= matchStartMs
			ta.FragStreaks[i].EndTime -= matchStartMs
		}
	}

	// Shift every per-player stream's timestamps and drop warmup
	// entries. The match-window + wall-clock anchors on Streams.Global also rebase.
	if streams := res.Streams; streams != nil {
		streams.Global.MatchStart -= matchStartMs
		streams.Global.MatchEnd -= matchStartMs
		if streams.Global.MatchStart < 0 {
			streams.Global.MatchStart = 0
		}
		// Record the demo→match offset and rebase pause anchors to match time.
		// AtMs only — DurationMs is a span, not a timestamp. Pauses during the
		// countdown go negative; keep them, they still consume wall time the
		// mapping must account for. DemoStartUnixMs is NOT shifted (it anchors
		// demo open, not match start).
		streams.Global.DemoOffset = matchStartMs
		for i := range streams.Global.Pauses {
			streams.Global.Pauses[i].AtMs -= matchStartMs
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

	if res.Damage != nil {
		for i := range res.Damage.Events {
			res.Damage.Events[i].Time -= matchStartMs
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
// the survivors. Mutates pt in place. Every column that is sample-
// aligned with T (X/Y/Z always, Li/H when present) must be trimmed by
// the same keepFrom — consumers (BuildLocGraph, view.RegionControl,
// airgibsPost) guard on `len(col) == len(pt.T)` and will silently skip
// the player if the lengths drift. All time arithmetic is int32 ms.
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
	}
	for i := range pt.T {
		pt.T[i] -= matchStartMs
	}
}

// deriveDemoStartAnchor fills the demo-open wall-clock anchor
// (Streams.Global.DemoStartUnixMs / DemoStartAccuracyMs) from the
// whole-second serverinfo `epoch` cvar when the millisecond-accurate
// mvdhidden 0x000B block was not present. TimelineAnalyzer.Finalize
// already set both fields (accuracy 1) when 0x000B was seen; this runs
// only as the fallback, so a non-zero accuracy means "leave it alone".
//
// `epoch` is the server clock in whole Unix seconds at demo open — the
// same instant 0x000B carries to the millisecond. It lives in
// result.Metadata.ServerInfo, which is why this is a post-processor:
// MetadataAnalyzer.Finalize has run by now. The anchor is demo-open, so
// it is independent of the match-relative time shift and does not need
// to run before/after normalizeMatchRelativeTimes. (Schema v23 moved the
// anchor from TimelineAnalysis to Streams.Global.)
func deriveDemoStartAnchor(res *Result, _ *CoreOutputs) {
	if res.Streams == nil || res.Streams.Global.DemoStartAccuracyMs != 0 {
		return // no streams, or 0x000B already supplied a finer anchor
	}
	if res.Metadata == nil || res.Metadata.ServerInfo == nil {
		return
	}
	epoch, ok := res.Metadata.ServerInfo["epoch"]
	if !ok {
		return
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(epoch), 10, 64)
	if err != nil || !plausibleDemoStartUnixMs(secs*1000) {
		return
	}
	res.Streams.Global.DemoStartUnixMs = secs * 1000
	res.Streams.Global.DemoStartAccuracyMs = 1000
}

// Plausible wall-clock window for a demo-open anchor, in Unix epoch
// milliseconds: [2000-01-01, 2100-01-01). QuakeWorld demos carrying a
// wall-clock source are 2026+, so this generous window accepts every real
// value while rejecting the non-timestamp 0x000B payloads some demos carry
// (e.g. 61, 11701 — see the DemoStartTimestampEvent handling in timeline.go).
const (
	minDemoStartUnixMs = 946684800000  // 2000-01-01T00:00:00Z
	maxDemoStartUnixMs = 4102444800000 // 2100-01-01T00:00:00Z
)

// plausibleDemoStartUnixMs reports whether v could be a real demo-open
// wall clock rather than a garbage / non-timestamp value.
func plausibleDemoStartUnixMs(v int64) bool {
	return v >= minDemoStartUnixMs && v < maxDemoStartUnixMs
}

// duelTeamNormalize is the post-processor wrapper around
// normalizeDuelTeams (defined in duel_normalize.go).
func duelTeamNormalize(res *Result, _ *CoreOutputs) {
	normalizeDuelTeams(res)
}

// scoreboardStatsPost fills MatchResult.Players[].Kills/Deaths from the
// frag-log-corrected FragResult.ByPlayer, joining on the final display
// name. It runs as a post-processor (not in the match analyser's
// Finalize) for two reasons: ByPlayer is only final after
// recoverTelefragTeamkills, and the join must use the *assembled*
// result's names — the same basis the web UI joins on — because a slot's
// Finalize-time name can differ from its final display name. Players
// whose name has no ByPlayer entry keep 0/0 (no frag log, or a name that
// never appeared in an obituary). KTX demoinfo stats are left untouched.
func scoreboardStatsPost(res *Result, _ *CoreOutputs) {
	if res.Match == nil || res.Frags == nil {
		return
	}
	// Suicides: count self-inflicted deaths (IsSuicide, killer == victim) per
	// victim from the final frag log. KTX demoinfo undercounts these — a
	// world-dealt self-death (fall / lava / squish / drown) bumps the world
	// entity's suicide counter, not the victim's (ktx/src/client.c:5132), and
	// a pentagram-deflect self-telefrag isn't credited to the victim either.
	suicides := make(map[string]int)
	for _, f := range res.Frags.Frags {
		if f.IsSuicide {
			suicides[f.Victim]++
		}
	}
	for i := range res.Match.Players {
		name := res.Match.Players[i].Name
		if pf := res.Frags.ByPlayer[name]; pf != nil {
			res.Match.Players[i].Kills = pf.Kills
			res.Match.Players[i].Deaths = pf.Deaths
		}
		res.Match.Players[i].Suicides = suicides[name]
	}
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
