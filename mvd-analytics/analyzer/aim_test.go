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
			// (The rl/gl direct/splash block gates on a linked rl/gl fire in
			// Shots, not on this opt-in stream — see F18 and the test below.)
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

	// Per-weapon: LG — 3 shots, 2 hits; the single miss (t=1000) joins the
	// ~1000 u beam, past max range → out of range.
	lg := findWeaponAim(pa, "lg")
	if lg == nil || lg.Shots != 3 || lg.Hits != 2 || lg.OutOfRange != 1 || lg.Blocked != 0 {
		t.Fatalf("lg weapon = %+v, want shots3 hits2 far1 blocked0", lg)
	}
}

// F18: the rl/gl direct/splash split must appear on a default parse — no
// opt-in streams.projectiles — as long as projectile linking produced a
// linked rl/gl fire. The old gate keyed on the stream's presence, which is
// emission-only, so every default parse silently lost the block.
func TestAimPostRocketBlockWithoutProjectileStream(t *testing.T) {
	res := &result.Result{
		Shots: &result.ShotsResult{
			Shots: []result.Shot{
				{Time: 2000, Player: "A", Weapon: "rl", Hit: true},
				{Time: 2100, Player: "A", Weapon: "rl", Hit: false},
			},
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
			// No Projectiles, no Beams — the default parse shape.
		},
	}

	aimPost(res, nil)

	if res.Aim == nil || len(res.Aim.Players) != 1 {
		t.Fatalf("expected 1 aim player, got %+v", res.Aim)
	}
	rl := findWeaponAim(res.Aim.Players[0], "rl")
	if rl == nil || rl.Direct != 1 || rl.Splash != 0 || rl.Missed != 1 {
		t.Fatalf("rl = %+v, want direct1 splash0 missed1 without the opt-in stream (F18)", rl)
	}
}

