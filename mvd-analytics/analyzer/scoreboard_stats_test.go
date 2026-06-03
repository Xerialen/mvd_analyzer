package analyzer

import "testing"

// scoreboardStatsPost copies the frag-log-corrected kills/deaths onto the
// match scoreboard (joining on the final display name) and counts suicides
// from the IsSuicide frag entries per victim. It must leave a player with no
// data at 0/0/0 and must not panic on missing sections.
func TestScoreboardStatsPost_CopiesCorrectedKillsDeaths(t *testing.T) {
	res := &Result{
		Match: &MatchResult{Players: []PlayerStat{
			{Name: "speedball", Frags: 58},
			{Name: "mj", Frags: 6},
			{Name: "latecomer", Frags: 1}, // no data → stays 0/0/0
		}},
		Frags: &FragResult{
			ByPlayer: map[string]*PlayerFrags{
				"speedball": {Kills: 59, Deaths: 7},
				"mj":        {Kills: 6, Deaths: 59},
			},
			// Two self-deaths for speedball (a fall + an rl), one for mj; the
			// non-suicide entry must not be counted.
			Frags: []FragEntry{
				{Killer: "speedball", Victim: "speedball", Weapon: "fall", IsSuicide: true},
				{Killer: "speedball", Victim: "speedball", Weapon: "rl", IsSuicide: true},
				{Killer: "mj", Victim: "mj", Weapon: "lava", IsSuicide: true},
				{Killer: "speedball", Victim: "mj", Weapon: "rl"}, // real kill, not a suicide
			},
		},
	}

	scoreboardStatsPost(res, nil)

	// {kills, deaths, suicides}
	want := map[string][3]int{
		"speedball": {59, 7, 2},
		"mj":        {6, 59, 1},
		"latecomer": {0, 0, 0},
	}
	for _, p := range res.Match.Players {
		w := want[p.Name]
		if p.Kills != w[0] || p.Deaths != w[1] || p.Suicides != w[2] {
			t.Errorf("%s: got kills=%d deaths=%d suicides=%d, want %d/%d/%d",
				p.Name, p.Kills, p.Deaths, p.Suicides, w[0], w[1], w[2])
		}
	}
}

func TestScoreboardStatsPost_NilSafe(t *testing.T) {
	// None of these should panic.
	scoreboardStatsPost(&Result{}, nil)
	scoreboardStatsPost(&Result{Match: &MatchResult{}}, nil)
	scoreboardStatsPost(&Result{Frags: &FragResult{}}, nil)
	scoreboardStatsPost(&Result{
		Match: &MatchResult{Players: []PlayerStat{{Name: "x", Frags: 1}}},
		Frags: &FragResult{},
	}, nil)
}
