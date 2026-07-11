package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// deathtype ints (KTX deathtype.h) used by the damage stream.
const (
	dtSGTest    = 2
	dtRLTest    = 7
	dtStompTest = 17 // dtSTOMP
	dtFallTest  = 16
	dtTeleTest  = 18 // dtTELE1
)

// buildDamageAnalyzer wires an analyzer with a red attacker (slot 0), a red
// teammate (slot 6), and five blue victims (slots 1-5) each holding a
// different weapon class, plus CoreOutputs and a KTX scoreboard.
func buildDamageAnalyzer() *DamageAnalyzer {
	a := NewDamageAnalyzer()
	ctx := &Context{}
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "alpha", Team: "red"}
	ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "bsg", Team: "blue"}
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "cmid", Team: "blue"}
	ctx.Players[3] = &events.PlayerInfo{Slot: 3, Name: "dlg", Team: "blue"}
	ctx.Players[4] = &events.PlayerInfo{Slot: 4, Name: "erl", Team: "blue"}
	ctx.Players[5] = &events.PlayerInfo{Slot: 5, Name: "fboth", Team: "blue"}
	ctx.Players[6] = &events.PlayerInfo{Slot: 6, Name: "gmate", Team: "red"}
	_ = a.Init(ctx)
	a.timing.Started = true
	return a
}

func damageCore() *CoreOutputs {
	return &CoreOutputs{Slots: map[int]SlotInfo{
		0: {Name: "alpha", Team: "red"},
		1: {Name: "bsg", Team: "blue"},
		2: {Name: "cmid", Team: "blue"},
		3: {Name: "dlg", Team: "blue"},
		4: {Name: "erl", Team: "blue"},
		5: {Name: "fboth", Team: "blue"},
		6: {Name: "gmate", Team: "red"},
	}}
}

func TestDamageAnalyzer_EWepBucketsByVictimWeapon(t *testing.T) {
	a := buildDamageAnalyzer()

	// Seed each victim's inventory (StatItems bitfield).
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatItems, Value: events.ITShotgun})
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: 2, StatIndex: events.StatItems, Value: events.ITSuperShotgun})
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: 3, StatIndex: events.StatItems, Value: events.ITLightning})
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: 4, StatIndex: events.StatItems, Value: events.ITRocketLauncher})
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: 5, StatIndex: events.StatItems, Value: events.ITRocketLauncher | events.ITLightning})

	// alpha RLs each enemy for 100.
	for slot := 1; slot <= 5; slot++ {
		a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: slot, Damage: 100, DeathType: dtRLTest, Time: 10})
	}
	// self-damage and team-damage (must not enter the enemy buckets).
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 0, Damage: 50, DeathType: dtRLTest, Time: 11})
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 6, Damage: 30, DeathType: dtRLTest, Time: 12})
	// world damage to alpha (env, non-player attacker).
	a.OnEvent(&events.DamageEvent{Attacker: -1, Victim: 0, Damage: 25, DeathType: dtFallTest, Time: 13})

	a.UseCoreOutputs(damageCore())
	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}
	if res.Damage == nil {
		t.Fatal("no DamageResult")
	}

	alpha := res.Damage.ByPlayer["alpha"]
	if alpha == nil {
		t.Fatal("no alpha aggregates")
	}
	if alpha.Given != 500 {
		t.Errorf("Given = %d, want 500", alpha.Given)
	}
	if alpha.EnemyVsSG != 100 || alpha.EnemyVsMid != 100 || alpha.EnemyVsLG != 100 ||
		alpha.EnemyVsRL != 100 || alpha.EnemyVsBoth != 100 {
		t.Errorf("buckets = sg:%d mid:%d lg:%d rl:%d both:%d, want 100 each",
			alpha.EnemyVsSG, alpha.EnemyVsMid, alpha.EnemyVsLG, alpha.EnemyVsRL, alpha.EnemyVsBoth)
	}
	if alpha.EWep != 300 {
		t.Errorf("EWep = %d, want 300 (lg+rl+both)", alpha.EWep)
	}
	if alpha.EnemyVsLG+alpha.EnemyVsRL+alpha.EnemyVsBoth != alpha.EWep {
		t.Errorf("EWep != lg+rl+both")
	}
	if alpha.GivenSelf != 50 {
		t.Errorf("GivenSelf = %d, want 50", alpha.GivenSelf)
	}
	if alpha.GivenTeam != 30 {
		t.Errorf("GivenTeam = %d, want 30", alpha.GivenTeam)
	}
	// alpha took self (50) + world (25); world is environmental.
	if alpha.Taken != 75 {
		t.Errorf("Taken = %d, want 75 (self 50 + world 25)", alpha.Taken)
	}
	if alpha.TakenEnv != 25 {
		t.Errorf("TakenEnv = %d, want 25", alpha.TakenEnv)
	}

	// Enemy RL damage flows into top-level ByWeapon (self/team excluded).
	if res.Damage.ByWeapon["rl"] != 500 {
		t.Errorf("ByWeapon[rl] = %d, want 500", res.Damage.ByWeapon["rl"])
	}

	// VictimWep label on a per-hit entry; world entry names "world".
	var sawBoth, sawWorld bool
	for _, e := range res.Damage.Events {
		if e.Victim == "fboth" && e.VictimWep != "both" {
			t.Errorf("fboth hit VictimWep = %q, want both", e.VictimWep)
		}
		if e.Victim == "fboth" {
			sawBoth = true
		}
		if e.Attacker == "world" {
			sawWorld = true
			if !e.IsEnv || e.Weapon != "fall" {
				t.Errorf("world entry = %+v, want IsEnv + weapon fall", e)
			}
		}
	}
	if !sawBoth || !sawWorld {
		t.Errorf("missing expected events (both=%v world=%v)", sawBoth, sawWorld)
	}
}

