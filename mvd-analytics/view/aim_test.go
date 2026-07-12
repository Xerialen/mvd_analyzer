package view

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// aimFixture builds a Result whose stored Aim covers two shooters (A, B) and
// whose Shots/Streams support a windowed recompute. A looks +X at B (+1000 X);
// A fires lg at t=1000/2000/3000. The stored Aim is what the analyzer would
// have produced; here we set it directly plus the raw Shots/Streams so the
// windowed path (aimcore.Compute) has real inputs.
func aimFixture() *result.Result {
	track := func(name string, x float64) result.PlayerStream {
		return result.PlayerStream{
			Name: name,
			Position: &result.PositionTrack{
				T:   []int32{0, 5000},
				X:   []float32{float32(x), float32(x)},
				Y:   []float32{0, 0},
				Z:   []float32{0, 0},
				VP:  []int16{0, 0},
				VYa: []int16{0, 0},
			},
		}
	}
	r := &result.Result{
		Shots: &result.ShotsResult{
			Shots: []result.Shot{
				{Time: 1000, Player: "A", Weapon: "lg", Hit: false},
				{Time: 2000, Player: "A", Weapon: "lg", Hit: true},
				{Time: 3000, Player: "A", Weapon: "lg", Hit: true},
			},
			ByPlayer: []result.PlayerShots{{Player: "A"}, {Player: "B"}},
		},
		Streams: &result.Streams{
			Players: []result.PlayerStream{track("A", 0), track("B", 1000)},
		},
	}
	// Stored aim: what the analyzer produced (two players, aggregates +
	// sample blocks). We keep it distinguishable from a recompute by giving
	// each player a Crosshair + LGRamp block and a weapons row.
	r.Aim = &result.AimResult{Players: []result.PlayerAim{
		{
			Player: "A", Mode: "duel",
			Weapons:   []result.WeaponAim{{Weapon: "lg", Shots: 3, Hits: 2}},
			Crosshair: &result.CrosshairSamples{T: []int32{1000, 2000, 3000}},
			LGRamp:    &result.LGRampSamples{Since: []int32{0, 1000, 2000}},
		},
		{
			Player: "B", Mode: "duel",
			Weapons:   []result.WeaponAim{{Weapon: "sg", Shots: 5, Hits: 1}},
			Crosshair: &result.CrosshairSamples{T: []int32{500}},
			LGRamp:    &result.LGRampSamples{Since: []int32{0}},
		},
	}}
	return r
}

func findAimPlayer(am *result.AimResult, name string) *result.PlayerAim {
	for i := range am.Players {
		if am.Players[i].Player == name {
			return &am.Players[i]
		}
	}
	return nil
}

