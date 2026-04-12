package analyzer

import (
	"sort"
	"strings"

	"github.com/mvd-analyzer/internal/loc"
)

// ============================================================================
// REGION CONTROL CONFIGURATION
//
// Region control tracks which team controls key areas of the map.
// There are two layers of configuration:
//
// 1. AUTO-DETECTION (controlKeywords):
//    Any loc name containing one of these keywords (as a dot/space-separated
//    token) becomes a tracked region. If multiple locs share the same keyword
//    but are far apart (>800 world units), they are split into separate regions
//    named by their distinguishing prefix (e.g., "high.RL" and "low.RL").
//
// 2. MAP-SPECIFIC CUSTOM REGIONS (mapCustomRegions):
//    For popular maps, you can define named regions from specific loc names.
//    These locs are excluded from auto-detection so they don't get merged
//    with keyword-based regions.
//
// To find loc names for a map, check internal/loc/locs/<map>.loc.
// The raw loc file uses variables like $loc_name_ra which become "RA" after
// substitution, and "$." becomes "." as separator. So "high$loc_name_separatorrl"
// becomes "high.RL". You can also see the final names in the browser's
// Region Control panel (the editable text fields show all loc names per region).
//
// Users can also edit region definitions in the browser without code changes.
// ============================================================================

// controlKeywords lists the item types that are auto-detected as regions.
// Add entries here to track additional item types across all maps.
var controlKeywords = map[string]bool{
	"RA": true, "RL": true, "LG": true, "QUAD": true,
}

type locWithKeyword struct {
	loc     loc.Location
	keyword string
}

// mapCustomRegions defines custom named regions for specific maps.
// To add a new map, add a key with the lowercase map name and a list of
// regions. Each region has a display name and a list of loc names to include.
//
// Example — adding custom regions for dm4:
//
//	"dm4": {
//	    {name: "RA Bridge", locNames: []string{"RA", "RA.bridge"}},
//	    {name: "Biosuit",   locNames: []string{"bio", "bio.water"}},
//	},
type customRegion struct {
	name     string   // Display name for this region
	locNames []string // Loc names to include (exact match against processed loc names)
}

var mapCustomRegions = map[string][]customRegion{
	// Schloss
	"schloss": {
		{name: "Tower", locNames: []string{"tower", "tower.entry", "tower.RL"}},
		{name: "Cathedral", locNames: []string{"cathedral", "cathedral.YA", "cathedral.SSG"}},
	},
	// E1M2 — Castle of the Damned
	"e1m2": {
		{name: "YA", locNames: []string{"YA", "YA.spikes", "YA.tele", "YA.water"}},
		{name: "MH", locNames: []string{"MH", "MH.above", "MH.entry", "MH.exit", "MH.low", "MH.rox", "MH.SNG"}},
	},
	// DM3 — The Abandoned Base
	"dm3": {
		{name: "YA", locNames: []string{"YA", "YA.box", "YA.up"}},
		// RA region excludes RA.tunnel — it's the lower passageway beneath
		// the RA platform and shouldn't count as RA control. Because the
		// custom region is named exactly "RA", the auto-detection for the
		// RA keyword is suppressed entirely (see buildControlRegions), so
		// RA.tunnel simply isn't tracked as a region.
		{name: "RA", locNames: []string{"RA", "RA.low", "RA.rox", "RA.entry"}},
	},
	// DM2 — The Claustrophobopolis
	"dm2": {
		{name: "Secret", locNames: []string{"secret"}},
		{name: "Backroom", locNames: []string{"RA.MH", "RA.MH/rox"}},
		{name: "Tele", locNames: []string{"tele", "tele.entry", "tele.YA", "tele.high"}},
	},
}