func TestDamageAnalyzer_OutOfMatchDroppedEverywhere(t *testing.T) {
	a := NewDamageAnalyzer()
	ctx := &Context{}
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "alpha", Team: "red"}
	ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "bravo", Team: "blue"}
	_ = a.Init(ctx)

	// Pre-match (warmup) hit — dropped from BOTH the aggregates AND the log.
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 40, DeathType: dtSGTest, Time: 1})
	a.OnEvent(&events.PrintEvent{Level: 2, Message: "The match has begun!\n", Time: 5})
	// In-match hit — counts and appears in the log.
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 60, DeathType: dtSGTest, Time: 6})
	// Post-match hit — also dropped everywhere.
	a.OnEvent(&events.IntermissionEvent{Time: 10})
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 25, DeathType: dtSGTest, Time: 11})

	a.UseCoreOutputs(&CoreOutputs{Slots: map[int]SlotInfo{
		0: {Name: "alpha", Team: "red"}, 1: {Name: "bravo", Team: "blue"},
	}})
	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}
	if got := res.Damage.ByPlayer["alpha"].Given; got != 60 {
		t.Errorf("Given = %d, want 60 (out-of-match hits excluded from aggregates)", got)
	}
	// The events log now carries ONLY the in-match hit — the source gate.
	if got := len(res.Damage.Events); got != 1 {
		t.Fatalf("Events = %d, want 1 (warmup + post-match hits dropped from the log)", got)
	}
	if got := res.Damage.Events[0].Damage; got != 60 {
		t.Errorf("surviving event damage = %d, want 60 (the in-match hit)", got)
	}
	// TotalDamage counts only the in-match hit too.
	if res.Damage.TotalDamage != 60 {
		t.Errorf("TotalDamage = %d, want 60", res.Damage.TotalDamage)
	}
}

