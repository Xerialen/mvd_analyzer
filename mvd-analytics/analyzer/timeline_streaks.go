package analyzer

import (
	"math"
	"sort"
)

// detectFragStreaks computes the top N longest frag streaks (spawn-to-death runs)
// ranked by number of frags. Each run starts at spawn and ends at death.
// Effective weapon (ewep) is the weapon with the most kills during the run.
func (a *TimelineAnalyzer) detectFragStreaks(topN int, names *NameTable, playerUserIDsByName map[string]int) []FragStreakEvent {
	// Build per-player sorted spawn and death time lists, resolving each
	// spawn/death to the identity on the slot *at that time* so a streak
	// that straddles a reconnect stays one run under one player name.
	spawnsByName := make(map[string][]float64)
	deathsByName := make(map[string][]float64)

	for _, s := range a.rawSpawns {
		if name, _ := a.resolveAt(s.PlayerNum, msTime(s.Time)); name != "" {
			spawnsByName[name] = append(spawnsByName[name], s.Time)
		}
	}
	for _, d := range a.rawDeaths {
		if name, _ := a.resolveAt(d.PlayerNum, msTime(d.Time)); name != "" {
			deathsByName[name] = append(deathsByName[name], d.Time)
		}
	}
	for name := range spawnsByName {
		sort.Float64s(spawnsByName[name])
	}
	for name := range deathsByName {
		sort.Float64s(deathsByName[name])
	}

	// Earliest credited frag per killer, from the canonical frag log.
	// Serves two purposes below: evidence that a player was already
	// alive before their first *recorded* spawn (the synthetic
	// match-start spawn), and seeding allPlayers so a player who never
	// died — and therefore has no spawn or death records at all — still
	// gets a run. Mirrors the killEvents condition (no suicides/
	// teamkills, no generic killers) so obituary noise can't fabricate
	// a run for a non-player name.
	fragEntries := a.coreFragEntries()
	firstFragMs := make(map[string]int32)
	for _, fe := range fragEntries {
		if fe.IsSuicide || fe.IsTeamKill || isGenericPlayer(fe.Killer) {
			continue
		}
		if t, ok := firstFragMs[fe.Killer]; !ok || fe.Time < t {
			firstFragMs[fe.Killer] = fe.Time
		}
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
	for name := range firstFragMs {
		allPlayers[name] = true
	}

	for name := range allPlayers {
		spawns := spawnsByName[name]
		deaths := deathsByName[name]

		// A player already alive when the match starts has no spawn
		// recorded for that first life: the parser emits the initial
		// SpawnEvent during warmup (first alive playerinfo sample),
		// which handleSpawn drops as pre-match. Without a match-start
		// spawn the run from match start to first death — often a
		// player's longest — is never built, and a player who never
		// died has no runs at all (gameId 224758: reload's 33-frag
		// opening run). Synthesize the spawn whenever a death or a
		// credited frag predates the first recorded spawn.
		//
		// Strictly after match start, not at it: KTX's match-start
		// reset can surface as a DeathEvent at exactly StartTime
		// (gameId 212260: nlk). That death is the reset, not the end
		// of a real first life — synthesizing a spawn for it would
		// pair the synthetic spawn with the *next* death and shift
		// every later run of the player off by one life.
		if a.timing.Started {
			startMs := msTime(a.timing.StartTime)
			firstSpawnMs := int32(math.MaxInt32)
			if len(spawns) > 0 {
				firstSpawnMs = msTime(spawns[0])
			}
			evidenceMs := int32(math.MaxInt32)
			if len(deaths) > 0 {
				evidenceMs = msTime(deaths[0])
			}
			if t, ok := firstFragMs[name]; ok && t < evidenceMs {
				evidenceMs = t
			}
			if evidenceMs > startMs && evidenceMs < firstSpawnMs {
				spawns = append([]float64{a.timing.StartTime}, spawns...)
			}
		}

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
				// No death found - run extends to match end (from
				// the timing detector). 0 falls through to the
				// no-frags-skip below.
				deathT = a.timing.EndTime
			}
			if deathT > spawnT {
				runs = append(runs, run{
					playerName: name,
					team:       a.core.TeamFor(name, names.TeamForName(name)),
					spawnTime:  spawnT,
					deathTime:  deathT,
				})
			}
		}
	}

	// For each run, count frags and determine effective weapon using FragEntries
	var allStreaks []FragStreakEvent

	for _, r := range runs {
		frags := 0
		weaponCounts := make(map[string]int)

		spawnTimeMs := msTime(r.spawnTime)
		deathTimeMs := msTime(r.deathTime)
		for _, fe := range fragEntries {
			if fe.Killer != r.playerName {
				continue
			}
			// FragEntry.Time is int32 ms (schema v8); compare against
			// the same unit.
			if fe.Time < spawnTimeMs || fe.Time > deathTimeMs {
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
			Time:       spawnTimeMs,
			EndTime:    deathTimeMs,
			PlayerName: r.playerName,
			Team:       r.team,
			Frags:      frags,
			Duration:   deathTimeMs - spawnTimeMs,
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
