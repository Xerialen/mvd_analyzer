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

func TestDamageAnalyzer_AggregatesGatedToMatch(t *testing.T) {
	a := NewDamageAnalyzer()
	ctx := &Context{}
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "alpha", Team: "red"}
	ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "bravo", Team: "blue"}
	_ = a.Init(ctx)

	// Pre-match (warmup) hit — in Events log, excluded from aggregates.
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 40, DeathType: dtSGTest, Time: 1})
	a.OnEvent(&events.PrintEvent{Level: 2, Message: "The match has begun!\n", Time: 5})
	// In-match hit — counts.
	a.OnEvent(&events.DamageEvent{Attacker: 0, Victim: 1, Damage: 60, DeathType: dtSGTest, Time: 6})

	a.UseCoreOutputs(&CoreOutputs{Slots: map[int]SlotInfo{
		0: {Name: "alpha", Team: "red"}, 1: {Name: "bravo", Team: "blue"},
	}})
	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}
	if got := res.Damage.ByPlayer["alpha"].Given; got != 60 {
		t.Errorf("Given = %d, want 60 (warmup hit excluded from aggregates)", got)
	}
	if got := len(res.Damage.Events); got != 2 {
		t.Errorf("Events = %d, want 2 (warmup hit kept in the log)", got)
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
	// The 9999 telefrag must NOT pollute damage figures.
	if alpha.Given != 100 {
		t.Errorf("Given = %d, want 100 (telefrag excluded)", alpha.Given)
	}
	if alpha.EWep != 100 || alpha.EnemyVsRL != 100 {
		t.Errorf("ewep=%d enemyVsRl=%d, want 100/100 (telefrag excluded)", alpha.EWep, alpha.EnemyVsRL)
	}
	if res.Damage.ByWeapon["tele"] != 0 {
		t.Errorf("byWeapon[tele] = %d, want 0 (telefrag is not weapon damage)", res.Damage.ByWeapon["tele"])
	}
	if res.Damage.TotalDamage != 100 {
		t.Errorf("totalDamage = %d, want 100 (telefrag excluded)", res.Damage.TotalDamage)
	}
	for _, e := range res.Damage.Events {
		if e.Weapon == "tele" || e.Weapon == "stomp" {
			t.Errorf("positional kill leaked into the damage events log: %+v", e)
		}
	}
	if res.Damage.ByWeapon["stomp"] != 0 {
		t.Errorf("byWeapon[stomp] = %d, want 0", res.Damage.ByWeapon["stomp"])
	}
	if alpha.Given != 100 { // unchanged by the 10-HP stomp
		t.Errorf("Given = %d, want 100 (stomp excluded too)", alpha.Given)
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
	if st := res.Damage.Stomps[0]; st.Attacker != "alpha" || st.Victim != "bsg" {
		t.Errorf("stomp entry = %+v, want alpha->bsg", st)
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
