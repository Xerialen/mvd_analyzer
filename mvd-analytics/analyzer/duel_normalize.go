package analyzer

// Duel-mode team normalization.
//
// Problem: in a 1v1 demo the team concept is meaningless or actively
// misleading. Two broken shapes we hit in practice:
//
//   1. Duel demos where each player picks an arbitrary "team" string for
//      colour. Example: `duel_evil's_kid_vs_grid[aerowalk]` — player
//      teams are "green" and "kis". Every team-aggregating code path
//      produces two one-player "teams" whose names are random colour
//      tags, and the UI renders two "Per Team" tables that restate the
//      "Per Player" tables with a noisier header.
//
//   2. 1v1 vs frogbot demos where the bot has no team at all. Example:
//      `duel_chr1s_vs_bro[povdmm4]` — chr1s.team = "blue", bro.team = "".
//      TimelineAnalyzer's teamData map gets keyed by empty string for
//      bro, then swallowed by a nil-guard; `match.teams` ends up as
//      `[{blue: 223}]` with bro entirely missing from the aggregation
//      layer. The per-player view still works, but every team-keyed
//      consumer (timeline region control, team-weapon graphs, team-frag
//      charts) reports half a demo.
//
// Fix: after all analyzers finalize, if we detect a 1v1 match, rewrite
// the team string on every player to the player's own name and rebuild
// every team-keyed aggregate in-place. The data model stays uniform for
// downstream consumers — every layer still has a `team` field and a
// team-keyed map — they just point to the player.
//
// The UI is expected to detect duel mode (via demoInfo.mode /
// metadata.matchSettings.mode or by checking whether team == player for
// every player) and suppress the redundant "Per Team" tables.

// normalizeDuelTeams rewrites teams to player-name-per-player if the
// match is a 1v1. Call this after all analyzers have finalized and
// populated `result`. The duel verdict and participant set come from the
// roster (the core-tier table), so this shares one source of truth with
// the producers that already stamp final labels at birth.
//
// The DemoInfo team rewrite (RosterAnalyzer.PopulateCore) and the
// Match.Players participant rebuild (MatchAnalyzer.Finalize) have already
// been migrated to the analyzers that own those results; this post-processor
// carries only the sections not yet migrated to born-correct labels.
func normalizeDuelTeams(result *Result, r *Roster) {
	if !r.Duel() {
		return
	}
	// Every duel team label is now stamped at birth by the producers reading
	// co.Roster (roster → DemoInfo; match → Match.Players; timeline → streams
	// and events; messages, items, weapon pickups, backpacks and shots →
	// their own records, including shots' duel-aware VictimKinds). This
	// post-processor no longer rewrites anything; it is deleted once the DAG's
	// teams:final barrier retires.
	_ = result
}

// isDuelResult returns true when the match is a 1v1, using the number
// of match participants (spectators excluded — KTX never includes them
// in the demoinfo.players array) as the primary signal. Mode strings
// are a secondary fallback for demos that KTX tagged explicitly but
// that somehow made it past the 2-player check (shouldn't happen in
// practice; kept as defence-in-depth).
func isDuelResult(result *Result) bool {
	// DemoInfo is authoritative when it parsed a players list: it is KTX's
	// end-of-match snapshot and its players array lists exactly the match
	// participants — spectators are never included (find_plr in ktx only
	// returns ct == ctPlayer clients; DemoInfoAnalyzer projects the JSON
	// players array verbatim). So a 4-player team game with DemoInfo present
	// returns false here even if MatchAnalyzer happened to aggregate only
	// two players. This correctly covers KTX duel, Hoony duel, LGC
	// (2 players), 1v1 coop, and 1-player-vs-1-bot scenarios.
	//
	// A DemoInfo with NO players is not authoritative: a failed demoinfo
	// JSON parse yields a RawJSON-only DemoInfoResult (demoinfo.go
	// parseBlocks), and treating its empty players list as "0 participants"
	// would veto duel detection on a genuine duel. Fall through instead.
	if result.DemoInfo != nil && len(result.DemoInfo.Players) > 0 {
		return len(result.DemoInfo.Players) == 2
	}
	// No usable demoinfo: fall back to the MatchAnalyzer participant count.
	if result.Match != nil && len(result.Match.Players) == 2 {
		return true
	}
	return false
}

// mergeFragEventsByTime merges two already-sorted TimelineFragEvent
// slices into a single time-ordered slice. Used when the duel pass
// synthesises missing frag events from the obituary stream and needs
// to splice them back into the existing timeline series.
func mergeFragEventsByTime(a, b []TimelineFragEvent) []TimelineFragEvent {
	out := make([]TimelineFragEvent, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].Time <= b[j].Time {
			out = append(out, a[i])
			i++
		} else {
			out = append(out, b[j])
			j++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}
