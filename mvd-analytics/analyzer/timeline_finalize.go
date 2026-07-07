package analyzer

import (
	"math"
	"sort"

	"github.com/mvd-analyzer/mvd-analytics/bspvis"
	"github.com/mvd-analyzer/mvd-analytics/loc"
	"github.com/mvd-analyzer/mvd-analytics/locvis"
	"github.com/mvd-analyzer/mvd-analytics/mapbsp"
	"github.com/mvd-analyzer/mvd-analytics/mapclip"
	"github.com/mvd-analyzer/mvd-reader/events"
)

// Finalize converts the raw per-bucket player state collected during parsing
// into the TimelineAnalysisResult shipped to the frontend. This is the
// orchestration step — most of the heavy lifting is delegated to the
// aggregate / powerup / streak / region helpers.
func (a *TimelineAnalyzer) Finalize(result *Result) error {
	// Try to load loc file from DemoInfo.Map if not already loaded
	if a.locFinder == nil && a.core != nil && a.core.DemoInfo != nil && a.core.DemoInfo.Map != "" {
		if finder, err := locvis.LoadForMap(a.core.DemoInfo.Map); err == nil {
			a.locFinder = finder
		}
	}

	// Load the clip hulls for floor-height traces: the worldspawn hull
	// plus one per submodel the demo's mover entities reference, so the
	// floor pass can stand players on lifts/doors at their streamed
	// origins. Missing corpus (no BSP for the map, or an HL/Q2 format we
	// don't parse) leaves clipHull nil → the PositionTrack.H column
	// stays absent.
	if a.clipHull == nil && a.core != nil && a.core.DemoInfo != nil && a.core.DemoInfo.Map != "" {
		if hull, moverHulls, err := mapclip.LoadForMapWithMovers(a.core.DemoInfo.Map, a.moverSubModels()); err == nil {
			a.clipHull = hull
			a.moverHulls = moverHulls
		}
	}

	// Load the hull-0 render BSP for the liquid-state column and
	// liquid-surface heights (schema v28). Loaded directly from mapbsp
	// rather than through locFinder: locvis requires the .loc corpus and
	// is nil on maps that have a BSP but no locs, which would silently
	// lose liquid data exactly where it's available.
	if a.visBSP == nil && a.core != nil && a.core.DemoInfo != nil && a.core.DemoInfo.Map != "" {
		if data := mapbsp.LoadBytes(a.core.DemoInfo.Map); data != nil {
			if vb, err := bspvis.LoadBytes(data); err == nil {
				a.visBSP = vb
			}
		}
	}

	// (Loc resolution + blip filter now run on the per-position-sample
	// PositionTrack.Li column directly; see resolveLocsAndFilterBlips
	// below.)

	// Use the shared name->team lookup from CoreOutputs (built once
	// after the demoinfo analyser finalises).
	var names *NameTable
	if a.core != nil {
		names = a.core.Names
	}

	// Bridge slot↔demoinfo via login join / name join.
	resolved := a.ctx.ResolveSlotDemoInfo()
	slotToTeam := make(map[int]string)
	slotToPlayer := make(map[int]string)
	for slot, di := range resolved {
		if di.Team != "" {
			slotToTeam[slot] = di.Team
			slotToPlayer[slot] = di.Name
		}
	}

	// Convert raw frag events to final events with player and team info.
	// Resolve each frag to the identity that held the slot *at frag time*
	// (resolveAt) so a player's pre-reconnect frags don't get relabelled
	// with whoever later took their slot.
	//
	// Gate on a resolvable *name*, not team — same rationale as
	// killEvents below. A duel player with an empty userinfo/demoinfo
	// team (gameId 224758: iddQd) resolves to team "" for the whole
	// match; team-gating silently dropped every one of their events, and
	// the duel post-processor could only paper over FragEvents by
	// re-synthesising them from obituaries. Team stays best-effort: ""
	// when unresolvable, rewritten to the player's name by
	// normalizeDuelTeams in 1v1s.
	fragEvents := make([]TimelineFragEvent, 0, len(a.rawFrags))
	for _, raw := range a.rawFrags {
		playerName, team := a.resolveAt(raw.PlayerNum, msTime(raw.Time))

		if playerName != "" {
			fragEvents = append(fragEvents, TimelineFragEvent{
				Time:   msTime(raw.Time),
				Player: playerName,
				Team:   team,
				Delta:  raw.Delta,
			})
		}
	}

	// Convert raw deaths to per-player death events for the frags/deaths
	// drill-down. Same authoritative protocol DeathEvent source and same
	// at-death-time identity resolution / name-gating as fragEvents, so a
	// player's death count here matches their scoreboard deaths (and thus
	// KTX efficiency = frags/(frags+deaths)).
	deathEvents := make([]TimelineDeathEvent, 0, len(a.rawDeaths))
	for _, raw := range a.rawDeaths {
		playerName, team := a.resolveAt(raw.PlayerNum, msTime(raw.Time))
		if playerName != "" {
			deathEvents = append(deathEvents, TimelineDeathEvent{
				Time:   msTime(raw.Time),
				Player: playerName,
				Team:   team,
			})
		}
	}

	// Convert the canonical frag log to per-player kill events for the
	// frags/deaths drill-down. Keyed on the killer and filtered to real
	// enemy kills (suicides/teamkills excluded, generic killers skipped) —
	// exactly the condition frags.byPlayer[name].kills is counted under
	// (frag.go handleObituaryPrint), so a player's cumulative killEvents
	// reconciles with byPlayer.kills and the kills-based efficiency.
	// FragEntry.Time is already int32 ms.
	//
	// Like fragEvents/deathEvents we do NOT gate on a resolvable team:
	// byPlayer.kills doesn't either, so gating here would silently drop a
	// player's whole kill curve in POV demos where the name↔team join is
	// incomplete (the consumer groups by player name and ignores team).
	// Team is therefore best-effort via the name table.
	var killEvents []TimelineKillEvent
	for _, fe := range a.coreFragEntries() {
		if fe.IsSuicide || fe.IsTeamKill || isGenericPlayer(fe.Killer) {
			continue
		}
		team := ""
		if names != nil {
			team = names.TeamForName(fe.Killer)
		}
		killEvents = append(killEvents, TimelineKillEvent{
			Time:   fe.Time,
			Player: fe.Killer,
			Team:   team,
		})
	}

	// Effective match end, computed once and shared by the powerup-close
	// pass (detectPowerupEvents) and the per-player stream finalize
	// (buildStreamsResult → streams.finalize). Both flush still-open
	// intervals at this instant, so they must agree — otherwise a demo cut
	// before intermission closes quad/pent/ring at one time and rl/lg/gl/…
	// at another (F13). timing.EndTime is the explicit end; without it we
	// fall back to the GLOBAL latest position sample (not any single
	// player's) so every player's intervals close at the same time. posT is
	// int32 ms (schema v8); EndTime is float64 seconds, converted here.
	matchEnd := a.timing.EndTime
	if matchEnd == 0 {
		for _, state := range a.playerState {
			if n := len(state.streams.posT); n > 0 {
				if t := float64(state.streams.posT[n-1]) * 0.001; t > matchEnd {
					matchEnd = t
				}
			}
		}
	}
	matchEndMs := msTime(matchEnd)

	// Detect powerup pickup events for Key Moments
	powerupEvents := a.detectPowerupEvents(matchEndMs)

	// Count frags during each powerup run
	for i := range powerupEvents {
		pe := &powerupEvents[i]
		for _, fe := range a.coreFragEntries() {
			if fe.Killer != pe.PlayerName || fe.IsSuicide || fe.IsTeamKill {
				continue
			}
			if fe.Time >= pe.Time && fe.Time <= pe.EndTime {
				pe.Frags++
			}
		}
	}

	// Export one label point per loc name for map visualization (schema
	// v31). The raw .loc corpus often has several points sharing a name;
	// emitting them all drew duplicate labels in the web view. Keep the
	// medoid — the actual corpus point minimizing summed distance to its
	// same-name siblings — never an averaged, possibly mid-air, centroid.
	var locationData []MapLocation
	if a.locFinder != nil {
		locationData = medoidLocations(a.locFinder.Locations())
	}

	// Build slot->name mapping for exports.
	//
	// Prefer the DemoInfo-derived name (resolved above via login join or
	// name join) over the live userinfo name. The two can differ when
	// the userinfo "name" field is an auth/login string but the player's
	// actual displayed netname is a different (often colored) string —
	// the frontend joins timeline data against DemoInfo player names, so
	// we must export the same name DemoInfo did or the per-player health/
	// armor stack disappears for that player.
	slotToName := make(map[int]string)
	for slot := 0; slot < events.MaxClients; slot++ {
		if name := slotToPlayer[slot]; name != "" {
			slotToName[slot] = name
		} else if player := a.ctx.Players[slot]; player != nil && player.Name != "" {
			slotToName[slot] = player.Name
		} else if name := a.playerNames[slot]; name != "" {
			slotToName[slot] = name
		}
	}

	// Resolve every native-rate position sample's nearest loc, smooth
	// short-residence wall-bleed via the blip filter, and emit the
	// resulting sparse Loc change stream into each player's stream
	// builder. Returns the ordered locTable we'll ship in Result.
	locTable := a.resolveLocsAndFilterBlips()

	// Trace each player's height above the floor beneath them at every
	// native-rate position sample (schema v24). Runs per-slot before the
	// reconnect merge, same as the loc pass above; no-op when no clip
	// hull is loaded for the map.
	a.resolveFloorHeights()
	// Derive per-sample velocity (units/sec) from the position columns by
	// central difference. Per-slot before the merge, like the passes
	// above; needs no BSP.
	a.resolveVelocities()
	// Drop the table entirely if only the sentinel slot exists — JSON
	// omitempty will then skip the field on the wire.
	if len(locTable) <= 1 {
		locTable = nil
	}

	// Build name -> UserID mapping for Hub viewer links. Key by the
	// reconnect-unified identity active on each slot session, and skip
	// sessions with no recorded play, so a phantom reconnect name (a
	// vacated slot taken by someone who never played) doesn't leak a
	// stray userid entry under a name that appears nowhere else.
	playerUserIDsByName := make(map[string]int)
	if a.core != nil && len(a.core.Sessions) > 0 {
		// Iterate slots in order: a player who reconnected appears under
		// the same name on >1 slot (each with its own userid); keep-first
		// over a map would pick a nondeterministic userid run to run.
		sessSlots := make([]int, 0, len(a.core.Sessions))
		for slot := range a.core.Sessions {
			sessSlots = append(sessSlots, slot)
		}
		sort.Ints(sessSlots)
		for _, slot := range sessSlots {
			st := a.playerState[slot]
			if st == nil {
				continue
			}
			uid := a.playerUserIDs[slot]
			if uid <= 0 {
				continue
			}
			for _, s := range a.core.Sessions[slot] {
				if s.Name == "" || !sessionHasPlay(&st.streams, s.StartMs, s.EndMs) {
					continue
				}
				if _, ok := playerUserIDsByName[s.Name]; !ok {
					playerUserIDsByName[s.Name] = uid
				}
			}
		}
	} else {
		for slot, userID := range a.playerUserIDs {
			if userID > 0 {
				if name := slotToName[slot]; name != "" {
					playerUserIDsByName[name] = userID
				}
			}
		}
	}

	// Detect top 5 longest frag streaks for Key Moments
	fragStreaks := a.detectFragStreaks(10, names, playerUserIDsByName)

	// Build result.TimelineAnalysis (with regions but no BucketStates
	// yet) and then result.Streams — both are needed by
	// regionControlPost (which calls view.RegionControl) to fill in
	// BucketStates/Stats from streams.
	result.TimelineAnalysis = &TimelineAnalysisResult{
		FragEvents:    fragEvents,
		DeathEvents:   deathEvents,
		KillEvents:    killEvents,
		PowerupEvents: powerupEvents,
		FragStreaks:   fragStreaks,
		LocationData:  locationData,
		LocTable:      locTable,
		PlayerUserIDs: playerUserIDsByName,
	}

	// matchEnd (and matchEndMs) were computed once above and already fed the
	// powerup-close pass; buildStreamsResult reuses the same value so weapon
	// and powerup intervals close consistently (F13).
	if streams := a.buildStreamsResult(slotToName, slotToTeam, a.timing.StartTime, matchEnd); streams != nil {
		result.Streams = streams

		// As of schema v23 the demo/wall-clock anchor lives on Streams.Global —
		// it describes how to map a stream's match time to wall-clock time, so
		// it belongs next to the match window rather than in TimelineAnalysis.
		// The clock owns both the anchor (0x000B ms / serverinfo `epoch` secs)
		// and the coalesced pauses; the timeline writes them here because it
		// owns Streams.Global. AtMs is demo-relative at this point; rebaseToMatch
		// (below) shifts it, and Global.MatchStart/MatchEnd/DemoOffset, once the
		// match-start shift is applied.
		if a.core != nil && a.core.Clock != nil {
			clk := a.core.Clock
			result.Streams.Global.DemoStartUnixMs = clk.DemoStartUnixMs
			result.Streams.Global.DemoStartAccuracyMs = clk.DemoStartAccuracyMs
			result.Streams.Global.Pauses = append([]TimelinePause(nil), clk.Pauses...)
		}
	}

	// Region control: detect regions + resolve team labels. The
	// per-bucket classification (BucketStates, Stats) is filled by the
	// regionControlPost post-processor, which calls view.RegionControl
	// on the assembled Result. We keep the analyzer-side work here
	// because region detection depends on locFinder + region overrides
	// + the analyzer's slot-to-team mapping (none of which view/
	// should reach for).
	if a.locFinder != nil {
		regions := a.buildControlRegions()
		for i := range regions {
			seen := make(map[string]struct{}, len(regions[i].Points))
			locs := make([]string, 0, len(regions[i].Points))
			for _, p := range regions[i].Points {
				if p.Name == "" {
					continue
				}
				if _, ok := seen[p.Name]; ok {
					continue
				}
				seen[p.Name] = struct{}{}
				locs = append(locs, p.Name)
			}
			sort.Strings(locs)
			regions[i].Locs = locs
		}
		if len(regions) > 0 {
			regionControl := &RegionControlResult{Regions: regions}

			teamSet := make(map[string]struct{})
			for _, t := range slotToTeam {
				if t != "" {
					teamSet[t] = struct{}{}
				}
			}
			if len(teamSet) == 2 {
				teamNames := make([]string, 0, 2)
				if a.core != nil && a.core.DemoInfo != nil && len(a.core.DemoInfo.Teams) == 2 {
					di := a.core.DemoInfo.Teams
					if _, ok0 := teamSet[di[0]]; ok0 {
						if _, ok1 := teamSet[di[1]]; ok1 {
							teamNames = append(teamNames, di[0], di[1])
						}
					}
				}
				if len(teamNames) != 2 {
					teamNames = teamNames[:0]
					for t := range teamSet {
						teamNames = append(teamNames, t)
					}
					sort.Strings(teamNames)
				}
				regionControl.TeamA = teamNames[0]
				regionControl.TeamB = teamNames[1]
			}
			result.TimelineAnalysis.RegionControl = regionControl
		}
	}

	// Born-correct timestamps: rebase every timeline-owned time field from the
	// demo clock to match-relative, using the shift the clock published. This
	// replaces the timeline's share of the old normalizeMatchRelativeTimes
	// rebase; it runs here so the timeline's own artifacts leave Finalize
	// already on the match clock (no post-hoc whole-Result pass).
	if ms := a.core.MatchStartMs(); ms > 0 {
		a.rebaseToMatch(result, ms)
	}
	return nil
}

