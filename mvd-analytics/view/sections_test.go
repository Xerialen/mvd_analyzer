package view

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func sectionsFixture() *result.Result {
	return &result.Result{
		Frags: &result.FragResult{
			TotalFrags: 3,
			ByWeapon:   map[string]int{"rl": 2, "lg": 1},
			ByPlayer: map[string]*result.PlayerFrags{
				"alpha": {Kills: 2, Deaths: 1, ByWeapon: map[string]int{"rl": 2}},
				"bravo": {Kills: 1, Deaths: 2, ByWeapon: map[string]int{"lg": 1}},
			},
			Frags: []result.FragEntry{
				{Time: 1000, Killer: "alpha", Victim: "bravo", Weapon: "rl"},
				{Time: 2000, Killer: "bravo", Victim: "alpha", Weapon: "lg"},
				{Time: 3000, Killer: "alpha", Victim: "bravo", Weapon: "rl"},
			},
		},
		Damage: &result.DamageResult{
			TotalDamage: 300,
			Telefrags:   []result.PositionalKill{{Time: 1500, Attacker: "alpha", Victim: "bravo"}},
			Stomps:      []result.PositionalKill{{Time: 1700, Attacker: "bravo", Victim: "alpha"}},
			Events: []result.DamageEntry{
				{Time: 1000, Attacker: "alpha", Victim: "bravo", Weapon: "rl", Damage: 100},
			},
		},
		Backpacks: []result.BackpackDrop{
			{Time: 1000, Player: "alpha", Weapon: "rl"},
			{Time: 2000, Player: "bravo", Weapon: "lg"},
		},
		WeaponPickups: []result.WeaponPickup{
			{Time: 1000, Player: "alpha", Weapon: "rl", Source: "world"},
			{Time: 2000, Player: "bravo", Weapon: "rl", Source: "backpack"},
		},
		Messages: &result.MessagesResult{
			Events: []result.MatchEvent{
				{Time: 5000, Type: "chat", Player: "alpha", Message: "gg"},
				{Time: 20000, Type: "teamsay", Player: "bravo", Message: "rl mid"},
				{Time: 30000, Type: "frag", Player: "alpha"},
			},
		},
	}
}

