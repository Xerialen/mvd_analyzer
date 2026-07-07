package analyzer

import "github.com/mvd-analyzer/mvd-analytics/view"

// Default post-processors for the registry. Each one is registered by
// NewDefaultRegistry; callers building a registry from scratch can
// pick which ones they want via RegisterPostProcessor.
//
// The old whole-Result time rebase (normalizeMatchRelativeTimes) and the
// demo-start-anchor fallback (deriveDemoStartAnchor) are gone: every producer
// now emits match-relative timestamps at its own Finalize by converting
// against co.Clock (clock.go), and the clock owns the demo-open wall-clock
// anchor the timeline writes onto Streams.Global. The rebasing helpers those
// producers share live in timeshift.go.

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

// locGraphPost runs BuildLocGraph on the assembled Result. Streams are already
// match-relative and carry final (roster) team labels (producers are born
// correct at Finalize), so the loc nodes/edges use the same team labels as the
// rest of the result.
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
// Streams are already match-relative and carry final (roster) team labels
// (producers are born correct at Finalize), so the classifier's per-player
// team labels are stable.
func regionControlPost(res *Result, _ *CoreOutputs) {
	if res == nil || res.TimelineAnalysis == nil {
		return
	}
	existing := res.TimelineAnalysis.RegionControl
	if existing == nil || len(existing.Regions) == 0 {
		return
	}
	rc, err := view.RegionControl(res, view.RegionControlOptions{})
	if err != nil {
		res.Errors = append(res.Errors, "region control: "+err.Error())
		return
	}
	if rc == nil {
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
