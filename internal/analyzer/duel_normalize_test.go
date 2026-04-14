package analyzer

import "testing"

func TestIsDuelResult(t *testing.T) {
	cases := []struct {
		name string
		r    *Result
		want bool
	}{
		{
			name: "two demoinfo players",
			r: &Result{
				DemoInfo: &DemoInfoResult{
					Players: []DemoInfoPlayer{{Name: "a"}, {Name: "b"}},
				},
			},
			want: true,
		},
		{
			name: "four demoinfo players",
			r: &Result{
				DemoInfo: &DemoInfoResult{
					Players: []DemoInfoPlayer{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}},
				},
			},
			want: false,
		},
		{
			name: "no demoinfo, two match players",
			r: &Result{
				Match: &MatchResult{Players: []PlayerStat{{Name: "a"}, {Name: "b"}}},
			},
			want: true,
		},
		{
			name: "no demoinfo, no match",
			r:    &Result{},
			want: false,
		},
		{
			name: "one demoinfo player",
			r: &Result{
				DemoInfo: &DemoInfoResult{
					Players: []DemoInfoPlayer{{Name: "solo"}},
				},
			},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isDuelResult(c.r)
			if got != c.want {
				t.Errorf("isDuelResult = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNormalizeDuelTeams_DemoInfoRewrite(t *testing.T) {
	r := &Result{
		DemoInfo: &DemoInfoResult{
			Teams: []string{"green", "kis"},
			Players: []DemoInfoPlayer{
				{Name: "alice", Team: "green"},
				{Name: "bob", Team: "kis"},
			},
		},
	}
	normalizeDuelTeams(r)

	if len(r.DemoInfo.Players) != 2 {
		t.Fatalf("expected 2 players, got %d", len(r.DemoInfo.Players))
	}
	for _, p := range r.DemoInfo.Players {
		if p.Team != p.Name {
			t.Errorf("player %q has team %q, want %q", p.Name, p.Team, p.Name)
		}
	}
	if len(r.DemoInfo.Teams) != 2 || r.DemoInfo.Teams[0] != "alice" || r.DemoInfo.Teams[1] != "bob" {
		t.Errorf("DemoInfo.Teams = %v, want [alice bob]", r.DemoInfo.Teams)
	}
}

func TestNormalizeDuelTeams_MatchRebuildFromDemoInfo(t *testing.T) {
	// Regression test: the LGC-vs-bot scenario where MatchAnalyzer dropped
	// the bot entirely because its team was "" and it had no per-slot
	// frag tracking. The normalizer should reconstruct the participant
	// list from demoInfo so both players appear in match.players.
	r := &Result{
		DemoInfo: &DemoInfoResult{
			Players: []DemoInfoPlayer{
				{Name: "chr1s", Team: "blue",
					Stats: &DemoInfoStats{Frags: 223, Kills: 150, Deaths: 15}},
				{Name: "/ bro", Team: "",
					Stats: &DemoInfoStats{Frags: 72, Kills: 15, Deaths: 39}},
			},
		},
		Match: &MatchResult{
			// MatchAnalyzer only saw chr1s — bot was filtered out.
			Players: []PlayerStat{
				{Name: "chr1s", Team: "blue", Frags: 223, Kills: 150, Deaths: 15},
			},
			Teams: []TeamStat{{Name: "blue", Frags: 223}},
		},
	}
	normalizeDuelTeams(r)

	if len(r.Match.Players) != 2 {
		t.Fatalf("match.Players after normalize: got %d players, want 2", len(r.Match.Players))
	}
	names := map[string]PlayerStat{}
	for _, p := range r.Match.Players {
		names[p.Name] = p
	}
	chr1s, ok := names["chr1s"]
	if !ok {
		t.Fatalf("chr1s missing from match.Players")
	}
	if chr1s.Team != "chr1s" || chr1s.Frags != 223 {
		t.Errorf("chr1s = %+v, want team=chr1s frags=223", chr1s)
	}
	bro, ok := names["/ bro"]
	if !ok {
		t.Fatalf("/ bro missing from match.Players — LGC regression")
	}
	if bro.Team != "/ bro" || bro.Frags != 72 {
		t.Errorf("bro = %+v, want team=/ bro frags=72", bro)
	}

	if len(r.Match.Teams) != 2 {
		t.Errorf("match.Teams has %d teams, want 2: %+v", len(r.Match.Teams), r.Match.Teams)
	}
}

func TestNormalizeDuelTeams_TimelineBucketRewrite(t *testing.T) {
	r := &Result{
		DemoInfo: &DemoInfoResult{
			Players: []DemoInfoPlayer{
				{Name: "a", Team: "green"},
				{Name: "b", Team: ""},
			},
		},
		TimelineAnalysis: &TimelineAnalysisResult{
			Buckets: []TimelineBucket{
				{
					StartTime: 0, EndTime: 1,
					PlayerData: map[string]*PlayerBucketData{
						"a": {Team: "green", HasRL: true, HasLG: true, Health: 100, Armor: 200, ArmorType: "ra"},
						"b": {Team: "", HasLG: true, Health: 50, Armor: 0, HasQuad: true},
					},
					// Original teamData: only "green" present (bot with team="" dropped).
					TeamData: map[string]*TeamBucketData{
						"green": {PlayersWithRLLG: 1, PlayersWithWeapons: 1, AvgHealth: 100, AvgArmor: 200, TotalHealth: 100, TotalArmor: 200},
					},
				},
			},
		},
	}
	normalizeDuelTeams(r)

	b := &r.TimelineAnalysis.Buckets[0]
	if len(b.TeamData) != 2 {
		t.Fatalf("teamData has %d entries, want 2", len(b.TeamData))
	}
	at, ok := b.TeamData["a"]
	if !ok {
		t.Fatalf("teamData missing 'a'")
	}
	if at.PlayersWithRLLG != 1 || at.AvgHealth != 100 || at.AvgArmor != 200 || at.TotalHealth != 100 {
		t.Errorf("team a: %+v — expected RLLG=1 hp=200 armor=200 totalH=100", at)
	}
	bt, ok := b.TeamData["b"]
	if !ok {
		t.Fatalf("teamData missing 'b' — duel normalizer failed to restore the team=\"\" player")
	}
	if bt.PlayersWithLG != 1 || bt.PlayersWithQuad != 1 || bt.AvgHealth != 50 {
		t.Errorf("team b: %+v — expected LG=1 quad=1 hp=50", bt)
	}
	// Player bucket data should have its team rewritten too.
	if b.PlayerData["a"].Team != "a" || b.PlayerData["b"].Team != "b" {
		t.Errorf("playerData teams not rewritten: a=%q b=%q", b.PlayerData["a"].Team, b.PlayerData["b"].Team)
	}
}

func TestNormalizeDuelTeams_NoOpForTeamMatches(t *testing.T) {
	// 4 players → not a duel → normalizer should leave everything alone.
	r := &Result{
		DemoInfo: &DemoInfoResult{
			Teams: []string{"red", "blue"},
			Players: []DemoInfoPlayer{
				{Name: "a", Team: "red"},
				{Name: "b", Team: "red"},
				{Name: "c", Team: "blue"},
				{Name: "d", Team: "blue"},
			},
		},
	}
	normalizeDuelTeams(r)
	if r.DemoInfo.Teams[0] != "red" || r.DemoInfo.Teams[1] != "blue" {
		t.Errorf("team names should not be rewritten for 4-player match: %v", r.DemoInfo.Teams)
	}
	for _, p := range r.DemoInfo.Players {
		if p.Team == p.Name {
			t.Errorf("player %q team rewritten to name in non-duel match", p.Name)
		}
	}
}

func TestTeamBucketFromPlayer(t *testing.T) {
	pd := &PlayerBucketData{
		Team: "ignored", HasRL: true, HasLG: true,
		HasQuad: true,
		Health:  150, Armor: 100, ArmorType: "ya",
		Shells: 50, Nails: 100, Rockets: 10, Cells: 80,
	}
	td := teamBucketFromPlayer(pd)
	if td.PlayersWithRLLG != 1 {
		t.Errorf("PlayersWithRLLG = %d, want 1", td.PlayersWithRLLG)
	}
	if td.PlayersWithQuad != 1 || td.PlayersWithPowerups != 1 {
		t.Errorf("quad = %d powerups = %d, want 1/1", td.PlayersWithQuad, td.PlayersWithPowerups)
	}
	if td.AvgHealth != 150 || td.AvgArmor != 100 || td.TotalHealth != 150 {
		t.Errorf("vitals wrong: %+v", td)
	}
	if td.ArmorByType["ya"] != 1 {
		t.Errorf("armorByType = %v, want ya:1", td.ArmorByType)
	}
	if td.TotalShells != 50 || td.TotalCells != 80 {
		t.Errorf("ammo wrong: %+v", td)
	}
}

func TestMergeFragEventsByTime(t *testing.T) {
	a := []TimelineFragEvent{
		{Time: 1.0, Player: "a"},
		{Time: 5.0, Player: "a"},
		{Time: 10.0, Player: "a"},
	}
	b := []TimelineFragEvent{
		{Time: 3.0, Player: "b"},
		{Time: 7.0, Player: "b"},
	}
	merged := mergeFragEventsByTime(a, b)
	wantTimes := []float64{1, 3, 5, 7, 10}
	if len(merged) != len(wantTimes) {
		t.Fatalf("merged len = %d, want %d", len(merged), len(wantTimes))
	}
	for i, fe := range merged {
		if fe.Time != wantTimes[i] {
			t.Errorf("merged[%d].Time = %v, want %v", i, fe.Time, wantTimes[i])
		}
	}
}
