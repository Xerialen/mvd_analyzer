package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/qwdemo/events"
	"github.com/mvd-analyzer/qwdemo/mvd"
)

func newTestWeaponPickupsAnalyzer() (*WeaponPickupsAnalyzer, *Context) {
	a := NewWeaponPickupsAnalyzer()
	ctx := &Context{FragsBySlot: map[int]int{}}
	_ = a.Init(ctx)
	a.matchStarted = true
	return a, ctx
}

// World-spawner RL pickup. ItemSpawnEvent classifies the entity, then
// the hint attributes the touch to slot 4. hadBefore=false (player had
// no RL bit) and kills=2 (two RL frags before next death; axe frag and
// post-death RL frag don't count).
func TestWeaponPickups_WorldRLWithKills(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[4] = &mvd.PlayerInfo{Slot: 4, Name: "ace", Team: "red"}

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 100, Kind: "rl", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 100, PlayerEnt: 5, Time: 10})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 4, Time: 30})

	ctx.FragEntries = []FragEntry{
		{Time: 12, Killer: "ace", Victim: "x", Weapon: "rl"},
		{Time: 15, Killer: "ace", Victim: "y", Weapon: "axe"}, // wrong weapon
		{Time: 20, Killer: "ace", Victim: "z", Weapon: "rl"},
		{Time: 40, Killer: "ace", Victim: "w", Weapon: "rl"}, // post-death
	}

	out, _ := a.Finalize()
	ps, ok := out.([]WeaponPickup)
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
	if p.NextDeathTime != 30 {
		t.Errorf("NextDeathTime = %v, want 30", p.NextDeathTime)
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
	ctx.Players[2] = &mvd.PlayerInfo{Slot: 2, Name: "hoarder", Team: "blue"}

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 77, Kind: "rl", Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 2, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 4})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 77, PlayerEnt: 3, Time: 5})

	ctx.FragEntries = []FragEntry{
		{Time: 6, Killer: "hoarder", Weapon: "rl"},
	}

	out, _ := a.Finalize()
	ps := out.([]WeaponPickup)
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
	ctx.Players[1] = &mvd.PlayerInfo{Slot: 1, Name: "dropper", Team: "red"}
	ctx.Players[2] = &mvd.PlayerInfo{Slot: 2, Name: "thief", Team: "blue"}

	_ = a.OnEvent(&events.BackpackDropHintEvent{BackpackEnt: 200, ItemFlags: 32, PlayerEnt: 2, Time: 10})
	_ = a.OnEvent(&events.BackpackPickupHintEvent{BackpackEnt: 200, PlayerEnt: 3, Time: 11})

	out, _ := a.Finalize()
	ps := out.([]WeaponPickup)
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
	if p.BackpackEnt != 200 || p.DropTime != 10 {
		t.Errorf("entNum/dropTime = %d/%v", p.BackpackEnt, p.DropTime)
	}
}

// Armors and health are hinted by //ktx took too; the analyzer only
// records weapon kinds. No pickup entry should be emitted for
// armor/health.
func TestWeaponPickups_NonWeaponHintsIgnored(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &mvd.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 1, Kind: "ra", Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 2, Kind: "mh", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 5})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 2, PlayerEnt: 1, Time: 6})

	out, _ := a.Finalize()
	if out != nil {
		t.Errorf("out = %v, want nil (no weapon pickups)", out)
	}
}

// Teamkills and suicides in FragEntries must not count toward Kills —
// those aren't real effectiveness signals.
func TestWeaponPickups_TeamkillsAndSuicidesExcluded(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &mvd.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 1, Kind: "rl", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 5})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 30})

	ctx.FragEntries = []FragEntry{
		{Time: 10, Killer: "p", Weapon: "rl", IsSuicide: true},
		{Time: 15, Killer: "p", Weapon: "rl", IsTeamKill: true},
		{Time: 20, Killer: "p", Weapon: "rl"}, // the only real frag
	}

	out, _ := a.Finalize()
	ps := out.([]WeaponPickup)
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
	ctx.Players[0] = &mvd.PlayerInfo{Slot: 0, Name: "p"}

	// Pickup 1 at t=10 (hadBefore=false), pickup 2 at t=20
	// (hadBefore=true after StatUpdate at t=11), death at t=30.
	// Frags at t=12, t=15, t=25, t=28 — all RL.
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 1, Kind: "rl", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 10})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 11})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 20})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 30})

	ctx.FragEntries = []FragEntry{
		{Time: 12, Killer: "p", Weapon: "rl"},
		{Time: 15, Killer: "p", Weapon: "rl"},
		{Time: 25, Killer: "p", Weapon: "rl"},
		{Time: 28, Killer: "p", Weapon: "rl"},
	}

	out, _ := a.Finalize()
	ps := out.([]WeaponPickup)
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
	ctx.Players[0] = &mvd.PlayerInfo{Slot: 0, Name: "p"}

	// Life 1: pickup at t=10 (fresh), death at t=30 — STAT_ITEMS
	// clears at death, which the server sends as a StatUpdate.
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 1, Kind: "rl", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 10})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 11})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 30})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 30})
	// Life 2: pickup at t=40 (fresh again), no further death.
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 40})

	ctx.FragEntries = []FragEntry{
		{Time: 20, Killer: "p", Weapon: "rl"}, // life 1
		{Time: 45, Killer: "p", Weapon: "rl"}, // life 2
		{Time: 50, Killer: "p", Weapon: "rl"}, // life 2
	}

	out, _ := a.Finalize()
	ps := out.([]WeaponPickup)
	if ps[0].Kills != 1 {
		t.Errorf("life-1 pickup kills = %d, want 1", ps[0].Kills)
	}
	if ps[1].HadBefore || ps[1].Kills != 2 {
		t.Errorf("life-2 pickup = %+v, want hadBefore=false kills=2", ps[1])
	}
}

// No matching death before match end → NextDeathTime=0, and every
// qualifying frag after the pickup counts (no upper bound).
func TestWeaponPickups_NoNextDeathKillsUnbounded(t *testing.T) {
	a, ctx := newTestWeaponPickupsAnalyzer()
	ctx.Players[0] = &mvd.PlayerInfo{Slot: 0, Name: "survivor"}

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 1, Kind: "lg", Time: 0})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 1, PlayerEnt: 1, Time: 5})

	ctx.FragEntries = []FragEntry{
		{Time: 10, Killer: "survivor", Weapon: "lg"},
		{Time: 50, Killer: "survivor", Weapon: "lg"},
		{Time: 99, Killer: "survivor", Weapon: "lg"},
	}

	out, _ := a.Finalize()
	ps := out.([]WeaponPickup)
	if ps[0].NextDeathTime != 0 {
		t.Errorf("NextDeathTime = %v, want 0", ps[0].NextDeathTime)
	}
	if ps[0].Kills != 3 {
		t.Errorf("Kills = %d, want 3 (all frags with lg, no death bound)", ps[0].Kills)
	}
}