func TestFrags_UnavailableAndFilter(t *testing.T) {
	if _, err := Frags(&result.Result{}, FragOptions{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil Frags: want ErrUnavailable, got %v", err)
	}
	r := sectionsFixture()
	// Case-insensitive weapon CSV; player narrows both ByPlayer and the log.
	out, err := Frags(r, FragOptions{Players: []string{"alpha"}, Weapons: []string{"RL"}})
	if err != nil {
		t.Fatalf("Frags: %v", err)
	}
	if len(out.ByPlayer) != 1 || out.ByPlayer["alpha"] == nil {
		t.Errorf("byPlayer = %v, want only alpha", out.ByPlayer)
	}
	if len(out.Frags) != 2 { // both rl kills by alpha
		t.Errorf("frags = %d, want 2", len(out.Frags))
	}
}

func TestDamage_UnavailableAndPositional(t *testing.T) {
	if _, err := Damage(&result.Result{}, DamageOptions{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil Damage: want ErrUnavailable, got %v", err)
	}
	r := sectionsFixture()
	// weapon=tele selects telefrags only, excludes stomps and weapon events.
	out, err := Damage(r, DamageOptions{Weapons: []string{"tele"}})
	if err != nil {
		t.Fatalf("Damage: %v", err)
	}
	if len(out.Telefrags) != 1 {
		t.Errorf("telefrags = %d, want 1", len(out.Telefrags))
	}
	if len(out.Stomps) != 0 {
		t.Errorf("stomps = %d, want 0", len(out.Stomps))
	}
	if len(out.Events) != 0 {
		t.Errorf("events = %d, want 0 (rl excluded by weapon=tele)", len(out.Events))
	}
}

func TestItems_AlwaysAvailable(t *testing.T) {
	// Absent Items is NOT ErrUnavailable — it returns an empty list (R3).
	out := Items(&result.Result{}, ItemOptions{})
	if out == nil || out.Items == nil || len(out.Items) != 0 {
		t.Fatalf("nil Items: want empty list, got %+v", out)
	}
}

func TestBackpacks_WeaponCSV(t *testing.T) {
	r := sectionsFixture()
	// R4: weapon is a CSV set — both rl and lg match.
	if got := Backpacks(r, BackpackOptions{Weapons: []string{"rl", "lg"}}); len(got) != 2 {
		t.Errorf("weapon=rl,lg: got %d, want 2", len(got))
	}
	if got := Backpacks(r, BackpackOptions{Weapons: []string{"LG"}}); len(got) != 1 || got[0].Weapon != "lg" {
		t.Errorf("weapon=LG: got %v, want one lg", got)
	}
}

func TestWeaponPickups_Source(t *testing.T) {
	r := sectionsFixture()
	got := WeaponPickups(r, WeaponPickupOptions{Source: "backpack"})
	if len(got) != 1 || got[0].Source != "backpack" {
		t.Errorf("source=backpack: got %v", got)
	}
}

// filterFixture is a synthetic result with hand-authored frag/damage logs
// including a suicide and a teamkill across several timestamps. The STORED
// aggregates are deliberately NOT a recompute of the log (mimicking the real
// pipeline, where Deaths / some ByWeapon come from other authoritative
// sources), so a test can prove the unfiltered path returns the stored values
// verbatim rather than recomputing.
func filterFixture() *result.Result {
	return &result.Result{
		Frags: &result.FragResult{
			// Deliberately "wrong" vs the log (e.g. bogus TotalFrags/ByWeapon)
			// so unfiltered==stored is distinguishable from a recompute.
			TotalFrags: 99,
			ByWeapon:   map[string]int{"authoritative": 1},
			ByPlayer: map[string]*result.PlayerFrags{
				"alpha": {Kills: 42, Deaths: 7, ByWeapon: map[string]int{"rl": 42}},
			},
			Frags: []result.FragEntry{
				{Time: 1000, Killer: "alpha", Victim: "bravo", Weapon: "rl"},
				{Time: 2000, Killer: "bravo", Victim: "alpha", Weapon: "lg"},
				{Time: 3000, Killer: "alpha", Victim: "bravo", Weapon: "rl"},
				{Time: 4000, Killer: "alpha", Victim: "alpha", Weapon: "rl", IsSuicide: true},
				{Time: 5000, Killer: "alpha", Victim: "charlie", Weapon: "rl", IsTeamKill: true},
			},
		},
		Damage: &result.DamageResult{
			TotalDamage: 9999, // bogus vs log, to distinguish stored from recompute
			ByWeapon:    map[string]int{"authoritative": 1},
			ByPlayer: map[string]*result.PlayerDamage{
				"alpha": {Given: 999, Taken: 1, ByWeapon: map[string]int{"rl": 999}},
			},
			Matrix: []result.DamagePair{
				{Attacker: "zzz", Victim: "yyy", Damage: 1, ByWeapon: map[string]int{"rl": 1}},
			},
			Events: []result.DamageEntry{
				{Time: 1000, Attacker: "alpha", Victim: "bravo", Weapon: "rl", Damage: 100, VictimWep: "rl"},
				{Time: 2000, Attacker: "bravo", Victim: "alpha", Weapon: "lg", Damage: 40, VictimWep: "lg"},
				{Time: 3000, Attacker: "alpha", Victim: "bravo", Weapon: "rl", Damage: 60, VictimWep: "sg"},
				{Time: 4000, Attacker: "alpha", Victim: "alpha", Weapon: "rl", Damage: 25, IsSelf: true},
			},
		},
	}
}

func TestFrags_UnfilteredReturnsStored(t *testing.T) {
	r := filterFixture()
	out, err := Frags(r, FragOptions{})
	if err != nil {
		t.Fatalf("Frags: %v", err)
	}
	if out != r.Frags {
		t.Fatalf("unfiltered Frags should return the stored pointer unchanged")
	}
	if !reflect.DeepEqual(out, r.Frags) {
		t.Fatalf("unfiltered Frags != stored")
	}
}

func TestFrags_SummaryNoFilterKeepsStoredAggregates(t *testing.T) {
	r := filterFixture()
	out, err := Frags(r, FragOptions{Summary: true})
	if err != nil {
		t.Fatalf("Frags: %v", err)
	}
	if out.Frags != nil {
		t.Errorf("summary should drop the log, got %d entries", len(out.Frags))
	}
	// Aggregates must be the STORED (authoritative) ones, not a recompute.
	if out.TotalFrags != 99 || out.ByWeapon["authoritative"] != 1 {
		t.Errorf("summary w/o filter must keep stored aggregates, got total=%d byWeapon=%v",
			out.TotalFrags, out.ByWeapon)
	}
	// The shared stored Result must not be mutated.
	if r.Frags.Frags == nil {
		t.Errorf("summary mutated the shared stored Result (Frags nil'd)")
	}
}

func TestFrags_PlayerFilterRecomputes(t *testing.T) {
	r := filterFixture()
	out, err := Frags(r, FragOptions{Players: []string{"alpha"}})
	if err != nil {
		t.Fatalf("Frags: %v", err)
	}
	// Filtered log: every entry involves alpha (all 5). TotalFrags=5.
	if out.TotalFrags != 5 {
		t.Errorf("TotalFrags = %d, want 5 (recomputed from filtered log)", out.TotalFrags)
	}
	// ByPlayer restricted to alpha.
	if len(out.ByPlayer) != 1 || out.ByPlayer["alpha"] == nil {
		t.Fatalf("byPlayer = %v, want only alpha", out.ByPlayer)
	}
	a := out.ByPlayer["alpha"]
	// alpha kills: t1000 rl, t3000 rl (enemy). Suicide (t4000) and teamkill
	// (t5000) excluded from kills.
	if a.Kills != 2 {
		t.Errorf("alpha.Kills = %d, want 2", a.Kills)
	}
	// alpha deaths: victim in t2000 (killed by bravo) + t4000 suicide = 2.
	if a.Deaths != 2 {
		t.Errorf("alpha.Deaths = %d, want 2", a.Deaths)
	}
	if a.TeamKills != 1 {
		t.Errorf("alpha.TeamKills = %d, want 1", a.TeamKills)
	}
	if !reflect.DeepEqual(a.ByWeapon, map[string]int{"rl": 2}) {
		t.Errorf("alpha.ByWeapon = %v, want {rl:2}", a.ByWeapon)
	}
	// top-level ByWeapon: enemy kills only (excl suicide+teamkill):
	// t1000 rl, t2000 lg, t3000 rl => rl:2, lg:1.
	if !reflect.DeepEqual(out.ByWeapon, map[string]int{"rl": 2, "lg": 1}) {
		t.Errorf("ByWeapon = %v, want {rl:2, lg:1}", out.ByWeapon)
	}
}

func TestFrags_WeaponFilterRecomputes(t *testing.T) {
	r := filterFixture()
	out, err := Frags(r, FragOptions{Weapons: []string{"RL"}})
	if err != nil {
		t.Fatalf("Frags: %v", err)
	}
	// rl entries: t1000, t3000, t4000(suicide), t5000(teamkill) => 4 total.
	if out.TotalFrags != 4 {
		t.Errorf("TotalFrags = %d, want 4", out.TotalFrags)
	}
	// top-level ByWeapon: enemy rl kills only = t1000, t3000 => rl:2.
	if !reflect.DeepEqual(out.ByWeapon, map[string]int{"rl": 2}) {
		t.Errorf("ByWeapon = %v, want {rl:2}", out.ByWeapon)
	}
	// alpha: 2 enemy rl kills, 1 self-death (suicide), 1 teamkill.
	a := out.ByPlayer["alpha"]
	if a == nil || a.Kills != 2 || a.Deaths != 1 || a.TeamKills != 1 {
		t.Errorf("alpha = %+v, want kills=2 deaths=1 tk=1", a)
	}
}

func TestFrags_TimeWindow(t *testing.T) {
	r := filterFixture()
	// from-only: keep entries at t>=2.5s => t3000,t4000,t5000 (3).
	if out, _ := Frags(r, FragOptions{From: 2.5}); out.TotalFrags != 3 {
		t.Errorf("from=2.5: TotalFrags=%d, want 3", out.TotalFrags)
	}
	// to-only: keep entries at t<=2.5s => t1000,t2000 (2).
	if out, _ := Frags(r, FragOptions{To: 2.5}); out.TotalFrags != 2 {
		t.Errorf("to=2.5: TotalFrags=%d, want 2", out.TotalFrags)
	}
	// both: [1.5,4.5] => t2000,t3000,t4000 (3).
	if out, _ := Frags(r, FragOptions{From: 1.5, To: 4.5}); out.TotalFrags != 3 {
		t.Errorf("[1.5,4.5]: TotalFrags=%d, want 3", out.TotalFrags)
	}
}

func TestFrags_CombinedFilters(t *testing.T) {
	r := filterFixture()
	// players=alpha, weapon=rl, window [0.5,3.5]: rl+alpha in [1,3.5] =>
	// t1000, t3000 (both alpha rl enemy kills). t4000 suicide is >3.5.
	out, _ := Frags(r, FragOptions{Players: []string{"alpha"}, Weapons: []string{"rl"}, From: 0.5, To: 3.5})
	if out.TotalFrags != 2 {
		t.Errorf("combined: TotalFrags=%d, want 2", out.TotalFrags)
	}
	a := out.ByPlayer["alpha"]
	if a == nil || a.Kills != 2 || a.Deaths != 0 || a.TeamKills != 0 {
		t.Errorf("combined alpha = %+v, want kills=2 deaths=0 tk=0", a)
	}
}

func TestFrags_SummaryUnderFilterDropsLog(t *testing.T) {
	r := filterFixture()
	out, _ := Frags(r, FragOptions{Players: []string{"alpha"}, Summary: true})
	if out.Frags != nil {
		t.Errorf("summary+filter should drop the log")
	}
	if out.TotalFrags != 5 { // still recomputed from the filtered log
		t.Errorf("TotalFrags=%d, want 5 (recomputed)", out.TotalFrags)
	}
}

func TestDamage_UnfilteredReturnsStored(t *testing.T) {
	r := filterFixture()
	out, err := Damage(r, DamageOptions{})
	if err != nil {
		t.Fatalf("Damage: %v", err)
	}
	if out != r.Damage || !reflect.DeepEqual(out, r.Damage) {
		t.Fatalf("unfiltered Damage should return stored unchanged")
	}
	// Summary w/o filter keeps stored aggregates, drops Events only.
	so, _ := Damage(r, DamageOptions{Summary: true})
	if so.Events != nil {
		t.Errorf("summary should drop Events")
	}
	if so.TotalDamage != 9999 || so.ByWeapon["authoritative"] != 1 {
		t.Errorf("summary must keep stored aggregates, got total=%d", so.TotalDamage)
	}
	if r.Damage.Events == nil {
		t.Errorf("summary mutated the shared stored Result (Events nil'd)")
	}
}

func TestDamage_FilteredRecomputeMatchesStoredOnCleanStream(t *testing.T) {
	// A DamageResult whose Events are a full, in-match, self-consistent stream:
	// recomputing aggregates from the (unfiltered-by-value) Events must equal a
	// hand-computed authoritative set. Here we filter by a players set covering
	// everyone so the recompute path runs but no event is dropped.
	r := filterFixture()
	out, err := Damage(r, DamageOptions{Players: []string{"alpha", "bravo"}})
	if err != nil {
		t.Fatalf("Damage: %v", err)
	}
	// Events involving alpha or bravo = all 4.
	if len(out.Events) != 4 {
		t.Fatalf("events = %d, want 4", len(out.Events))
	}
	// TotalDamage = 100+40+60+25 = 225.
	if out.TotalDamage != 225 {
		t.Errorf("TotalDamage = %d, want 225", out.TotalDamage)
	}
	// alpha: Given = 100 (t1000) + 60 (t3000) = 160; GivenSelf = 25 (t4000);
	// Taken = 40 (from bravo) + 25 (self) = 65.
	a := out.ByPlayer["alpha"]
	if a.Given != 160 || a.GivenSelf != 25 || a.Taken != 65 {
		t.Errorf("alpha = given=%d givenSelf=%d taken=%d, want 160/25/65", a.Given, a.GivenSelf, a.Taken)
	}
	if !reflect.DeepEqual(a.ByWeapon, map[string]int{"rl": 160}) {
		t.Errorf("alpha.ByWeapon = %v, want {rl:160}", a.ByWeapon)
	}
	// alpha EWep buckets: t1000 victim rl (EnemyVsRL 100, EWep 100),
	// t3000 victim sg (EnemyVsSG 60, no EWep). EWep=100.
	if a.EnemyVsRL != 100 || a.EnemyVsSG != 60 || a.EWep != 100 {
		t.Errorf("alpha buckets: rl=%d sg=%d ewep=%d, want 100/60/100", a.EnemyVsRL, a.EnemyVsSG, a.EWep)
	}
	// bravo: Given = 40 (t2000, victim alpha holding lg); Taken = 100+60 = 160.
	b := out.ByPlayer["bravo"]
	if b.Given != 40 || b.Taken != 160 {
		t.Errorf("bravo = given=%d taken=%d, want 40/160", b.Given, b.Taken)
	}
	if b.EnemyVsLG != 40 || b.EWep != 40 {
		t.Errorf("bravo buckets: lg=%d ewep=%d, want 40/40", b.EnemyVsLG, b.EWep)
	}
	// top-level ByWeapon: enemy dmg by weapon = rl:160 (alpha), lg:40 (bravo).
	if !reflect.DeepEqual(out.ByWeapon, map[string]int{"rl": 160, "lg": 40}) {
		t.Errorf("ByWeapon = %v, want {rl:160, lg:40}", out.ByWeapon)
	}
}

func TestDamage_MatrixPopulatedWhenFiltered(t *testing.T) {
	r := filterFixture()
	// The QA-reported gap: filtered responses used to leave matrix null.
	out, _ := Damage(r, DamageOptions{Players: []string{"alpha", "bravo"}})
	if out.Matrix == nil {
		t.Fatalf("matrix must be populated when filtered")
	}
	// Enemy pairs only (self-damage excluded from matrix):
	//   alpha->bravo: 100+60 = 160 ; bravo->alpha: 40.
	want := []result.DamagePair{
		{Attacker: "alpha", Victim: "bravo", Damage: 160, ByWeapon: map[string]int{"rl": 160}},
		{Attacker: "bravo", Victim: "alpha", Damage: 40, ByWeapon: map[string]int{"lg": 40}},
	}
	if !reflect.DeepEqual(out.Matrix, want) {
		t.Errorf("matrix = %+v, want %+v", out.Matrix, want)
	}
}

func TestFilteredEmptyLogIsArrayNotNull(t *testing.T) {
	// null log = dropped by summary; [] log = included but the filter matched
	// nothing. A filter with no hits must serialize the log as [].
	r := filterFixture()
	d, _ := Damage(r, DamageOptions{Players: []string{"nobody"}})
	if d.Events == nil {
		t.Errorf("filtered-empty damage.events must be [], not null")
	}
	f, _ := Frags(r, FragOptions{Players: []string{"nobody"}})
	if f.Frags == nil {
		t.Errorf("filtered-empty frags.frags must be [], not null")
	}
}

func TestDamage_TimeWindowAndWeapon(t *testing.T) {
	r := filterFixture()
	// weapon=rl, window [0.5,3.5]: rl events at t1000,t3000 => total 160.
	out, _ := Damage(r, DamageOptions{Weapons: []string{"rl"}, From: 0.5, To: 3.5})
	if len(out.Events) != 2 || out.TotalDamage != 160 {
		t.Errorf("rl [0.5,3.5]: events=%d total=%d, want 2/160", len(out.Events), out.TotalDamage)
	}
	// to-only 1.5s: only t1000 (alpha->bravo 100).
	out2, _ := Damage(r, DamageOptions{To: 1.5})
	if len(out2.Events) != 1 || out2.TotalDamage != 100 {
		t.Errorf("to=1.5: events=%d total=%d, want 1/100", len(out2.Events), out2.TotalDamage)
	}
}

// TestDamage_AllPlayersRecomputeEqualsStored pins the source-gate invariant:
// with Damage.Events now match-gated at the analyzer, the stored aggregates
// ARE a pure fold of Events, so an all-players recompute (which reproduces the
// analyzer's fold) must equal the stored aggregates EXACTLY. This is the
// property the filter's "narrow everything" path relies on — the +420-style
// over-count from out-of-match events in the log is gone.
func TestDamage_AllPlayersRecomputeEqualsStored(t *testing.T) {
	// A self-consistent DamageResult: the stored aggregates are the true fold
	// of the (all in-match) Events, exactly as the analyzer would emit them.
	events := []result.DamageEntry{
		{Time: 1000, Attacker: "alpha", Victim: "bravo", Weapon: "rl", Damage: 100, VictimWep: "rl"},
		{Time: 2000, Attacker: "bravo", Victim: "alpha", Weapon: "lg", Damage: 40, VictimWep: "lg"},
		{Time: 3000, Attacker: "alpha", Victim: "bravo", Weapon: "rl", Damage: 60, VictimWep: "sg"},
	}
	stored := &result.DamageResult{
		TotalDamage: 200,
		ByWeapon:    map[string]int{"rl": 160, "lg": 40},
		ByPlayer: map[string]*result.PlayerDamage{
			"alpha": {Given: 160, Taken: 40, ByWeapon: map[string]int{"rl": 160},
				EnemyVsRL: 100, EnemyVsSG: 60, EWep: 100},
			"bravo": {Given: 40, Taken: 160, ByWeapon: map[string]int{"lg": 40},
				EnemyVsLG: 40, EWep: 40},
		},
		Matrix: []result.DamagePair{
			{Attacker: "alpha", Victim: "bravo", Damage: 160, ByWeapon: map[string]int{"rl": 160}},
			{Attacker: "bravo", Victim: "alpha", Damage: 40, ByWeapon: map[string]int{"lg": 40}},
		},
		Events: events,
	}
	r := &result.Result{Damage: stored}

	out, err := Damage(r, DamageOptions{Players: []string{"alpha", "bravo"}})
	if err != nil {
		t.Fatalf("Damage: %v", err)
	}
	if out.TotalDamage != stored.TotalDamage {
		t.Errorf("TotalDamage recompute=%d, stored=%d", out.TotalDamage, stored.TotalDamage)
	}
	if !reflect.DeepEqual(out.ByWeapon, stored.ByWeapon) {
		t.Errorf("ByWeapon recompute=%v, stored=%v", out.ByWeapon, stored.ByWeapon)
	}
	if !reflect.DeepEqual(out.ByPlayer, stored.ByPlayer) {
		t.Errorf("ByPlayer recompute=%v, stored=%v", out.ByPlayer, stored.ByPlayer)
	}
	if !reflect.DeepEqual(out.Matrix, stored.Matrix) {
		t.Errorf("Matrix recompute=%v, stored=%v", out.Matrix, stored.Matrix)
	}
}

// TestDamage_FromRoundsToNearestMs pins the seconds→ms rounding: a bound of
// 0.29s must map to 290ms (round), not 289ms (truncate). An event at exactly
// 290ms is then kept by from=0.29.
func TestDamage_FromRoundsToNearestMs(t *testing.T) {
	r := &result.Result{Damage: &result.DamageResult{
		ByWeapon: map[string]int{}, ByPlayer: map[string]*result.PlayerDamage{},
		Events: []result.DamageEntry{
			{Time: 290, Attacker: "alpha", Victim: "bravo", Weapon: "rl", Damage: 50, VictimWep: "rl"},
		},
	}}
	out, err := Damage(r, DamageOptions{From: 0.29})
	if err != nil {
		t.Fatalf("Damage: %v", err)
	}
	if len(out.Events) != 1 {
		t.Errorf("from=0.29 dropped the t=290ms event (truncation bug): events=%d, want 1", len(out.Events))
	}
}

func TestChat_DefaultsAndWindow(t *testing.T) {
	r := sectionsFixture()
	// Default types = chat,teamsay (the frag is excluded).
	if got := Chat(r, ChatOptions{}); len(got) != 2 {
		t.Errorf("default: got %d, want 2", len(got))
	}
	// Time window in seconds keeps only the teamsay at t=20s.
	got := Chat(r, ChatOptions{From: 15, To: 100})
	if len(got) != 1 || got[0].Type != "teamsay" {
		t.Errorf("window [15,100]: got %v", got)
	}
}

func itemsFixture() *result.Result {
	return &result.Result{Items: &result.ItemsResult{Items: []result.ItemTimeline{
		{
			Name: "ya_1", Kind: "ya", EntNum: 42, Loc: "tower",
			Phases: []result.ItemPhase{
				{AvailableFrom: 0, TakenAt: 5000, TakenBy: "p1", Team: "red", RespawnAt: 25000},
				{AvailableFrom: 25000, TakenAt: 30000, TakenBy: "p2", Team: "blue", RespawnAt: 50000},
				{AvailableFrom: 50000, TakenAt: 90000, TakenBy: "p1", Team: "red", RespawnAt: 110000},
				{AvailableFrom: 110000},
			},
		},
		{
			Name: "quad", Kind: "quad", EntNum: 43,
			Phases: []result.ItemPhase{
				{AvailableFrom: 0, TakenAt: 62000, TakenBy: "p2", Team: "blue"},
			},
		},
	}}}
}

// TestItems_Window: phases OVERLAPPING [from,to] survive; an open-ended
// phase (respawnAt 0) overlaps any later window.
func TestItems_Window(t *testing.T) {
	v := Items(itemsFixture(), ItemOptions{From: 26, To: 60})
	if len(v.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(v.Items))
	}
	ya := v.Items[0]
	// Phase[0] ends (respawns) at 25s < from=26 → dropped. Phase[1]
	// [25,50) and phase[2] [50,110) overlap. Phase[3] starts at 110 ≥
	// to=60 → dropped.
	if len(ya.Phases) != 2 || ya.Phases[0].TakenAt != 30000 || ya.Phases[1].TakenAt != 90000 {
		t.Fatalf("ya phases = %+v", ya.Phases)
	}
	// quad's single phase is open-ended (never respawned) → overlaps.
	if len(v.Items[1].Phases) != 1 {
		t.Fatalf("quad phases = %+v", v.Items[1].Phases)
	}
}

// TestItemsSummary_CountsInsideWindow: the summary counts takes INSIDE
// the window (not overlap), keeps zero-take items, and firstTake is the
// earliest counted take.
func TestItemsSummary_CountsInsideWindow(t *testing.T) {
	s := ItemsSummary(itemsFixture(), ItemOptions{To: 60})
	if len(s.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(s.Items))
	}
	ya := s.Items[0]
	if ya.TakenCount != 2 { // takes at 5s and 30s; the 90s take is outside
		t.Errorf("ya takenCount = %d, want 2", ya.TakenCount)
	}
	if ya.ByPlayer["p1"] != 1 || ya.ByPlayer["p2"] != 1 {
		t.Errorf("ya byPlayer = %+v", ya.ByPlayer)
	}
	if ya.FirstTake == nil || ya.FirstTake.T != 5.0 || ya.FirstTake.TakenBy != "p1" {
		t.Errorf("ya firstTake = %+v", ya.FirstTake)
	}
	quad := s.Items[1]
	if quad.TakenCount != 0 || quad.FirstTake != nil {
		t.Errorf("quad (taken at 62s, outside to=60) = %+v", quad)
	}
}

