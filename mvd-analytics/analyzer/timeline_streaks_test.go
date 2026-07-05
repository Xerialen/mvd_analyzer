package analyzer

import "testing"

// newStreakTestAnalyzer builds a TimelineAnalyzer wired just far enough
// for detectFragStreaks: match timing set, slot→name resolution via
// playerNames, and the canonical frag log via CoreOutputs.
func newStreakTestAnalyzer(startTime, endTime float64, names map[int]string, frags []FragEntry) *TimelineAnalyzer {
	a := NewTimelineAnalyzer()
	a.ctx = &Context{}
	a.timing.Started = true
	a.timing.StartTime = startTime
	a.timing.Ended = true
	a.timing.EndTime = endTime
	for slot, name := range names {
		a.playerNames[slot] = name
	}
	a.UseCoreOutputs(&CoreOutputs{FragEntries: frags})
	return a
}

func findStreak(streaks []FragStreakEvent, player string, frags int) *FragStreakEvent {
	for i := range streaks {
		if streaks[i].PlayerName == player && streaks[i].Frags == frags {
			return &streaks[i]
		}
	}
	return nil
}

// TestFragStreaks_InitialSpawnRun reproduces gameId 224758: a player who
// is already alive when the match starts has no recorded spawn for that
// first life (the parser's initial SpawnEvent fires during warmup and is
// dropped as pre-match), so the run from match start to first death was
// never built. Here the dominating player racks up 5 frags before dying
// once — the 5-frag opening run must exist and start at match start.
func TestFragStreaks_InitialSpawnRun(t *testing.T) {
	frags := []FragEntry{
		{Time: 30000, Killer: "dom", Victim: "prey", Weapon: "rl"},
		{Time: 60000, Killer: "dom", Victim: "prey", Weapon: "rl"},
		{Time: 90000, Killer: "dom", Victim: "prey", Weapon: "rl"},
		{Time: 120000, Killer: "dom", Victim: "prey", Weapon: "lg"},
		{Time: 150000, Killer: "dom", Victim: "prey", Weapon: "rl"},
		{Time: 200000, Killer: "prey", Victim: "dom", Weapon: "rl"},
		{Time: 250000, Killer: "dom", Victim: "prey", Weapon: "rl"},
	}
	a := newStreakTestAnalyzer(10.0, 310.0, map[int]string{0: "dom", 1: "prey"}, frags)

	// dom's only death/respawn cycle. prey dies five times, respawning
	// two seconds after each death; prey's initial spawn is likewise
	// unrecorded.
	a.rawDeaths = append(a.rawDeaths, deathEvent{Time: 200.0, PlayerNum: 0})
	a.rawSpawns = append(a.rawSpawns, deathEvent{Time: 202.0, PlayerNum: 0})
	for _, dt := range []float64{30, 60, 90, 120, 150} {
		a.rawDeaths = append(a.rawDeaths, deathEvent{Time: dt, PlayerNum: 1})
		a.rawSpawns = append(a.rawSpawns, deathEvent{Time: dt + 2, PlayerNum: 1})
	}
	// prey also dies once more mid-match after dom's respawn.
	a.rawDeaths = append(a.rawDeaths, deathEvent{Time: 250.0, PlayerNum: 1})
	a.rawSpawns = append(a.rawSpawns, deathEvent{Time: 252.0, PlayerNum: 1})

	streaks := a.detectFragStreaks(10, nil, map[string]int{})

	opening := findStreak(streaks, "dom", 5)
	if opening == nil {
		t.Fatalf("missing dom's 5-frag opening run; got %+v", streaks)
	}
	if opening.Time != 10000 {
		t.Errorf("opening run starts at %d ms, want 10000 (match start)", opening.Time)
	}
	if opening.EndTime != 200000 {
		t.Errorf("opening run ends at %d ms, want 200000 (first death)", opening.EndTime)
	}
	if opening.Ewep != "rl" {
		t.Errorf("opening run ewep = %q, want rl", opening.Ewep)
	}
	if second := findStreak(streaks, "dom", 1); second == nil {
		t.Errorf("missing dom's 1-frag post-respawn run; got %+v", streaks)
	}
}

// TestFragStreaks_NeverDiedPlayer is the extreme of the same bug: a
// player who never dies has no spawn or death records at all, so before
// the synthetic match-start spawn they had no runs — and no streak —
// despite fragging the whole match.
func TestFragStreaks_NeverDiedPlayer(t *testing.T) {
	frags := []FragEntry{
		{Time: 20000, Killer: "dom", Victim: "prey", Weapon: "rl"},
		{Time: 40000, Killer: "dom", Victim: "prey", Weapon: "rl"},
		{Time: 60000, Killer: "dom", Victim: "prey", Weapon: "rl"},
	}
	a := newStreakTestAnalyzer(10.0, 300.0, map[int]string{0: "dom", 1: "prey"}, frags)
	for _, dt := range []float64{20, 40, 60} {
		a.rawDeaths = append(a.rawDeaths, deathEvent{Time: dt, PlayerNum: 1})
		a.rawSpawns = append(a.rawSpawns, deathEvent{Time: dt + 2, PlayerNum: 1})
	}

	streaks := a.detectFragStreaks(10, nil, map[string]int{})

	full := findStreak(streaks, "dom", 3)
	if full == nil {
		t.Fatalf("missing dom's full-match run; got %+v", streaks)
	}
	if full.Time != 10000 || full.EndTime != 300000 {
		t.Errorf("full-match run spans [%d, %d] ms, want [10000, 300000]", full.Time, full.EndTime)
	}
}

