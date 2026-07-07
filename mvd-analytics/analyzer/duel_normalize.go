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

	// Build the name → synthetic team map from the roster's participants. For
	// 1v1 this is literally `name → name`, but keeping the indirection makes
	// the rewrite loops below trivially extend to 1vN if we ever need it.
	nameToTeam := map[string]string{}
	for _, name := range r.Participants() {
		nameToTeam[name] = name
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

	// Rewrite pickup-data team labels. ItemAnalyzer / WeaponPickups /
	// Backpacks finalize before this post-processor runs and stamp the
	// raw userinfo team on every record, so without this pass the
	// frontend Pickups tab buckets duel pickups under stale colour
	// strings that no longer match any player's (rewritten) team.
	if result.Items != nil {
		for i := range result.Items.Items {
			it := &result.Items.Items[i]
			for j := range it.Phases {
				ph := &it.Phases[j]
				if ph.TakenBy == "" {
					continue
				}
				if t, ok := nameToTeam[ph.TakenBy]; ok {
					ph.Team = t
				}
			}
		}
	}
	for i := range result.WeaponPickups {
		wp := &result.WeaponPickups[i]
		if t, ok := nameToTeam[wp.Player]; ok {
			wp.Team = t
		}
		if wp.Dropper != "" {
			if t, ok := nameToTeam[wp.Dropper]; ok {
				wp.DropperTeam = t
			}
		}
	}
	for i := range result.Backpacks {
		bp := &result.Backpacks[i]
		if t, ok := nameToTeam[bp.Player]; ok {
			bp.Team = t
		}
	}

	// Rewrite shot-stream team labels. aimPost runs after this
	// post-processor and derives its per-player teams from Shots, so
	// fixing the stream here also fixes Aim.Players[].Team.
	if result.Shots != nil {
		for i := range result.Shots.Shots {
			s := &result.Shots.Shots[i]
			if t, ok := nameToTeam[s.Player]; ok {
				s.Team = t
			}
		}
		for i := range result.Shots.ByPlayer {
			p := &result.Shots.ByPlayer[i]
			if t, ok := nameToTeam[p.Player]; ok {
				p.Team = t
			}
		}

		// Correct victim classification. victimKindOf compares the raw
		// userinfo team strings at analyzer time, so a duel where both
		// players happen to share a non-empty colour team classifies
		// every hit on the opponent as "team". In a 1v1 any non-self
		// victim is by definition an enemy — flip those, restoring the
		// all-enemy-omitted wire convention (emitKinds) where the flip
		// leaves no informative kind. aimPost reads VictimKinds after
		// this pass, so the Aim enemy/team splits follow. (Damage has no
		// equivalent rewrite here: DamageAnalyzer classifies IsTeam
		// duel-aware at birth — isDuelResult in damage.go Finalize — so
		// its events, aggregates and matrix are already enemy-labelled.)
		for i := range result.Shots.Shots {
			s := &result.Shots.Shots[i]
			if s.VictimKinds == nil {
				continue
			}
			informative := false
			for j, k := range s.VictimKinds {
				if k == "team" {
					s.VictimKinds[j] = "enemy"
				}
				if s.VictimKinds[j] != "enemy" {
					informative = true
				}
			}
			if !informative {
				s.VictimKinds = nil
			}
		}
		// The per-weapon hit buckets count fires, not victims, but a
		// duel has exactly one opponent pair and victimKindOf is
		// deterministic per pair — so per shooter either every
		// opponent hit landed in TeamHits (shared colour team) or
		// every one landed in EnemyHits, never both. Folding TeamHits
		// into EnemyHits is therefore exact, not an approximation.
		for i := range result.Shots.ByPlayer {
			bw := result.Shots.ByPlayer[i].ByWeapon
			for j := range bw {
				if bw[j].TeamHits > 0 {
					bw[j].EnemyHits += bw[j].TeamHits
					bw[j].TeamHits = 0
				}
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