func TestDamageAnalyzer_PositionalKillsSeparated(t *testing.T) {
	a := buildDamageAnalyzer()
	// erl (slot 4) holds an RL; alpha RLs erl for 100 (real damage), then
	// telefrags erl (deathtype tele, reported as the 9999 sentinel).
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: 4, StatIndex: events.StatItems, Value: events.ITRocketLauncher})
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 4, Damage: 100, DeathType: dtRLTest, Time: 10})
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 4, Damage: 9999, DeathType: dtTeleTest, Time: 11})
	// A stomp (10 HP in normal play) is a positional kill, not weapon damage.
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 10, DeathType: dtStompTest, Time: 12})

	a.UseCoreOutputs(damageCore())
	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}

	alpha := res.Damage.ByPlayer["alpha"]
	// The 9999 telefrag sentinel must NOT pollute damage figures — but the
	// fold-in adds each positional kill's honest value: this telefrag lands
	// on a 0-HP shadow (the RL hit emptied erl same-frame) so folds 0, and
	// the 10-HP stomp folds 10. Given = 100 (RL) + 0 + 10.
	if alpha.Given != 110 {
		t.Errorf("Given = %d, want 110 (rl 100 + tele fold 0 + stomp fold 10)", alpha.Given)
	}
	if alpha.EWep != 100 || alpha.EnemyVsRL != 100 {
		t.Errorf("ewep=%d enemyVsRl=%d, want 100/100 (positional kills stay out of EWep)", alpha.EWep, alpha.EnemyVsRL)
	}
	if res.Damage.ByWeapon["tele"] != 0 {
		t.Errorf("byWeapon[tele] = %d, want 0 (telefrag is not weapon damage)", res.Damage.ByWeapon["tele"])
	}
	if res.Damage.TotalDamage != 100 {
		t.Errorf("totalDamage = %d, want 100 (a fold of the events log only)", res.Damage.TotalDamage)
	}
	for _, e := range res.Damage.Events {
		if e.Weapon == "tele" || e.Weapon == "stomp" {
			t.Errorf("positional kill leaked into the damage events log: %+v", e)
		}
	}
	if res.Damage.ByWeapon["stomp"] != 0 {
		t.Errorf("byWeapon[stomp] = %d, want 0", res.Damage.ByWeapon["stomp"])
	}
	// Both must be tracked separately instead.
	if alpha.Telefrags != 1 || alpha.Stomps != 1 {
		t.Errorf("alpha telefrags=%d stomps=%d, want 1/1", alpha.Telefrags, alpha.Stomps)
	}
	if len(res.Damage.Telefrags) != 1 {
		t.Fatalf("Telefrags list = %d, want 1", len(res.Damage.Telefrags))
	}
	tf := res.Damage.Telefrags[0]
	if tf.Attacker != "alpha" || tf.Victim != "erl" || tf.IsTeam {
		t.Errorf("telefrag entry = %+v, want alpha->erl, not team", tf)
	}
	if len(res.Damage.Stomps) != 1 {
		t.Fatalf("Stomps list = %d, want 1", len(res.Damage.Stomps))
	}
	if st := res.Damage.Stomps[0]; st.Attacker != "alpha" || st.Victim != "bsg" || st.Bounded != 10 {
		t.Errorf("stomp entry = %+v, want alpha->bsg bounded 10", st)
	}
}

// T7: telefrags and stomps fold their bounded value into given/taken in
// BOTH families, per KTX's own accumulation (combat.c:1046-1076 — tele and
// stomp map to wpNONE, so they land in dmg totals but not per-weapon ones).
func TestDamageAnalyzer_PositionalKillFoldIn(t *testing.T) {
	// Enemy telefrag through armor: bounded = full armor + remaining health.
	a := buildDamageAnalyzer()
	seedVitals(a, 4, 80, 150, events.ITArmor3)
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 4, Damage: 9999, DeathType: dtTeleTest, Time: 10})
	d := finalizeDamage(t, a)
	alpha := d.ByPlayer["alpha"]
	if alpha.Given != 230 {
		t.Errorf("raw Given = %d, want 230 (150 armor + 80 health; the 9999 sentinel never folds)", alpha.Given)
	}
	if alpha.Bounded == nil || alpha.Bounded.Given != 230 {
		t.Errorf("bounded Given = %+v, want 230", alpha.Bounded)
	}
	if erl := d.ByPlayer["erl"]; erl.Taken != 230 || erl.Bounded.Taken != 230 {
		t.Errorf("victim taken = %d/%d, want 230/230", erl.Taken, erl.Bounded.Taken)
	}
	if d.Telefrags[0].Bounded != 230 {
		t.Errorf("kill bounded = %d, want 230", d.Telefrags[0].Bounded)
	}
	if alpha.EWep != 0 || len(d.ByWeapon) != 0 || d.TotalDamage != 0 {
		t.Errorf("tele fold leaked into EWep/ByWeapon/TotalDamage: %d/%v/%d", alpha.EWep, d.ByWeapon, d.TotalDamage)
	}

	// Team telefrag: GivenTeam in both families; the credit counter stays 0.
	a = buildDamageAnalyzer()
	seedVitals(a, 6, 100, 0, 0)
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 6, Damage: 9999, DeathType: dtTeleTest, Time: 10})
	d = finalizeDamage(t, a)
	alpha = d.ByPlayer["alpha"]
	if alpha.GivenTeam != 100 || alpha.Bounded == nil || alpha.Bounded.GivenTeam != 100 {
		t.Errorf("team tele GivenTeam = %d/%+v, want 100/100", alpha.GivenTeam, alpha.Bounded)
	}
	if alpha.Given != 0 || alpha.Telefrags != 0 {
		t.Errorf("team tele credited as enemy: given=%d count=%d", alpha.Given, alpha.Telefrags)
	}

	// dtTELE2 pent-deflect: an ordinary tele wire event with the pent holder
	// as attacker — the arriving mortal's spawn state folds in.
	a = buildDamageAnalyzer()
	seedVitals(a, 1, 100, 0, 0)
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 9999, DeathType: events.DtTele2, Time: 10})
	d = finalizeDamage(t, a)
	if got := d.ByPlayer["alpha"].Given; got != 100 {
		t.Errorf("deflect Given = %d, want 100", got)
	}
	if d.Telefrags[0].Bounded != 100 || d.ByPlayer["alpha"].Telefrags != 1 {
		t.Errorf("deflect kill = %+v count=%d, want bounded 100, credited", d.Telefrags[0], d.ByPlayer["alpha"].Telefrags)
	}

	// Stomp through armor: the honest wire value folds raw; the bounded
	// arithmetic still applies (YA absorbs 6 of 10).
	a = buildDamageAnalyzer()
	seedVitals(a, 1, 3, 40, events.ITArmor2)
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 10, DeathType: dtStompTest, Time: 10})
	d = finalizeDamage(t, a)
	alpha = d.ByPlayer["alpha"]
	// save=ceil(6)=6, take=4 capped to 3 HP -> bounded 9, raw 10.
	if alpha.Given != 10 || alpha.Bounded.Given != 9 {
		t.Errorf("stomp fold = raw %d / bounded %d, want 10/9", alpha.Given, alpha.Bounded.Given)
	}
	if d.Stomps[0].Bounded != 9 {
		t.Errorf("stomp kill bounded = %d, want 9", d.Stomps[0].Bounded)
	}

	// Skipped mode: no fold-in at all — v53 exclusion semantics.
	a = buildDamageAnalyzer()
	a.OnEvent(&events.ServerInfoEvent{Key: "k_midair", Value: "1"})
	seedVitals(a, 4, 80, 150, events.ITArmor3)
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 4, Damage: 9999, DeathType: dtTeleTest, Time: 10})
	d = finalizeDamage(t, a)
	if alpha := d.ByPlayer["alpha"]; alpha.Given != 0 || alpha.Bounded != nil {
		t.Errorf("skipped-mode tele folded anyway: given=%d bounded=%+v", alpha.Given, alpha.Bounded)
	}
	if d.Telefrags[0].Bounded != 0 {
		t.Errorf("skipped-mode kill bounded = %d, want absent", d.Telefrags[0].Bounded)
	}
}

