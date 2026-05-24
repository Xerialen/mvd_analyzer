package analyzer

import (
	"sort"
)

// LocGraphResult / LocNode / LocEdge now live in qwanalytics/result and
// are re-exported via type aliases in interface.go. BuildLocGraph below
// constructs and returns them; nothing else in this file declares them.
//
// At schema v7 BuildLocGraph walks each player's PositionTrack
// natively: per-sample (X, Y, Li) drives both node-time accumulation
// (sum of inter-sample dt per loc) and edge classification (loc
// transitions, with displacement check for teleports). Spawn / death
// timestamps reset the cursor so a death-then-respawn never produces
// a spurious edge across the gap.

// teleportBaseThreshold is the per-axis "max plausible movement per
// second" limit. A transition whose per-axis displacement exceeds
// bucketDuration * teleportBaseThreshold in the single sample where
// the loc changed is classified as a teleport. Mirrors the frontend
// constant at app.js (MAX_MOVE_PER_BUCKET = 2500 * bucketDuration).
const teleportBaseThreshold = 2500.0

// locgraphSampleDt is the assumed per-sample interval used for
// teleport classification + node-time accumulation when the actual
// sample-to-sample dt exceeds it. Native position samples arrive at
// roughly 13 ms apart (~77 Hz) but can have gaps when a player dies;
// clamping prevents a death-induced 5 s gap from inflating one
// loc's residence by 5 s.
const locgraphSampleDt = 0.05

