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
// populated `result`.
func normalizeDuelTeams(result *Result) {
	if !isDuelResult(result) {
		return
	}

	// Build the slot/name → synthetic team map. For 1v1 this is literally
	// `name → name`, but keeping the indirection makes the rewrite loops
	// below trivially extend to 1vN if we ever need it.
	nameToTeam := map[string]string{}

	if result.DemoInfo != nil {
		for i := range result.DemoInfo.Players {
			p := &result.DemoInfo.Players[i]
			nameToTeam[p.Name] = p.Name
			p.Team = p.Name
		}
		// Rebuild the DemoInfo.Teams list so it reflects the synthetic
		// one-player-per-team layout. Order follows the player array.
		teams := make([]string, 0, len(result.DemoInfo.Players))
		for _, p := range result.DemoInfo.Players {
			teams = append(teams, p.Name)
		}
		result.DemoInfo.Teams = teams
	}

	if result.Match != nil {
		// MatchAnalyzer rejects players whose parser-side state looks
		// like a spectator (empty team, no svc_updatefrags events) —
		// that's how frogbots drop out of match.Players even though
		// they played the whole demo. In duel mode we trust
		// demoInfo.players as the source of truth for match
		// participants (it's KTX's end-of-match snapshot and always
		// includes every player it tracked stats for) and reconstruct
		// match.Players from it, merging in any per-player frag count
		// that MatchAnalyzer had already tracked.
		existing := map[string]PlayerStat{}
		for _, p := range result.Match.Players {
			existing[p.Name] = p
		}
		rebuilt := make([]PlayerStat, 0, len(existing))
		if result.DemoInfo != nil && len(result.DemoInfo.Players) > 0 {
			for _, dp := range result.DemoInfo.Players {
				ps, ok := existing[dp.Name]
				if !ok {
					ps = PlayerStat{Name: dp.Name}
				}
				ps.Team = dp.Name
				if dp.Stats != nil {
					ps.Frags = dp.Stats.Frags
					ps.Kills = dp.Stats.Kills
					ps.Deaths = dp.Stats.Deaths
				}
				rebuilt = append(rebuilt, ps)
				if _, ok := nameToTeam[dp.Name]; !ok {
					nameToTeam[dp.Name] = dp.Name
				}
			}
		} else {
			// No demoInfo to cross-check against — fall back to the
			// MatchAnalyzer's original list, just rewriting teams.
			for _, p := range result.Match.Players {
				p.Team = p.Name
				rebuilt = append(rebuilt, p)
				if _, ok := nameToTeam[p.Name]; !ok {
					nameToTeam[p.Name] = p.Name
				}
			}
		}
		result.Match.Players = rebuilt

		// Rebuild match.Teams from the merged player list.
		teams := make([]TeamStat, 0, len(result.Match.Players))
		for _, p := range result.Match.Players {
			teams = append(teams, TeamStat{
				Name:  p.Name,
				Frags: p.Frags,
			})
		}
		result.Match.Teams = teams
	}

	// Rebuild high-res bucket team data (TD) for duel mode. The original
	// TD was built during Finalize with the pre-normalization team names,
	// and players with team="" (e.g., bots) were skipped entirely. Rebuild
	// from the per-player data (P) using the synthetic name→name teams.
	if result.TimelineAnalysis != nil {
		for i := range result.TimelineAnalysis.HighResBuckets {
			hb := &result.TimelineAnalysis.HighResBuckets[i]
			if len(hb.P) == 0 {
				continue
			}
			newTD := make(map[string]*HighResTeamData, 2)
			for name, pd := range hb.P {
				team, ok := nameToTeam[name]
				if !ok {
					continue
				}
				td := newTD[team]
				if td == nil {
					td = &HighResTeamData{}
					newTD[team] = td
				}
				// Weapon categories (mutually exclusive)
				switch {
				case pd.RL && pd.LG:
					td.RLLG++
					td.W++
				case pd.RL:
					td.RL++
					td.W++
				case pd.LG:
					td.LG++
					td.W++
				}
				// Powerups
				if pd.Q {
					td.Q++
					td.Pw++
				}
				if pd.Pent {
					td.Pe++
					td.Pw++
				}
				if pd.R {
					td.R++
					td.Pw++
				}
				// Vitals
				td.TH += pd.H
				td.TA += pd.A
				if pd.AT != "" {
					if td.ABT == nil {
						td.ABT = make(map[string]int)
					}
					td.ABT[pd.AT]++
				}
			}
			hb.TD = newTD
		}
	}

	// Rewrite frag-event team strings. The timeline frag stream carries
	// a team label alongside each frag; it's used by the UI to colour
	// team-frag bars. Point it at the synthetic team.
	if result.TimelineAnalysis != nil {
		// Frogbots don't emit svc_updatefrags, so TimelineAnalyzer's
		// FragEvents stream is built from the real players only — the
		// bot simply never appears in it even though the match has all
		// its obituary frags in result.Frags.Frags. For the duel score
		// timeline to render correctly we synthesise the missing
		// player's entries from the obituary stream, which is sourced
		// from svc_print and captures bots and humans identically.
		if result.Frags != nil && len(result.Frags.Frags) > 0 {
			existingPlayers := map[string]bool{}
			for _, fe := range result.TimelineAnalysis.FragEvents {
				existingPlayers[fe.Player] = true
			}
			// Missing players are any name in nameToTeam (i.e. any
			// participant after the duel rewrite) that never appeared
			// as a killer in the timeline frag stream.
			missing := map[string]bool{}
			for name := range nameToTeam {
				if !existingPlayers[name] {
					missing[name] = true
				}
			}
			if len(missing) > 0 {
				synthesised := make([]TimelineFragEvent, 0)
				for _, fr := range result.Frags.Frags {
					if fr.Killer == "" {
						continue
					}
					if !missing[fr.Killer] {
						continue
					}
					delta := 1
					if fr.IsSuicide || fr.IsTeamKill {
						delta = -1
					}
					synthesised = append(synthesised, TimelineFragEvent{
						Time:   fr.Time,
						Player: fr.Killer,
						Team:   fr.Killer, // duel: team == name
						Delta:  delta,
					})
				}
				if len(synthesised) > 0 {
					// Merge by time so consumers that assume the slice
					// is monotonically non-decreasing (chart rendering,
					// binary search) keep working.
					merged := mergeFragEventsByTime(result.TimelineAnalysis.FragEvents, synthesised)
					result.TimelineAnalysis.FragEvents = merged
				}
			}
		}
		for i := range result.TimelineAnalysis.FragEvents {
			fe := &result.TimelineAnalysis.FragEvents[i]
			if t, ok := nameToTeam[fe.Player]; ok {
				fe.Team = t
			}
		}
		for i := range result.TimelineAnalysis.FragStreaks {
			fs := &result.TimelineAnalysis.FragStreaks[i]
			if t, ok := nameToTeam[fs.PlayerName]; ok {
				fs.Team = t
			}
		}
		for i := range result.TimelineAnalysis.PowerupEvents {
			pe := &result.TimelineAnalysis.PowerupEvents[i]
			if t, ok := nameToTeam[pe.PlayerName]; ok {
				pe.Team = t
			}
		}
	}

	// Rewrite chat / frag event team labels in messages so the timeline
	// chat pane paints them under the synthetic team colour.
	if result.Messages != nil {
		for i := range result.Messages.Events {
			e := &result.Messages.Events[i]
			if t, ok := nameToTeam[e.Player]; ok {
				e.Team = t
			}
		}
	}
}

// isDuelResult returns true when the match is a 1v1, using the number
// of match participants (spectators excluded — KTX never includes them
// in the demoinfo.players array) as the primary signal. Mode strings
// are a secondary fallback for demos that KTX tagged explicitly but
// that somehow made it past the 2-player check (shouldn't happen in
// practice; kept as defence-in-depth).
func isDuelResult(result *Result) bool {
	// Primary signal: exactly two match participants. This correctly
	// covers KTX duel, Hoony duel, LGC (2 players), 1v1 coop, and
	// 1-player-vs-1-bot scenarios.
	if result.DemoInfo != nil && len(result.DemoInfo.Players) == 2 {
		return true
	}
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

