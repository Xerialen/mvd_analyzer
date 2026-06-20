package view

import (
	"errors"
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