// BuildLocGraph aggregates each player's native-rate PositionTrack
// into a loc-to-loc movement graph. Runs after time normalization /
// warmup filtering so it sees only match-time data. Returns nil if
// streams are absent.
func BuildLocGraph(result *Result) *LocGraphResult {
	if result == nil || result.TimelineAnalysis == nil || result.Streams == nil {
		return nil
	}
	ta := result.TimelineAnalysis
	if len(ta.LocTable) == 0 {
		return nil
	}

	teamByName := make(map[string]string)
	if result.DemoInfo != nil {
		for _, p := range result.DemoInfo.Players {
			if p.Name != "" && p.Team != "" {
				teamByName[p.Name] = p.Team
			}
		}
	}

	resolveLoc := func(li int16) string {
		if li > 0 && int(li) < len(ta.LocTable) {
			return ta.LocTable[li]
		}
		return ""
	}

	teleportThreshold := float32(locgraphSampleDt * teleportBaseThreshold)

	nodes := make(map[string]*LocNode)
	edges := make(map[string]*LocEdge)
	edgeKey := func(from, to string) string { return from + "\x00" + to }

	ensureNode := func(name string) *LocNode {
		n := nodes[name]
		if n == nil {
			n = &LocNode{Name: name, ByPlayer: make(map[string]float64)}
			nodes[name] = n
		}
		return n
	}
	ensureEdge := func(from, to, kind string) *LocEdge {
		k := edgeKey(from, to)
		e := edges[k]
		if e == nil {
			e = &LocEdge{From: from, To: to, Kind: kind, ByPlayer: make(map[string]int)}
			edges[k] = e
		}
		return e
	}

	// addWeight folds dt into a conditioned node LocWeights (RL/LG-armed or
	// quad), lazily allocating it so locs the condition never touched stay
	// nil (omitempty in JSON).
	addWeight := func(w **LocWeights, player, team string, dt float64) {
		if *w == nil {
			*w = &LocWeights{ByPlayer: make(map[string]float64)}
		}
		(*w).Total += dt
		(*w).ByPlayer[player] += dt
		if team != "" {
			if (*w).ByTeam == nil {
				(*w).ByTeam = make(map[string]float64)
			}
			(*w).ByTeam[team] += dt
		}
	}

	// addEdgeWeight is the transition-count analogue of addWeight for a
	// conditioned LocEdgeWeights.
	addEdgeWeight := func(w **LocEdgeWeights, player, team string) {
		if *w == nil {
			*w = &LocEdgeWeights{ByPlayer: make(map[string]int)}
		}
		(*w).Total++
		(*w).ByPlayer[player]++
		if team != "" {
			if (*w).ByTeam == nil {
				(*w).ByTeam = make(map[string]int)
			}
			(*w).ByTeam[team]++
		}
	}

	// makeInside returns a predicate reporting whether time t falls inside
	// any of a player's sorted, non-overlapping presence intervals. It
	// advances an internal cursor, so it is only valid for queries at
	// monotonically non-decreasing t — which is how the position track is
	// walked below.
	makeInside := func(ivs []Interval) func(int32) bool {
		idx := 0
		return func(t int32) bool {
			for idx < len(ivs) && ivs[idx].End <= t {
				idx++
			}
			return idx < len(ivs) && ivs[idx].Start <= t && t < ivs[idx].End
		}
	}

	for _, p := range result.Streams.Players {
		pt := p.Position
		if pt == nil || len(pt.T) == 0 || len(pt.Li) != len(pt.T) {
			continue
		}
		boundaries := mergeBoundaries(p.Spawns, p.Deaths)
		bIdx := 0
		// Presence predicates for the conditioned metrics, walked at the
		// same monotonically increasing sample times as the position track.
		insideRL := makeInside(p.RL)
		insideLG := makeInside(p.LG)
		insideQuad := makeInside(p.Quad)
		insidePent := makeInside(p.Pent)
		// Per-player cursor: tracks the loc + position of the last
		// sample we counted. Reset at boundary crossings (death/spawn)
		// and at gaps in the loc track (Li=0).
		var (
			curLoc   string
			curX     float32
			curY     float32
			havePrev bool
		)
		team := teamByName[p.Name]

		for i := range pt.T {
			t := pt.T[i] // int32 ms
			// Cross any boundaries we've passed; reset cursor. Both
			// sides are int32 ms — comparison is exact (this is the
			// site where float roundtrip previously produced spurious
			// teleport edges across gib-respawn boundaries).
			for bIdx < len(boundaries) && boundaries[bIdx] <= t {
				havePrev = false
				bIdx++
			}

			li := pt.Li[i]
			if li == 0 {
				havePrev = false
				continue
			}
			locName := resolveLoc(li)
			if locName == "" {
				havePrev = false
				continue
			}

			x := float32(pt.X[i])
			y := float32(pt.Y[i])

			// Node-time: residence in this loc grows by the gap to the
			// next sample (clamped by locgraphSampleDt to avoid death
			// gaps inflating node-time). dt is float64 seconds — the
			// public LocNode.Total unit — converted once from the int32-
			// ms delta.
			var dt float64
			if i+1 < len(pt.T) {
				dt = float64(pt.T[i+1]-t) * 0.001
			} else {
				dt = locgraphSampleDt
			}
			if dt > locgraphSampleDt {
				dt = locgraphSampleDt
			}
			if dt < 0 {
				dt = 0
			}
			node := ensureNode(locName)
			node.Total += dt
			node.ByPlayer[p.Name] += dt
			if team != "" {
				if node.ByTeam == nil {
					node.ByTeam = make(map[string]float64)
				}
				node.ByTeam[team] += dt
			}

			// Conditioned metrics: this sample's combat posture, used for
			// both node-time and (below) the transition it may trigger.
			// Evaluate all three predicates (no short-circuit) so each cursor
			// tracks t independently.
			rl, lg, quad, pent := insideRL(t), insideLG(t), insideQuad(t), insidePent(t)
			armed := rl || lg
			if armed {
				addWeight(&node.Armed, p.Name, team, dt)
			} else {
				addWeight(&node.Unarmed, p.Name, team, dt)
			}
			if quad {
				addWeight(&node.Quad, p.Name, team, dt)
			}
			if pent {
				addWeight(&node.Pent, p.Name, team, dt)
			}

			if !havePrev {
				curLoc = locName
				curX = x
				curY = y
				havePrev = true
				continue
			}
			if locName != curLoc {
				dx := x - curX
				if dx < 0 {
					dx = -dx
				}
				dy := y - curY
				if dy < 0 {
					dy = -dy
				}
				disp := dx
				if dy > disp {
					disp = dy
				}
				kind := "normal"
				if disp > teleportThreshold {
					kind = "teleport"
				}
				edge := ensureEdge(curLoc, locName, kind)
				edge.Total++
				edge.ByPlayer[p.Name]++
				if team != "" {
					if edge.ByTeam == nil {
						edge.ByTeam = make(map[string]int)
					}
					edge.ByTeam[team]++
				}
				// Condition the transition on the destination sample's
				// posture so each metric yields a self-contained movement
				// graph (armed/quad edges + nodes).
				if armed {
					addEdgeWeight(&edge.Armed, p.Name, team)
				} else {
					addEdgeWeight(&edge.Unarmed, p.Name, team)
				}
				if quad {
					addEdgeWeight(&edge.Quad, p.Name, team)
				}
				if pent {
					addEdgeWeight(&edge.Pent, p.Name, team)
				}
				curLoc = locName
			}
			curX = x
			curY = y
		}
	}

	// Attach world coordinates from LocationData where available.
	coordByName := make(map[string]MapLocation, len(ta.LocationData))
	for _, loc := range ta.LocationData {
		if _, exists := coordByName[loc.Name]; !exists {
			coordByName[loc.Name] = loc
		}
	}

	out := &LocGraphResult{
		Locs:  make([]LocNode, 0, len(nodes)),
		Edges: make([]LocEdge, 0, len(edges)),
	}
	for _, n := range nodes {
		if c, ok := coordByName[n.Name]; ok {
			n.X, n.Y, n.Z = c.X, c.Y, c.Z
		}
		out.Locs = append(out.Locs, *n)
	}
	for _, e := range edges {
		out.Edges = append(out.Edges, *e)
	}
	sort.Slice(out.Locs, func(i, j int) bool { return out.Locs[i].Name < out.Locs[j].Name })
	sort.Slice(out.Edges, func(i, j int) bool {
		if out.Edges[i].From != out.Edges[j].From {
			return out.Edges[i].From < out.Edges[j].From
		}
		return out.Edges[i].To < out.Edges[j].To
	})
	return out
}