// buildControlRegions groups locations by item keyword and clusters spatially
func (a *TimelineAnalyzer) buildControlRegions() []ControlRegion {
	locs := a.locFinder.Locations()
	if len(locs) == 0 {
		return nil
	}

	// Get map name for map-specific customization
	mapName := ""
	if a.ctx.DemoInfo != nil && a.ctx.DemoInfo.Map != "" {
		mapName = strings.ToLower(a.ctx.DemoInfo.Map)
		if idx := strings.LastIndex(mapName, "/"); idx >= 0 {
			mapName = mapName[idx+1:]
		}
		mapName = strings.TrimSuffix(mapName, ".bsp")
	}

	// Build set of locs consumed by custom regions (so they're excluded from auto-detection)
	// and the set of auto-detect keywords that a custom region has fully claimed.
	// A custom region claims a keyword whenever its name (uppercased) matches
	// an entry in controlKeywords — in that case the auto-detector skips that
	// keyword entirely so the curated definition is the single source of truth
	// (no leftover one-loc clusters competing under the same name).
	customConsumed := make(map[string]bool)
	customClaimedKeywords := make(map[string]bool)
	customDefs := mapCustomRegions[mapName]
	for _, cr := range customDefs {
		for _, ln := range cr.locNames {
			customConsumed[ln] = true
		}
		if controlKeywords[strings.ToUpper(cr.name)] {
			customClaimedKeywords[strings.ToUpper(cr.name)] = true
		}
	}

	// Group locations by any matching keyword token in their name
	// e.g., "cellar.RL" matches RL, "RA.stairs" matches RA
	groups := make(map[string][]locWithKeyword)

	for _, l := range locs {
		// Skip locs consumed by custom regions
		if customConsumed[l.Name] {
			continue
		}

		tokens := strings.FieldsFunc(l.Name, func(r rune) bool {
			return r == '.' || r == ' '
		})
		for _, token := range tokens {
			upper := strings.ToUpper(token)
			if controlKeywords[upper] {
				groups[upper] = append(groups[upper], locWithKeyword{loc: l, keyword: upper})
				break // Only match first keyword per location
			}
		}
	}

	var regions []ControlRegion

	// Build custom regions from map-specific definitions
	locByName := make(map[string][]loc.Location)
	for _, l := range locs {
		locByName[l.Name] = append(locByName[l.Name], l)
	}
	for _, cr := range customDefs {
		region := ControlRegion{Name: cr.name}
		var sumX, sumY float32
		var count int
		for _, ln := range cr.locNames {
			for _, l := range locByName[ln] {
				region.Points = append(region.Points, MapLocation{
					X: l.X, Y: l.Y, Z: l.Z, Name: l.Name,
				})
				sumX += l.X
				sumY += l.Y
				count++
			}
		}
		if count > 0 {
			region.CentroidX = sumX / float32(count)
			region.CentroidY = sumY / float32(count)
			regions = append(regions, region)
		}
	}

	for keyword, locs := range groups {
		if len(locs) == 0 {
			continue
		}
		// A custom region with this exact name has full ownership of the
		// keyword — skip auto-detection so the curated list isn't padded
		// with leftover loc clusters under the same name.
		if customClaimedKeywords[keyword] {
			continue
		}
		clusters := clusterLocations(locs, 800)

		for _, cluster := range clusters {
			region := ControlRegion{}

			// Name the region
			if len(clusters) == 1 {
				region.Name = keyword
			} else {
				// Find most common second token for naming
				region.Name = nameCluster(keyword, cluster)
			}

			// Build points and centroid
			var sumX, sumY float32
			for _, lk := range cluster {
				region.Points = append(region.Points, MapLocation{
					X:    lk.loc.X,
					Y:    lk.loc.Y,
					Z:    lk.loc.Z,
					Name: lk.loc.Name,
				})
				sumX += lk.loc.X
				sumY += lk.loc.Y
			}
			region.CentroidX = sumX / float32(len(cluster))
			region.CentroidY = sumY / float32(len(cluster))

			regions = append(regions, region)
		}
	}

	// Sort regions by name for stable output
	sort.Slice(regions, func(i, j int) bool {
		return regions[i].Name < regions[j].Name
	})

	return regions
}

// clusterLocations groups locations by spatial proximity using single-linkage clustering
func clusterLocations(locs []locWithKeyword, threshold float64) [][]locWithKeyword {
	n := len(locs)
	if n == 0 {
		return nil
	}

	// Union-Find
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	threshSq := threshold * threshold
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			dx := float64(locs[i].loc.X - locs[j].loc.X)
			dy := float64(locs[i].loc.Y - locs[j].loc.Y)
			if dx*dx+dy*dy < threshSq {
				union(i, j)
			}
		}
	}

	// Group by root
	clusterMap := make(map[int][]locWithKeyword)
	for i, l := range locs {
		root := find(i)
		clusterMap[root] = append(clusterMap[root], l)
	}

	var result [][]locWithKeyword
	for _, c := range clusterMap {
		result = append(result, c)
	}
	return result
}

// nameCluster names a cluster based on the most common second token
func nameCluster(keyword string, cluster []locWithKeyword) string {
	// Find the most common non-keyword token across loc names in this cluster.
	// E.g., for keyword "RL", locs like "high.RL" → token "high", "low.RL" → token "low"
	keywordLower := strings.ToLower(keyword)
	tokenCounts := make(map[string]int)
	for _, lk := range cluster {
		tokens := strings.FieldsFunc(lk.loc.Name, func(r rune) bool {
			return r == '.' || r == ' '
		})
		for _, t := range tokens {
			lower := strings.ToLower(t)
			if lower != keywordLower {
				tokenCounts[lower]++
			}
		}
	}

	bestToken := ""
	bestCount := 0
	for token, count := range tokenCounts {
		if count > bestCount {
			bestCount = count
			bestToken = token
		}
	}

	if bestToken != "" {
		return bestToken + "." + keyword
	}
	return keyword
}
