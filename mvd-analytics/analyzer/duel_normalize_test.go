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
				{Name: "chr1s", Team: "blue", Frags: 223},
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

// Pickup / shot data carries the raw userinfo team stamped at analyzer
// Finalize time; the duel pass must re-point all of it at the synthetic
// name-per-player teams or the frontend's team-keyed pickup aggregation
// buckets everything under stale colour strings.
func TestNormalizeDuelTeams_PickupAndShotTeamsRewritten(t *testing.T) {
	r := &Result{
		DemoInfo: &DemoInfoResult{
			Players: []DemoInfoPlayer{
				{Name: "alice", Team: "green"},
				{Name: "bob", Team: ""},
			},
		},
		Items: &ItemsResult{Items: []ItemTimeline{
			{Kind: "ra", Phases: []ItemPhase{
				{TakenAt: 1000, TakenBy: "alice", Team: "green"},
				{TakenAt: 2000, TakenBy: "bob", Team: ""},
				{AvailableFrom: 3000}, // open phase — untouched
			}},
		}},
		WeaponPickups: []WeaponPickup{
			{Player: "alice", Team: "green", Weapon: "rl", Source: "world"},
			{Player: "bob", Team: "", Weapon: "rl", Source: "backpack",
				Dropper: "alice", DropperTeam: "green"},
		},
		Backpacks: []BackpackDrop{
			{Player: "alice", Team: "green", Weapon: "rl"},
		},
		Shots: &ShotsResult{
			Shots:    []Shot{{Player: "bob", Team: "", Weapon: "sg"}},
			ByPlayer: []PlayerShots{{Player: "alice", Team: "green"}},
		},
	}
	normalizeDuelTeams(r)

	phases := r.Items.Items[0].Phases
	if phases[0].Team != "alice" || phases[1].Team != "bob" {
		t.Errorf("item phase teams = %q/%q, want alice/bob", phases[0].Team, phases[1].Team)
	}
	if phases[2].Team != "" {
		t.Errorf("open phase team = %q, want untouched empty", phases[2].Team)
	}
	if r.WeaponPickups[0].Team != "alice" {
		t.Errorf("weaponPickups[0].Team = %q, want alice", r.WeaponPickups[0].Team)
	}
	if r.WeaponPickups[1].Team != "bob" || r.WeaponPickups[1].DropperTeam != "alice" {
		t.Errorf("weaponPickups[1] teams = %q/%q, want bob/alice",
			r.WeaponPickups[1].Team, r.WeaponPickups[1].DropperTeam)
	}
	if r.Backpacks[0].Team != "alice" {
		t.Errorf("backpacks[0].Team = %q, want alice", r.Backpacks[0].Team)
	}
	if r.Shots.Shots[0].Team != "bob" || r.Shots.ByPlayer[0].Team != "alice" {
		t.Errorf("shot teams = %q/%q, want bob/alice",
			r.Shots.Shots[0].Team, r.Shots.ByPlayer[0].Team)
	}
}

// victimKindOf compares raw userinfo team strings at analyzer time, so
// a duel where both players share a non-empty colour team classifies
// every opponent hit as "team". The duel pass reclassifies: in a 1v1
// any non-self victim is an enemy, all-enemy kind slices fold back to
// the omitted wire form, and the per-weapon team buckets fold into the
// enemy buckets (exact — one opponent pair classifies uniformly).
func TestNormalizeDuelTeams_VictimKindsReclassified(t *testing.T) {
	r := &Result{
		DemoInfo: &DemoInfoResult{
			Players: []DemoInfoPlayer{
				{Name: "alice", Team: "red"},
				{Name: "bob", Team: "red"}, // same colour team → analyzer said "team"
			},
		},
		Shots: &ShotsResult{
			Shots: []Shot{
				{Player: "alice", Team: "red", Weapon: "lg", Hit: true,
					Victims: []string{"bob"}, VictimKinds: []string{"team"}},
				{Player: "alice", Team: "red", Weapon: "rl", Hit: true,
					Victims: []string{"bob", "alice"}, VictimKinds: []string{"team", "self"}},
				{Player: "bob", Team: "red", Weapon: "rl", Hit: true,
					Victims: []string{"bob"}, VictimKinds: []string{"self"}},
			},
			ByPlayer: []PlayerShots{
				{Player: "alice", Team: "red", ByWeapon: []WeaponShots{
					{Weapon: "lg", Shots: 10, Hits: 4, TeamHits: 4},
					{Weapon: "rl", Shots: 6, Hits: 3, TeamHits: 2, SelfHits: 1},
				}},
			},
		},
	}
	normalizeDuelTeams(r)

	if r.Shots.Shots[0].VictimKinds != nil {
		t.Errorf("all-enemy kinds should fold to omitted, got %v", r.Shots.Shots[0].VictimKinds)
	}
	if got := r.Shots.Shots[1].VictimKinds; len(got) != 2 || got[0] != "enemy" || got[1] != "self" {
		t.Errorf("kinds = %v, want [enemy self]", got)
	}
	if got := r.Shots.Shots[2].VictimKinds; len(got) != 1 || got[0] != "self" {
		t.Errorf("self-only kinds must survive, got %v", got)
	}
	bw := r.Shots.ByPlayer[0].ByWeapon
	if bw[0].EnemyHits != 4 || bw[0].TeamHits != 0 {
		t.Errorf("lg buckets = %+v, want enemyHits=4 teamHits=0", bw[0])
	}
	if bw[1].EnemyHits != 2 || bw[1].TeamHits != 0 || bw[1].SelfHits != 1 {
		t.Errorf("rl buckets = %+v, want enemyHits=2 teamHits=0 selfHits=1", bw[1])
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

func TestMergeFragEventsByTime(t *testing.T) {
	a := []TimelineFragEvent{
		{Time: 1000, Player: "a"},
		{Time: 5000, Player: "a"},
		{Time: 10000, Player: "a"},
	}
	b := []TimelineFragEvent{
		{Time: 3000, Player: "b"},
		{Time: 7000, Player: "b"},
	}
	merged := mergeFragEventsByTime(a, b)
	wantTimes := []int32{1000, 3000, 5000, 7000, 10000}
	if len(merged) != len(wantTimes) {
		t.Fatalf("merged len = %d, want %d", len(merged), len(wantTimes))
	}
	for i, fe := range merged {
		if fe.Time != wantTimes[i] {
			t.Errorf("merged[%d].Time = %v, want %v", i, fe.Time, wantTimes[i])
		}
	}
}
