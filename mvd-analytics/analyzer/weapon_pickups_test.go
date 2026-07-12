package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

func newTestWeaponPickupsAnalyzer() (*WeaponPickupsAnalyzer, *Context) {
	a := NewWeaponPickupsAnalyzer()
	ctx := &Context{}
	_ = a.Init(ctx)
	a.timing.Started = true
	return a, ctx
}

// World-spawner RL pickup. ItemSpawnEvent classifies the entity, then
// the hint attributes the touch to slot 4. hadBefore=false (player had
// no RL bit) and kills=2 (two RL frags before next death; axe frag and
// post-death RL frag don't count).
func TestWeaponPickups_WorldRLWithKills(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[4] = &events.PlayerInfo{Slot: 4, Name: "ace", Team: "red"}

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 100, Kind: "rl", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 100, PlayerEnt: 5, Time: 10})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 4, Time: 30})

	a.core = &CoreOutputs{FragEntries: []FragEntry{
		{Time: 12000, Killer: "ace", Victim: "x", Weapon: "rl"},
		{Time: 15000, Killer: "ace", Victim: "y", Weapon: "axe"}, // wrong weapon
		{Time: 20000, Killer: "ace", Victim: "z", Weapon: "rl"},
		{Time: 40000, Killer: "ace", Victim: "w", Weapon: "rl"}, // post-death
	}}

	r := &Result{}
	_ = a.Finalize(r)
	out := r.WeaponPickups
	ps := out
	ok := ps != nil
	if !ok || len(ps) != 1 {
		t.Fatalf("out = %v, want 1 pickup", out)
	}
	p := ps[0]
	if p.Weapon != "rl" || p.Source != "world" || p.HadBefore {
		t.Errorf("got %+v, want weapon=rl source=world hadBefore=false", p)
	}
	if p.Kills != 2 {
		t.Errorf("Kills = %d, want 2 (two RL frags in the window)", p.Kills)
	}
	if p.NextDeathTime != 30000 {
		t.Errorf("NextDeathTime = %v, want 30000", p.NextDeathTime)
	}
}

// Redundant grab: player already held the weapon (STAT_ITEMS RL bit
// set before the pickup hint). hadBefore=true, so the pickup is
// tracked — the denial label in the frontend depends on it — but
// kills are NOT credited to it: the player would have made those
// kills anyway with the RL they already had. Attribution instead
// goes to whichever earlier pickup granted the weapon.
func TestWeaponPickups_HadBeforeDoesNotClaimKills(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "hoarder", Team: "blue"}

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 77, Kind: "rl", Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 2, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 4})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 77, PlayerEnt: 3, Time: 5})

	a.core = &CoreOutputs{FragEntries: []FragEntry{
		{Time: 6000, Killer: "hoarder", Weapon: "rl"},
	}}

	r := &Result{}
	_ = a.Finalize(r)
	out := r.WeaponPickups
	ps := out
	if !ps[0].HadBefore {
		t.Errorf("HadBefore should be true — player had RL bit set before pickup")
	}
	if ps[0].Kills != 0 {
		t.Errorf("Kills = %d, want 0 (redundant grab must not claim kills)", ps[0].Kills)
	}
}

// Backpack pickup: drop hint → pickup hint. Pickup entry carries the
// dropper's identity via the backpackEnt join.
func TestWeaponPickups_BackpackPickupAttribution(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "dropper", Team: "red"}
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "thief", Team: "blue"}

	_ = a.OnEvent(&events.BackpackDropHintEvent{BackpackEnt: 200, ItemFlags: 32, PlayerEnt: 2, Time: 10})
	_ = a.OnEvent(&events.BackpackPickupHintEvent{BackpackEnt: 200, PlayerEnt: 3, Time: 11})

	r := &Result{}
	_ = a.Finalize(r)
	out := r.WeaponPickups
	ps := out
	if len(ps) != 1 {
		t.Fatalf("want 1 pickup, got %d", len(ps))
	}
	p := ps[0]
	if p.Source != "backpack" || p.Weapon != "rl" {
		t.Errorf("got source=%s weapon=%s", p.Source, p.Weapon)
	}
	if p.Player != "thief" || p.Team != "blue" {
		t.Errorf("picker = %s/%s, want thief/blue", p.Player, p.Team)
	}
	if p.Dropper != "dropper" || p.DropperTeam != "red" {
		t.Errorf("dropper = %s/%s, want dropper/red", p.Dropper, p.DropperTeam)
	}
	if p.BackpackEnt != 200 || p.DropTime != 10000 {
		t.Errorf("entNum/dropTime = %d/%v", p.BackpackEnt, p.DropTime)
	}
}