func TestItemsSummary_FullMatch(t *testing.T) {
	s := ItemsSummary(itemsFixture(), ItemOptions{})
	if s.Items[0].TakenCount != 3 {
		t.Errorf("ya takenCount = %d, want 3", s.Items[0].TakenCount)
	}
	if s.Items[1].FirstTake == nil || s.Items[1].FirstTake.T != 62.0 {
		t.Errorf("quad firstTake = %+v", s.Items[1].FirstTake)
	}
}

func TestBackpacks_Window(t *testing.T) {
	r := &result.Result{Backpacks: []result.BackpackDrop{
		{Time: 5000, Player: "p1", Weapon: "rl"},
		{Time: 65000, Player: "p2", Weapon: "lg"},
	}}
	out := Backpacks(r, BackpackOptions{From: 10, To: 70})
	if len(out) != 1 || out[0].Player != "p2" {
		t.Fatalf("windowed backpacks = %+v", out)
	}
}

func TestWeaponPickups_Window(t *testing.T) {
	r := &result.Result{WeaponPickups: []result.WeaponPickup{
		{Time: 5000, Player: "p1", Weapon: "rl", Source: "world"},
		{Time: 65000, Player: "p2", Weapon: "rl", Source: "backpack"},
	}}
	out := WeaponPickups(r, WeaponPickupOptions{To: 60})
	if len(out) != 1 || out[0].Player != "p1" {
		t.Fatalf("windowed pickups = %+v", out)
	}
}

