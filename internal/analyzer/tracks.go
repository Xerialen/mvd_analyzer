package analyzer

// PlayerTrack contains all movement data for a single player
type PlayerTrack struct {
	Team  string      `json:"team"`
	Lives []LifeTrack `json:"lives"`
}

// LifeTrack represents one life (spawn to death) for a player
type LifeTrack struct {
	SpawnTime float64         `json:"spawnTime"`
	DeathTime float64         `json:"deathTime,omitempty"` // omit if still alive at match end
	Positions []TrackPosition `json:"positions"`
}

// TrackPosition is a single position sample
type TrackPosition struct {
	Time     float64 `json:"time"`
	Location string  `json:"location"`
}

// TracksResult is the top-level structure for track export
type TracksResult struct {
	Map     string                  `json:"map"`
	Players map[string]*PlayerTrack `json:"players"`
}

// ExtractTracks processes timeline data to extract per-player movement tracks segmented by lives
func ExtractTracks(result *Result) *TracksResult {
	if result.TimelineAnalysis == nil {
		return nil
	}

	timeline := result.TimelineAnalysis

	// Get map name from demoInfo if available
	mapName := ""
	if result.DemoInfo != nil {
		mapName = result.DemoInfo.Map
	}

	tracks := &TracksResult{
		Map:     mapName,
		Players: make(map[string]*PlayerTrack),
	}

	// Build team lookup from demoInfo
	teamByName := make(map[string]string)
	if result.DemoInfo != nil {
		for _, p := range result.DemoInfo.Players {
			if p.Name != "" && p.Team != "" {
				teamByName[p.Name] = p.Team
			}
		}
	}

	// Track state for each player: whether they're alive and current life
	type playerState struct {
		alive       bool
		currentLife *LifeTrack
		team        string
	}
	states := make(map[string]*playerState)

	// Resolve loc name from index
	resolveLoc := func(li int) string {
		if li > 0 && li < len(timeline.LocTable) {
			return timeline.LocTable[li]
		}
		return ""
	}

	// Process high-res buckets chronologically
	for _, bucket := range timeline.HighResBuckets {
		bucketTime := bucket.T

		// Track which players we see in this bucket
		seenPlayers := make(map[string]bool)

		for playerName, pData := range bucket.P {
			seenPlayers[playerName] = true

			state, exists := states[playerName]
			if !exists {
				team := teamByName[playerName]
				state = &playerState{alive: false, team: team}
				states[playerName] = state
			}

			// Player is alive if they have health > 0
			isAlive := pData.H > 0

			if isAlive && !state.alive {
				// Player just spawned - start a new life
				state.alive = true
				state.currentLife = &LifeTrack{
					SpawnTime: bucketTime,
					Positions: []TrackPosition{},
				}
			}

			if isAlive && state.currentLife != nil {
				// Record position only when location changes
				locName := resolveLoc(pData.Li)
				if locName != "" {
					positions := state.currentLife.Positions
					// Only add if location is different from the last recorded position
					if len(positions) == 0 || positions[len(positions)-1].Location != locName {
						state.currentLife.Positions = append(state.currentLife.Positions, TrackPosition{
							Time:     bucketTime,
							Location: locName,
						})
					}
				}
			}

			if !isAlive && state.alive {
				// Player just died - finalize the life
				state.alive = false
				if state.currentLife != nil {
					state.currentLife.DeathTime = bucketTime
					// Add to player's track
					if _, ok := tracks.Players[playerName]; !ok {
						tracks.Players[playerName] = &PlayerTrack{
							Team:  state.team,
							Lives: []LifeTrack{},
						}
					}
					tracks.Players[playerName].Lives = append(tracks.Players[playerName].Lives, *state.currentLife)
					state.currentLife = nil
				}
			}
		}

		// Check for players who were alive but are no longer in this bucket (died)
		for playerName, state := range states {
			if state.alive && !seenPlayers[playerName] {
				// Player disappeared - they died
				state.alive = false
				if state.currentLife != nil {
					state.currentLife.DeathTime = bucketTime
					if _, ok := tracks.Players[playerName]; !ok {
						tracks.Players[playerName] = &PlayerTrack{
							Team:  state.team,
							Lives: []LifeTrack{},
						}
					}
					tracks.Players[playerName].Lives = append(tracks.Players[playerName].Lives, *state.currentLife)
					state.currentLife = nil
				}
			}
		}
	}

	// Finalize any lives that are still ongoing (player alive at match end)
	for playerName, state := range states {
		if state.alive && state.currentLife != nil && len(state.currentLife.Positions) > 0 {
			// Don't set DeathTime - omitempty will exclude it
			if _, ok := tracks.Players[playerName]; !ok {
				tracks.Players[playerName] = &PlayerTrack{
					Team:  state.team,
					Lives: []LifeTrack{},
				}
			}
			tracks.Players[playerName].Lives = append(tracks.Players[playerName].Lives, *state.currentLife)
		}
	}

	return tracks
}
