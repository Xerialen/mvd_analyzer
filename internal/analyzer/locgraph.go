package analyzer

// LocGraphResult is the aggregate movement graph: loc nodes weighted by
// time-spent, directed edges weighted by transition count. Per-player and
// per-team breakdowns are carried on every node and edge so the frontend
// can filter without re-aggregating.
type LocGraphResult struct {
	Locs  []LocNode `json:"locs"`
	Edges []LocEdge `json:"edges"`
}

type LocNode struct {
	Name     string             `json:"name"`
	X        float32            `json:"x"`
	Y        float32            `json:"y"`
	Z        float32            `json:"z"`
	Total    float64            `json:"total"`
	ByPlayer map[string]float64 `json:"byPlayer"`
	ByTeam   map[string]float64 `json:"byTeam,omitempty"`
}

type LocEdge struct {
	From     string         `json:"from"`
	To       string         `json:"to"`
	Kind     string         `json:"kind"`
	Total    int            `json:"total"`
	ByPlayer map[string]int `json:"byPlayer"`
	ByTeam   map[string]int `json:"byTeam,omitempty"`
}

// teleportBaseThreshold mirrors the frontend constant at app.js:4160: the
// per-axis "max plausible movement per second" limit. Any per-axis
// displacement exceeding bucketDuration * teleportBaseThreshold between
// consecutive buckets is classified as a teleport. Frontend uses
// MAX_MOVE_PER_BUCKET = 2500 * bucketDuration, so the per-second base is 2500.
const teleportBaseThreshold = 2500.0

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

	type playerCursor struct {
		loc   string
		lastX float32
		lastY float32
		seen  bool
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

			invalidPos := p.X == 0 && p.Y == 0
			skip := invalidPos || p.Sp || p.D

			if skip {
				cur.loc = ""
				cur.seen = true
				continue
			}

			locName := resolveLoc(p.Li)
			team := teamByName[name]

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

			if locName != "" && cur.loc != "" && locName != cur.loc {
				dx := p.X - cur.lastX
				if dx < 0 {
					dx = -dx
				}
				dy := p.Y - cur.lastY
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
			}

			if locName != "" {
				cur.loc = locName
				cur.lastX = p.X
				cur.lastY = p.Y
			} else {
				cur.loc = ""
			}
			cur.seen = true
		}

		// Reset cursor for players who were previously seen but are absent
		// from this bucket — matches the dropout handling in tracks.go:136-152.
		for name, cur := range cursors {
			if cur.seen && !seenThisBucket[name] {
				cur.loc = ""
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
