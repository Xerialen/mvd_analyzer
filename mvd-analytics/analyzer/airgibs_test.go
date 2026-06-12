package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// airgibTestResult builds a minimal Result exercising the airgib filter:
// an airborne rocket hit, a grounded one, a self hit, a team hit, and a
// non-rocket hit — only the first should survive.
func airgibTestResult() *result.Result {
	vic := result.PlayerStream{
		Name: "vic", Team: "red",
		Position: &result.PositionTrack{
			T:  []int32{900, 1000, 1100, 1200},
			X:  []int32{0, 0, 0, 0},
			Y:  []int32{0, 0, 0, 0},
			Z:  []int32{180, 200, 24, 0},
			Li: []int16{1, 1, 1, 0},
			H:  []int32{120, 150, 0, result.NoFloor}, // airborne, airborne, grounded, void
		},
	}
	att := result.PlayerStream{Name: "att", Team: "blue", Position: &result.PositionTrack{
		T: []int32{1000}, X: []int32{0}, Y: []int32{0}, Z: []int32{40}, H: []int32{0},
	}}
	return &result.Result{
		Streams: &result.Streams{Players: []result.PlayerStream{vic, att}},
		Damage: &result.DamageResult{Events: []result.DamageEntry{
			{Time: 1000, Attacker: "att", Victim: "vic", Weapon: "rl", Damage: 110},               // airborne → airgib
			{Time: 1100, Attacker: "att", Victim: "vic", Weapon: "rl", Damage: 90},                // grounded → no
			{Time: 1000, Attacker: "att", Victim: "vic", Weapon: "lg", Damage: 30},                // not a rocket → no
			{Time: 1000, Attacker: "vic", Victim: "vic", Weapon: "rl", Damage: 50, IsSelf: true},  // self → no
			{Time: 1000, Attacker: "mate", Victim: "vic", Weapon: "rl", Damage: 40, IsTeam: true}, // team → no
		}},
		Frags: &result.FragResult{Frags: []result.FragEntry{
			{Time: 1040, Killer: "att", Victim: "vic", Weapon: "rl"}, // lethal for the airborne hit
		}},
		TimelineAnalysis: &result.TimelineAnalysisResult{
			LocTable:      []string{"", "MID"},
			PlayerUserIDs: map[string]int{"att": 5, "vic": 7},
		},
	}
}

func TestAirgibsPost_DetectsAirborneRocketHit(t *testing.T) {
	res := airgibTestResult()
	airgibsPost(res, &CoreOutputs{})

	got := res.TimelineAnalysis.Airgibs
	if len(got) != 1 {
		t.Fatalf("airgibs = %d, want 1: %+v", len(got), got)
	}
	a := got[0]
	if a.Attacker != "att" || a.Victim != "vic" {
		t.Errorf("players = %s→%s, want att→vic", a.Attacker, a.Victim)
	}
	if a.Height != 150 {
		t.Errorf("height = %d, want 150 (sample at t=1000)", a.Height)
	}
	if a.HeightAboveAttacker != 160 {
		t.Errorf("heightAboveAttacker = %d, want 160 (victim Z 200 - shooter Z 40)", a.HeightAboveAttacker)
	}
	if a.Loc != "MID" {
		t.Errorf("loc = %q, want MID", a.Loc)
	}
	if !a.Lethal {
		t.Errorf("lethal = false, want true (rocket frag at 1040)")
	}
	if a.AttackerUserID != 5 || a.VictimUserID != 7 {
		t.Errorf("userIDs = %d/%d, want 5/7", a.AttackerUserID, a.VictimUserID)
	}
}

func TestAirgibsPost_SortedByHeightUncapped(t *testing.T) {
	// Build many airborne hits with ascending height; expect every
	// qualifying hit emitted (no cap, schema v30), sorted descending.
	const n = 25
	pos := &result.PositionTrack{}
	var dmg []result.DamageEntry
	for i := 0; i < n; i++ {
		tMs := int32(1000 + i)
		pos.T = append(pos.T, tMs)
		pos.X = append(pos.X, 0)
		pos.Y = append(pos.Y, 0)
		pos.Z = append(pos.Z, 0)
		pos.H = append(pos.H, int32(100+i)) // ascending height, all airborne
		dmg = append(dmg, result.DamageEntry{Time: tMs, Attacker: "att", Victim: "vic", Weapon: "rl", Damage: 100})
	}
	res := &result.Result{
		Streams:          &result.Streams{Players: []result.PlayerStream{{Name: "vic", Position: pos}}},
		Damage:           &result.DamageResult{Events: dmg},
		TimelineAnalysis: &result.TimelineAnalysisResult{},
	}
	airgibsPost(res, &CoreOutputs{})

	got := res.TimelineAnalysis.Airgibs
	if len(got) != n {
		t.Fatalf("airgibs = %d, want all %d qualifying hits", len(got), n)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Height < got[i].Height {
			t.Fatalf("not sorted by height desc at %d: %d < %d", i, got[i-1].Height, got[i].Height)
		}
	}
	if got[0].Height != int32(100+n-1) {
		t.Errorf("top height = %d, want %d", got[0].Height, 100+n-1)
	}
	// The attacker has no stream in this fixture: the shooter gap stays
	// at the neutral 0 rather than inventing a value.
	if got[0].HeightAboveAttacker != 0 {
		t.Errorf("heightAboveAttacker = %d, want 0 without an attacker track", got[0].HeightAboveAttacker)
	}
}

func TestAirgibsPost_NoHeightColumnNoAirgibs(t *testing.T) {
	// A victim with positions but no H column (BSP-less run): no airgibs.
	res := &result.Result{
		Streams: &result.Streams{Players: []result.PlayerStream{{
			Name:     "vic",
			Position: &result.PositionTrack{T: []int32{1000}, X: []int32{0}, Y: []int32{0}, Z: []int32{0}},
		}}},
		Damage: &result.DamageResult{Events: []result.DamageEntry{
			{Time: 1000, Attacker: "att", Victim: "vic", Weapon: "rl", Damage: 110},
		}},
		TimelineAnalysis: &result.TimelineAnalysisResult{},
	}
	airgibsPost(res, &CoreOutputs{})
	if len(res.TimelineAnalysis.Airgibs) != 0 {
		t.Errorf("airgibs = %d, want 0 without an H column", len(res.TimelineAnalysis.Airgibs))
	}
}