// F19: the damage records feeding the rl/gl direct split are windowed to
// match time — a warmup direct rocket (negative post-normalize time) or a
// post-match one (past matchEnd) must not inflate Direct / deflate Splash.
func TestAimPostDamageWindowedToMatch(t *testing.T) {
	res := &result.Result{
		Shots: &result.ShotsResult{
			Shots: []result.Shot{
				{Time: 2000, Player: "A", Weapon: "rl", Hit: true},
				{Time: 2100, Player: "A", Weapon: "rl", Hit: true},
			},
		},
		Damage: &result.DamageResult{
			Events: []result.DamageEntry{
				{Time: 2000, Attacker: "A", Victim: "B", Weapon: "rl"},                 // in-match direct
				{Time: 2100, Attacker: "A", Victim: "B", Weapon: "rl", IsSplash: true}, // in-match splash
				{Time: -500, Attacker: "A", Victim: "B", Weapon: "rl"},                 // warmup direct — excluded
				{Time: 3500, Attacker: "A", Victim: "B", Weapon: "rl"},                 // post-match direct — excluded
			},
		},
		Streams: &result.Streams{
			Global: result.GlobalStream{MatchEnd: 3000},
			Players: []result.PlayerStream{
				aimTrack("A", 0, 0, 0, 3000),
				aimTrack("B", 1000, 0, 0, 3000),
			},
		},
	}

	aimPost(res, nil)

	if res.Aim == nil || len(res.Aim.Players) != 1 {
		t.Fatalf("expected 1 aim player, got %+v", res.Aim)
	}
	rl := findWeaponAim(res.Aim.Players[0], "rl")
	if rl == nil || rl.Direct != 1 || rl.Splash != 1 || rl.Missed != 0 {
		t.Fatalf("rl = %+v, want direct1 splash1 missed0 (out-of-window damage excluded, F19)", rl)
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

// A team-victim hit flags its crosshair sample and slices the per-weapon
// counters: the Team split carries the team hit/pellets, the Enemy split is
// emitted alongside (all-zero here — every hit was friendly fire) so the
// consumer's enemy view never falls back to the unsplit counters. A second,
// all-enemy player gets no Team column and no splits at all.
func TestAimPostTeamHitSplits(t *testing.T) {
	res := &result.Result{
		Shots: &result.ShotsResult{
			Shots: []result.Shot{
				// A shotguns teammate C: 12 damage = 3 of 6 pellets.
				{Time: 1000, Player: "A", Team: "red", Weapon: "sg", Hit: true,
					Victims: []string{"C"}, VictimKinds: []string{"team"}},
				// B shotguns enemy A: the common all-enemy case (kinds omitted).
				{Time: 2000, Player: "B", Team: "blue", Weapon: "sg", Hit: true,
					Victims: []string{"A"}},
			},
			ByPlayer: []result.PlayerShots{
				{Player: "A", Team: "red"},
				{Player: "B", Team: "blue"},
				{Player: "C", Team: "red"},
			},
		},
		Damage: &result.DamageResult{
			Events: []result.DamageEntry{
				{Time: 1000, Attacker: "A", Victim: "C", Weapon: "sg", Damage: 12, IsTeam: true},
				{Time: 2000, Attacker: "B", Victim: "A", Weapon: "sg", Damage: 24},
			},
		},
		Streams: &result.Streams{
			Players: []result.PlayerStream{
				aimTrack("A", 0, 0, 0, 3000),
				aimTrack("B", 1000, 0, 0, 3000),
				aimTrack("C", 1000, 300, 0, 3000),
			},
		},
	}
	aimPost(res, nil)
	if res.Aim == nil || len(res.Aim.Players) != 2 {
		t.Fatalf("expected 2 aim players, got %+v", res.Aim)
	}
	pa, pb := res.Aim.Players[0], res.Aim.Players[1]

	// A: the sample targets teammate C and is Team-flagged.
	if pa.Crosshair == nil || len(pa.Crosshair.T) != 1 || pa.Crosshair.Target[0] != "C" {
		t.Fatalf("A crosshair = %+v, want 1 sample targeting C", pa.Crosshair)
	}
	if len(pa.Crosshair.Team) != 1 || !pa.Crosshair.Team[0] {
		t.Errorf("A crosshair Team = %v, want [true]", pa.Crosshair.Team)
	}
	sg := findWeaponAim(pa, "sg")
	if sg == nil || sg.Hits != 1 || sg.PelletHits != 3 {
		t.Fatalf("A sg = %+v, want hits1 pelletHits3", sg)
	}
	if sg.Team == nil || sg.Team.Hits != 1 || sg.Team.PelletHits != 3 || sg.Team.Partial != 1 {
		t.Errorf("A sg Team split = %+v, want hits1 pelletHits3 partial1", sg.Team)
	}
	if sg.Enemy == nil || sg.Enemy.Hits != 0 || sg.Enemy.PelletHits != 0 || sg.Enemy.Miss != 1 {
		t.Errorf("A sg Enemy split = %+v, want emitted all-zero hits with miss1", sg.Enemy)
	}
	if sg.Self != nil {
		t.Errorf("A sg Self split = %+v, want nil (hitscan cannot self-hit)", sg.Self)
	}

	// B: all-enemy — no Team column, no splits (consumers fall back to the
	// top-level counters).
	if pb.Crosshair == nil || pb.Crosshair.Team != nil {
		t.Fatalf("B crosshair = %+v, want samples with nil Team", pb.Crosshair)
	}
	bsg := findWeaponAim(pb, "sg")
	if bsg == nil || bsg.Hits != 1 || bsg.Enemy != nil || bsg.Team != nil || bsg.Self != nil {
		t.Errorf("B sg = %+v, want hits1 with no splits", bsg)
	}
}

// A connected LG fire whose only victims are teammates is Team-flagged on the
// ramp (consumers score enemy ramp hit% as hit && !team); an enemy hit in the
// same shaft stays unflagged.
func TestAimPostRampTeamFlag(t *testing.T) {
	res := &result.Result{
		Shots: &result.ShotsResult{
			Shots: []result.Shot{
				{Time: 1000, Player: "A", Team: "red", Weapon: "lg", Hit: true,
					Victims: []string{"C"}, VictimKinds: []string{"team"}},
				{Time: 1050, Player: "A", Team: "red", Weapon: "lg", Hit: true,
					Victims: []string{"B"}},
			},
			ByPlayer: []result.PlayerShots{
				{Player: "A", Team: "red"},
				{Player: "B", Team: "blue"},
				{Player: "C", Team: "red"},
			},
		},
		Streams: &result.Streams{
			Players: []result.PlayerStream{
				aimTrack("A", 0, 0, 0, 3000),
				aimTrack("B", 1000, 0, 0, 3000),
				aimTrack("C", 1000, 300, 0, 3000),
			},
		},
	}
	aimPost(res, nil)
	if res.Aim == nil || len(res.Aim.Players) != 1 {
		t.Fatalf("expected 1 aim player, got %+v", res.Aim)
	}
	ramp := res.Aim.Players[0].LGRamp
	if ramp == nil || len(ramp.Since) != 2 {
		t.Fatalf("lgRamp = %+v, want 2 fires", ramp)
	}
	if len(ramp.Team) != 2 || !ramp.Team[0] || ramp.Team[1] {
		t.Errorf("lgRamp Team = %v, want [true false]", ramp.Team)
	}
}

// Classification of a single missed LG fire: blocked (the beam stopped
// short on geometry and its extension to full range crosses the enemy hull
// — the obstruction denied a would-be hit), far (the beam ran its full
// ~600 u length and its extension to infinity crosses the enemy hull — on
// target, enemy beyond reach), miss (every other whiff — an aim error, no
// enemy on the beam's line).
func TestAimPostLGMissClasses(t *testing.T) {
	cases := []struct {
		name       string
		bx, by, bz float64 // enemy B origin
		ex, ey, ez float32 // beam endpoint; start is always A's muzzle (0,0,22)
		want       string
	}{
		// Perpendicular wall shot 500 u from B: nothing was in the way of a
		// would-be hit, so it is a plain miss (pre-v47 this was "blocked").
		{"wide wall shot", 1000, 0, 0, 0, 500, 0, "miss"},
		// Beam stops at x=200 aimed at B at x=400: the extension to full
		// range crosses B's hull, so the obstruction denied a real hit.
		{"enemy behind obstruction", 400, 0, 0, 200, 0, 10, "blocked"},
		// Beam passes 24 u off B's hull mid-flight and ends on a wall far
		// behind: the extension never crosses the hull → an aim error, not
		// blocked (pre-v47 this was "blocked").
		{"passed the hull mid-flight", 200, 40, 0, 500, 0, 4, "miss"},
		// Non-hit whose beam ends inside the hull (a lag-compensation /
		// track-interpolation artifact): the extension starts inside the
		// hull, so it reads as a denied would-be hit.
		{"endpoint on the hull", 300, 0, 0, 300, 0, 4, "blocked"},
		// Full-length beam pointing dead at B beyond max range: on target,
		// denied only by reach.
		{"enemy on the line beyond reach", 1000, 0, 0, 590, 0, 22, "far"},
		// Full-length beam with B perpendicular to it: aimed into open
		// space, nobody anywhere on the line — a plain miss, not "far".
		{"full-length into open space", 0, 1000, 0, 590, 0, 22, "miss"},
		// Full-length beam that narrowly passed the hull mid-flight with
		// nobody on the line beyond: an aim error too.
		{"full-length past the hull", 200, 40, 0, 590, 0, 22, "miss"},
		// Degenerate zero-length beam: no direction to extend.
		{"zero-length beam", 1000, 0, 0, 0, 0, 22, "miss"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &result.Result{
				Shots: &result.ShotsResult{
					Shots:    []result.Shot{{Time: 1000, Player: "A", Weapon: "lg", Hit: false}},
					ByPlayer: []result.PlayerShots{{Player: "A"}, {Player: "B"}},
				},
				Streams: &result.Streams{
					Players: []result.PlayerStream{
						aimTrack("A", 0, 0, 0, 3000),
						aimTrack("B", tc.bx, tc.by, tc.bz, 3000),
					},
					Beams: &result.BeamStreams{
						T:  []int32{1000},
						Sx: []float32{0}, Sy: []float32{0}, Sz: []float32{22},
						Ex: []float32{tc.ex}, Ey: []float32{tc.ey}, Ez: []float32{tc.ez},
					},
				},
			}
			aimPost(res, nil)
			if res.Aim == nil || len(res.Aim.Players) != 1 {
				t.Fatalf("expected 1 aim player, got %+v", res.Aim)
			}
			lg := findWeaponAim(res.Aim.Players[0], "lg")
			if lg == nil {
				t.Fatalf("no lg weapon aim")
			}
			got := map[string]int{
				"blocked": lg.Blocked,
				"far":     lg.OutOfRange,
				"miss":    lg.Miss,
			}
			for class, n := range got {
				want := 0
				if class == tc.want {
					want = 1
				}
				if n != want {
					t.Errorf("%s = %d, want %d (weapon %+v)", class, n, want, lg)
				}
			}
		})
	}
}