// Armors and health are hinted by //ktx took too; the analyzer only
// records weapon kinds. No pickup entry should be emitted for
// armor/health.
func TestWeaponPickups_NonWeaponHintsIgnored(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 1, Kind: "ra", Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 2, Kind: "mh", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 5})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 2, PlayerEnt: 1, Time: 6})

	r := &Result{}
	_ = a.Finalize(r)
	out := r.WeaponPickups
	if out != nil {
		t.Errorf("out = %v, want nil (no weapon pickups)", out)
	}
}

// Teamkills and suicides in FragEntries must not count toward Kills —
// those aren't real effectiveness signals.
func TestWeaponPickups_TeamkillsAndSuicidesExcluded(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 1, Kind: "rl", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 5})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 30})

	a.core = &CoreOutputs{FragEntries: []FragEntry{
		{Time: 10000, Killer: "p", Weapon: "rl", IsSuicide: true},
		{Time: 15000, Killer: "p", Weapon: "rl", IsTeamKill: true},
		{Time: 20000, Killer: "p", Weapon: "rl"}, // the only real frag
	}}

	r := &Result{}
	_ = a.Finalize(r)
	out := r.WeaponPickups
	ps := out
	if ps[0].Kills != 1 {
		t.Errorf("Kills = %d, want 1 (suicide and TK excluded)", ps[0].Kills)
	}
}

// Two pickups of the same weapon in the same life: the first (fresh)
// grabs all the kill credit; any subsequent redundant grabs
// (hadBefore=true) get 0. This is the rule that makes
// "enemy RL" / "xfer RL" chips read as 0 kills unless the picker
// had never held the weapon this life.
func TestWeaponPickups_RedundantSecondPickupGetsZero(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	// Pickup 1 at t=10 (hadBefore=false), pickup 2 at t=20
	// (hadBefore=true after StatUpdate at t=11), death at t=30.
	// Frags at t=12, t=15, t=25, t=28 — all RL.
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 1, Kind: "rl", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 10})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 11})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 20})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 30})

	a.core = &CoreOutputs{FragEntries: []FragEntry{
		{Time: 12000, Killer: "p", Weapon: "rl"},
		{Time: 15000, Killer: "p", Weapon: "rl"},
		{Time: 25000, Killer: "p", Weapon: "rl"},
		{Time: 28000, Killer: "p", Weapon: "rl"},
	}}

	r := &Result{}
	_ = a.Finalize(r)
	out := r.WeaponPickups
	ps := out
	if len(ps) != 2 {
		t.Fatalf("want 2 pickups, got %d", len(ps))
	}
	// Pickup 1 (hadBefore=false, granted the weapon) owns all 4 kills
	// in the life. Pickup 2 (redundant) owns 0.
	if ps[0].Kills != 4 {
		t.Errorf("pickup[0].Kills = %d, want 4 (granting pickup)", ps[0].Kills)
	}
	if ps[1].HadBefore != true || ps[1].Kills != 0 {
		t.Errorf("pickup[1] = %+v, want HadBefore=true Kills=0 (redundant grab)", ps[1])
	}
}

