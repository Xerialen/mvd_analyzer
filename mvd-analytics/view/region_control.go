package view

import (
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// Region-control state codes used as a compact one-char-per-bucket
// encoding for RegionControlResult.BucketStates. Mirror
// classifyRegionState in mvd-web/static/app.js. Exported so consumers
// (frontend, MCP wrappers, tests) can decode the bucketStates strings.
const (
	RegionStateEmpty            byte = '_'
	RegionStateTeamAControl     byte = 'A'
	RegionStateTeamAWeakControl byte = 'a'
	RegionStateContested        byte = 'C'
	RegionStateWeakContested    byte = 'c'
	RegionStateTeamBControl     byte = 'B'
	RegionStateTeamBWeakControl byte = 'b'
)

// RegionControlOptions tunes a RegionControl query. Every field is
// optional; defaults are derived from r.TimelineAnalysis.RegionControl
// (regions + team labels — populated by the analyzer's region-
// detection pass during Finalize) and r.Match.Players (team-of-name).
//
// Callers that already know the answer — typically the WASM bridge,
// where the user is editing region definitions in the UI — pass
// explicit Regions / TeamA / TeamB / TeamOf overrides.
type RegionControlOptions struct {
	WindowMs  int                      // bucket resolution; 0 → 50
	StartTime float64                  // sub-window lower bound in seconds; 0 → match start
	EndTime   float64                  // sub-window upper bound in seconds; 0 → match end
	Regions   []result.ControlRegion   // overrides r.TimelineAnalysis.RegionControl.Regions
	TeamA     string                   // overrides r.TimelineAnalysis.RegionControl.TeamA
	TeamB     string                   // overrides r.TimelineAnalysis.RegionControl.TeamB
	TeamOf    func(name string) string // overrides default closure over r.Match.Players
}

// RegionControlView aliases result.RegionControlResult so the view-
// package surface uses the same XxxView vocabulary as the other view
// functions. The aliased type is the canonical one because the same
// shape is baked into parse-time Result by the regionControlPost
// post-processor.
type RegionControlView = result.RegionControlResult

// RegionControl computes per-bucket region presence + armed-state
// classification (A/a/B/b/C/c/_) and match-aggregate state
// percentages. Returns a fully populated *result.RegionControlResult
// or, when there's nothing to compute (no regions, no two teams, no
// match window), one with Regions/TeamA/TeamB filled and no
// BucketStates/Stats.
//
// Walks result.Streams natively: per windowMs (default 50) bucket,
// find each player's last position sample with T ≤ bucket-start (via
// PositionTrack.Li), look up loc → region, check armed via RL/LG
// interval membership, tally per region per team, classify. "Armed"
// means RL or LG. Pre-spawn / dead samples (Li=0) skipped.
//
// All time arithmetic is integer milliseconds (schema v8); the
// bucket grid is anchored at r.Streams.Global.MatchStart.
func RegionControl(r *result.Result, opts RegionControlOptions) (*result.RegionControlResult, error) {
	if r == nil || r.Streams == nil {
		return &result.RegionControlResult{}, nil
	}

	// 1. Regions: explicit override else baked-in.
	regions := opts.Regions
	if regions == nil && r.TimelineAnalysis != nil && r.TimelineAnalysis.RegionControl != nil {
		regions = r.TimelineAnalysis.RegionControl.Regions
	}
	out := &result.RegionControlResult{Regions: regions}
	if len(regions) == 0 {
		return out, nil
	}

	// 2. Team labels: explicit override else compute from Match.Players
	//    (the canonical post-normalize scoreboard). Fall back to the
	//    baked RegionControl.TeamA/TeamB only when Match is absent.
	//
	//    Why prefer Match.Players over the baked values: duelTeamNormalize
	//    rewrites Match.Players[].Team from real team names ("red") to
	//    per-player synthetic names ("bananfalco"). The regionControlPost
	//    runs after duelTeamNormalize, so Match.Players already carries
	//    the canonical labels; reading them keeps the teamOf closure and
	//    teamA/teamB consistent.
	teamA, teamB := opts.TeamA, opts.TeamB
	if teamA == "" || teamB == "" {
		ta, tb := inferTeamsFromMatch(r)
		if teamA == "" {
			teamA = ta
		}
		if teamB == "" {
			teamB = tb
		}
		if (teamA == "" || teamB == "") && r.TimelineAnalysis != nil && r.TimelineAnalysis.RegionControl != nil {
			if teamA == "" {
				teamA = r.TimelineAnalysis.RegionControl.TeamA
			}
			if teamB == "" {
				teamB = r.TimelineAnalysis.RegionControl.TeamB
			}
		}
	}
	out.TeamA = teamA
	out.TeamB = teamB
	if teamA == "" || teamB == "" {
		// Not a binary-team layout — return the regions only, mirrors
		// pre-refactor behaviour.
		return out, nil
	}

	// 3. teamOf: explicit override else default closure over Match.Players
	//    (with DemoInfo fallback), stripping the "#slot" disambiguation
	//    suffix the analyzer adds for name collisions.
	teamOf := opts.TeamOf
	if teamOf == nil {
		nameToTeam := defaultNameToTeam(r)
		teamOf = func(name string) string {
			base := name
			if idx := strings.LastIndex(name, "#"); idx >= 0 {
				base = name[:idx]
			}
			return nameToTeam[base]
		}
	}

	// 4. Window resolution + optional sub-window.
	windowMs := opts.WindowMs
	if windowMs <= 0 {
		windowMs = 50
	}
	var startMs, endMs int32
	if opts.StartTime > 0 {
		startMs = int32(opts.StartTime * 1000)
	}
	if opts.EndTime > 0 {
		endMs = int32(opts.EndTime * 1000)
	}

	bucketStates, stats := classifyRegions(r, regions, teamA, teamB, teamOf, windowMs, startMs, endMs)
	out.BucketStates = bucketStates
	out.Stats = stats
	return out, nil
}

// inferTeamsFromMatch picks two team labels for A/B from the canonical
// Match.Players list. Preference order (mirrors the pre-refactor
// analyzer.timeline_finalize behaviour):
//
//  1. DemoInfo.Teams[0]/[1] when both names are present in
//     Match.Players. KTX's two-team layout drives that list.
//  2. Otherwise Match.Teams[0]/[1] order.
//  3. Otherwise the first two distinct teams encountered walking
//     Match.Players.
//
// Returns ("", "") when fewer than two distinct teams are present —
// the caller then short-circuits to "no binary team layout".
func inferTeamsFromMatch(r *result.Result) (string, string) {
	if r.Match == nil {
		return "", ""
	}
	seen := make(map[string]struct{}, len(r.Match.Players))
	for _, p := range r.Match.Players {
		if p.Team != "" {
			seen[p.Team] = struct{}{}
		}
	}
	if len(seen) < 2 {
		return "", ""
	}

	// 1. DemoInfo.Teams ordering preference — mirrors the analyzer's
	//    pre-refactor ordering at Finalize time.
	if r.DemoInfo != nil && len(r.DemoInfo.Teams) == 2 {
		t0, t1 := r.DemoInfo.Teams[0], r.DemoInfo.Teams[1]
		if _, ok0 := seen[t0]; ok0 {
			if _, ok1 := seen[t1]; ok1 {
				return t0, t1
			}
		}
	}

	// 2. Match.Teams ordering.
	pair := make([]string, 0, 2)
	for _, t := range r.Match.Teams {
		if _, ok := seen[t.Name]; ok {
			pair = append(pair, t.Name)
			delete(seen, t.Name)
			if len(pair) == 2 {
				return pair[0], pair[1]
			}
		}
	}

	// 3. Walk-order fallback from Match.Players.
	for _, p := range r.Match.Players {
		if p.Team == "" {
			continue
		}
		if _, present := seen[p.Team]; !present {
			continue
		}
		pair = append(pair, p.Team)
		delete(seen, p.Team)
		if len(pair) == 2 {
			return pair[0], pair[1]
		}
	}
	if len(pair) >= 2 {
		return pair[0], pair[1]
	}
	return "", ""
}

// defaultNameToTeam builds a player → team map for region tallying.
// Streams.Players is the primary source because it covers every
// player with any stream data — including spectator-edge-case
// players that MatchAnalyzer filters out of Match.Players. The
// classifier walks Streams.Players, so anyone with positions needs a
// team mapping or their positions silently drop. Match.Players and
// DemoInfo.Players are folded in as backstops for names that lack a
// Streams entry.
//
// Mirrors what the pre-refactor analyzer.timeline_finalize closure
// did via slotToTeam (analyzer state held the same complete map).
func defaultNameToTeam(r *result.Result) map[string]string {
	nameToTeam := make(map[string]string)
	if r.Streams != nil {
		for _, p := range r.Streams.Players {
			if p.Name != "" && p.Team != "" {
				nameToTeam[p.Name] = p.Team
			}
		}
	}
	if r.Match != nil {
		for _, p := range r.Match.Players {
			if p.Name != "" && p.Team != "" {
				if _, ok := nameToTeam[p.Name]; !ok {
					nameToTeam[p.Name] = p.Team
				}
			}
		}
	}
	if r.DemoInfo != nil {
		for _, p := range r.DemoInfo.Players {
			if p.Name != "" && p.Team != "" {
				if _, ok := nameToTeam[p.Name]; !ok {
					nameToTeam[p.Name] = p.Team
				}
			}
		}
	}
	return nameToTeam
}

// classifyRegions is the per-bucket walker — formerly
// analyzer.ComputeRegionControl. Kept private; callers go through
// RegionControl, which resolves defaults from the Result.
//
// optsStartMs / optsEndMs clamp the bucket grid to a sub-window; 0
// means "no override" (use MatchStart / MatchEnd respectively).
func classifyRegions(
	r *result.Result,
	regions []result.ControlRegion,
	teamA, teamB string,
	teamOf func(playerName string) string,
	windowMs int,
	optsStartMs, optsEndMs int32,
) (map[string]string, map[string]result.RegionStats) {
	if r == nil || r.Streams == nil || len(regions) == 0 {
		return nil, nil
	}
	bucketDurMs := int32(windowMs)

	var locTable []string
	if r.TimelineAnalysis != nil {
		locTable = r.TimelineAnalysis.LocTable
	}

	// regionByLoc: lower-cased loc name → region name (case-insensitive
	// matching, same as the on-disk regions JSON loader).
	regionByLoc := make(map[string]string)
	for _, rg := range regions {
		for _, ln := range rg.Locs {
			regionByLoc[strings.ToLower(ln)] = rg.Name
		}
	}
	if len(regionByLoc) == 0 {
		return nil, nil
	}

	// Pre-resolve each region's loc-index set so the inner loop is an
	// integer hashtable lookup, not a string-lower per sample.
	regionByLi := make(map[int16]string, len(regionByLoc))
	for li, name := range locTable {
		if rn, ok := regionByLoc[strings.ToLower(name)]; ok {
			regionByLi[int16(li)] = rn
		}
	}
	if len(regionByLi) == 0 {
		return nil, nil
	}

	// Bucket grid is anchored at MatchStart (always 0 on the
	// match-relative clock every producer stamps at Finalize). The optional sub-window
	// clamps to a tighter range; default behaviour (both opts zero)
	// covers the full match.
	matchStart := r.Streams.Global.MatchStart
	matchEnd := r.Streams.Global.MatchEnd
	gridStart := matchStart
	gridEnd := matchEnd
	if optsStartMs > gridStart {
		gridStart = optsStartMs
	}
	if optsEndMs > 0 && optsEndMs < gridEnd {
		gridEnd = optsEndMs
	}
	if gridEnd <= gridStart {
		return nil, nil
	}
	// Round-half-up integer division.
	nBuckets := int((gridEnd - gridStart + bucketDurMs/2) / bucketDurMs)
	if nBuckets <= 0 {
		return nil, nil
	}

	type counts struct{ aWpn, aNo, bWpn, bNo int }
	presence := make([]map[string]*counts, nBuckets)
	for i := range presence {
		presence[i] = make(map[string]*counts, len(regions))
		for _, rg := range regions {
			presence[i][rg.Name] = &counts{}
		}
	}

	// Per-region per-player attribution tally. Counted in lock-step with
	// the team-aggregate `presence` above so the two views never disagree
	// about who was where.
	type playerTally struct {
		team    string
		armed   int
		unarmed int
	}
	byPlayer := make(map[string]map[string]*playerTally, len(regions))
	for _, rg := range regions {
		byPlayer[rg.Name] = make(map[string]*playerTally)
	}

	// Per-player walk: for each bucket find the latest position sample
	// with T ≤ bucket-start, classify region presence + armed.
	for _, p := range r.Streams.Players {
		team := teamOf(p.Name)
		if team == "" || (team != teamA && team != teamB) {
			continue
		}
		pt := p.Position
		if pt == nil || len(pt.T) == 0 || len(pt.Li) != len(pt.T) {
			continue
		}
		sIdx := 0
		for bi := 0; bi < nBuckets; bi++ {
			bucketStart := gridStart + int32(bi)*bucketDurMs
			for sIdx+1 < len(pt.T) && pt.T[sIdx+1] <= bucketStart {
				sIdx++
			}
			if pt.T[sIdx] > bucketStart {
				// Player hasn't started emitting positions yet.
				continue
			}
			li := pt.Li[sIdx]
			if li == 0 {
				continue
			}
			regionName, ok := regionByLi[li]
			if !ok {
				continue
			}
			armed := intervalContains(p.RL, bucketStart) ||
				intervalContains(p.LG, bucketStart)
			c := presence[bi][regionName]
			if c == nil {
				continue
			}
			switch team {
			case teamA:
				if armed {
					c.aWpn++
				} else {
					c.aNo++
				}
			case teamB:
				if armed {
					c.bWpn++
				} else {
					c.bNo++
				}
			}
			// Per-player attribution: bump this player's counter for
			// this region. Lazy-allocate so we only record entries for
			// players that actually appeared in the region.
			pt := byPlayer[regionName][p.Name]
			if pt == nil {
				pt = &playerTally{team: team}
				byPlayer[regionName][p.Name] = pt
			}
			if armed {
				pt.armed++
			} else {
				pt.unarmed++
			}
		}
	}

	// Classify per bucket per region; tally aggregate state percentages.
	stateBuf := make(map[string][]byte, len(regions))
	totals := make(map[string]*result.RegionStats, len(regions))
	for _, rg := range regions {
		stateBuf[rg.Name] = make([]byte, 0, nBuckets)
		totals[rg.Name] = &result.RegionStats{}
	}
	for bi := 0; bi < nBuckets; bi++ {
		for _, rg := range regions {
			c := presence[bi][rg.Name]
			state := classifyRegionState(c.aWpn, c.aNo, c.bWpn, c.bNo)
			stateBuf[rg.Name] = append(stateBuf[rg.Name], state)
			t := totals[rg.Name]
			switch state {
			case RegionStateEmpty:
				t.Empty++
			case RegionStateTeamAControl:
				t.TeamAControl++
			case RegionStateTeamAWeakControl:
				t.TeamAWeakControl++
			case RegionStateContested:
				t.Contested++
			case RegionStateWeakContested:
				t.WeakContested++
			case RegionStateTeamBControl:
				t.TeamBControl++
			case RegionStateTeamBWeakControl:
				t.TeamBWeakControl++
			}
		}
	}

	bucketStates := make(map[string]string, len(regions))
	stats := make(map[string]result.RegionStats, len(regions))
	total := float64(nBuckets)
	pct := func(n float64) float64 { return float64(int(n/total*1000+0.5)) / 10 }
	for _, rg := range regions {
		bucketStates[rg.Name] = string(stateBuf[rg.Name])
		t := totals[rg.Name]
		rgStats := result.RegionStats{
			TeamAControl:     pct(t.TeamAControl),
			TeamAWeakControl: pct(t.TeamAWeakControl),
			Contested:        pct(t.Contested),
			WeakContested:    pct(t.WeakContested),
			Empty:            pct(t.Empty),
			TeamBWeakControl: pct(t.TeamBWeakControl),
			TeamBControl:     pct(t.TeamBControl),
		}
		if pm := byPlayer[rg.Name]; len(pm) > 0 {
			rgStats.ByPlayer = make(map[string]result.RegionPlayerStats, len(pm))
			for name, t := range pm {
				rgStats.ByPlayer[name] = result.RegionPlayerStats{
					Team:    t.team,
					Armed:   t.armed,
					Unarmed: t.unarmed,
				}
			}
		}
		stats[rg.Name] = rgStats
	}
	return bucketStates, stats
}

// classifyRegionState is the seven-state decision rule. Faithful port
// of mvd-web/static/app.js: classifyRegionState.
func classifyRegionState(aWpn, aNo, bWpn, bNo int) byte {
	aT := aWpn + aNo
	bT := bWpn + bNo
	if aT == 0 && bT == 0 {
		return RegionStateEmpty
	}
	if aT > 0 && bT == 0 {
		if aWpn > 0 {
			return RegionStateTeamAControl
		}
		return RegionStateTeamAWeakControl
	}
	if bT > 0 && aT == 0 {
		if bWpn > 0 {
			return RegionStateTeamBControl
		}
		return RegionStateTeamBWeakControl
	}
	// Both teams present.
	if aWpn > 0 && bWpn == 0 {
		return RegionStateTeamAControl
	}
	if bWpn > 0 && aWpn == 0 {
		return RegionStateTeamBControl
	}
	if aWpn > 0 && bWpn > 0 {
		return RegionStateContested
	}
	return RegionStateWeakContested
}