func TestAim_Unavailable(t *testing.T) {
	if _, err := Aim(&result.Result{}, AimOptions{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Aim on empty result = %v, want ErrUnavailable", err)
	}
}

// Unfiltered Aim returns the STORED aim by identity — the extraction/view path
// must not recompute or copy when nothing is asked for.
func TestAim_UnfilteredReturnsStored(t *testing.T) {
	r := aimFixture()
	got, err := Aim(r, AimOptions{})
	if err != nil {
		t.Fatalf("Aim: %v", err)
	}
	if got != r.Aim {
		t.Fatalf("unfiltered Aim = %p, want the stored %p (same pointer)", got, r.Aim)
	}
}

// players= with no window selects the named players' STORED aim (match-wide),
// byte-identical to their stored PlayerAim.
func TestAim_PlayersOnlySelectsStored(t *testing.T) {
	r := aimFixture()
	got, err := Aim(r, AimOptions{Players: []string{"A"}})
	if err != nil {
		t.Fatalf("Aim: %v", err)
	}
	if len(got.Players) != 1 || got.Players[0].Player != "A" {
		t.Fatalf("players=[A] returned %+v, want only A", got.Players)
	}
	stored := findAimPlayer(r.Aim, "A")
	if !reflect.DeepEqual(got.Players[0], *stored) {
		t.Errorf("players=[A] aim != stored A aim:\n got %+v\nwant %+v", got.Players[0], *stored)
	}
}

// summary drops the Crosshair + LGRamp sample blocks but keeps Player/Team/
// Mode/Weapons; it does not mutate the shared stored aim.
func TestAim_SummaryDropsSampleBlocks(t *testing.T) {
	r := aimFixture()
	got, err := Aim(r, AimOptions{Summary: true})
	if err != nil {
		t.Fatalf("Aim: %v", err)
	}
	if len(got.Players) != 2 {
		t.Fatalf("summary players = %d, want 2", len(got.Players))
	}
	for _, pa := range got.Players {
		if pa.Crosshair != nil || pa.LGRamp != nil {
			t.Errorf("player %s: summary kept sample blocks (crosshair=%v lgRamp=%v)", pa.Player, pa.Crosshair, pa.LGRamp)
		}
		if len(pa.Weapons) == 0 {
			t.Errorf("player %s: summary dropped weapons", pa.Player)
		}
	}
	// The stored aim must still carry its sample blocks (no mutation).
	if a := findAimPlayer(r.Aim, "A"); a.Crosshair == nil || a.LGRamp == nil {
		t.Errorf("summary mutated the stored aim: A crosshair=%v lgRamp=%v", a.Crosshair, a.LGRamp)
	}
}

// A time window RECOMPUTES over the windowed shots. The window [1.5s, 5s]
// excludes A's t=1000 fire, so lg shots drop from 3 to 2, and every crosshair
// sample must fall inside the window.
func TestAim_TimeWindowRecomputes(t *testing.T) {
	r := aimFixture()
	got, err := Aim(r, AimOptions{From: 1.5})
	if err != nil {
		t.Fatalf("Aim: %v", err)
	}
	a := findAimPlayer(got, "A")
	if a == nil {
		t.Fatalf("window recompute dropped player A: %+v", got.Players)
	}
	var lg *result.WeaponAim
	for i := range a.Weapons {
		if a.Weapons[i].Weapon == "lg" {
			lg = &a.Weapons[i]
		}
	}
	if lg == nil || lg.Shots != 2 {
		t.Fatalf("windowed lg = %+v, want 2 shots (t=1000 excluded)", lg)
	}
	// Every crosshair sample within the window (from=1500ms, no upper bound).
	if a.Crosshair != nil {
		for i, tt := range a.Crosshair.T {
			if tt < 1500 {
				t.Errorf("crosshair sample %d at t=%d is before the window start 1500", i, tt)
			}
		}
	}
}

// A window with an upper bound only: [0, 1.5s] keeps just A's t=1000 fire.
func TestAim_TimeWindowUpperBound(t *testing.T) {
	r := aimFixture()
	got, err := Aim(r, AimOptions{To: 1.5})
	if err != nil {
		t.Fatalf("Aim: %v", err)
	}
	a := findAimPlayer(got, "A")
	if a == nil {
		t.Fatalf("window recompute dropped A: %+v", got.Players)
	}
	var lg *result.WeaponAim
	for i := range a.Weapons {
		if a.Weapons[i].Weapon == "lg" {
			lg = &a.Weapons[i]
		}
	}
	if lg == nil || lg.Shots != 1 {
		t.Fatalf("windowed lg = %+v, want 1 shot (only t=1000 <= 1500)", lg)
	}
}

// summary composes with a window: recompute, then drop the sample blocks.
func TestAim_WindowAndSummary(t *testing.T) {
	r := aimFixture()
	got, err := Aim(r, AimOptions{From: 1.5, Summary: true})
	if err != nil {
		t.Fatalf("Aim: %v", err)
	}
	a := findAimPlayer(got, "A")
	if a == nil {
		t.Fatalf("dropped A: %+v", got.Players)
	}
	if a.Crosshair != nil || a.LGRamp != nil {
		t.Errorf("window+summary kept sample blocks on A: crosshair=%v lgRamp=%v", a.Crosshair, a.LGRamp)
	}
	if len(a.Weapons) == 0 {
		t.Errorf("window+summary dropped weapons on A")
	}
}

// TestFilteredEmptyAimIsArrayNotNull mirrors TestFilteredEmptyLogIsArrayNotNull
// for aim: a scoping filter (players or window) that matches no shooter must
// return players:[], never null — the shape the summary path already produced.
func TestFilteredEmptyAimIsArrayNotNull(t *testing.T) {
	r := aimFixture()

	// players filter matching nobody (no window: the players-select branch).
	got, err := Aim(r, AimOptions{Players: []string{"nobody"}})
	if err != nil {
		t.Fatalf("Aim: %v", err)
	}
	if got.Players == nil {
		t.Errorf("players-filtered-empty aim.players must be [], not null")
	}

	// window matching no shots (recompute branch), scoped to nobody so the
	// recompute yields no players.
	got2, err := Aim(r, AimOptions{From: 1.5, Players: []string{"nobody"}})
	if err != nil {
		t.Fatalf("Aim: %v", err)
	}
	if got2.Players == nil {
		t.Errorf("windowed-filtered-empty aim.players must be [], not null")
	}
}