// TestFragStreaks_MatchStartResetKill reproduces the nlk pairing shift
// from gameId 212260: KTX's match-start reset surfaces as a DeathEvent
// at exactly StartTime. That death must not earn a synthetic spawn —
// doing so pairs the synthetic spawn with the player's first *real*
// death and shifts every later run off by one life (runs then span
// recorded deaths and double-count frags).
func TestFragStreaks_MatchStartResetKill(t *testing.T) {
	frags := []FragEntry{
		// Two frags in the first real life, one in the second.
		{Time: 20000, Killer: "nlk", Victim: "prey", Weapon: "rl"},
		{Time: 30000, Killer: "nlk", Victim: "prey", Weapon: "rl"},
		{Time: 60000, Killer: "nlk", Victim: "prey", Weapon: "sg"},
	}
	a := newStreakTestAnalyzer(10.0, 300.0, map[int]string{0: "nlk", 1: "prey"}, frags)

	// Reset kill at exactly match start, respawn, then a real
	// death/respawn cycle.
	a.rawDeaths = append(a.rawDeaths, deathEvent{Time: 10.0, PlayerNum: 0})
	a.rawSpawns = append(a.rawSpawns, deathEvent{Time: 10.5, PlayerNum: 0})
	a.rawDeaths = append(a.rawDeaths, deathEvent{Time: 40.0, PlayerNum: 0})
	a.rawSpawns = append(a.rawSpawns, deathEvent{Time: 42.0, PlayerNum: 0})
	for _, dt := range []float64{20, 30, 60} {
		a.rawDeaths = append(a.rawDeaths, deathEvent{Time: dt, PlayerNum: 1})
		a.rawSpawns = append(a.rawSpawns, deathEvent{Time: dt + 2, PlayerNum: 1})
	}

	streaks := a.detectFragStreaks(10, nil, map[string]int{})

	first := findStreak(streaks, "nlk", 2)
	if first == nil {
		t.Fatalf("missing nlk's 2-frag first real life; got %+v", streaks)
	}
	if first.Time != 10500 || first.EndTime != 40000 {
		t.Errorf("first life spans [%d, %d] ms, want [10500, 40000]", first.Time, first.EndTime)
	}
	second := findStreak(streaks, "nlk", 1)
	if second == nil {
		t.Fatalf("missing nlk's 1-frag second life; got %+v", streaks)
	}
	if second.Time != 42000 || second.EndTime != 300000 {
		t.Errorf("second life spans [%d, %d] ms, want [42000, 300000]", second.Time, second.EndTime)
	}
	// No run may span another recorded death of the same player — that
	// is the off-by-one-life signature.
	for _, s := range streaks {
		if s.PlayerName == "nlk" && s.Time < 40000 && s.EndTime > 40000 {
			t.Errorf("run [%d, %d] spans nlk's recorded death at 40000 — pairing shifted", s.Time, s.EndTime)
		}
	}
}

// TestFragStreaks_MidMatchJoinerUnaffected guards the synthetic spawn's
// condition: a player whose first recorded event is a mid-match spawn
// (spectator joining the game) has no death or frag before it, so their
// first run must start at that spawn — not get stretched back to match
// start.
func TestFragStreaks_MidMatchJoinerUnaffected(t *testing.T) {
	frags := []FragEntry{
		{Time: 120000, Killer: "late", Victim: "prey", Weapon: "rl"},
	}
	a := newStreakTestAnalyzer(10.0, 300.0, map[int]string{2: "late", 1: "prey"}, frags)
	a.rawSpawns = append(a.rawSpawns, deathEvent{Time: 100.0, PlayerNum: 2})
	a.rawDeaths = append(a.rawDeaths, deathEvent{Time: 120.0, PlayerNum: 1})
	a.rawSpawns = append(a.rawSpawns, deathEvent{Time: 122.0, PlayerNum: 1})

	streaks := a.detectFragStreaks(10, nil, map[string]int{})

	run := findStreak(streaks, "late", 1)
	if run == nil {
		t.Fatalf("missing late-joiner's run; got %+v", streaks)
	}
	if run.Time != 100000 {
		t.Errorf("late-joiner's run starts at %d ms, want 100000 (their actual spawn)", run.Time)
	}
}