// F20: in a 1v1 where both players share a non-empty colour team, damage is
// classified enemy at birth — consistent with the duel-normalized
// Shots.VictimKinds instead of contradicting them (airgibsPost and aimPost
// read IsTeam downstream, and the matrix/EWep buckets only fill on the
// enemy path).
func TestDamageAnalyzer_DuelSharedTeamClassifiedEnemy(t *testing.T) {
	build := func() (*DamageAnalyzer, *Result) {
		a := NewDamageAnalyzer()
		ctx := &Context{}
		ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "alpha", Team: "green"}
		ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "bravo", Team: "green"}
		_ = a.Init(ctx)
		a.timing.Started = true
		a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatItems, Value: events.ITRocketLauncher})
		a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 100, DeathType: dtRLTest, Time: 10})
		a.UseCoreOutputs(&CoreOutputs{Slots: map[int]SlotInfo{
			0: {Name: "alpha", Team: "green"}, 1: {Name: "bravo", Team: "green"},
		}})
		return a, &Result{}
	}

	// Duel: DemoInfo lists exactly the two participants, so the roster
	// classifies the match as a 1v1 (the born-correct duel verdict damage now
	// reads from co.Roster instead of isDuelResult(result)).
	a, res := build()
	res.DemoInfo = &DemoInfoResult{Players: []DemoInfoPlayer{
		{Name: "alpha", Team: "green"}, {Name: "bravo", Team: "green"},
	}}
	a.core.Roster = newRoster(res.DemoInfo)
	if err := a.Finalize(res); err != nil {
		t.Fatal(err)
	}
	e := res.Damage.Events[0]
	if e.IsTeam {
		t.Errorf("duel shared-team hit classified IsTeam — contradicts duel-normalized victimKinds (F20)")
	}
	if e.VictimWep != "rl" {
		t.Errorf("VictimWep = %q, want rl (enemy path fills it)", e.VictimWep)
	}
	alpha := res.Damage.ByPlayer["alpha"]
	if alpha.Given != 100 || alpha.GivenTeam != 0 {
		t.Errorf("Given/GivenTeam = %d/%d, want 100/0", alpha.Given, alpha.GivenTeam)
	}
	if len(res.Damage.Matrix) != 1 {
		t.Errorf("Matrix entries = %d, want 1", len(res.Damage.Matrix))
	}

	// Control — a third participant makes it a real team game: the same
	// shared-colour hit stays team damage.
	a2, res2 := build()
	res2.DemoInfo = &DemoInfoResult{Players: []DemoInfoPlayer{
		{Name: "alpha", Team: "green"}, {Name: "bravo", Team: "green"}, {Name: "charlie", Team: "red"},
	}}
	a2.core.Roster = newRoster(res2.DemoInfo)
	if err := a2.Finalize(res2); err != nil {
		t.Fatal(err)
	}
	if !res2.Damage.Events[0].IsTeam {
		t.Errorf("team-game shared-team hit lost its IsTeam flag")
	}
	if got := res2.Damage.ByPlayer["alpha"].GivenTeam; got != 100 {
		t.Errorf("GivenTeam = %d, want 100", got)
	}
}