// After a death + respawn, the player's inventory resets; the next
// RL pickup is hadBefore=false even though an earlier life's pickup
// was also hadBefore=false. Kills after the respawn go to the new
// granting pickup, not the dead life's.
func TestWeaponPickups_FreshPickupAfterDeathIsItsOwnGrant(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	// Life 1: pickup at t=10 (fresh), death at t=30 — STAT_ITEMS
	// clears at death, which the server sends as a StatUpdate.
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 1, Kind: "rl", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 10})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 11})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 30})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 30})
	// Life 2: pickup at t=40 (fresh again), no further death.
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 40})

	a.core = &CoreOutputs{FragEntries: []FragEntry{
		{Time: 20000, Killer: "p", Weapon: "rl"}, // life 1
		{Time: 45000, Killer: "p", Weapon: "rl"}, // life 2
		{Time: 50000, Killer: "p", Weapon: "rl"}, // life 2
	}}

	r := &Result{}
	_ = a.Finalize(r)
	out := r.WeaponPickups
	ps := out
	if ps[0].Kills != 1 {
		t.Errorf("life-1 pickup kills = %d, want 1", ps[0].Kills)
	}
	if ps[1].HadBefore || ps[1].Kills != 2 {
		t.Errorf("life-2 pickup = %+v, want hadBefore=false kills=2", ps[1])
	}
}

// Weapon-stay (dmm3): no //ktx took fires for weapons, so the pickup
// must be synthesized from the STAT_ITEMS flip. The picker stood on
// the RL pad → source="world", inferred=true, hadBefore=false, and
// kill credit flows exactly as for a hint-based pickup.
func TestWeaponPickups_WeaponStaySynthesisWorld(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "ace", Team: "red"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 100, Kind: "rl", Origin: [3]float32{500, 500, 100}, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItShotgun, Time: 5}) // seeds baseline
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{510, 500, 100}, Time: 9.9})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItShotgun | wpItRocketLauncher, Time: 10})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 30})

	a.core = &CoreOutputs{FragEntries: []FragEntry{
		{Time: 12000, Killer: "ace", Victim: "x", Weapon: "rl"},
		{Time: 40000, Killer: "ace", Victim: "y", Weapon: "rl"}, // post-death
	}}

	r := &Result{}
	_ = a.Finalize(r)
	ps := r.WeaponPickups
	if len(ps) != 1 {
		t.Fatalf("want 1 pickup, got %d: %+v", len(ps), ps)
	}
	p := ps[0]
	if p.Weapon != "rl" || p.Source != "world" || !p.Inferred || p.HadBefore {
		t.Errorf("got %+v, want weapon=rl source=world inferred=true hadBefore=false", p)
	}
	if p.Kills != 1 {
		t.Errorf("Kills = %d, want 1 (one RL frag before next death)", p.Kills)
	}
}

// Weapon-stay: a backpack grant flips the same STAT_ITEMS bit, but the
// //ktx bp hint precedes the flip on the wire — exactly one record must
// come out, with source="backpack".
func TestWeaponPickups_WeaponStayBackpackNotDoubleCounted(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "dropper", Team: "red"}
	ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "thief", Team: "blue"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatItems, Value: 0, Time: 5}) // seeds baseline
	_ = a.OnEvent(&events.BackpackDropHintEvent{BackpackEnt: 200, ItemFlags: 32, PlayerEnt: 1, Time: 10})
	_ = a.OnEvent(&events.BackpackPickupHintEvent{BackpackEnt: 200, PlayerEnt: 2, Time: 11})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 11.2})

	r := &Result{}
	_ = a.Finalize(r)
	ps := r.WeaponPickups
	if len(ps) != 1 {
		t.Fatalf("want exactly 1 pickup (no stat-flip duplicate), got %d: %+v", len(ps), ps)
	}
	if ps[0].Source != "backpack" || ps[0].Player != "thief" || ps[0].Inferred {
		t.Errorf("got %+v, want source=backpack player=thief inferred=false", ps[0])
	}
}

