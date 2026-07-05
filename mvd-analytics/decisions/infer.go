package decisions

import (
	"sort"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// Inference constants (v1, deliberately simple and pickup-anchored):
// a goal decision is assumed to have been committed to at most
// inferLookbackMs before the pickup, clamped to the picker's latest
// spawn and previous pickup — i.e. "somewhere on the final approach".
const (
	inferLookbackMs  = 8000
	inferConfidence  = 0.6
	inferBackpackCnf = 0.5
)

// AttachInferred reverse-engineers pickup-anchored goal decisions from an
// analyzed demo (no log needed) and attaches them as Result.Decisions with
// source "inferred". Every world-item pickup (ItemTimeline phases) and every
// RL/LG backpack pickup (WeaponPickups) becomes one type=goal record whose
// chosen goal is the picked item, timestamped at the estimated commit time.
//
// Known v1 limits (by design): denied approaches, aborted runs and powerup
// camping produce no records; candidates/desire/score are unknowable from
// the demo; Confidence marks the whole record as inferred.
func AttachInferred(res *result.Result) {
	dec := &result.Decisions{Source: "inferred"}
	rx := newResolver(res, dec)

	type pick struct {
		t      int32
		player string
		team   string
		goal   result.DecisionGoal
		trig   string
		cnf    float32
	}
	var picks []pick

	if res.Items != nil {
		for i := range res.Items.Items {
			it := &res.Items.Items[i]
			for _, ph := range it.Phases {
				if ph.TakenBy == "" || ph.TakenAt <= 0 {
					continue
				}
				picks = append(picks, pick{
					t: ph.TakenAt, player: ph.TakenBy, team: ph.Team,
					goal: result.DecisionGoal{Kind: it.Kind, Name: it.Name, EntNum: it.EntNum,
						X: it.X, Y: it.Y, Z: it.Z, Loc: it.Loc},
					trig: "inferred_pickup", cnf: inferConfidence,
				})
			}
		}
	}
	for _, wp := range res.WeaponPickups {
		if wp.Source != "backpack" {
			continue
		}
		picks = append(picks, pick{
			t: wp.Time, player: wp.Player, team: wp.Team,
			goal: result.DecisionGoal{Kind: "backpack", Name: wp.Weapon, EntNum: wp.BackpackEnt},
			trig: "inferred_backpack", cnf: inferBackpackCnf,
		})
	}

	sort.Slice(picks, func(i, j int) bool { return picks[i].t < picks[j].t })

	// Per-player floors: a decision can't predate the latest spawn or the
	// previous pickup — the previous decision was still being executed.
	lastPickAt := map[string]int32{}
	for _, p := range picks {
		t := p.t - inferLookbackMs
		if s := latestSpawnBefore(rx.streams[p.player], p.t); s > t {
			t = s
		}
		if prev, ok := lastPickAt[p.player]; ok && prev > t {
			t = prev
		}
		if t < 0 {
			t = 0
		}
		lastPickAt[p.player] = p.t

		rec := result.DecisionRecord{
			T: t, Player: p.player, Team: p.team, Slot: slotOf(res, p.player),
			Type: "goal", Trigger: p.trig, Confidence: p.cnf,
		}
		g := p.goal
		rec.Chosen = &g
		rec.Loc = rx.locAt(p.player, t)
		rec.X, rec.Y, rec.Z = posAt(rx.streams[p.player], t)
		rec.State = stateAt(rx.streams[p.player], t)
		dec.Records = append(dec.Records, rec)
	}

	res.Decisions = dec
}

func slotOf(res *result.Result, name string) int {
	if res.TimelineAnalysis != nil {
		if s, ok := res.TimelineAnalysis.PlayerSlots[name]; ok {
			return s
		}
	}
	return -1
}

func latestSpawnBefore(p *result.PlayerStream, t int32) int32 {
	if p == nil {
		return 0
	}
	best := int32(0)
	for _, s := range p.Spawns {
		if s <= t && s > best {
			best = s
		}
	}
	return best
}

func posAt(p *result.PlayerStream, t int32) (x, y, z float32) {
	if p == nil || p.Position == nil || len(p.Position.T) == 0 {
		return 0, 0, 0
	}
	i := sort.Search(len(p.Position.T), func(i int) bool { return p.Position.T[i] > t })
	if i == 0 {
		i = 1
	}
	return p.Position.X[i-1], p.Position.Y[i-1], p.Position.Z[i-1]
}

// stateAt reconstructs the picker's resource snapshot at time t from the
// sparse change streams and possession/powerup intervals.
func stateAt(p *result.PlayerStream, t int32) *result.DecisionState {
	if p == nil {
		return nil
	}
	st := &result.DecisionState{
		H: changeAt(p.Health, t), A: changeAt(p.Armor, t),
		AT: changeStrAt(p.ArmorType, t),
		SH: changeAt(p.Shells, t), NL: changeAt(p.Nails, t),
		RK: changeAt(p.Rockets, t), CL: changeAt(p.Cells, t),
		RL: inInterval(p.RL, t), LG: inInterval(p.LG, t), GL: inInterval(p.GL, t),
		SSG: inInterval(p.SSG, t), SNG: inInterval(p.SNG, t),
		Q: inInterval(p.Quad, t), PE: inInterval(p.Pent, t), R: inInterval(p.Ring, t),
		AW: itBitWeapon(int(changeAt(p.ActiveWeapon, t))),
	}
	return st
}

func changeAt(cs []result.ChangeI16, t int32) int16 {
	v := int16(0)
	for _, c := range cs {
		if c.T > t {
			break
		}
		v = c.V
	}
	return v
}

func changeStrAt(cs []result.ChangeStr, t int32) string {
	v := ""
	for _, c := range cs {
		if c.T > t {
			break
		}
		v = c.V
	}
	return v
}

func inInterval(iv []result.Interval, t int32) bool {
	for _, i := range iv {
		if t >= i.Start && t < i.End {
			return true
		}
	}
	return false
}
