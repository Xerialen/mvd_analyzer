package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// TestFragSelfKillWeaponLabels: self-kills keep the weapon/cause that
// produced them so a real /kill ("X suicides", −2) is distinguishable from
// a weapon self-detonation (−1). Only the /kill keeps weapon "suicide".
func TestFragSelfKillWeaponLabels(t *testing.T) {
	cases := []struct {
		msg    string
		weapon string
	}{
		{"nexus suicides", "suicide"},                  // the /kill console command
		{"nexus discovers blast radius", "rl"},          // RL self-splash
		{"nexus becomes bored with life", "rl"},         // RL self (other random msg)
		{"nexus tries to put the pin back in", "gl"},    // GL self-detonation
		{"nexus electrocutes himself", "lg"},            // LG self-discharge
		{"nexus discharges into the lava", "lg"},        // LG discharge
		{"nexus fell to his death", "fall"},             // environmental
		{"nexus somehow becomes bored with life", "suicide"}, // unknown-cause catch-all
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			a := NewFragAnalyzer()
			ctx := &Context{FragsBySlot: map[int]int{}}
			ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "nexus", Team: "red"}
			_ = a.Init(ctx)
			a.timing.Started = true

			_ = a.OnEvent(&events.PrintEvent{Level: 1, Message: tc.msg + "\n", Time: 1})
			var res Result
			if err := a.Finalize(&res); err != nil {
				t.Fatal(err)
			}

			if len(res.Frags.Frags) != 1 {
				t.Fatalf("got %d frag entries, want 1", len(res.Frags.Frags))
			}
			f := res.Frags.Frags[0]
			if !f.IsSuicide || f.Killer != "nexus" || f.Victim != "nexus" {
				t.Fatalf("entry = %+v, want a nexus self-kill", f)
			}
			if f.Weapon != tc.weapon {
				t.Errorf("%q → weapon %q, want %q", tc.msg, f.Weapon, tc.weapon)
			}
			// A self-kill must never inflate the per-weapon enemy-kill tally.
			if n := res.Frags.ByWeapon[tc.weapon]; n != 0 {
				t.Errorf("byWeapon[%q] = %d after a self-kill, want 0", tc.weapon, n)
			}
		})
	}
}

// TestFragByWeaponEnemyKillsOnly: an enemy kill counts toward ByWeapon; a
// suicide with the same weapon does not.
func TestFragByWeaponEnemyKillsOnly(t *testing.T) {
	a := NewFragAnalyzer()
	ctx := &Context{FragsBySlot: map[int]int{}}
	ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "killa", Team: "red"}
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "prey", Team: "blue"}
	_ = a.Init(ctx)
	a.timing.Started = true

	_ = a.OnEvent(&events.PrintEvent{Level: 1, Message: "prey was nailed by killa\n", Time: 1})
	_ = a.OnEvent(&events.PrintEvent{Level: 1, Message: "killa discovers blast radius\n", Time: 2})
	var res Result
	if err := a.Finalize(&res); err != nil {
		t.Fatal(err)
	}

	if res.Frags.ByWeapon["ng"] != 1 {
		t.Errorf("byWeapon[ng] = %d, want 1 (enemy kill)", res.Frags.ByWeapon["ng"])
	}
	if res.Frags.ByWeapon["rl"] != 0 {
		t.Errorf("byWeapon[rl] = %d, want 0 (RL self-kill must not count)", res.Frags.ByWeapon["rl"])
	}
	if res.Frags.ByPlayer["killa"].Kills != 1 {
		t.Errorf("killa kills = %d, want 1", res.Frags.ByPlayer["killa"].Kills)
	}
}
