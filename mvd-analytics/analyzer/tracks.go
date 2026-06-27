package analyzer

// PlayerTrack contains all movement data for a single player.
type PlayerTrack struct {
	Team  string      `json:"team"`
	Lives []LifeTrack `json:"lives"`
}

// LifeTrack represents one life (spawn to death) for a player.
type LifeTrack struct {
	SpawnTime float64         `json:"spawnTime"`
	DeathTime float64         `json:"deathTime,omitempty"` // omit if still alive at match end
	Positions []TrackPosition `json:"positions"`
}

// TrackPosition is a single loc transition during a life.
type TrackPosition struct {
	Time     float64 `json:"time"`
	Location string  `json:"location"`
}

// TracksResult is the top-level structure for track export.
type TracksResult struct {
	Map     string                  `json:"map"`
	Players map[string]*PlayerTrack `json:"players"`
}

// ExtractTracks walks each player's PositionTrack and emits per-life
// movement tracks (spawn→death sequences of loc transitions).
//
// At schema v7 it operates on result.Streams natively: PositionTrack
// (with the Li column populated by the analyzer's loc resolution +
// blip filter) drives loc transitions; Spawns/Deaths timestamps drive
// life boundaries. No bucket intermediate.
//
// Currently shelved scaffolding for upcoming movement-pattern
// visualisations — the analyzer that wraps this is not registered
// in NewDefaultRegistry, so ExtractTracks has no production callers
// today. When the analyzer is revived, this implementation is the
// production-ready entry point.
//
// KNOWN GAP when revived: the loop below starts alive=false and opens a
// life only on a spawn, but KTX demos do NOT record the match-start spawn
// (the first recorded spawn is the first *respawn*, after the first death),
// so each player's opening life (match start → first death) is currently
// dropped. Treat the player as alive from match start (open an initial life
// at the first position sample when no spawn precedes the first death), the
// way view.playerActiveInWindow and analyzer.losAliveAt do. See the LOS
// liveness fix for the same root cause.
func ExtractTracks(result *Result) *TracksResult {
	if result == nil || result.TimelineAnalysis == nil || result.Streams == nil {
		return nil
	}
	timeline := result.TimelineAnalysis

	mapName := ""
	if result.DemoInfo != nil {
		mapName = result.DemoInfo.Map
	}

	tracks := &TracksResult{
		Map:     mapName,
		Players: make(map[string]*PlayerTrack),
	}

	teamByName := make(map[string]string)
	if result.DemoInfo != nil {
		for _, p := range result.DemoInfo.Players {
			if p.Name != "" && p.Team != "" {
				teamByName[p.Name] = p.Team
			}
		}
	}

	resolveLoc := func(li int16) string {
		if li > 0 && int(li) < len(timeline.LocTable) {
			return timeline.LocTable[li]
		}
		return ""
	}

	// matchEnd is float64 seconds on the public schema; convert once to
	// int32 ms for boundary walking. processBoundaries below works
	// entirely in ms — both pt.T and Spawns/Deaths are []int32 ms after
	// schema v8 so comparisons are exact.
	matchEndMs := int32(result.Streams.Global.MatchEnd * 1000)

	for _, p := range result.Streams.Players {
		pt := p.Position
		if pt == nil || len(pt.T) == 0 || len(pt.Li) != len(pt.T) {
			continue
		}

		spawns := p.Spawns
		deaths := p.Deaths

		var (
			sIdx, dIdx int
			alive      bool
			curLife    *LifeTrack
			lives      []LifeTrack
		)

		// processBoundaries advances the spawn/death cursors to time
		// `tMs` (inclusive), opening / closing lives along the way.
		// Spawn at exact tMs opens a life *before* the position sample
		// at tMs is evaluated; death at exact tMs closes it before — so
		// a sample that lands exactly on a death boundary is treated
		// as already-dead.
		processBoundaries := func(tMs int32) {
			for sIdx < len(spawns) || dIdx < len(deaths) {
				var nextMs int32
				isSpawn := false
				switch {
				case sIdx < len(spawns) && (dIdx >= len(deaths) || spawns[sIdx] <= deaths[dIdx]):
					nextMs = spawns[sIdx]
					isSpawn = true
				default:
					nextMs = deaths[dIdx]
				}
				if nextMs > tMs {
					return
				}
				if isSpawn {
					if !alive {
						alive = true
						curLife = &LifeTrack{SpawnTime: float64(nextMs) * 0.001}
					}
					sIdx++
				} else {
					if alive && curLife != nil {
						curLife.DeathTime = float64(nextMs) * 0.001
						lives = append(lives, *curLife)
						curLife = nil
					}
					alive = false
					dIdx++
				}
			}
		}

		for i := range pt.T {
			tMs := pt.T[i]
			processBoundaries(tMs)
			if !alive || curLife == nil {
				continue
			}
			locName := resolveLoc(pt.Li[i])
			if locName == "" {
				continue
			}
			n := len(curLife.Positions)
			if n == 0 || curLife.Positions[n-1].Location != locName {
				curLife.Positions = append(curLife.Positions, TrackPosition{
					Time: float64(tMs) * 0.001, Location: locName,
				})
			}
		}

		// Drain remaining boundaries past the last position sample
		// (e.g. a death at match end with no further positions).
		processBoundaries(matchEndMs)

		// Finalize any life still open at match end (player alive
		// when the demo cut). DeathTime stays zero — JSON omitempty
		// will drop it.
		if alive && curLife != nil && len(curLife.Positions) > 0 {
			lives = append(lives, *curLife)
		}

		if len(lives) > 0 {
			tracks.Players[p.Name] = &PlayerTrack{
				Team:  teamByName[p.Name],
				Lives: lives,
			}
		}
	}

	return tracks
}
