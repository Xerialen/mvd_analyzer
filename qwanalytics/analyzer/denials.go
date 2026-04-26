package analyzer

import (
	"sort"
)

// buildDenialsPost is the post-processor that derives DenialsResult
// from the already-finalised Items + LocGraph + TimelineAnalysis.
//
// Time bases line up because this runs after normalizeMatchRelativeTimes:
//   - HighResBuckets[i].T is match-relative
//   - Items.Items[i].Phases[j].TakenAt is match-relative
// so we can match a pickup to a bucket directly without re-shifting.
//
// Definitions (see qwanalytics/analyzer/denials.md for the full
// rationale):
//
//   * Region of an item at loc A = {A} ∪ {B : edges[A→B].Total ≥ 10
//     AND edges[B→A].Total ≥ 10}. Both directions required so that a
//     loc only counts as connected when the run is symmetric — one-way
//     drops and rare jumps are excluded from the "control" footprint.
//
//   * "Weapon-bearer" = a player carrying RL or LG, OR carrying Quad.
//     Quad is included because a Quad player without a weapon is still
//     a credible threat over the region they cover.
//
//   * Denial of item I at time T by player P (team team1):
//       - I.Kind ∈ {ra, ya, mh, lg, rl, quad, pent, ring}
//       - P holds no RL/LG (Quad ignored for the picker).
//       - The opposing team has ≥1 weapon-bearer in the region.
//       - team1 has 0 weapon-bearers in the region (otherwise the pickup
//         is a normal contested grab, not a steal).
//
//   * Hoover of item I at time T by player P (team team1):
//       - I.Kind ∈ {ra, ya, mh}
//       - P holds no RL/LG.
//       - There is ≥1 same-team weapon-bearer in the region whose
//         current armor (RA, YA) or health (MH) is below the per-item
//         threshold:
//             RA: armor < 75
//             YA: armor < 50
//             MH: health ≤ 50
const (
	denialEdgeMinTraversals = 10

	hooverArmorRAThreshold  = 75 // RA: teammate armor < 75
	hooverArmorYAThreshold  = 50 // YA: teammate armor < 50
	hooverHealthMHThreshold = 50 // MH: teammate health ≤ 50
)

// denialItemKinds is the set of kinds eligible for the steal metric.
var denialItemKinds = map[string]bool{
	"ra": true, "ya": true, "mh": true,
	"lg": true, "rl": true,
	"quad": true, "pent": true, "ring": true,
}

// hooverItemKinds is the set of kinds eligible for the same-team
// hoover metric (armors + MH only).
var hooverItemKinds = map[string]bool{
	"ra": true, "ya": true, "mh": true,
}

func buildDenialsPost(result *Result, _ *CoreOutputs) {
	if result == nil {
		return
	}
	if result.Items == nil || len(result.Items.Items) == 0 {
		return
	}
	if result.TimelineAnalysis == nil || len(result.TimelineAnalysis.HighResBuckets) == 0 {
		return
	}
	if result.LocGraph == nil {
		return
	}

	region := buildRegionMap(result.LocGraph)
	teamByName := buildTeamByName(result)
	userIDByName := result.TimelineAnalysis.PlayerUserIDs
	buckets := result.TimelineAnalysis.HighResBuckets

	out := &DenialsResult{}

	for _, item := range result.Items.Items {
		if !denialItemKinds[item.Kind] && !hooverItemKinds[item.Kind] {
			continue
		}
		if item.Loc == "" {
			continue
		}
		neighbors := region[item.Loc]
		for _, phase := range item.Phases {
			if phase.TakenAt <= 0 || phase.TakenBy == "" {
				continue
			}
			pickerTeam := phase.Team
			if pickerTeam == "" {
				pickerTeam = teamByName[phase.TakenBy]
			}
			if pickerTeam == "" {
				continue
			}
			b := nearestBucket(buckets, phase.TakenAt)
			if b == nil {
				continue
			}
			pickerData := b.P[phase.TakenBy]
			if pickerData == nil {
				continue
			}
			// Picker must be without weapon (no RL, no LG).
			if pickerData.RL || pickerData.LG {
				continue
			}

			locTable := result.TimelineAnalysis.LocTable
			enemyW, sameW, sameNeedy, sameNeedyVal, sameNeedyStat := scanRegion(
				b, locTable, item.Loc, neighbors, pickerTeam, item.Kind, teamByName,
			)

			userID := 0
			if userIDByName != nil {
				userID = userIDByName[phase.TakenBy]
			}

			if denialItemKinds[item.Kind] && enemyW > 0 && sameW == 0 {
				out.Denials = append(out.Denials, DenialEvent{
					Time:         phase.TakenAt,
					Player:       phase.TakenBy,
					Team:         pickerTeam,
					Item:         item.Kind,
					Loc:          item.Loc,
					EnemyWeapons: enemyW,
					PlayerUserID: userID,
				})
			}

			if hooverItemKinds[item.Kind] && sameNeedy != "" {
				out.Hoovers = append(out.Hoovers, HooverEvent{
					Time:          phase.TakenAt,
					Player:        phase.TakenBy,
					Team:          pickerTeam,
					Item:          item.Kind,
					Loc:           item.Loc,
					NeedyTeammate: sameNeedy,
					NeedyStat:     sameNeedyStat,
					NeedyValue:    sameNeedyVal,
					PlayerUserID:  userID,
				})
			}
		}
	}

	sort.Slice(out.Denials, func(i, j int) bool { return out.Denials[i].Time < out.Denials[j].Time })
	sort.Slice(out.Hoovers, func(i, j int) bool { return out.Hoovers[i].Time < out.Hoovers[j].Time })

	if len(out.Denials) == 0 && len(out.Hoovers) == 0 {
		return
	}
	result.Denials = out
}