// Weapon-stay: a flip with no weapon pad anywhere near the picker (a
// non-RL/LG backpack grant, which has no hint in any mode) is recorded
// with source="unknown" rather than guessed to be a world grab.
func TestWeaponPickups_WeaponStayFlipAwayFromPadIsUnknown(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 100, Kind: "gl", Origin: [3]float32{5000, 5000, 100}, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 5})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{0, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItGrenadeLauncher, Time: 10})

	r := &Result{}
	_ = a.Finalize(r)
	ps := r.WeaponPickups
	if len(ps) != 1 {
		t.Fatalf("want 1 pickup, got %d", len(ps))
	}
	if ps[0].Source != "unknown" || !ps[0].Inferred {
		t.Errorf("got %+v, want source=unknown inferred=true", ps[0])
	}
}

// Weapon-stay: a spawn loadout that grants weapons (dmm5-style) must
// not read as pickups — whether the loadout stat lands just before the
// SpawnEvent (dead-state absorb) or just after it (spawn window; the
// update lands in the respawn frame).
func TestWeaponPickups_WeaponStaySpawnLoadoutNotRecorded(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\5"`, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 5})
	// STAT-before-SPAWN ordering: loadout lands while still flagged dead.
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 10})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems,
		Value: wpItShotgun | wpItSuperShotgun, Time: 20})
	_ = a.OnEvent(&events.SpawnEvent{PlayerNum: 0, Time: 20})
	// SPAWN-before-STAT ordering: loadout lands in the respawn frame.
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 30})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 30})
	_ = a.OnEvent(&events.SpawnEvent{PlayerNum: 0, Time: 40})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems,
		Value: wpItShotgun | wpItSuperShotgun | wpItRocketLauncher, Time: 40.02})

	r := &Result{}
	_ = a.Finalize(r)
	if len(r.WeaponPickups) != 0 {
		t.Fatalf("want 0 pickups from spawn loadouts, got %+v", r.WeaponPickups)
	}
}

// The kill.mvd regression: the wire's death frame orders
// DEATH → loadout STAT → SPAWN. The death resets the flip baseline and
// the loadout update re-seeds it; the spawn must NOT wipe that seed,
// or the player's first pickup of the new life reads as a silent
// re-seed and is lost.
func TestWeaponPickups_WeaponStayPickupAfterDeathFrameRecorded(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 100, Kind: "rl", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 1 | wpItRocketLauncher, Time: 5}) // seed: holds RL
	// Death frame, exact wire order observed in kill.mvd:
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 13.8})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 1, Time: 13.8}) // respawn loadout (SG only)
	_ = a.OnEvent(&events.SpawnEvent{PlayerNum: 0, Time: 13.8})
	// First pickup of the new life, well past the spawn window.
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{10, 0, 0}, Time: 15.0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 1 | wpItRocketLauncher, Time: 15.1})

	r := &Result{}
	_ = a.Finalize(r)
	ps := r.WeaponPickups
	if len(ps) != 1 {
		t.Fatalf("want 1 pickup (post-respawn RL grab), got %d: %+v", len(ps), ps)
	}
	if ps[0].Weapon != "rl" || ps[0].Source != "world" || !ps[0].Inferred {
		t.Errorf("got %+v, want weapon=rl source=world inferred=true", ps[0])
	}
}

// Grab-then-die (hub game 224763): the player touches the pad and is
// killed inside one stat interval; the death-frame flush puts the flip
// after the DeathEvent at the same timestamp. KTX counted the touch,
// so the flip must be recorded despite the dead flag — while a flip
// arriving later in the dead window (respawn bookkeeping) must not.
func TestWeaponPickups_WeaponStayDeathFrameGrabRecorded(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 100, Kind: "rl", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 1, Time: 5})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{10, 0, 0}, Time: 9.99})
	// Death frame: DEATH then the flip at the same timestamp.
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 10})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 1 | wpItRocketLauncher, Time: 10})
	// Later, still dead: a bookkeeping flip must be absorbed.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 1 | wpItRocketLauncher | wpItLightning, Time: 11.5})

	r := &Result{}
	_ = a.Finalize(r)
	ps := r.WeaponPickups
	if len(ps) != 1 {
		t.Fatalf("want exactly the death-frame RL grab, got %d: %+v", len(ps), ps)
	}
	if ps[0].Weapon != "rl" || ps[0].Source != "world" || !ps[0].Inferred {
		t.Errorf("got %+v, want weapon=rl source=world inferred=true", ps[0])
	}
}

