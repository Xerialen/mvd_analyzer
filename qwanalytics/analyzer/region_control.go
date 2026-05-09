package analyzer

import (
	"strings"
)

// Region-control state codes used by ComputeRegionControl as a compact
// one-char-per-bucket encoding. Mirror classifyRegionState in
// qw-web/static/app.js. Exported as constants for consumers (frontend,
// MCP wrappers, tests) that want to decode the bucketStates strings.
const (
	RegionStateEmpty            byte = '_'
	RegionStateTeamAControl     byte = 'A'
	RegionStateTeamAWeakControl byte = 'a'
	RegionStateContested        byte = 'C'
	RegionStateWeakContested    byte = 'c'
	RegionStateTeamBControl     byte = 'B'
	RegionStateTeamBWeakControl byte = 'b'
)

// ComputeRegionControl runs the per-bucket region-control classifier
// over a finished timeline and returns:
//
//   - bucketStates: region name -> string of length len(buckets), one
//     state byte per bucket (codes above);
//   - stats:        region name -> match-aggregate share of each state,
//     expressed as percentages (0..100, one decimal place; the seven
//     values sum to 100 within rounding).
//
// The function is pure: same inputs always produce the same output. It
// is called from the analyzer pipeline with the analysis-time regions,
// and is also re-callable from WASM / future MCP wrappers when a
// consumer supplies edited regions.
//
// Inputs:
//
//   - buckets:   the finalized HighResBuckets array. Must include team
//                aggregates only as far as P[playerName] entries are
//                present; this function reads only P, not TD.
//   - locTable:  TimelineAnalysisResult.LocTable. HighResPlayerData.Li
//                indexes into this; index 0 is the empty sentinel.
//   - regions:   the regions to evaluate. Each region's Locs field is
//                authoritative — Points/Centroid are ignored.
//   - teamA, teamB: the two team names to classify against. Players
//                whose team is neither are ignored.
//   - teamOf:    name -> team callback. Returning the empty string
//                excludes the player from the count.
//
// Mirrors qw-web/static/app.js: classifyRegionState (4275-4285),
// recomputeRegionStats (5098-5180), getRegionControlAtTime (5288-5341).
// "Armed" means RL or LG. Dead players (D=true or H<=0) are skipped.
func ComputeRegionControl(
	buckets []HighResBucket,
	locTable []string,
	regions []ControlRegion,
	teamA, teamB string,
	teamOf func(playerName string) string,
) (map[string]string, map[string]RegionStats) {
	if len(regions) == 0 || len(buckets) == 0 {
		return nil, nil
	}

	// regionByLoc maps the canonical loc name (lower-cased) to the
	// region name. We lower-case for case-insensitive matching, same as
	// the on-disk regions JSON loader (timeline_regions.go:80-81).
	regionByLoc := make(map[string]string)
	for _, r := range regions {
		for _, ln := range r.Locs {
			regionByLoc[strings.ToLower(ln)] = r.Name
		}
	}
	if len(regionByLoc) == 0 {
		return nil, nil
	}

	// One counter struct per region, reused per bucket.
	type counts struct {
		aWpn, aNo, bWpn, bNo int
	}

	// Pre-allocate per-region byte buffers and aggregate counters.
	stateBuf := make(map[string][]byte, len(regions))
	totals := make(map[string]*RegionStats, len(regions))
	for _, r := range regions {
		stateBuf[r.Name] = make([]byte, 0, len(buckets))
		totals[r.Name] = &RegionStats{}
	}

	presence := make(map[string]*counts, len(regions))
	for _, r := range regions {
		presence[r.Name] = &counts{}
	}

	for i := range buckets {
		// Reset per-bucket counts.
		for _, c := range presence {
			c.aWpn, c.aNo, c.bWpn, c.bNo = 0, 0, 0, 0
		}

		// Tally living players into their region.
		for name, pd := range buckets[i].P {
			if pd == nil {
				continue
			}
			if pd.D || pd.H <= 0 {
				continue
			}
			if pd.Li <= 0 || int(pd.Li) >= len(locTable) {
				continue
			}
			locName := locTable[pd.Li]
			regionName, ok := regionByLoc[strings.ToLower(locName)]
			if !ok {
				continue
			}
			team := teamOf(name)
			if team == "" {
				continue
			}
			armed := pd.RL || pd.LG
			c := presence[regionName]
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
		}

		// Classify and append one byte per region.
		for _, r := range regions {
			c := presence[r.Name]
			state := classifyRegionState(c.aWpn, c.aNo, c.bWpn, c.bNo)
			stateBuf[r.Name] = append(stateBuf[r.Name], state)
			t := totals[r.Name]
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

	// Convert raw counts to percentages (one decimal place).
	bucketStates := make(map[string]string, len(regions))
	stats := make(map[string]RegionStats, len(regions))
	total := float64(len(buckets))
	pct := func(n float64) float64 { return float64(int(n/total*1000+0.5)) / 10 }
	for _, r := range regions {
		bucketStates[r.Name] = string(stateBuf[r.Name])
		t := totals[r.Name]
		stats[r.Name] = RegionStats{
			TeamAControl:     pct(t.TeamAControl),
			TeamAWeakControl: pct(t.TeamAWeakControl),
			Contested:        pct(t.Contested),
			WeakContested:    pct(t.WeakContested),
			Empty:            pct(t.Empty),
			TeamBWeakControl: pct(t.TeamBWeakControl),
			TeamBControl:     pct(t.TeamBControl),
		}
	}
	return bucketStates, stats
}

// recomputeRegionStatsFromStrings rebuilds the per-region match-aggregate
// percentages from already-encoded bucketStates strings. Used by the
// match-relative postprocess after it slices the warmup prefix off, so
// the published Stats reflects match-only state.
func recomputeRegionStatsFromStrings(bucketStates map[string]string) map[string]RegionStats {
	out := make(map[string]RegionStats, len(bucketStates))
	for name, s := range bucketStates {
		var t RegionStats
		total := float64(len(s))
		if total == 0 {
			out[name] = t
			continue
		}
		var emp, aC, aW, con, wcon, bC, bW int
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case RegionStateEmpty:
				emp++
			case RegionStateTeamAControl:
				aC++
			case RegionStateTeamAWeakControl:
				aW++
			case RegionStateContested:
				con++
			case RegionStateWeakContested:
				wcon++
			case RegionStateTeamBControl:
				bC++
			case RegionStateTeamBWeakControl:
				bW++
			}
		}
		pct := func(n int) float64 { return float64(int(float64(n)/total*1000+0.5)) / 10 }
		t.Empty = pct(emp)
		t.TeamAControl = pct(aC)
		t.TeamAWeakControl = pct(aW)
		t.Contested = pct(con)
		t.WeakContested = pct(wcon)
		t.TeamBControl = pct(bC)
		t.TeamBWeakControl = pct(bW)
		out[name] = t
	}
	return out
}

// classifyRegionState is the seven-state decision rule, faithful port
// of qw-web/static/app.js:4275-4285.
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
