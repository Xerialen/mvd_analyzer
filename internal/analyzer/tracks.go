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

	// Track state for each player: whether they're alive and current life
	type playerState struct {
		alive       bool
		currentLife *LifeTrack
		team        string
	}
	states := make(map[string]*playerState)

	// Process buckets chronologically
	for _, bucket := range timeline.Buckets {
		bucketTime := bucket.StartTime

		// Track which players we see in this bucket
		seenPlayers := make(map[string]bool)

		for playerName, pData := range bucket.PlayerData {
			seenPlayers[playerName] = true

			state, exists := states[playerName]
			if !exists {
				state = &playerState{alive: false, team: pData.Team}
				states[playerName] = state
			}

			// Player is alive if they have health > 0
			isAlive := pData.Health > 0

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
				if pData.Location != "" {
					positions := state.currentLife.Positions
					// Only add if location is different from the last recorded position
					if len(positions) == 0 || positions[len(positions)-1].Location != pData.Location {
						state.currentLife.Positions = append(state.currentLife.Positions, TrackPosition{
							Time:     bucketTime,
							Location: pData.Location,
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
