package analyzer

import (
	"sort"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// openingSpawnLocWindowMs bounds how far past match start a player's
// loc change stream is searched for their spawn location. Post-rebase
// the carry-forward entry sits at exactly t=0, so this only matters
// for a player whose first loc sample lags the match start by a frame
// or two.
const openingSpawnLocWindowMs = 500

// openingPost projects the match opening out of the already-built
// sections: each player's match-start spawn location (from the streams'
// loc column at t=0) and the first in-match take of every contested
// item spawner (from the items timelines). Pure projection — no new
// facts; the artifact exists so "how did the opening go" is one small
// fetch. Tracked spawners are the majors: armors, mega, powerups, and
// the RL/LG weapon pads.
//
// No-op when no match start was detected (t=0 would be the demo open,
// not an opening) or when there are no streams to read spawns from.
func openingPost(res *Result, co *CoreOutputs) {
	if res == nil || res.Streams == nil || co.MatchStartMs() <= 0 {
		return
	}

	opening := &result.OpeningResult{}

	var locTable []string
	if res.TimelineAnalysis != nil {
		locTable = res.TimelineAnalysis.LocTable
	}
	for i := range res.Streams.Players {
		p := &res.Streams.Players[i]
		// Present-and-alive at match start: the same carry-forward
		// predicate synthesizeMatchStartSpawns keys on.
		if !(len(p.Health) > 0 && p.Health[0].T == 0 && p.Health[0].V > 0) {
			continue
		}
		op := result.OpeningPlayer{Name: p.Name, Team: p.Team}
		// Spawn loc: prefer the first entry strictly after t=0 (the
		// match-start respawn teleport landing at the next position
		// sample) over the t=0 carry-forward entry, which holds the
		// countdown-end location. No post-0 change within the window
		// means the loc name didn't change across the respawn and the
		// carry is correct. Mirrors view.locAtSpawn.
		for _, c := range p.Loc {
			if c.T > openingSpawnLocWindowMs {
				break
			}
			if int(c.V) > 0 && int(c.V) < len(locTable) {
				op.Loc = locTable[c.V]
			}
			if c.T > 0 {
				break
			}
		}
		opening.Players = append(opening.Players, op)
	}
	sort.Slice(opening.Players, func(i, j int) bool {
		a, b := opening.Players[i], opening.Players[j]
		if a.Team != b.Team {
			return a.Team < b.Team
		}
		return a.Name < b.Name
	})

	if res.Items != nil {
		for _, it := range res.Items.Items {
			switch it.Category() {
			case "armor", "mega", "powerup":
			case "weapon":
				if it.Kind != "rl" && it.Kind != "lg" {
					continue
				}
			default:
				continue
			}
			for _, ph := range it.Phases {
				if ph.TakenAt == 0 && ph.TakenBy == "" {
					continue // untaken availability phase
				}
				if ph.TakenAt < 0 {
					continue // warmup take (pre-match phases carry negative times)
				}
				opening.FirstTakes = append(opening.FirstTakes, result.OpeningTake{
					Item:    it.Name,
					Kind:    it.Kind,
					EntNum:  it.EntNum,
					Loc:     it.Loc,
					Time:    ph.TakenAt,
					TakenBy: ph.TakenBy,
					Team:    ph.Team,
				})
				break // first in-match take only
			}
		}
	}
	sort.Slice(opening.FirstTakes, func(i, j int) bool {
		a, b := opening.FirstTakes[i], opening.FirstTakes[j]
		if a.Time != b.Time {
			return a.Time < b.Time
		}
		return a.Item < b.Item
	})

	if len(opening.Players) == 0 && len(opening.FirstTakes) == 0 {
		return // nothing to say; omit the section rather than ship an empty shell
	}
	res.Opening = opening
}
