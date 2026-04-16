package analyzer

import (
	"sort"

	"github.com/mvd-analyzer/internal/loc"
	"github.com/mvd-analyzer/internal/mvd"
)

// Finalize converts the raw per-bucket player state collected during parsing
// into the TimelineAnalysisResult shipped to the frontend. This is the
// orchestration step — most of the heavy lifting is delegated to the
// aggregate / powerup / streak / region helpers.
func (a *TimelineAnalyzer) Finalize() (interface{}, error) {
	// Do a final sample at the end
	if len(a.buckets) > 0 {
		lastBucket := a.buckets[len(a.buckets)-1]
		if lastBucket.endTime > a.lastSampleTime {
			a.sampleCurrentState(lastBucket.endTime)
		}
	}

	// Try to load loc file from DemoInfo.Map if not already loaded
	if a.locFinder == nil && a.ctx.DemoInfo != nil && a.ctx.DemoInfo.Map != "" {
		if finder, err := loc.LoadForMap(a.ctx.DemoInfo.Map); err == nil {
			a.locFinder = finder
		}
	}

	// Resolve location names now that we have the loc finder
	if a.locFinder != nil {
		for _, bucket := range a.buckets {
			for _, pData := range bucket.playerData {
				if pData.pos.x != 0 || pData.pos.y != 0 || pData.pos.z != 0 {
					pData.location = a.locFinder.FindNearest(pData.pos.x, pData.pos.y, pData.pos.z)
				}
			}
		}
	}

	// Build a name->team lookup from DemoInfo (authoritative source)
	// Use both exact name and normalized name for matching
	nameToTeam := make(map[string]string)
	normNameToTeam := make(map[string]string) // Normalized names (lowercase, alphanumeric only)

	if a.ctx.DemoInfo != nil {
		for _, p := range a.ctx.DemoInfo.Players {
			if p.Name == "" || p.Team == "" {
				continue
			}
			nameToTeam[p.Name] = p.Team
			normNameToTeam[normalizePlayerName(p.Name)] = p.Team
		}
	}

	// Bridge slot↔demoinfo via login join / name join.
	resolved := a.ctx.ResolveSlotDemoInfo()
	slotToTeam := make(map[int]string)
	slotToPlayer := make(map[int]string)
	for slot, di := range resolved {
		if di.Team != "" {
			slotToTeam[slot] = di.Team
			slotToPlayer[slot] = di.Name
		}
	}

	// Convert raw frag events to final events with player and team info
	fragEvents := make([]TimelineFragEvent, 0, len(a.rawFrags))
	for _, raw := range a.rawFrags {
		// Prefer the demoinfo-resolved name (via slotToPlayer) so the
		// emitted player name matches what the timeline buckets and the
		// frontend's demoinfo-keyed Team Status panel expect. Fall back to
		// the userinfo name only when neither the login join nor the name
		// join matched this slot to a demoinfo entry.
		playerName := slotToPlayer[raw.PlayerNum]
		team := slotToTeam[raw.PlayerNum]

		if playerName == "" {
			if player := a.ctx.Players[raw.PlayerNum]; player != nil {
				playerName = player.Name
				if team == "" {
					team = player.Team
				}
			}
		}
		if playerName == "" {
			if name, ok := a.playerNames[raw.PlayerNum]; ok {
				playerName = name
			}
		}

		// If we still have a name but no team, look it up in DemoInfo by name.
		if playerName != "" && team == "" {
			team = nameToTeam[playerName]
			if team == "" {
				team = normNameToTeam[normalizePlayerName(playerName)]
			}
		}

		if team != "" {
			fragEvents = append(fragEvents, TimelineFragEvent{
				Time:   raw.Time,
				Player: playerName,
				Team:   team,
				Delta:  raw.Delta,
			})
		}
	}

	// Detect powerup pickup events for Key Moments
	powerupEvents := a.detectPowerupEvents(nameToTeam, slotToTeam, slotToPlayer)

	// Count frags during each powerup run
	for i := range powerupEvents {
		pe := &powerupEvents[i]
		for _, fe := range a.ctx.FragEntries {
			if fe.Killer != pe.PlayerName || fe.IsSuicide || fe.IsTeamKill {
				continue
			}
			if fe.Time >= pe.Time && fe.Time <= pe.EndTime {
				pe.Frags++
			}
		}
	}

	// Export location data for map visualization
	var locationData []MapLocation
	if a.locFinder != nil {
		locs := a.locFinder.Locations()
		locationData = make([]MapLocation, len(locs))
		for i, l := range locs {
			locationData[i] = MapLocation{
				X:    l.X,
				Y:    l.Y,
				Z:    l.Z,
				Name: l.Name,
			}
		}
	}

	// Build slot->name mapping for exports.
	//
	// Prefer the DemoInfo-derived name (resolved above via login join or
	// name join) over the live userinfo name. The two can differ when
	// the userinfo "name" field is an auth/login string but the player's
	// actual displayed netname is a different (often colored) string —
	// the frontend joins timeline data against DemoInfo player names, so
	// we must export the same name DemoInfo did or the per-player health/
	// armor stack disappears for that player.
	slotToName := make(map[int]string)
	for slot := 0; slot < mvd.MaxClients; slot++ {
		if name := slotToPlayer[slot]; name != "" {
			slotToName[slot] = name
		} else if player := a.ctx.Players[slot]; player != nil && player.Name != "" {
			slotToName[slot] = player.Name
		} else if name := a.playerNames[slot]; name != "" {
			slotToName[slot] = name
		}
	}

	// Build the interned loc-name table once. Index 0 is reserved for the
	// empty/no-loc case so HighResPlayerData.Li can lean on json:omitempty.
	// pData.location was already populated server-side in the loop above
	// using loc.FindNearest (3D Euclidean, equivalent to ezQuake's
	// TP_LocationName). Stamping an integer index into each high-res record
	// is what plumbs that authoritative result through to the JS frontend.
	//
	// Names are sorted before indexing so the resulting table is
	// deterministic across runs (bucket.playerData is a Go map and would
	// otherwise be visited in random order, shuffling the indices on every
	// invocation and breaking byte-for-byte regression diffs).
	locTable := []string{""}
	locIndex := map[string]int{"": 0}
	if a.locFinder != nil {
		seen := make(map[string]struct{})
		for _, bucket := range a.buckets {
			for _, pData := range bucket.playerData {
				if pData.location == "" {
					continue
				}
				seen[pData.location] = struct{}{}
			}
		}
		names := make([]string, 0, len(seen))
		for name := range seen {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			locIndex[name] = len(locTable)
			locTable = append(locTable, name)
		}
	}
	// Drop the table entirely if only the sentinel slot exists — JSON
	// omitempty will then skip the field on the wire.
	if len(locTable) <= 1 {
		locTable = nil
	}

	// Export high-res buckets with per-player data and pre-computed team aggregations
	highResBuckets := a.exportHighResBuckets(slotToName, slotToTeam, locIndex)

	// Build name -> UserID mapping for Hub viewer links
	playerUserIDsByName := make(map[string]int)
	for slot, userID := range a.playerUserIDs {
		if userID > 0 {
			name := slotToName[slot]
			if name != "" {
				playerUserIDsByName[name] = userID
			}
		}
	}

	// Detect top 5 longest frag streaks for Key Moments
	fragStreaks := a.detectFragStreaks(10, nameToTeam, playerUserIDsByName)

	// Auto-detect control regions from loc data (stats computed client-side)
	var regionControl *RegionControlResult
	if a.locFinder != nil {
		regions := a.buildControlRegions()
		if len(regions) > 0 {
			regionControl = &RegionControlResult{Regions: regions}
		}
	}

	result := &TimelineAnalysisResult{
		HighResDuration: a.bucketDuration,
		MatchStartTime:  a.matchStartTime,
		HighResBuckets:  highResBuckets,
		FragEvents:      fragEvents,
		PowerupEvents:   powerupEvents,
		FragStreaks:      fragStreaks,
		LocationData:    locationData,
		LocTable:        locTable,
		PlayerUserIDs:   playerUserIDsByName,
		RegionControl:   regionControl,
	}

	return result, nil
}