// rebaseToMatch shifts every timeline-owned timestamp in result from the demo
// clock to match-relative (t' = t - matchStartMs), dropping warmup samples the
// same way the old normalizeMatchRelativeTimes did. Only the timeline's own
// artifacts are touched: the TimelineAnalysis event streams, Streams.Global's
// match window / offset / pauses, and each player's + mover's stream. Shots'
// spatial streams (Projectiles/Beams/Nails) are rebased by the shots node,
// which produces them. Called only when a match start was detected (ms > 0),
// mirroring the old rebase's early return.
func (a *TimelineAnalyzer) rebaseToMatch(result *Result, matchStartMs int32) {
	if ta := result.TimelineAnalysis; ta != nil {
		for i := range ta.FragEvents {
			ta.FragEvents[i].Time -= matchStartMs
		}
		for i := range ta.DeathEvents {
			ta.DeathEvents[i].Time -= matchStartMs
		}
		for i := range ta.KillEvents {
			ta.KillEvents[i].Time -= matchStartMs
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

	streams := result.Streams
	if streams == nil {
		return
	}
	// The match-window + wall-clock anchors on Streams.Global also rebase.
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
	for mi := range streams.Movers {
		shiftAndClampMoverStream(&streams.Movers[mi], matchStartMs)
	}
}

// pauseCoalesceGapSec separates one pause from the next. mvdsv emits a
// paused_duration sample per idle frame (idlefps 4–30, so ≤250ms apart) and
// the game clock is frozen across a pause, so intra-pause samples cluster
// within a few hundred ms; distinct pauses are separated by real gameplay
// (seconds). 0.5s cleanly splits them. A pause/unpause/pause cycle shorter
// than this merges into one segment — acceptable, the summed duration is
// preserved.
const pauseCoalesceGapSec = 0.5

// coalescePauses folds the raw per-idle-frame paused_duration samples into one
// segment per pause. AtMs is the frozen game time the pause sits at (the latest
// sample time in the run — the plateau the demo clock holds while paused);
// DurationMs is the summed real wall-clock time of the run. Times are
// demo-relative here; rebaseToMatch (this file) rebases AtMs to match time.
func coalescePauses(samples []pauseSample) []TimelinePause {
	if len(samples) == 0 {
		return nil
	}
	var pauses []TimelinePause
	runStartIdx := 0
	flush := func(end int) {
		dur := 0
		for _, s := range samples[runStartIdx:end] {
			dur += s.DurationMs
		}
		// Latest sample time is the frozen plateau; the leading transition
		// frame sits a few ms earlier.
		pauses = append(pauses, TimelinePause{
			AtMs:       msTime(samples[end-1].Time),
			DurationMs: int32(dur),
		})
	}
	for i := 1; i < len(samples); i++ {
		if samples[i].Time-samples[i-1].Time > pauseCoalesceGapSec {
			flush(i)
			runStartIdx = i
		}
	}
	flush(len(samples))
	return pauses
}

// medoidLocations collapses the loc corpus to one MapLocation per name —
// the medoid of that name's points (the point minimizing summed 3D
// distance to its same-name siblings). The medoid is an actual corpus
// point, so a name whose points straddle two disjoint spots resolves to
// the more central real position rather than an averaged mid-air one.
// Output order follows first-seen name order for determinism.
func medoidLocations(locs []loc.Location) []MapLocation {
	if len(locs) == 0 {
		return nil
	}
	order := make([]string, 0)
	byName := make(map[string][]loc.Location)
	for _, l := range locs {
		if _, ok := byName[l.Name]; !ok {
			order = append(order, l.Name)
		}
		byName[l.Name] = append(byName[l.Name], l)
	}
	out := make([]MapLocation, 0, len(order))
	for _, name := range order {
		pts := byName[name]
		best := 0
		bestSum := float32(math.MaxFloat32)
		for i := range pts {
			var sum float32
			for j := range pts {
				if i == j {
					continue
				}
				dx := pts[i].X - pts[j].X
				dy := pts[i].Y - pts[j].Y
				dz := pts[i].Z - pts[j].Z
				sum += float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
			}
			if i == 0 || sum < bestSum {
				bestSum = sum
				best = i
			}
		}
		m := pts[best]
		out = append(out, MapLocation{X: m.X, Y: m.Y, Z: m.Z, Name: m.Name})
	}
	return out
}
