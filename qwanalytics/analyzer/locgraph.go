package analyzer

// LocGraphResult / LocNode / LocEdge now live in qwanalytics/result and
// are re-exported via type aliases in interface.go. BuildLocGraph below
// constructs and returns them; nothing else in this file declares them.

// teleportBaseThreshold mirrors the frontend constant at app.js:4160: the
// per-axis "max plausible movement per second" limit. Any per-axis
// displacement exceeding bucketDuration * teleportBaseThreshold between
// consecutive buckets is classified as a teleport. Frontend uses
// MAX_MOVE_PER_BUCKET = 2500 * bucketDuration, so the per-second base is 2500.
const teleportBaseThreshold = 2500.0

// locGraphHysteresisBuckets is the number of consecutive buckets the player
// must be seen in a new loc before we commit the transition and emit an
// edge. With the default 0.05s bucket and value 2, a player has to spend at
// least 100ms in the new loc before it counts — enough to suppress the
// nearest-loc jitter that happens right at loc boundaries.
const locGraphHysteresisBuckets = 2

// BuildLocGraph aggregates HighResBuckets into a loc-to-loc movement graph.
// Runs after time normalization / warmup filtering so it sees only match-time
// buckets. Returns nil if there is no timeline data.
func BuildLocGraph(result *Result) *LocGraphResult {
	if result == nil || result.TimelineAnalysis == nil {
		return nil
	}
	ta := result.TimelineAnalysis
	if len(ta.HighResBuckets) == 0 {
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

	resolveLoc := func(li int) string {
		if li > 0 && li < len(ta.LocTable) {
			return ta.LocTable[li]
		}
		return ""
	}

	bucketDuration := ta.HighResDuration
	if bucketDuration <= 0 {
		bucketDuration = 0.05
	}
	teleportThreshold := float32(bucketDuration * teleportBaseThreshold)

	// Belt-and-suspenders death reset: the in-bucket p.D / p.Sp markers
	// already cover standard deaths, but in standard QW a player gibbed
	// deeply negative can respawn effectively instantly — possibly in the
	// same 50ms bucket as the death — which makes both markers false and
	// would otherwise bridge the death with a spurious edge. The
	// authoritative frag list records every death, so we use it as a second
	// independent signal to reset the cursor across any death time.
	deathsByPlayer := map[string][]float64{}
	if result.Frags != nil {
		for _, f := range result.Frags.Frags {
			if f.Victim != "" {
				deathsByPlayer[f.Victim] = append(deathsByPlayer[f.Victim], f.Time)
			}
		}
	}

	// playerCursor tracks hysteresis state: the last committed loc plus any
	// candidate loc we've seen but haven't committed yet (requires
	// locGraphHysteresisBuckets consecutive confirmations to suppress
	// nearest-loc jitter at loc boundaries).
	type playerCursor struct {
		loc           string  // last committed loc
		lastX, lastY  float32 // position at last commit (for teleport classification)
		pendingLoc    string  // candidate new loc
		pendingCount  int     // consecutive buckets we've seen pendingLoc
		pendingStartX float32 // position when pendingLoc first appeared
		pendingStartY float32
		deathIdx      int  // next unprocessed entry in deathsByPlayer[name]
		seen          bool // player was present in a bucket we processed
	}
	cursors := make(map[string]*playerCursor)

	nodes := make(map[string]*LocNode)
	// FIXME: edges are keyed only by (from, to); if both a "normal" and a
	// "teleport" transition occur between the same pair of locs they collapse
	// into whichever type was seen first. Future: key edges by (from,to,kind).
	edgeKey := func(from, to string) string { return from + "\x00" + to }
	edges := make(map[string]*LocEdge)

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

	resetCursor := func(cur *playerCursor) {
		cur.loc = ""
		cur.pendingLoc = ""
		cur.pendingCount = 0
	}

	for _, bucket := range ta.HighResBuckets {
		seenThisBucket := make(map[string]bool, len(bucket.P))

		for name, p := range bucket.P {
			if p == nil {
				continue
			}
			seenThisBucket[name] = true
			cur := cursors[name]
			if cur == nil {
				cur = &playerCursor{}
				cursors[name] = cur
			}

			// Advance the per-player death pointer past any deaths that
			// occurred at or before this bucket's time; each crossed death
			// resets the cursor so no edge can bridge it.
			deaths := deathsByPlayer[name]
			for cur.deathIdx < len(deaths) && deaths[cur.deathIdx] <= bucket.T {
				resetCursor(cur)
				cur.deathIdx++
			}

			invalidPos := p.X == 0 && p.Y == 0
			skip := invalidPos || p.Sp || p.D

			if skip {
				resetCursor(cur)
				cur.seen = true
				continue
			}

			locName := resolveLoc(p.Li)
			team := teamByName[name]

			// Node time accumulation uses the raw per-bucket loc — jitter
			// still reflects as time spent, which is fine.
			if locName != "" {
				node := ensureNode(locName)
				node.Total += bucketDuration
				node.ByPlayer[name] += bucketDuration
				if team != "" {
					if node.ByTeam == nil {
						node.ByTeam = make(map[string]float64)
					}
					node.ByTeam[team] += bucketDuration
				}
			}

			// Hysteresis for edge emission. Three cases when locName differs
			// from cur.loc:
			//   (a) first loc we've seen since a reset: seed cur.loc silently.
			//   (b) matches the pending candidate: bump the count and
			//       commit + emit the edge once the confirmation threshold
			//       is reached.
			//   (c) a different candidate: restart the pending window.
			//
			// Teleport classification measures displacement across the single
			// bucket when the player first left the committed loc — i.e.
			// cur.lastX (refreshed every bucket while still in cur.loc) to
			// cur.pendingStartX (first bucket in the new loc). Using the
			// commit-to-commit distance would mis-classify every long
			// traversal as a teleport.
			if locName != "" && locName != cur.loc {
				if cur.loc == "" {
					// No committed loc yet (first-ever sample or post-reset)
					// — just seed. No edge, no pending state needed.
					cur.loc = locName
					cur.lastX = p.X
					cur.lastY = p.Y
					cur.pendingLoc = ""
					cur.pendingCount = 0
				} else if locName == cur.pendingLoc {
					cur.pendingCount++
					if cur.pendingCount >= locGraphHysteresisBuckets {
						dx := cur.pendingStartX - cur.lastX
						if dx < 0 {
							dx = -dx
						}
						dy := cur.pendingStartY - cur.lastY
						if dy < 0 {
							dy = -dy
						}
						displacement := dx
						if dy > displacement {
							displacement = dy
						}
						kind := "normal"
						if displacement > teleportThreshold {
							kind = "teleport"
						}
						edge := ensureEdge(cur.loc, locName, kind)
						edge.Total++
						edge.ByPlayer[name]++
						if team != "" {
							if edge.ByTeam == nil {
								edge.ByTeam = make(map[string]int)
							}
							edge.ByTeam[team]++
						}
						cur.loc = locName
						cur.lastX = p.X
						cur.lastY = p.Y
						cur.pendingLoc = ""
						cur.pendingCount = 0
					}
				} else {
					cur.pendingLoc = locName
					cur.pendingCount = 1
					cur.pendingStartX = p.X
					cur.pendingStartY = p.Y
				}
			} else if locName == cur.loc {
				// We're in the committed loc; refresh the "last position
				// inside cur.loc" so teleport classification at the next
				// transition uses the actual pre-jump position, not the
				// one from when we first entered cur.loc. Also drop any
				// pending candidate — it was a short detour.
				cur.lastX = p.X
				cur.lastY = p.Y
				cur.pendingLoc = ""
				cur.pendingCount = 0
			}

			cur.seen = true
		}

		// Reset cursor for players who were previously seen but are absent
		// from this bucket — matches the dropout handling in tracks.go:136-152.
		for name, cur := range cursors {
			if cur.seen && !seenThisBucket[name] {
				resetCursor(cur)
				cur.seen = false
			}
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
	return out
}
