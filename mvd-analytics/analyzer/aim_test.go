package analyzer

import (
	"math"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// flat track: constant position + view over [0,end], so SampleAt covers any
// fire time in range.
func aimTrack(name string, x, y, z float64, end int32) result.PlayerStream {
	return result.PlayerStream{
		Name: name,
		Position: &result.PositionTrack{
			T:   []int32{0, end},
			X:   []float32{float32(x), float32(x)},
			Y:   []float32{float32(y), float32(y)},
			Z:   []float32{float32(z), float32(z)},
			VP:  []int16{0, 0},
			VYa: []int16{0, 0},
		},
	}
}

func TestAimPostDuel(t *testing.T) {
	// A at origin looking +X (yaw 0, pitch 0); B straight ahead at +1000 X.
	res := &result.Result{
		Shots: &result.ShotsResult{
			Shots: []result.Shot{
				{Time: 1000, Player: "A", Weapon: "lg", Hit: false},
				{Time: 1050, Player: "A", Weapon: "lg", Hit: true},
				{Time: 1100, Player: "A", Weapon: "lg", Hit: true},
				{Time: 2000, Player: "A", Weapon: "rl", Hit: true},
				{Time: 2100, Player: "A", Weapon: "rl", Hit: false},
			},
			ByPlayer: []result.PlayerShots{{Player: "A"}, {Player: "B"}},
		},
		Damage: &result.DamageResult{
			Events: []result.DamageEntry{
				{Time: 2000, Attacker: "A", Victim: "B", Weapon: "rl"}, // non-splash → direct
			},
		},
		Streams: &result.Streams{
			Players: []result.PlayerStream{
				aimTrack("A", 0, 0, 0, 3000),
				aimTrack("B", 1000, 0, 0, 3000),
			},
			Projectiles: &result.ProjectileStreams{}, // non-nil → rocket block enabled
			Beams: &result.BeamStreams{
				T:  []int32{1000},
				Sx: []float32{0}, Sy: []float32{0}, Sz: []float32{22}, // muzzle ≈ A eye
				Ex: []float32{1000}, Ey: []float32{0}, Ez: []float32{4}, // endpoint on B
			},
		},
	}

	aimPost(res, nil)

	if res.Aim == nil || len(res.Aim.Players) != 1 {
		t.Fatalf("expected 1 aim player, got %+v", res.Aim)
	}
	pa := res.Aim.Players[0]
	if pa.Player != "A" || pa.Mode != "duel" {
		t.Fatalf("player/mode = %q/%q, want A/duel", pa.Player, pa.Mode)
	}

	// Crosshair: 3 hitscan (lg) fires, all attributed to B, aimed dead-on in
	// yaw (B is straight ahead).
	if pa.Crosshair == nil || len(pa.Crosshair.T) != 3 {
		t.Fatalf("crosshair = %+v, want 3 samples", pa.Crosshair)
	}
	for i := range pa.Crosshair.T {
		if pa.Crosshair.Target[i] != "B" {
			t.Errorf("sample %d target = %q, want B", i, pa.Crosshair.Target[i])
		}
		if math.Abs(float64(pa.Crosshair.NYaw[i])) > 0.05 {
			t.Errorf("sample %d NYaw = %v, want ~0", i, pa.Crosshair.NYaw[i])
		}
	}
	if got := pa.Crosshair.Hit; !(got[0] == false && got[1] && got[2]) {
		t.Errorf("crosshair Hit = %v, want [false true true]", got)
	}

	// LG ramp: one shaft (50 ms gaps < 150), so Since = 0,50,100.
	if pa.LGRamp == nil || len(pa.LGRamp.Since) != 3 ||
		pa.LGRamp.Since[0] != 0 || pa.LGRamp.Since[1] != 50 || pa.LGRamp.Since[2] != 100 {
		t.Fatalf("lgRamp Since = %+v, want [0 50 100]", pa.LGRamp)
	}

	// Per-weapon: RL — N=2, one linked hit which is a direct; identity holds.
	rl := findWeaponAim(pa, "rl")
	if rl == nil || rl.Shots != 2 || rl.Hits != 1 || rl.Direct != 1 || rl.Splash != 0 || rl.Missed != 1 {
		t.Fatalf("rl weapon = %+v, want shots2 hits1 direct1 splash0 missed1", rl)
	}
	if rl.Direct+rl.Splash+rl.Missed != rl.Shots {
		t.Errorf("rocket identity broken: %+v", rl)
	}

	// Per-weapon: LG — 3 shots, 2 hits; the single miss (t=1000) joins the beam
	// ending on B → near-miss.
	lg := findWeaponAim(pa, "lg")
	if lg == nil || lg.Shots != 3 || lg.Hits != 2 || lg.NearMiss != 1 || lg.Blocked != 0 {
		t.Fatalf("lg weapon = %+v, want shots3 hits2 near1 blocked0", lg)
	}
}

func findWeaponAim(pa result.PlayerAim, w string) *result.WeaponAim {
	for i := range pa.Weapons {
		if pa.Weapons[i].Weapon == w {
			return &pa.Weapons[i]
		}
	}
	return nil
}

// Attribution must skip a dead enemy: dead players keep streaming position
// samples (the death-anim body), so without the alive gate B — dead but
// dead-center at the first fire — would win over the alive, off-center C.
// The second fire lands after B's respawn, when B must win again.
func TestAimPostDeadEnemyExcluded(t *testing.T) {
	b := aimTrack("B", 1000, 0, 0, 3000)
	b.Deaths = []int32{500}
	b.Spawns = []int32{1500}
	res := &result.Result{
		Shots: &result.ShotsResult{
			Shots: []result.Shot{
				{Time: 1000, Player: "A", Team: "red", Weapon: "lg", Hit: false},
				{Time: 2000, Player: "A", Team: "red", Weapon: "lg", Hit: false},
			},
			ByPlayer: []result.PlayerShots{
				{Player: "A", Team: "red"},
				{Player: "B", Team: "blue"},
				{Player: "C", Team: "blue"},
			},
		},
		Streams: &result.Streams{
			Players: []result.PlayerStream{
				aimTrack("A", 0, 0, 0, 3000),
				b,
				aimTrack("C", 1000, 300, 0, 3000),
			},
		},
	}
	aimPost(res, nil)
	if res.Aim == nil || len(res.Aim.Players) != 1 {
		t.Fatalf("expected 1 aim player, got %+v", res.Aim)
	}
	cs := res.Aim.Players[0].Crosshair
	if cs == nil || len(cs.T) != 2 {
		t.Fatalf("crosshair = %+v, want 2 samples", cs)
	}
	if cs.Target[0] != "C" {
		t.Errorf("sample at t=1000 target = %q, want C (B dead)", cs.Target[0])
	}
	if cs.Target[1] != "B" {
		t.Errorf("sample at t=2000 target = %q, want B (respawned)", cs.Target[1])
	}
}

// In a duel a fire while the lone enemy is dead yields no crosshair sample —
// the per-weapon fire counts keep it, the placement series drops it.
func TestAimPostDuelDeadEnemyDropped(t *testing.T) {
	b := aimTrack("B", 1000, 0, 0, 3000)
	b.Deaths = []int32{500}
	b.Spawns = []int32{1500}
	res := &result.Result{
		Shots: &result.ShotsResult{
			Shots:    []result.Shot{{Time: 1000, Player: "A", Weapon: "sg", Hit: false}},
			ByPlayer: []result.PlayerShots{{Player: "A"}, {Player: "B"}},
		},
		Streams: &result.Streams{
			Players: []result.PlayerStream{aimTrack("A", 0, 0, 0, 3000), b},
		},
	}
	aimPost(res, nil)
	if res.Aim == nil || len(res.Aim.Players) != 1 {
		t.Fatalf("expected 1 aim player, got %+v", res.Aim)
	}
	pa := res.Aim.Players[0]
	if pa.Crosshair != nil {
		t.Fatalf("crosshair = %+v, want nil (enemy dead at fire time)", pa.Crosshair)
	}
	if sg := findWeaponAim(pa, "sg"); sg == nil || sg.Shots != 1 {
		t.Fatalf("sg weapon = %+v, want shots1 (fire still counted)", sg)
	}
}

// A hit attributes to its server-confirmed victim even when the victim dies
// on that very shot: the killing blow lands in the same frame as the death,
// so the liveness gate alone would drop the victim and hand the sample to
// the other live enemy — wherever they are (observed as edge-bin "hits" in
// the crosshair histograms, e.g. dyaw −75° on a beam ending inside the
// victim's hull). The dead-center victim B must win over the off-center
// live C.
func TestAimPostKillingBlowAttributesVictim(t *testing.T) {
	b := aimTrack("B", 1000, 0, 0, 3000)
	b.Deaths = []int32{1000} // dies on A's t=1000 shot
	res := &result.Result{
		Shots: &result.ShotsResult{
			Shots: []result.Shot{
				{Time: 1000, Player: "A", Team: "red", Weapon: "lg", Hit: true, Victims: []string{"B"}},
			},
			ByPlayer: []result.PlayerShots{
				{Player: "A", Team: "red"},
				{Player: "B", Team: "blue"},
				{Player: "C", Team: "blue"},
			},
		},
		Streams: &result.Streams{
			Players: []result.PlayerStream{
				aimTrack("A", 0, 0, 0, 3000),
				b,
				aimTrack("C", 1000, 300, 0, 3000),
			},
		},
	}
	aimPost(res, nil)
	if res.Aim == nil || len(res.Aim.Players) != 1 {
		t.Fatalf("expected 1 aim player, got %+v", res.Aim)
	}
	cs := res.Aim.Players[0].Crosshair
	if cs == nil || len(cs.T) != 1 {
		t.Fatalf("crosshair = %+v, want 1 sample", cs)
	}
	if cs.Target[0] != "B" {
		t.Errorf("target = %q, want B (confirmed victim)", cs.Target[0])
	}
	if math.Abs(float64(cs.NYaw[0])) > 0.05 {
		t.Errorf("NYaw = %v, want ~0 (aimed dead-on the victim)", cs.NYaw[0])
	}
}

// A duel killing blow keeps its crosshair sample: with the lone enemy read
// as dead at the fire time there is no heuristic candidate, but the
// confirmed victim carries the sample.
func TestAimPostDuelKillingBlowKept(t *testing.T) {
	b := aimTrack("B", 1000, 0, 0, 3000)
	b.Deaths = []int32{1000}
	res := &result.Result{
		Shots: &result.ShotsResult{
			Shots:    []result.Shot{{Time: 1000, Player: "A", Weapon: "lg", Hit: true, Victims: []string{"B"}}},
			ByPlayer: []result.PlayerShots{{Player: "A"}, {Player: "B"}},
		},
		Streams: &result.Streams{
			Players: []result.PlayerStream{aimTrack("A", 0, 0, 0, 3000), b},
		},
	}
	aimPost(res, nil)
	if res.Aim == nil || len(res.Aim.Players) != 1 {
		t.Fatalf("expected 1 aim player, got %+v", res.Aim)
	}
	cs := res.Aim.Players[0].Crosshair
	if cs == nil || len(cs.T) != 1 || cs.Target[0] != "B" {
		t.Fatalf("crosshair = %+v, want 1 sample targeting B", cs)
	}
}

// A miss whose beam ends far from any enemy is "blocked", not a near miss.
func TestAimPostReachBlocked(t *testing.T) {
	res := &result.Result{
		Shots: &result.ShotsResult{
			Shots:    []result.Shot{{Time: 1000, Player: "A", Weapon: "lg", Hit: false}},
			ByPlayer: []result.PlayerShots{{Player: "A"}, {Player: "B"}},
		},
		Streams: &result.Streams{
			Players: []result.PlayerStream{
				aimTrack("A", 0, 0, 0, 3000),
				aimTrack("B", 1000, 0, 0, 3000),
			},
			Beams: &result.BeamStreams{
				T:  []int32{1000},
				Sx: []float32{0}, Sy: []float32{0}, Sz: []float32{22},
				Ex: []float32{0}, Ey: []float32{500}, Ez: []float32{0}, // far from B
			},
		},
	}
	aimPost(res, nil)
	if res.Aim == nil || len(res.Aim.Players) != 1 {
		t.Fatalf("expected 1 aim player, got %+v", res.Aim)
	}
	lg := findWeaponAim(res.Aim.Players[0], "lg")
	if lg == nil || lg.Blocked != 1 || lg.NearMiss != 0 {
		t.Fatalf("lg weapon = %+v, want blocked1 near0", lg)
	}
}
