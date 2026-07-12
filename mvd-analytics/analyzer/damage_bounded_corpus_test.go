package analyzer_test

// Corpus-wide validation of the bounded damage reconstruction against
// KTX's own accounting embedded in each demo: the per-player scoreboard
// (demoInfo.players[].dmg — bounded dmg_dealt totals) and the per-player-
// per-weapon splits (weapons[].damage.{enemy,team}). The wire carries only
// the unbound value, so near-equality of the reconstruction against these
// independent totals is the correctness signal for the whole shadow-vitals
// + T_Damage-arithmetic pipeline (analyzer/damage.go).
//
// Expected residual per player is small: ±1 ceiling slop per armor-
// absorbing hit (save is re-derived from the wire int, not KTX's float),
// plus unknown-initial-state joins. The tolerances below were measured
// across the 10-demo corpus and pinned with headroom; a regression that
// breaks the arithmetic (wrong armor fraction, missed nullification,
// broken checkpointing) overshoots them by an order of magnitude.
//
// Run with -v to see every per-player and per-weapon delta.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
)

// Tolerances: measured corpus max |delta| + headroom, pinned 2026-07-12
// across the 10-demo corpus (50 players, ~200 weapon rows). Re-measure with
// -v when they trip on an intended change.
//
// Residual mechanisms (why not zero):
//   - ±1 ceiling slop per armor-absorbing hit (save re-derived from the
//     wire int, not KTX's float).
//   - The one-frame stat window: stats/items broadcast at end of frame, so
//     a hit in the same frame as a mid-frame pickup (mega, armor), respawn
//     or corpse gib reconstructs against the previous frame's state. Single
//     hits, bounded by one blast's damage. Measured extremes: given −44
//     (one SG blast on a same-frame respawn), ewep −130 (a duel's RL/LG
//     pickups reclassifying same-frame hits' victim-weapon bucket).
//   - team/taken reconcile to ±1 corpus-wide.
const (
	tolBoundedGiven = 60  // measured max |Δ| 44
	tolBoundedTaken = 60  // measured max |Δ| 44 (the given outliers' victim side)
	tolBoundedEWep  = 150 // measured max |Δ| 130 (victim-item one-frame window)
	tolBoundedTeam  = 10  // measured max |Δ| 1
	tolBoundedByWep = 60  // measured max |Δ| 44
)

func TestBoundedReconciliationCorpus(t *testing.T) {
	corpus := loadCorpus(t)
	if len(corpus) == 0 {
		t.Skip("testdata/corpus.json has no entries")
	}
	cacheDir := filepath.Join("..", "testdata", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("create cache dir: %v", err)
	}

	within := func(t *testing.T, what string, stream, score, tol int) {
		t.Helper()
		if d := stream - score; d > tol || d < -tol {
			t.Errorf("%s: stream %d vs KTX %d (delta %+d exceeds ±%d)", what, stream, score, d, tol)
		}
	}

	for _, entry := range corpus {
		t.Run(entry.Label, func(t *testing.T) {
			mvdPath := ensureCached(t, cacheDir, entry)
			res, err := analyzer.NewDefaultRegistry().Analyze(mvdPath)
			if err != nil {
				t.Fatalf("analyze: %v", err)
			}
			d := res.Damage
			if d == nil {
				t.Skip("no damage section")
			}
			if d.BoundedMode != "standard" {
				t.Skipf("bounded reconstruction skipped (%s)", d.BoundedMode)
			}
			if d.Scoreboard == nil {
				t.Skip("no KTX scoreboard in this demo")
			}

			// Per-player scoreboard reconciliation.
			for name, delta := range d.Scoreboard.ByPlayer {
				b := delta.Bounded
				if b == nil {
					t.Errorf("%s: no bounded delta despite standard mode", name)
					continue
				}
				t.Logf("%-18s givenΔ=%+4d takenΔ=%+4d ewepΔ=%+4d teamΔ=%+4d",
					name,
					b.StreamGiven-delta.ScoreGiven,
					b.StreamTaken-delta.ScoreTaken,
					b.StreamEWep-delta.ScoreEWep,
					b.StreamTeam-b.ScoreTeam)
				within(t, name+" bounded given", b.StreamGiven, delta.ScoreGiven, tolBoundedGiven)
				within(t, name+" bounded taken", b.StreamTaken, delta.ScoreTaken, tolBoundedTaken)
				within(t, name+" bounded ewep", b.StreamEWep, delta.ScoreEWep, tolBoundedEWep)
				within(t, name+" bounded team", b.StreamTeam, b.ScoreTeam, tolBoundedTeam)
			}

			// Per-weapon reconciliation: our bounded byWeapon vs the KTX
			// demostats weapons[].damage.enemy (key names verified identical:
			// axe/sg/ssg/ng/sng/gl/rl/lg — KTX WpName, ktx/src/stats.c:358).
			// Team-per-weapon has no stored aggregate; derive it from the
			// events log (tele/stomp excluded on both sides — KTX wpNONE).
			if res.DemoInfo != nil {
				for _, p := range res.DemoInfo.Players {
					pd := d.ByPlayer[p.Name]
					if pd == nil || pd.Bounded == nil {
						continue
					}
					teamByWep := map[string]int{}
					for _, e := range d.Events {
						if e.IsTeam && e.Attacker == p.Name {
							v := e.Damage
							if e.Bounded != nil {
								v = *e.Bounded
							}
							teamByWep[e.Weapon] += v
						}
					}
					for wname, w := range p.Weapons {
						if w == nil || w.Damage == nil {
							continue
						}
						if w.Damage.Enemy != 0 || pd.Bounded.ByWeapon[wname] != 0 {
							t.Logf("%-18s %-4s enemyΔ=%+4d", p.Name, wname,
								pd.Bounded.ByWeapon[wname]-w.Damage.Enemy)
							within(t, p.Name+" "+wname+" enemy", pd.Bounded.ByWeapon[wname], w.Damage.Enemy, tolBoundedByWep)
						}
						if w.Damage.Team != 0 || teamByWep[wname] != 0 {
							t.Logf("%-18s %-4s teamΔ =%+4d", p.Name, wname,
								teamByWep[wname]-w.Damage.Team)
							within(t, p.Name+" "+wname+" team", teamByWep[wname], w.Damage.Team, tolBoundedByWep)
						}
					}
				}
			}

			// Pin the live pent-deflect coverage: dm3 contains "Satan's power
			// deflects nlk's telefrag" — on the wire an ordinary dtTELE2
			// dmgdone with the pent holder as attacker and nlk as victim.
			// The fold-in must reconstruct a positive bounded value for it.
			if entry.Label == "4on4_osams_ra_230426_dm3" {
				found := false
				for _, tf := range d.Telefrags {
					if tf.Victim == "nlk" && tf.Bounded != nil && *tf.Bounded > 0 {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("dm3 deflect pin: no telefrag on nlk with a positive bounded value (%+v)", d.Telefrags)
				}
			}
		})
	}
}