func TestDamageAnalyzer_ScoreboardReconciliation(t *testing.T) {
	a := buildDamageAnalyzer()
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: 4, StatIndex: events.StatItems, Value: events.ITRocketLauncher})
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 4, Damage: 100, DeathType: dtRLTest, Time: 10})

	co := damageCore()
	co.DemoInfo = &DemoInfoResult{Players: []DemoInfoPlayer{
		{Name: "alpha", Team: "red", Dmg: &DemoInfoDmg{Given: 80, Taken: 0, EnemyWeapons: 75}},
	}}
	a.UseCoreOutputs(co)
	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}
	sb := res.Damage.Scoreboard
	if sb == nil || sb.ByPlayer["alpha"] == nil {
		t.Fatal("no reconciliation for alpha")
	}
	d := sb.ByPlayer["alpha"]
	if d.StreamGiven != 100 || d.ScoreGiven != 80 {
		t.Errorf("given: stream=%d score=%d, want 100/80", d.StreamGiven, d.ScoreGiven)
	}
	if d.StreamEWep != 100 || d.ScoreEWep != 75 {
		t.Errorf("ewep: stream=%d score=%d, want 100/75", d.StreamEWep, d.ScoreEWep)
	}
}

// --- bounded reconstruction (schema v54) ---------------------------------
//
// The wire carries only KTX's unbound damage; the tests below pin the per-hit
// reconstruction of the bounded (scoreboard) value against hand-computed
// T_Damage arithmetic (ktx/src/combat.c:618-783).

// seedVitals pushes authoritative health/armor/items stats for a slot.
func seedVitals(a *DamageAnalyzer, slot, health, armor, items int) {
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: slot, StatIndex: events.StatHealth, Value: health})
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: slot, StatIndex: events.StatArmor, Value: armor})
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: slot, StatIndex: events.StatItems, Value: items})
}

// boundedOf returns the effective bounded value of an events-log entry
// (the omit-when-equal convention: nil means "equals Damage").
func boundedOf(e DamageEntry) int {
	if e.Bounded != nil {
		return *e.Bounded
	}
	return e.Damage
}

func finalizeDamage(t *testing.T, a *DamageAnalyzer) *DamageResult {
	t.Helper()
	a.UseCoreOutputs(damageCore())
	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}
	if res.Damage == nil {
		t.Fatal("no DamageResult")
	}
	return res.Damage
}

// T1: the three armor tiers absorb per their KTX fraction; with 10 HP left
// the health share caps, exposing the armor split in the bounded value.
func TestDamageAnalyzer_BoundedArmorTiers(t *testing.T) {
	a := buildDamageAnalyzer()
	seedVitals(a, 1, 10, 100, events.ITArmor1) // GA 0.3
	seedVitals(a, 2, 10, 100, events.ITArmor2) // YA 0.6
	seedVitals(a, 3, 10, 100, events.ITArmor3) // RA 0.8
	for slot := 1; slot <= 3; slot++ {
		a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: slot, Damage: 100, DeathType: dtRLTest, Time: 10})
	}
	d := finalizeDamage(t, a)
	want := map[string]int{"bsg": 40, "cmid": 70, "dlg": 90} // save 30/60/80 + 10 HP
	for _, e := range d.Events {
		if got := boundedOf(e); got != want[e.Victim] {
			t.Errorf("%s bounded = %d, want %d", e.Victim, got, want[e.Victim])
		}
	}
	alpha := d.ByPlayer["alpha"]
	if alpha.Bounded == nil || alpha.Bounded.Given != 200 {
		t.Fatalf("bounded Given = %+v, want 200 (40+70+90)", alpha.Bounded)
	}
	if alpha.Given != 300 {
		t.Errorf("raw Given = %d, want 300 (raw family untouched)", alpha.Given)
	}
	// Victims' bounded taken mirror the same values.
	if bp := d.ByPlayer["dlg"].Bounded; bp == nil || bp.Taken != 90 {
		t.Errorf("dlg bounded Taken = %+v, want 90", bp)
	}
}

