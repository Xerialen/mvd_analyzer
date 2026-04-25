package analyzer

import "sort"

// detectFragStreaks computes the top N longest frag streaks (spawn-to-death runs)
// ranked by number of frags. Each run starts at spawn and ends at death.
// Effective weapon (ewep) is the weapon with the most kills during the run.
func (a *TimelineAnalyzer) detectFragStreaks(topN int, names *NameTable, playerUserIDsByName map[string]int) []FragStreakEvent {
	resolved := a.ctx.ResolveSlotDemoInfo()

	// Helper: resolve slot to player name
	slotName := func(slot int) string {
		if di, ok := resolved[slot]; ok && di.Name != "" {
			return di.Name
		}
		if player := a.ctx.Players[slot]; player != nil {
			return player.Name
		}
		if n, ok := a.playerNames[slot]; ok {
			return n
		}
		return ""
	}

	// Build per-player sorted spawn and death time lists
	spawnsByName := make(map[string][]float64)
	deathsByName := make(map[string][]float64)

	for _, s := range a.rawSpawns {
		if name := slotName(s.PlayerNum); name != "" {
			spawnsByName[name] = append(spawnsByName[name], s.Time)
		}
	}
	for _, d := range a.rawDeaths {
		if name := slotName(d.PlayerNum); name != "" {
			deathsByName[name] = append(deathsByName[name], d.Time)
		}
	}
	for name := range spawnsByName {
		sort.Float64s(spawnsByName[name])
	}
	for name := range deathsByName {
		sort.Float64s(deathsByName[name])
	}

	// Build runs: pair each spawn with the next death
	type run struct {
		playerName string
		team       string
		spawnTime  float64
		deathTime  float64
	}
	var runs []run

	// Collect all player names
	allPlayers := make(map[string]bool)
	for name := range spawnsByName {
		allPlayers[name] = true
	}
	for name := range deathsByName {
		allPlayers[name] = true
	}

	for name := range allPlayers {
		spawns := spawnsByName[name]
		deaths := deathsByName[name]
		di := 0

		for _, spawnT := range spawns {
			// Find next death after this spawn
			for di < len(deaths) && deaths[di] <= spawnT {
				di++
			}
			deathT := 0.0
			if di < len(deaths) {
				deathT = deaths[di]
				di++
			} else {
				// No death found - run extends to end of match
				if len(a.buckets) > 0 {
					deathT = a.buckets[len(a.buckets)-1].endTime
				}
			}
			if deathT > spawnT {
				runs = append(runs, run{
					playerName: name,
					team:       names.TeamForName(name),
					spawnTime:  spawnT,
					deathTime:  deathT,
				})
			}
		}
	}

	// For each run, count frags and determine effective weapon using FragEntries
	fragEntries := a.coreFragEntries()
	var allStreaks []FragStreakEvent

	for _, r := range runs {
		frags := 0
		weaponCounts := make(map[string]int)

		for _, fe := range fragEntries {
			if fe.Killer != r.playerName {
				continue
			}
			if fe.Time < r.spawnTime || fe.Time > r.deathTime {
				continue
			}
			if fe.IsSuicide || fe.IsTeamKill {
				continue
			}
			frags++
			weaponCounts[fe.Weapon]++
		}

		if frags == 0 {
			continue
		}

		// Determine effective weapon (most kills). Iterate weapon names in
		// sorted order so ties resolve deterministically (Go map iteration
		// is randomized and would otherwise pick a different tied weapon
		// each run).
		weps := make([]string, 0, len(weaponCounts))
		for wep := range weaponCounts {
			weps = append(weps, wep)
		}
		sort.Strings(weps)
		ewep := ""
		maxWepKills := 0
		for _, wep := range weps {
			if weaponCounts[wep] > maxWepKills {
				maxWepKills = weaponCounts[wep]
				ewep = wep
			}
		}

		allStreaks = append(allStreaks, FragStreakEvent{
			Time:       r.spawnTime,
			EndTime:    r.deathTime,
			PlayerName: r.playerName,
			Team:       r.team,
			Frags:      frags,
			Duration:   r.deathTime - r.spawnTime,
			Ewep:       ewep,
		})
	}

	// Sort by frags descending
	sort.Slice(allStreaks, func(i, j int) bool {
		if allStreaks[i].Frags != allStreaks[j].Frags {
			return allStreaks[i].Frags > allStreaks[j].Frags
		}
		return allStreaks[i].Duration < allStreaks[j].Duration // Tie-break: shorter run = more impressive
	})

	// Set UserIDs
	for i := range allStreaks {
		if uid, ok := playerUserIDsByName[allStreaks[i].PlayerName]; ok {
			allStreaks[i].PlayerUserID = uid
		}
	}

	// Return top N
	if len(allStreaks) > topN {
		allStreaks = allStreaks[:topN]
	}

	return allStreaks
}
