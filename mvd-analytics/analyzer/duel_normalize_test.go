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

// The DemoInfo team rewrite moved from normalizeDuelTeams into
// RosterAnalyzer.PopulateCore (the analyzer that owns DemoInfo). It must
// stamp the synthetic one-player-per-team layout on a 1v1's DemoInfo.
func TestRoster_DemoInfoRewrite(t *testing.T) {
	di := &DemoInfoResult{
		Teams: []string{"green", "kis"},
		Players: []DemoInfoPlayer{
			{Name: "alice", Team: "green"},
			{Name: "bob", Team: "kis"},
		},
	}
	co := &CoreOutputs{DemoInfo: di}
	(&RosterAnalyzer{}).PopulateCore(co)

	if !co.Roster.Duel() {
		t.Fatalf("two demoinfo players should be a duel")
	}
	if len(di.Players) != 2 {
		t.Fatalf("expected 2 players, got %d", len(di.Players))
	}
	for _, p := range di.Players {
		if p.Team != p.Name {
			t.Errorf("player %q has team %q, want %q", p.Name, p.Team, p.Name)
		}
	}
	if len(di.Teams) != 2 || di.Teams[0] != "alice" || di.Teams[1] != "bob" {
		t.Errorf("DemoInfo.Teams = %v, want [alice bob]", di.Teams)
	}
}

// The Match.Players participant rebuild moved from normalizeDuelTeams into
// MatchAnalyzer.Finalize (rebuildDuelMatch). Regression: the LGC-vs-bot
// scenario where MatchAnalyzer dropped the bot entirely because its team was
// "" and it had no per-slot frag tracking — the demoinfo-authoritative merge
// must reconstruct both participants.
func TestRebuildDuelMatch_FromDemoInfo(t *testing.T) {
	di := &DemoInfoResult{
		Players: []DemoInfoPlayer{
			{Name: "chr1s", Team: "blue",
				Stats: &DemoInfoStats{Frags: 223, Kills: 150, Deaths: 15}},
			{Name: "/ bro", Team: "",
				Stats: &DemoInfoStats{Frags: 72, Kills: 15, Deaths: 39}},
		},
	}
	mr := &MatchResult{
		// MatchAnalyzer only saw chr1s — bot was filtered out.
		Players: []PlayerStat{
			{Name: "chr1s", Team: "blue", Frags: 223},
		},
		Teams: []TeamStat{{Name: "blue", Frags: 223}},
	}
	rebuildDuelMatch(mr, di)

	if len(mr.Players) != 2 {
		t.Fatalf("match.Players after rebuild: got %d players, want 2", len(mr.Players))
	}
	names := map[string]PlayerStat{}
	for _, p := range mr.Players {
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

	if len(mr.Teams) != 2 {
		t.Errorf("match.Teams has %d teams, want 2: %+v", len(mr.Teams), mr.Teams)
	}
}

// Pickup / stream / message producers stamp raw userinfo teams and then apply
// the roster label via co.TeamFor. This is the seam that replaced the per-
// section normalizeDuelTeams pickup/item/backpack/message rewrites: a tracked
// participant relabels to their own name, a non-participant (spectator, open
// phase) keeps its raw team. Producers whose player key isn't a participant
// name (or is empty) pass through unchanged.
func TestRoster_TeamForRelabelsParticipants(t *testing.T) {
	di := &DemoInfoResult{
		Players: []DemoInfoPlayer{
			{Name: "alice", Team: "green"},
			{Name: "bob", Team: ""},
		},
	}
	r := newRoster(di)
	if !r.Duel() {
		t.Fatalf("two demoinfo players should be a duel")
	}
	cases := []struct {
		name, raw, want string
	}{
		{"alice", "green", "alice"}, // picker/dropper participant → own name
		{"bob", "", "bob"},          // teamless participant → own name
		{"", "", ""},                // open phase (no owner) → untouched
		{"speccer", "obs", "obs"},   // non-participant spectator chat → raw team
	}
	for _, c := range cases {
		if got := r.TeamFor(c.name, c.raw); got != c.want {
			t.Errorf("TeamFor(%q,%q) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

// Shots team labels are still rewritten by normalizeDuelTeams until the shots
// producer migrates (block h). A participant's raw team relabels to their name.
func TestNormalizeDuelTeams_ShotTeamsRewritten(t *testing.T) {
	r := &Result{
		DemoInfo: &DemoInfoResult{
			Players: []DemoInfoPlayer{
				{Name: "alice", Team: "green"},
				{Name: "bob", Team: ""},
			},
		},
		Shots: &ShotsResult{
			Shots:    []Shot{{Player: "bob", Team: "", Weapon: "sg"}},
			ByPlayer: []PlayerShots{{Player: "alice", Team: "green"}},
		},
	}
	normalizeDuelTeams(r, newRoster(r.DemoInfo))

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
	normalizeDuelTeams(r, newRoster(r.DemoInfo))

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

func TestRoster_NoOpForTeamMatches(t *testing.T) {
	// 4 players → not a duel → roster leaves DemoInfo untouched, and TeamFor
	// passes raw teams through.
	di := &DemoInfoResult{
		Teams: []string{"red", "blue"},
		Players: []DemoInfoPlayer{
			{Name: "a", Team: "red"},
			{Name: "b", Team: "red"},
			{Name: "c", Team: "blue"},
			{Name: "d", Team: "blue"},
		},
	}
	co := &CoreOutputs{DemoInfo: di}
	(&RosterAnalyzer{}).PopulateCore(co)

	if co.Roster.Duel() {
		t.Fatalf("4-player match must not be a duel")
	}
	if di.Teams[0] != "red" || di.Teams[1] != "blue" {
		t.Errorf("team names should not be rewritten for 4-player match: %v", di.Teams)
	}
	for _, p := range di.Players {
		if p.Team == p.Name {
			t.Errorf("player %q team rewritten to name in non-duel match", p.Name)
		}
		if got := co.Roster.TeamFor(p.Name, p.Team); got != p.Team {
			t.Errorf("TeamFor(%q,%q)=%q, want raw team passthrough", p.Name, p.Team, got)
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