// T2: armor break mid-hit — the shadow armor hits 0 so the same-frame
// follow-up absorbs nothing and caps on the reduced health.
func TestDamageAnalyzer_BoundedArmorBreakSequential(t *testing.T) {
	a := buildDamageAnalyzer()
	seedVitals(a, 1, 100, 5, events.ITArmor2) // YA, 5 armor left
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 60, DeathType: dtRLTest, Time: 10})
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 60, DeathType: dtRLTest, Time: 10})
	d := finalizeDamage(t, a)
	if len(d.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(d.Events))
	}
	// Hit 1: save=min(ceil(36),5)=5, take=55 of 100 HP -> bounded 60 == raw.
	if d.Events[0].Bounded != nil {
		t.Errorf("hit1 bounded = %d, want nil (armor cap does not change save+take)", *d.Events[0].Bounded)
	}
	// Hit 2: armor 0 now, health 45 -> bounded 45.
	if got := boundedOf(d.Events[1]); got != 45 {
		t.Errorf("hit2 bounded = %d, want 45 (armor broke, health 45 caps)", got)
	}
}

// T3+T4: the overkill cap is the whole point; equal values are omitted.
func TestDamageAnalyzer_BoundedOverkillAndOmitWhenEqual(t *testing.T) {
	a := buildDamageAnalyzer()
	seedVitals(a, 1, 30, 0, 0)
	seedVitals(a, 2, 100, 0, 0)
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 100, DeathType: dtRLTest, Time: 10})
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 2, Damage: 27, DeathType: dtSGTest, Time: 11})
	d := finalizeDamage(t, a)
	if got := boundedOf(d.Events[0]); got != 30 {
		t.Errorf("overkill bounded = %d, want 30", got)
	}
	if d.Events[0].Bounded == nil {
		t.Errorf("overkill hit must carry an explicit bounded value")
	}
	if d.Events[1].Bounded != nil {
		t.Errorf("no-overkill hit bounded = %d, want omitted", *d.Events[1].Bounded)
	}
	if bg := d.ByPlayer["alpha"].Bounded; bg == nil || bg.Given != 57 {
		t.Fatalf("bounded Given = %+v, want 57 (30+27)", bg)
	}
}

// T5: pent zeroes the health share but armor is still consumed
// (combat.c:728-737 zeroes take only; the save block precedes it).
func TestDamageAnalyzer_BoundedPent(t *testing.T) {
	a := buildDamageAnalyzer()
	seedVitals(a, 1, 100, 100, events.ITArmor3|events.ITInvulnerability)
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 100, DeathType: dtRLTest, Time: 10})
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 100, DeathType: dtRLTest, Time: 10})
	d := finalizeDamage(t, a)
	// Hit 1: save=80, take zeroed -> bounded 80.
	if got := boundedOf(d.Events[0]); got != 80 {
		t.Errorf("pent hit1 bounded = %d, want 80 (armor only)", got)
	}
	// Hit 2 same frame: shadow armor 20, health untouched at 100 -> bounded 20.
	if got := boundedOf(d.Events[1]); got != 20 {
		t.Errorf("pent hit2 bounded = %d, want 20 (armor 20 left, health never touched)", got)
	}
}

