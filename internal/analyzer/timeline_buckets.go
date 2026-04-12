package analyzer

// Bucket export and aggregation:
//
//   exportHighResBuckets       50 ms buckets → HighResBucket (map view)
//   aggregateToGraphBuckets    1 s windows → TimelineBucket  (graphs)
//   aggregateWindow            one window → one TimelineBucket
//
// playerWindowAggregate / teamAggregator are the scratch types used while
// folding a window of high-res buckets into one graph bucket.

// exportHighResBuckets converts internal buckets to compact export format for map.
// locIndex maps the already-resolved loc name (set in Finalize) to an integer
// index into TimelineAnalysisResult.LocTable. Records with no loc get Li = 0,
// which json:omitempty drops on the wire.
func (a *TimelineAnalyzer) exportHighResBuckets(slotToName map[int]string, locIndex map[string]int) []HighResBucket {
	result := make([]HighResBucket, 0, len(a.buckets))

	for _, b := range a.buckets {
		if len(b.playerData) == 0 {
			continue // Skip empty buckets
		}

		hb := HighResBucket{
			T: b.startTime,
			P: make(map[string]*HighResPlayerData),
		}

		for slot, pd := range b.playerData {
			name := slotToName[slot]
			if name == "" {
				name = pd.name // Fallback to stored name
			}
			if name == "" {
				continue
			}

			hb.P[name] = &HighResPlayerData{
				X:       pd.pos.x,
				Y:       pd.pos.y,
				H:       pd.vitals.health,
				A:       pd.vitals.armor,
				AT:      pd.vitals.armorType,
				RL:      pd.weapons.rl,
				LG:      pd.weapons.lg,
				SSG:     pd.weapons.ssg,
				SNG:     pd.weapons.sng,
				Q:       pd.powerups.quad,
				Pent:    pd.powerups.pent,
				R:       pd.powerups.ring,
				Rockets: pd.ammo.rockets,
				Cells:   pd.ammo.cells,
				D:       pd.dead,
				Sp:      pd.spawn,
				Li:      locIndex[pd.location], // 0 (omitted) when no loc
			}
		}

		if len(hb.P) > 0 {
			result = append(result, hb)
		}
	}

	return result
}

// aggregateToGraphBuckets groups high-res buckets into 1s buckets for graphs
func (a *TimelineAnalyzer) aggregateToGraphBuckets(slotToName map[int]string, slotToTeam map[int]string) []TimelineBucket {
	if len(a.buckets) == 0 {
		return nil
	}

	// Calculate how many high-res buckets per graph bucket
	bucketsPerSecond := int(a.graphBucketDuration / a.bucketDuration)
	if bucketsPerSecond < 1 {
		bucketsPerSecond = 1
	}

	// Calculate number of graph buckets needed
	numGraphBuckets := (len(a.buckets) + bucketsPerSecond - 1) / bucketsPerSecond
	graphBuckets := make([]TimelineBucket, 0, numGraphBuckets)

	for i := 0; i < len(a.buckets); i += bucketsPerSecond {
		end := i + bucketsPerSecond
		if end > len(a.buckets) {
			end = len(a.buckets)
		}

		// Aggregate this window
		graphBucket := a.aggregateWindow(a.buckets[i:end], slotToName, slotToTeam)
		graphBuckets = append(graphBuckets, graphBucket)
	}

	return graphBuckets
}