// The flip baseline must be maintained through warmup: when a player's
// first in-match STAT_ITEMS update is already the pickup (the spawn
// burst landed before the match-start print), seeding on it would
// swallow the grant.
func TestWeaponPickups_WeaponStayWarmupBaselineCarries(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	a.timing.Started = false // exercise the warmup phase explicitly
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 100, Kind: "rl", Origin: [3]float32{0, 0, 0}, Time: 0})
	// Warmup: baseline seeds (SG only) before the match starts.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 1, Time: 8})
	a.timing.Started = true
	// First in-match update IS the RL grant.
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{10, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 1 | wpItRocketLauncher, Time: 10})

	r := &Result{}
	_ = a.Finalize(r)
	ps := r.WeaponPickups
	if len(ps) != 1 {
		t.Fatalf("want 1 pickup (warmup-seeded baseline), got %d: %+v", len(ps), ps)
	}
	if ps[0].Weapon != "rl" || !ps[0].Inferred {
		t.Errorf("got %+v, want weapon=rl inferred=true", ps[0])
	}
}

// Non-weapon-stay mode (dmm1): the synthesis gate stays off; a
// STAT_ITEMS flip on its own produces nothing (the //ktx took hint
// pipeline owns dmm1 pickups).
func TestWeaponPickups_NoSynthesisInDmm1(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\1"`, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 5})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 10})

	r := &Result{}
	_ = a.Finalize(r)
	if len(r.WeaponPickups) != 0 {
		t.Fatalf("want 0 pickups (no synthesis in dmm1), got %+v", r.WeaponPickups)
	}
}

// Defensive path: if a //ktx took hint arrives *after* the stat flip
// already synthesized a record, the record is upgraded in place (the
// hint is authoritative) instead of appending a duplicate.
func TestWeaponPickups_LateHintUpgradesInferredRecord(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 100, Kind: "rl", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 5})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{10, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 10})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 100, PlayerEnt: 1, Time: 10.1})

	r := &Result{}
	_ = a.Finalize(r)
	ps := r.WeaponPickups
	if len(ps) != 1 {
		t.Fatalf("want 1 pickup (late hint upgrades, not duplicates), got %d: %+v", len(ps), ps)
	}
	if ps[0].Source != "world" || ps[0].Inferred || ps[0].HadBefore {
		t.Errorf("got %+v, want source=world inferred=false hadBefore=false", ps[0])
	}
}

// No matching death before match end → NextDeathTime=0, and every
// qualifying frag after the pickup counts (no upper bound).
func TestWeaponPickups_NoNextDeathKillsUnbounded(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "survivor"}

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 1, Kind: "lg", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 5})

	a.core = &CoreOutputs{FragEntries: []FragEntry{
		{Time: 10000, Killer: "survivor", Weapon: "lg"},
		{Time: 50000, Killer: "survivor", Weapon: "lg"},
		{Time: 99000, Killer: "survivor", Weapon: "lg"},
	}}

	r := &Result{}
	_ = a.Finalize(r)
	out := r.WeaponPickups
	ps := out
	if ps[0].NextDeathTime != 0 {
		t.Errorf("NextDeathTime = %v, want 0", ps[0].NextDeathTime)
	}
	if ps[0].Kills != 3 {
		t.Errorf("Kills = %d, want 3 (all frags with lg, no death bound)", ps[0].Kills)
	}
}