// T6: the KTX teamplay nullification rules (combat.c:738-753). tp1 zeroes
// mates AND self; tp3 zeroes mates only; tp4 zeroes armor too; dtSUICIDE is
// exempt from all of them (combat.c:722).
func TestDamageAnalyzer_BoundedTeamplayRules(t *testing.T) {
	run := func(tp string, f func(a *DamageAnalyzer)) *DamageResult {
		a := buildDamageAnalyzer()
		a.OnEvent(&events.ServerInfoEvent{Key: "teamplay", Value: tp})
		f(a)
		return finalizeDamage(t, a)
	}

	// tp1 team hit: no armor -> bounded 0 (a real zero, not omitted).
	d := run("1", func(a *DamageAnalyzer) {
		seedVitals(a, 6, 100, 0, 0)
		a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 6, Damage: 30, DeathType: dtRLTest, Time: 10})
	})
	if got := boundedOf(d.Events[0]); got != 0 || d.Events[0].Bounded == nil {
		t.Errorf("tp1 team hit bounded = %d (ptr %v), want explicit 0", got, d.Events[0].Bounded)
	}

	// tp1 self hit: also nullified ((tp_num()==1) has no targ!=attacker guard).
	d = run("1", func(a *DamageAnalyzer) {
		seedVitals(a, 0, 100, 0, 0)
		a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 0, Damage: 30, DeathType: dtRLTest, Time: 10})
	})
	if got := boundedOf(d.Events[0]); got != 0 {
		t.Errorf("tp1 self hit bounded = %d, want 0", got)
	}

	// tp3 self hit: self still takes damage (tp3 requires targ != attacker).
	d = run("3", func(a *DamageAnalyzer) {
		seedVitals(a, 0, 100, 0, 0)
		a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 0, Damage: 30, DeathType: dtRLTest, Time: 10})
	})
	if d.Events[0].Bounded != nil {
		t.Errorf("tp3 self hit bounded = %d, want nil (== raw 30)", *d.Events[0].Bounded)
	}

	// tp3 team hit: nullified.
	d = run("3", func(a *DamageAnalyzer) {
		seedVitals(a, 6, 100, 0, 0)
		a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 6, Damage: 30, DeathType: dtRLTest, Time: 10})
	})
	if got := boundedOf(d.Events[0]); got != 0 {
		t.Errorf("tp3 team hit bounded = %d, want 0", got)
	}

	// tp4 team hit: armor untouched too -> bounded 0 even with RA.
	d = run("4", func(a *DamageAnalyzer) {
		seedVitals(a, 6, 100, 100, events.ITArmor3)
		a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 6, Damage: 30, DeathType: dtRLTest, Time: 10})
	})
	if got := boundedOf(d.Events[0]); got != 0 {
		t.Errorf("tp4 team hit bounded = %d, want 0 (no armor consumption either)", got)
	}

	// dtSUICIDE under tp1: exempt from nullification.
	d = run("1", func(a *DamageAnalyzer) {
		seedVitals(a, 0, 100, 0, 0)
		a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 0, Damage: 30, DeathType: events.DtSuicide, Time: 10})
	})
	if d.Events[0].Bounded != nil {
		t.Errorf("suicide bounded = %d, want nil (nullification skipped)", *d.Events[0].Bounded)
	}
}

// T8: KTX damage-indicator sentinels must not poison the vitals shadow.
func TestDamageAnalyzer_BoundedStatSentinelsRejected(t *testing.T) {
	a := buildDamageAnalyzer()
	seedVitals(a, 1, 40, 0, 0)
	// Sentinels: health 1000+dmg indicator, armor feedback value.
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatHealth, Value: 1042})
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatArmor, Value: 1080})
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 100, DeathType: dtRLTest, Time: 10})
	d := finalizeDamage(t, a)
	if got := boundedOf(d.Events[0]); got != 40 {
		t.Errorf("bounded = %d, want 40 (sentinel stats rejected, real health 40)", got)
	}
}

// T9: two hits in one frame (no stat refresh in between) cap sequentially.
func TestDamageAnalyzer_BoundedSameFrameSequentialCap(t *testing.T) {
	a := buildDamageAnalyzer()
	seedVitals(a, 1, 100, 0, 0)
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 60, DeathType: dtRLTest, Time: 10})
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 60, DeathType: dtRLTest, Time: 10})
	d := finalizeDamage(t, a)
	if d.Events[0].Bounded != nil {
		t.Errorf("hit1 bounded = %d, want nil (60 of 100)", *d.Events[0].Bounded)
	}
	if got := boundedOf(d.Events[1]); got != 40 {
		t.Errorf("hit2 bounded = %d, want 40 (health 40 left)", got)
	}
}

// T10: an authoritative stat update between hits checkpoints the shadow.
func TestDamageAnalyzer_BoundedCheckpointResync(t *testing.T) {
	a := buildDamageAnalyzer()
	seedVitals(a, 1, 100, 0, 0)
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 60, DeathType: dtRLTest, Time: 10})
	// Mega pickup / respawn: the server says 100 again.
	a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatHealth, Value: 100})
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 60, DeathType: dtRLTest, Time: 11})
	d := finalizeDamage(t, a)
	if d.Events[1].Bounded != nil {
		t.Errorf("post-checkpoint bounded = %d, want nil (health back to 100)", *d.Events[1].Bounded)
	}
}