// aggregateWindow aggregates a slice of high-res buckets into a single graph bucket
func (a *TimelineAnalyzer) aggregateWindow(buckets []*timelineBucketData, slotToName map[int]string, slotToTeam map[int]string) TimelineBucket {
	if len(buckets) == 0 {
		return TimelineBucket{
			PlayerData: make(map[string]*PlayerBucketData),
			TeamData:   make(map[string]*TeamBucketData),
		}
	}

	result := TimelineBucket{
		StartTime:  buckets[0].startTime,
		EndTime:    buckets[len(buckets)-1].endTime,
		PlayerData: make(map[string]*PlayerBucketData),
		TeamData:   make(map[string]*TeamBucketData),
	}

	// Track per-player aggregates across the window
	// For weapons/powerups: take MAX (peak control)
	// For health/armor: take LAST value (current state)
	playerAggregates := make(map[string]*playerWindowAggregate)

	for _, b := range buckets {
		for slot, pRaw := range b.playerData {
			name := slotToName[slot]
			if name == "" {
				name = pRaw.name
			}
			if name == "" {
				continue
			}

			team := pRaw.team
			if team == "" {
				team = slotToTeam[slot]
			}

			if playerAggregates[name] == nil {
				playerAggregates[name] = &playerWindowAggregate{team: team}
			}
			agg := playerAggregates[name]

			// Weapons/powerups OR-fold across the window — peak control.
			agg.weapons.orInPlace(pRaw.weapons)
			agg.powerups.orInPlace(pRaw.powerups)

			// Vitals/ammo/position take the last sample in the window.
			agg.vitals = pRaw.vitals
			agg.ammo = pRaw.ammo
			agg.pos = pRaw.pos
			agg.location = pRaw.location
		}
	}

	// Build PlayerData from aggregates
	teamAggregates := make(map[string]*teamAggregator)

	for name, agg := range playerAggregates {
		result.PlayerData[name] = &PlayerBucketData{
			Team:      agg.team,
			HasRL:     agg.weapons.rl,
			HasLG:     agg.weapons.lg,
			HasQuad:   agg.powerups.quad,
			HasPent:   agg.powerups.pent,
			HasRing:   agg.powerups.ring,
			Health:    agg.vitals.health,
			Armor:     agg.vitals.armor,
			ArmorType: agg.vitals.armorType,
			Shells:    agg.ammo.shells,
			Nails:     agg.ammo.nails,
			Rockets:   agg.ammo.rockets,
			Cells:     agg.ammo.cells,
			X:         agg.pos.x,
			Y:         agg.pos.y,
			Z:         agg.pos.z,
			Location:  agg.location,
		}

		// Aggregate for team stats
		team := agg.team
		if team != "" {
			if teamAggregates[team] == nil {
				teamAggregates[team] = &teamAggregator{
					armorByType: make(map[string]int),
				}
			}
			ta := teamAggregates[team]

			// Weapons
			switch {
			case agg.weapons.rl && agg.weapons.lg:
				ta.playersWithRLLG++
				ta.playersWithWeapons++
			case agg.weapons.rl:
				ta.playersWithRL++
				ta.playersWithWeapons++
			case agg.weapons.lg:
				ta.playersWithLG++
				ta.playersWithWeapons++
			}

			// Powerups
			if agg.powerups.quad {
				ta.playersWithQuad++
				ta.playersWithPowerups++
			}
			if agg.powerups.pent {
				ta.playersWithPent++
				ta.playersWithPowerups++
			}
			if agg.powerups.ring {
				ta.playersWithRing++
				ta.playersWithPowerups++
			}

			// Health/Armor
			ta.healthSamples = append(ta.healthSamples, agg.vitals.health)
			ta.armorSamples = append(ta.armorSamples, agg.vitals.armor)
			if agg.vitals.armorType != "" {
				ta.armorByType[agg.vitals.armorType]++
			}

			// Ammo
			ta.totalShells += agg.ammo.shells
			ta.totalNails += agg.ammo.nails
			ta.totalRockets += agg.ammo.rockets
			ta.totalCells += agg.ammo.cells
		}
	}

	// Build TeamData from aggregates
	for team, ta := range teamAggregates {
		result.TeamData[team] = &TeamBucketData{
			PlayersWithRL:       ta.playersWithRL,
			PlayersWithLG:       ta.playersWithLG,
			PlayersWithRLLG:     ta.playersWithRLLG,
			PlayersWithWeapons:  ta.playersWithWeapons,
			PlayersWithQuad:     ta.playersWithQuad,
			PlayersWithPent:     ta.playersWithPent,
			PlayersWithRing:     ta.playersWithRing,
			PlayersWithPowerups: ta.playersWithPowerups,
			AvgHealth:           average(ta.healthSamples),
			AvgArmor:            average(ta.armorSamples),
			TotalHealth:         sum(ta.healthSamples),
			TotalArmor:          sum(ta.armorSamples),
			ArmorByType:         ta.armorByType,
			TotalShells:         ta.totalShells,
			TotalNails:          ta.totalNails,
			TotalRockets:        ta.totalRockets,
			TotalCells:          ta.totalCells,
		}
	}

	return result
}

// playerWindowAggregate holds per-player data during window aggregation.
// Weapons/powerups OR-fold across the window (peak control), the rest take
// the last sample. The substruct shapes match playerBucketRawData so a
// whole group can be copied with a single assignment.
type playerWindowAggregate struct {
	team     string
	weapons  weaponLoadout
	powerups powerupLoadout
	vitals   vitals
	ammo     ammoCounts
	pos      playerPosition
	location string
}

// teamAggregator is used during finalization to aggregate player data into team data
type teamAggregator struct {
	playersWithRL       int
	playersWithLG       int
	playersWithRLLG     int
	playersWithWeapons  int
	playersWithQuad     int
	playersWithPent     int
	playersWithRing     int
	playersWithPowerups int
	healthSamples       []int
	armorSamples        []int
	armorByType         map[string]int
	totalShells         int
	totalNails          int
	totalRockets        int
	totalCells          int
}

// average calculates the average of a slice of ints
func average(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	s := 0
	for _, v := range values {
		s += v
	}
	return float64(s) / float64(len(values))
}

// sum calculates the sum of a slice of ints
func sum(values []int) int {
	s := 0
	for _, v := range values {
		s += v
	}
	return s
}