// TestItems_WindowBoundaries: the window is CLOSED [from,to] like the
// sibling endpoints; a weapon-stay zero-length phase (takenAt ==
// respawnAt) landing exactly on `from` survives, and a take at exactly
// `to` counts in the summary.
func TestItems_WindowBoundaries(t *testing.T) {
	r := &result.Result{Items: &result.ItemsResult{Items: []result.ItemTimeline{
		{ // weapon-stay convention: zero-length unavailability at the take.
			Name: "rl_1", Kind: "rl", EntNum: 9,
			Phases: []result.ItemPhase{
				{AvailableFrom: 0, TakenAt: 30000, TakenBy: "p1", RespawnAt: 30000},
				{AvailableFrom: 30000},
			},
		},
	}}}
	v := Items(r, ItemOptions{From: 30})
	if len(v.Items) != 1 || len(v.Items[0].Phases) != 2 {
		t.Fatalf("zero-length phase at from boundary dropped: %+v", v.Items)
	}
	s := ItemsSummary(r, ItemOptions{From: 30})
	if s.Items[0].TakenCount != 1 {
		t.Errorf("take at exactly from: takenCount = %d, want 1", s.Items[0].TakenCount)
	}
	s = ItemsSummary(r, ItemOptions{To: 30})
	if s.Items[0].TakenCount != 1 {
		t.Errorf("take at exactly to: takenCount = %d, want 1 (closed window, getFrags parity)", s.Items[0].TakenCount)
	}
	s = ItemsSummary(r, ItemOptions{To: 29.999})
	if s.Items[0].TakenCount != 0 {
		t.Errorf("take just past to: takenCount = %d, want 0", s.Items[0].TakenCount)
	}
}