// buildRegionMap returns, for each loc that appears in the LocGraph, the
// set of locs (including itself) that satisfy the both-directions ≥ 10
// traversals gate. Building it once amortises over every item phase.
func buildRegionMap(g *LocGraphResult) map[string]map[string]bool {
	out := make(map[string]map[string]bool)
	if g == nil {
		return out
	}

	// Gather totals per directed pair.
	totals := make(map[string]map[string]int)
	for _, e := range g.Edges {
		if e.From == "" || e.To == "" {
			continue
		}
		if totals[e.From] == nil {
			totals[e.From] = make(map[string]int)
		}
		totals[e.From][e.To] += e.Total
	}

	// Seed every node with itself in its region — so a pickup on a
	// loc with no qualifying neighbors still has a non-empty region.
	for _, n := range g.Locs {
		if n.Name == "" {
			continue
		}
		out[n.Name] = map[string]bool{n.Name: true}
	}
	// Include any from/to seen in edges that wasn't in the node list
	// (defensive; node list should be complete in practice).
	addNode := func(name string) {
		if name == "" {
			return
		}
		if out[name] == nil {
			out[name] = map[string]bool{name: true}
		}
	}
	for from, dst := range totals {
		addNode(from)
		for to := range dst {
			addNode(to)
		}
	}

	for from, dst := range totals {
		for to, ab := range dst {
			if ab < denialEdgeMinTraversals {
				continue
			}
			if totals[to][from] < denialEdgeMinTraversals {
				continue
			}
			out[from][to] = true
		}
	}
	return out
}

// buildTeamByName uses DemoInfo as the primary source of truth (mirrors
// the rest of the analytics; demoinfo is the post-match summary), then
// falls back to anything visible on item phases.
func buildTeamByName(result *Result) map[string]string {
	out := make(map[string]string)
	if result.DemoInfo != nil {
		for _, p := range result.DemoInfo.Players {
			if p.Name != "" && p.Team != "" {
				out[p.Name] = p.Team
			}
		}
	}
	if result.Items != nil {
		for _, it := range result.Items.Items {
			for _, ph := range it.Phases {
				if ph.TakenBy != "" && ph.Team != "" {
					if _, ok := out[ph.TakenBy]; !ok {
						out[ph.TakenBy] = ph.Team
					}
				}
			}
		}
	}
	return out
}

// nearestBucket returns the bucket whose T is closest to t, or nil if
// the slice is empty. Uses sort.Search since buckets are sorted by T.
func nearestBucket(buckets []HighResBucket, t float64) *HighResBucket {
	if len(buckets) == 0 {
		return nil
	}
	idx := sort.Search(len(buckets), func(i int) bool {
		return buckets[i].T >= t
	})
	if idx == 0 {
		return &buckets[0]
	}
	if idx >= len(buckets) {
		return &buckets[len(buckets)-1]
	}
	prev := &buckets[idx-1]
	next := &buckets[idx]
	if t-prev.T <= next.T-t {
		return prev
	}
	return next
}

// scanRegion walks every player present in the bucket and tallies, for
// the given item region:
//   - enemyW: count of opposing-team players in the region holding RL/LG
//     or Quad.
//   - sameW: count of same-team players in the region holding RL/LG or
//     Quad (used to decide whether a pickup is a clean denial vs. a
//     contested pickup we don't want to count).
//   - sameNeedy: name of a same-team weapon-bearer in the region whose
//     armor / health is below the per-item hoover threshold; "" if none.
//   - sameNeedyVal: that teammate's relevant stat value.
//   - sameNeedyStat: "armor" or "health" depending on item kind.
//
// Returns the first qualifying needy teammate (deterministic via
// alphabetical name iteration) — the metric is "did anyone need it",
// the specific identity is reported only for the table column.
func scanRegion(
	b *HighResBucket,
	locTable []string,
	itemLoc string,
	region map[string]bool,
	pickerTeam string,
	itemKind string,
	teamByName map[string]string,
) (enemyW, sameW int, sameNeedy string, sameNeedyVal int, sameNeedyStat string) {
	if b == nil || b.P == nil {
		return
	}
	// Iterate names deterministically so the chosen needy teammate is
	// stable across runs.
	names := make([]string, 0, len(b.P))
	for n := range b.P {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		p := b.P[name]
		if p == nil {
			continue
		}
		locName := ""
		if p.Li > 0 && p.Li < len(locTable) {
			locName = locTable[p.Li]
		}
		// Region matches by loc name (which uniquely identifies a
		// loc-graph node). Empty location → not in any region.
		if locName == "" {
			continue
		}
		inRegion := locName == itemLoc || (region != nil && region[locName])
		if !inRegion {
			continue
		}
		hasWeapon := p.RL || p.LG || p.Q
		if !hasWeapon {
			continue
		}
		team := teamByName[name]
		if team == "" {
			continue
		}
		if team == pickerTeam {
			sameW++
			if sameNeedy == "" {
				switch itemKind {
				case "ra":
					if p.A < hooverArmorRAThreshold {
						sameNeedy = name
						sameNeedyVal = p.A
						sameNeedyStat = "armor"
					}
				case "ya":
					if p.A < hooverArmorYAThreshold {
						sameNeedy = name
						sameNeedyVal = p.A
						sameNeedyStat = "armor"
					}
				case "mh":
					if p.H <= hooverHealthMHThreshold {
						sameNeedy = name
						sameNeedyVal = p.H
						sameNeedyStat = "health"
					}
				}
			}
		} else {
			enemyW++
		}
	}
	return
}
