package analyzer

// Bucket export:
//
//   exportHighResBuckets   internal buckets → HighResBucket with per-player
//                          snapshots and pre-computed team aggregations.

// exportHighResBuckets converts internal buckets to the compact export format.
// Each bucket gets per-player data (position, vitals, weapons, powerups) and
// pre-computed team aggregations (weapon/powerup counts, total health/armor,
// armor-by-type distribution) so downstream consumers don't need to re-derive
// team-level stats.
//
// locIndex maps the already-resolved loc name (set in Finalize) to an integer
// index into TimelineAnalysisResult.LocTable. Records with no loc get Li = 0,
// which json:omitempty drops on the wire.
func (a *TimelineAnalyzer) exportHighResBuckets(slotToName map[int]string, slotToTeam map[int]string, locIndex map[string]int) []HighResBucket {
	result := make([]HighResBucket, 0, len(a.buckets))

	for _, b := range a.buckets {
		if len(b.playerData) == 0 {
			continue // Skip empty buckets
		}

		hb := HighResBucket{
			T: b.startTime,
			P: make(map[string]*HighResPlayerData),
		}

		// Build per-player data and accumulate team aggregations
		teamData := make(map[string]*HighResTeamData)

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
				Z:       pd.pos.z,
				H:       pd.vitals.health,
				A:       pd.vitals.armor,
				AT:      pd.vitals.armorType,
				RL:      pd.weapons.rl,
				LG:      pd.weapons.lg,
				GL:      pd.weapons.gl,
				SSG:     pd.weapons.ssg,
				SNG:     pd.weapons.sng,
				Q:       pd.powerups.quad,
				Pent:    pd.powerups.pent,
				R:       pd.powerups.ring,
				Shells:  pd.ammo.shells,
				Nails:   pd.ammo.nails,
				Rockets: pd.ammo.rockets,
				Cells:   pd.ammo.cells,
				D:       pd.dead,
				Sp:      pd.spawn,
				Li:      locIndex[pd.location], // 0 (omitted) when no loc
			}

			// Accumulate team aggregations
			team := slotToTeam[slot]
			if team == "" {
				team = pd.team
			}
			if team == "" {
				continue
			}

			td := teamData[team]
			if td == nil {
				td = &HighResTeamData{}
				teamData[team] = td
			}

			// Weapon categories (mutually exclusive RL/LG/RLLG split)
			switch {
			case pd.weapons.rl && pd.weapons.lg:
				td.RLLG++
				td.W++
			case pd.weapons.rl:
				td.RL++
				td.W++
			case pd.weapons.lg:
				td.LG++
				td.W++
			}

			// GL is an independent axis from the RL/LG categorisation.
			if pd.weapons.gl {
				td.GL++
			}

			// Powerups
			if pd.powerups.quad {
				td.Q++
				td.Pw++
			}
			if pd.powerups.pent {
				td.Pe++
				td.Pw++
			}
			if pd.powerups.ring {
				td.R++
				td.Pw++
			}

			// Health/Armor totals
			td.TH += pd.vitals.health
			td.TA += pd.vitals.armor
			if pd.vitals.armorType != "" {
				if td.ABT == nil {
					td.ABT = make(map[string]int)
				}
				td.ABT[pd.vitals.armorType]++
			}
		}

		if len(hb.P) > 0 {
			if len(teamData) > 0 {
				hb.TD = teamData
			}
			result = append(result, hb)
		}
	}

	return result
}