// T11: modes whose T_Damage rewrites are unobservable skip the bounded
// family entirely — no half-right numbers.
func TestDamageAnalyzer_BoundedSkippedModes(t *testing.T) {
	for _, tc := range []struct{ cvar, mode string }{
		{"k_midair", "midair"}, {"k_instagib", "instagib"}, {"k_dmgfrags", "dmgfrags"},
	} {
		a := buildDamageAnalyzer()
		a.OnEvent(&events.ServerInfoEvent{Key: tc.cvar, Value: "1"})
		seedVitals(a, 1, 30, 0, 0)
		// Overkill hit that WOULD get a bounded value in standard mode.
		a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 100, DeathType: dtRLTest, Time: 10})
		co := damageCore()
		co.DemoInfo = &DemoInfoResult{Players: []DemoInfoPlayer{
			{Name: "alpha", Team: "red", Dmg: &DemoInfoDmg{Given: 30}},
		}}
		a.UseCoreOutputs(co)
		var res Result
		if err := a.Finalize(&res); err != nil {
			t.Fatal(err)
		}
		d := res.Damage
		if want := "skipped:" + tc.mode; d.BoundedMode != want {
			t.Errorf("%s: BoundedMode = %q, want %q", tc.cvar, d.BoundedMode, want)
		}
		if d.Dmg != "" {
			t.Errorf("%s: Dmg = %q, want empty (only the raw family is stored)", tc.cvar, d.Dmg)
		}
		if d.Events[0].Bounded != nil {
			t.Errorf("%s: event carries bounded despite skip", tc.cvar)
		}
		if d.ByPlayer["alpha"].Bounded != nil {
			t.Errorf("%s: byPlayer carries a bounded nest despite skip", tc.cvar)
		}
		if d.Scoreboard.ByPlayer["alpha"].Bounded != nil {
			t.Errorf("%s: scoreboard delta carries bounded despite skip", tc.cvar)
		}
	}
}

// T12: a victim with no stat history defaults to the 100/0 spawn state.
func TestDamageAnalyzer_BoundedUnknownVitalsDefault(t *testing.T) {
	a := buildDamageAnalyzer()
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 150, DeathType: dtRLTest, Time: 10})
	d := finalizeDamage(t, a)
	if got := boundedOf(d.Events[0]); got != 100 {
		t.Errorf("bounded = %d, want 100 (spawn-state default)", got)
	}
}

// T13: the scoreboard delta's bounded nest — enemy-only taken and dmg.team.
func TestDamageAnalyzer_BoundedScoreboardDelta(t *testing.T) {
	a := buildDamageAnalyzer()
	seedVitals(a, 4, 100, 0, 0)
	seedVitals(a, 6, 100, 0, 0)
	// Enemy overkill: bounded 100 of raw 150.
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 4, Damage: 150, DeathType: dtRLTest, Time: 10})
	// Team hit: bounded team damage 40 (tp 0 here: no nullification).
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 6, Damage: 40, DeathType: dtRLTest, Time: 11})

	co := damageCore()
	co.DemoInfo = &DemoInfoResult{Players: []DemoInfoPlayer{
		{Name: "alpha", Team: "red", Dmg: &DemoInfoDmg{Given: 100, Team: 41}},
		{Name: "erl", Team: "blue", Dmg: &DemoInfoDmg{Taken: 100}},
	}}
	a.UseCoreOutputs(co)
	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}
	alpha := res.Damage.Scoreboard.ByPlayer["alpha"]
	if alpha.Bounded == nil {
		t.Fatal("no bounded delta for alpha")
	}
	if alpha.Bounded.StreamGiven != 100 || alpha.Bounded.StreamTeam != 40 || alpha.Bounded.ScoreTeam != 41 {
		t.Errorf("alpha bounded delta = %+v, want given 100 / team 40 / scoreTeam 41", alpha.Bounded)
	}
	if alpha.Bounded.StreamTaken != 0 {
		t.Errorf("alpha bounded taken = %d, want 0 (took nothing)", alpha.Bounded.StreamTaken)
	}
	erl := res.Damage.Scoreboard.ByPlayer["erl"]
	if erl.Bounded == nil || erl.Bounded.StreamTaken != 100 {
		t.Fatalf("erl bounded delta = %+v, want enemy-only taken 100", erl.Bounded)
	}
	// The stored result self-describes the family and mode.
	if res.Damage.Dmg != "both" || res.Damage.BoundedMode != "standard" {
		t.Errorf("dmg/boundedMode = %q/%q, want both/standard", res.Damage.Dmg, res.Damage.BoundedMode)
	}
}
