package analyzer

import (
	"sort"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// Tolerances for victim-named teamkill killer recovery. Telefrag / stomp /
// squish all put the killer on the victim, so at the kill instant the two
// sit within roughly a player hull (KTX VEC_HULL is ±16 xy, the telefrag
// trigger box ±17; observed ~31 QU in practice while the nearest innocent
// teammate was 1500+ away). The teamkiller's −1 frag penalty lands on the
// same frame.
const (
	telefragPosHorizTol int32 = 40  // QU — 32-wide hull + sampling slop
	telefragPosVertTol  int32 = 64  // QU — allow a hull height (stomp from above)
	telefragPosTimeWin  int32 = 200 // ms — nearest position sample to the kill
	telefragDeltaWin    int32 = 256 // ms — the −1 frag-penalty window
)

// recoverTelefragTeamkills attributes victim-named teammate teamkills
// ("X was telefragged/crushed/jumped by his teammate") whose obituary
// hides the killer. It combines two independent signals at the kill
// instant — the killer is co-located with the victim, AND the killer takes
// the KTX teamkill frag penalty (a −1 in the frag stream). Requiring the
// two to agree (or, when only one is determinate, not to conflict) keeps a
// rare position or score alias from misattributing the kill.
//
// Runs as a post-processor BEFORE normalizeMatchRelativeTimes, so the
// obituary times (CoreOutputs), Streams positions, and FragEvents are all
// still demo-relative and share one clock. Deaths are untouched (the
// victim's death is already counted from the protocol DeathEvent); this
// only fills in the killer side.
func recoverTelefragTeamkills(res *Result, co *CoreOutputs) {
	if res.Frags == nil || res.Streams == nil || co == nil || len(co.VictimNamedTeamkills) == 0 {
		return
	}

	posByName := make(map[string]*result.PositionTrack, len(res.Streams.Players))
	teamByName := make(map[string]string, len(res.Streams.Players))
	for i := range res.Streams.Players {
		p := &res.Streams.Players[i]
		if p.Position != nil {
			posByName[p.Name] = p.Position
		}
		teamByName[p.Name] = p.Team
	}

	type negDelta struct {
		player string
		tMs    int32
	}
	var negDeltas []negDelta
	if res.TimelineAnalysis != nil {
		for _, fe := range res.TimelineAnalysis.FragEvents {
			if fe.Delta < 0 {
				negDeltas = append(negDeltas, negDelta{fe.Player, fe.Time})
			}
		}
	}

	appended := false
	for _, tk := range co.VictimNamedTeamkills {
		victim := tk.Victim
		vTeam := teamByName[victim]
		if vTeam == "" {
			vTeam = co.Names.TeamForName(victim)
		}
		if vTeam == "" {
			continue
		}

		mates := map[string]bool{}
		for name, team := range teamByName {
			if name != victim && team == vTeam {
				mates[name] = true
			}
		}

		// Position signal: teammates co-located with the victim at the kill.
		posSet := map[string]bool{}
		if vt := posByName[victim]; vt != nil {
			if vx, vy, vz, ok := positionAt(vt, tk.Time); ok {
				for m := range mates {
					mt := posByName[m]
					if mt == nil {
						continue
					}
					if mx, my, mz, ok := positionAt(mt, tk.Time); ok &&
						absI32(mx-vx) <= telefragPosHorizTol &&
						absI32(my-vy) <= telefragPosHorizTol &&
						absI32(mz-vz) <= telefragPosVertTol {
						posSet[m] = true
					}
				}
			}
		}

		// Frag-penalty signal: teammates that lost a frag at the kill.
		deltaSet := map[string]bool{}
		for _, d := range negDeltas {
			if mates[d.player] && absI32(d.tMs-tk.Time) <= telefragDeltaWin {
				deltaSet[d.player] = true
			}
		}

		killer := combineTeamkillSignals(posSet, deltaSet)
		if killer == "" {
			continue
		}

		if pf := res.Frags.ByPlayer[killer]; pf != nil {
			pf.TeamKills++
		} else {
			res.Frags.ByPlayer[killer] = &result.PlayerFrags{TeamKills: 1, ByWeapon: map[string]int{}}
		}
		entry := tk
		entry.Killer = killer
		res.Frags.Frags = append(res.Frags.Frags, entry)
		appended = true
	}

	if appended {
		sort.SliceStable(res.Frags.Frags, func(i, j int) bool {
			return res.Frags.Frags[i].Time < res.Frags.Frags[j].Time
		})
		res.Frags.TotalFrags = len(res.Frags.Frags)
	}
}

// combineTeamkillSignals resolves the killer from the position and
// frag-penalty candidate sets, requiring the two to agree (or, when only
// one is determinate, not to conflict) so a lone position or score alias
// can't misattribute the kill:
//   - exactly one teammate in BOTH sets        → that teammate (strongest)
//   - more than one in both                     → "" (ambiguous even combined)
//   - else exactly one in a single set, the other empty → that teammate
//   - otherwise (conflict or ambiguity)         → "" (leave unrecovered)
func combineTeamkillSignals(posSet, deltaSet map[string]bool) string {
	var both []string
	for name := range posSet {
		if deltaSet[name] {
			both = append(both, name)
		}
	}
	if len(both) == 1 {
		return both[0]
	}
	if len(both) > 1 {
		return ""
	}
	if len(posSet) == 1 && len(deltaSet) == 0 {
		return onlyKey(posSet)
	}
	if len(deltaSet) == 1 && len(posSet) == 0 {
		return onlyKey(deltaSet)
	}
	return ""
}

func onlyKey(m map[string]bool) string {
	for k := range m {
		return k
	}
	return ""
}

// positionAt returns the (x,y,z) sample nearest tMs within
// telefragPosTimeWin, or ok=false when no sample is that close.
func positionAt(pt *result.PositionTrack, tMs int32) (x, y, z int32, ok bool) {
	n := len(pt.T)
	if n == 0 {
		return 0, 0, 0, false
	}
	lo, hi := 0, n
	for lo < hi {
		mid := (lo + hi) / 2
		if pt.T[mid] < tMs {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	best, bestGap := -1, telefragPosTimeWin+1
	for _, i := range [2]int{lo - 1, lo} {
		if i < 0 || i >= n {
			continue
		}
		if g := absI32(pt.T[i] - tMs); g < bestGap {
			bestGap, best = g, i
		}
	}
	if best < 0 {
		return 0, 0, 0, false
	}
	return pt.X[best], pt.Y[best], pt.Z[best], true
}
