package analyzer

import (
	"math"

	"github.com/mvd-analyzer/internal/mvd"
	"github.com/mvd-analyzer/internal/parser"
)

// WeaponStatsAnalyzer tracks weapon usage statistics
type WeaponStatsAnalyzer struct {
	ctx         *Context
	playerStats map[int]*playerWeaponStats
}

type playerWeaponStats struct {
	// Current state for tracking
	activeWeapon int
	shells       int
	nails        int
	rockets      int
	cells        int
	health       int     // Current health for damage capping
	armor        int     // Current armor value
	armorType    float64 // Armor absorption rate (0.3=green, 0.6=yellow, 0.8=red)
	lastRespawn  float64 // Time of last respawn (for filtering false shots)

	// Accumulated stats per weapon
	weapons map[string]*weaponData

	// Environmental damage received
	lavaDamage    int
	slimeDamage   int
	drownDamage   int
	fallDamage    int
	squishDamage  int // World-attributed squish only
	triggerDamage int
}

type weaponData struct {
	Shots      int // Tracked via ammo decreases
	Hits       int // Tracked via damage events (non-splash direct hits)
	Damage     int // Effective damage dealt to enemies (capped at target health)
	Overkill   int // Damage beyond what was needed to kill (raw - effective)
	TeamDamage int // Damage dealt to teammates
	SelfDamage int // Self-inflicted damage
}

// NewWeaponStatsAnalyzer creates a new weapon stats analyzer
func NewWeaponStatsAnalyzer() *WeaponStatsAnalyzer {
	return &WeaponStatsAnalyzer{
		playerStats: make(map[int]*playerWeaponStats),
	}
}

func (a *WeaponStatsAnalyzer) Name() string { return "weaponstats" }

func (a *WeaponStatsAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

func (a *WeaponStatsAnalyzer) OnEvent(event parser.Event) error {
	switch e := event.(type) {
	case *parser.StatUpdateEvent:
		return a.handleStatUpdate(e)
	case *parser.DamageEvent:
		return a.handleDamage(e)
	}
	return nil
}

func (a *WeaponStatsAnalyzer) handleStatUpdate(e *parser.StatUpdateEvent) error {
	stats := a.getOrCreate(e.PlayerNum)

	switch e.StatIndex {
	case mvd.StatHealth:
		// Detect respawn: health jumps from <= 0 to >= 100
		// Or health suddenly becomes exactly 100 (common respawn value)
		if stats.health <= 0 && e.Value >= 100 {
			stats.lastRespawn = e.Time
		}
		stats.health = e.Value

	case mvd.StatArmor:
		stats.armor = e.Value

	case mvd.StatItems:
		// Determine armor type from item flags
		// IT_ARMOR3 (red) = 80% absorption
		// IT_ARMOR2 (yellow) = 60% absorption
		// IT_ARMOR1 (green) = 30% absorption
		if e.Value&mvd.ITArmor3 != 0 {
			stats.armorType = 0.8
		} else if e.Value&mvd.ITArmor2 != 0 {
			stats.armorType = 0.6
		} else if e.Value&mvd.ITArmor1 != 0 {
			stats.armorType = 0.3
		} else {
			stats.armorType = 0 // No armor
		}

	case mvd.StatActiveWeapon:
		stats.activeWeapon = e.Value

	case mvd.StatShells:
		if stats.shells > 0 && e.Value < stats.shells && e.Value >= 0 {
			decrease := stats.shells - e.Value
			// Skip if within respawn grace period (0.5 seconds)
			isRespawnWindow := e.Time-stats.lastRespawn < 0.5
			// Skip respawn resets: decrease to spawn default (25) with large decrease
			isRespawnReset := e.Value == 25 && decrease > 10

			if isRespawnWindow || isRespawnReset {
				// Skip - this is a respawn reset, not actual shooting
			} else if a.isShellWeapon(stats.activeWeapon) {
				// Only count as shots if active weapon uses shells
				weapon := a.getShellWeapon(stats.activeWeapon)
				ammoPerShot := 1
				if stats.activeWeapon == mvd.ITSuperShotgun {
					ammoPerShot = 2
				}
				shots := decrease / ammoPerShot
				if shots > 0 {
					a.recordShot(stats, weapon, shots)
				}
			}
		}
		stats.shells = e.Value

	case mvd.StatNails:
		if stats.nails > 0 && e.Value < stats.nails && e.Value >= 0 {
			decrease := stats.nails - e.Value
			// Skip if within respawn grace period (0.5 seconds)
			isRespawnWindow := e.Time-stats.lastRespawn < 0.5
			// Skip respawn resets: decrease to spawn default (0) with large decrease
			isRespawnReset := e.Value == 0 && decrease > 10

			if isRespawnWindow || isRespawnReset {
				// Skip - this is a respawn reset, not actual shooting
			} else if a.isNailWeapon(stats.activeWeapon) {
				// Only count as shots if active weapon uses nails
				weapon := a.getNailWeapon(stats.activeWeapon)
				ammoPerShot := 1
				if stats.activeWeapon == mvd.ITSuperNailgun {
					ammoPerShot = 2
				}
				shots := decrease / ammoPerShot
				if shots > 0 {
					a.recordShot(stats, weapon, shots)
				}
			}
		}
		stats.nails = e.Value

	case mvd.StatRockets:
		if stats.rockets > 0 && e.Value < stats.rockets && e.Value >= 0 {
			decrease := stats.rockets - e.Value
			// Skip if within respawn grace period (0.5 seconds)
			isRespawnWindow := e.Time-stats.lastRespawn < 0.5
			// Skip respawn resets: decrease to spawn default (0) with large decrease
			isRespawnReset := e.Value == 0 && decrease > 5

			if isRespawnWindow || isRespawnReset {
				// Skip - this is a respawn reset, not actual shooting
			} else if a.isRocketWeapon(stats.activeWeapon) {
				// Only count as shots if active weapon uses rockets
				weapon := a.getRocketWeapon(stats.activeWeapon)
				a.recordShot(stats, weapon, decrease) // 1 rocket per shot for both GL and RL
			}
		}
		stats.rockets = e.Value

	case mvd.StatCells:
		if stats.cells > 0 && e.Value < stats.cells && e.Value >= 0 {
			decrease := stats.cells - e.Value
			// Skip if within respawn grace period (0.5 seconds)
			isRespawnWindow := e.Time-stats.lastRespawn < 0.5
			// Skip respawn resets: decrease to spawn default (0) with large decrease
			isRespawnReset := e.Value == 0 && decrease > 5

			if isRespawnWindow || isRespawnReset {
				// Skip - this is a respawn reset, not actual shooting
			} else if a.isLGWeapon(stats.activeWeapon) {
				// Only count if active weapon is LG
				a.recordShot(stats, "lg", decrease) // 1 cell per LG "tick"
			}
		}
		stats.cells = e.Value
	}

	return nil
}

func (a *WeaponStatsAnalyzer) handleDamage(e *parser.DamageEvent) error {
	weapon := mvd.DeathTypeToWeapon(e.DeathType)

	// Handle environmental damage (victim tracking only)
	if mvd.IsEnvironmentalDamage(e.DeathType) {
		if e.Victim >= 0 && e.Victim < mvd.MaxClients {
			victim := a.getOrCreate(e.Victim)
			effectiveDmg, _ := a.calculateEffectiveDamage(e.Damage, victim)

			envType := mvd.EnvironmentalDamageType(e.DeathType)
			switch envType {
			case "lava":
				victim.lavaDamage += effectiveDmg
			case "slime":
				victim.slimeDamage += effectiveDmg
			case "drown":
				victim.drownDamage += effectiveDmg
			case "fall":
				victim.fallDamage += effectiveDmg
			case "trigger":
				victim.triggerDamage += effectiveDmg
			case "squish":
				// World-attributed squish - track as environmental
				victim.squishDamage += effectiveDmg
			}

			// Update victim state
			a.applyDamageToVictim(e.Damage, victim)
		}
		return nil
	}

	// For player-attributed damage, we need a valid weapon
	if weapon == "unknown" {
		return nil
	}

	// Track attacker's damage output
	if e.Attacker >= 0 && e.Attacker < mvd.MaxClients {
		attacker := a.getOrCreate(e.Attacker)
		wd := attacker.getWeapon(weapon)

		if e.Attacker == e.Victim {
			// Self-damage - use the same armor+health calculation
			victim := a.getOrCreate(e.Victim)
			effectiveDmg, _ := a.calculateEffectiveDamage(e.Damage, victim)
			wd.SelfDamage += effectiveDmg

			// Update victim state
			a.applyDamageToVictim(e.Damage, victim)
		} else {
			// Get victim's state for damage calculation
			victim := a.getOrCreate(e.Victim)

			// Calculate damage exactly like KTX does:
			// dmg_dealt = armor_absorbed + min(health_damage, victim_health)
			effectiveDamage, overkill := a.calculateEffectiveDamage(e.Damage, victim)

			// Check if team damage
			isTeamDamage := a.isTeamDamage(e.Attacker, e.Victim)
			if isTeamDamage {
				wd.TeamDamage += effectiveDamage
			} else {
				wd.Damage += effectiveDamage
				wd.Overkill += overkill
				// Count hits from direct damage (not splash) to enemies only
				if !e.IsSplash {
					wd.Hits++
				}
			}

			// Update victim's armor and health after damage
			a.applyDamageToVictim(e.Damage, victim)
		}
	}

	return nil
}

// calculateEffectiveDamage computes damage exactly like KTX:
// MVD damage = raw_damage (before armor split, after quad/handicap)
// armor_absorbed = ceil(armorType * raw_damage), capped at armor_value
// health_damage = ceil(raw_damage - armor_absorbed)
// effective_damage = armor_absorbed + min(health_damage, victim_health)
func (a *WeaponStatsAnalyzer) calculateEffectiveDamage(rawDamage int, victim *playerWeaponStats) (effective, overkill int) {
	// Calculate armor absorption using ceiling (like KTX's newceil)
	var armorAbsorbed int
	if victim.armor > 0 && victim.armorType > 0 {
		// Armor absorbs ceil(armorType * damage), capped at current armor value
		absorbed := int(math.Ceil(float64(rawDamage) * victim.armorType))
		if absorbed > victim.armor {
			absorbed = victim.armor
		}
		armorAbsorbed = absorbed
	}

	// Health damage is what's left after armor (using ceiling like KTX)
	healthDamage := int(math.Ceil(float64(rawDamage) - float64(armorAbsorbed)))
	if healthDamage < 0 {
		healthDamage = 0
	}

	// Cap health damage at victim's current health
	effectiveHealthDamage := healthDamage
	if victim.health > 0 && effectiveHealthDamage > victim.health {
		effectiveHealthDamage = victim.health
	} else if victim.health <= 0 {
		effectiveHealthDamage = 0
	}

	// Overkill is the health damage beyond what was needed
	overkill = healthDamage - effectiveHealthDamage

	// Total effective damage = armor absorbed + capped health damage
	effective = armorAbsorbed + effectiveHealthDamage

	return effective, overkill
}

// applyDamageToVictim updates victim's armor and health after taking damage
func (a *WeaponStatsAnalyzer) applyDamageToVictim(rawDamage int, victim *playerWeaponStats) {
	// Calculate armor absorption using ceiling (like KTX)
	var armorAbsorbed int
	if victim.armor > 0 && victim.armorType > 0 {
		absorbed := int(math.Ceil(float64(rawDamage) * victim.armorType))
		if absorbed > victim.armor {
			absorbed = victim.armor
		}
		armorAbsorbed = absorbed
		victim.armor -= armorAbsorbed
	}

	// Apply health damage using ceiling (like KTX)
	healthDamage := int(math.Ceil(float64(rawDamage) - float64(armorAbsorbed)))
	if victim.health > 0 {
		victim.health -= healthDamage
		if victim.health < 0 {
			victim.health = 0
		}
	}
}

// isTeamDamage checks if attacker and victim are on the same team
func (a *WeaponStatsAnalyzer) isTeamDamage(attackerNum, victimNum int) bool {
	if a.ctx == nil {
		return false
	}
	attacker := a.ctx.Players[attackerNum]
	victim := a.ctx.Players[victimNum]
	if attacker == nil || victim == nil {
		return false
	}
	// Same team if team names match and are non-empty
	return attacker.Team != "" && attacker.Team == victim.Team
}

// recordShot records shots fired for a weapon
func (a *WeaponStatsAnalyzer) recordShot(stats *playerWeaponStats, weapon string, count int) {
	if weapon == "" {
		return
	}
	wd := stats.getWeapon(weapon)
	wd.Shots += count
}

// isShellWeapon checks if active weapon uses shells
func (a *WeaponStatsAnalyzer) isShellWeapon(activeWeapon int) bool {
	return activeWeapon == mvd.ITShotgun || activeWeapon == mvd.ITSuperShotgun
}

// isNailWeapon checks if active weapon uses nails
func (a *WeaponStatsAnalyzer) isNailWeapon(activeWeapon int) bool {
	return activeWeapon == mvd.ITNailgun || activeWeapon == mvd.ITSuperNailgun
}

// isRocketWeapon checks if active weapon uses rockets
func (a *WeaponStatsAnalyzer) isRocketWeapon(activeWeapon int) bool {
	return activeWeapon == mvd.ITGrenadeLauncher || activeWeapon == mvd.ITRocketLauncher
}

// isLGWeapon checks if active weapon uses cells
func (a *WeaponStatsAnalyzer) isLGWeapon(activeWeapon int) bool {
	return activeWeapon == mvd.ITLightning || activeWeapon == mvd.ITSuperLightning
}

// getShellWeapon determines if SG or SSG based on active weapon
func (a *WeaponStatsAnalyzer) getShellWeapon(activeWeapon int) string {
	switch activeWeapon {
	case mvd.ITSuperShotgun:
		return "ssg"
	case mvd.ITShotgun:
		return "sg"
	default:
		// If weapon doesn't match, guess based on common usage
		// SSG is more commonly used
		if activeWeapon&mvd.ITSuperShotgun != 0 {
			return "ssg"
		}
		return "sg"
	}
}

// getNailWeapon determines if NG or SNG based on active weapon
func (a *WeaponStatsAnalyzer) getNailWeapon(activeWeapon int) string {
	switch activeWeapon {
	case mvd.ITSuperNailgun:
		return "sng"
	case mvd.ITNailgun:
		return "ng"
	default:
		if activeWeapon&mvd.ITSuperNailgun != 0 {
			return "sng"
		}
		return "ng"
	}
}

// getRocketWeapon determines if GL or RL based on active weapon
func (a *WeaponStatsAnalyzer) getRocketWeapon(activeWeapon int) string {
	switch activeWeapon {
	case mvd.ITRocketLauncher:
		return "rl"
	case mvd.ITGrenadeLauncher:
		return "gl"
	default:
		if activeWeapon&mvd.ITRocketLauncher != 0 {
			return "rl"
		}
		return "gl"
	}
}

func (a *WeaponStatsAnalyzer) Finalize() (interface{}, error) {
	result := &WeaponStatsResult{
		PlayerStats: make(map[string]*PlayerWeaponStatsEntry),
	}

	for playerNum, stats := range a.playerStats {
		if a.ctx.Players[playerNum] != nil {
			name := a.ctx.Players[playerNum].Name
			if name != "" && (len(stats.weapons) > 0 || stats.hasEnvironmentalDamage()) {
				entry := &PlayerWeaponStatsEntry{
					Weapons: make(map[string]*WeaponStatEntry),
				}
				for weapon, wd := range stats.weapons {
					entry.Weapons[weapon] = &WeaponStatEntry{
						Shots:      wd.Shots,
						Hits:       wd.Hits,
						Damage:     wd.Damage,
						Overkill:   wd.Overkill,
						TeamDamage: wd.TeamDamage,
						SelfDamage: wd.SelfDamage,
						Accuracy:   calculateAccuracy(wd.Shots, wd.Hits),
					}
				}

				// Add environmental damage if any
				if stats.hasEnvironmentalDamage() {
					entry.Environment = &EnvironmentalDamage{
						Lava:    stats.lavaDamage,
						Slime:   stats.slimeDamage,
						Drown:   stats.drownDamage,
						Fall:    stats.fallDamage,
						Squish:  stats.squishDamage,
						Trigger: stats.triggerDamage,
					}
				}

				result.PlayerStats[name] = entry
			}
		}
	}

	return result, nil
}

// hasEnvironmentalDamage returns true if the player has any environmental damage
func (s *playerWeaponStats) hasEnvironmentalDamage() bool {
	return s.lavaDamage > 0 || s.slimeDamage > 0 || s.drownDamage > 0 ||
		s.fallDamage > 0 || s.squishDamage > 0 || s.triggerDamage > 0
}

func calculateAccuracy(shots, hits int) float64 {
	if shots == 0 {
		return 0
	}
	return float64(hits) / float64(shots) * 100
}

func (a *WeaponStatsAnalyzer) getOrCreate(playerNum int) *playerWeaponStats {
	if s, ok := a.playerStats[playerNum]; ok {
		return s
	}
	s := &playerWeaponStats{
		weapons: make(map[string]*weaponData),
	}
	a.playerStats[playerNum] = s
	return s
}

func (s *playerWeaponStats) getWeapon(name string) *weaponData {
	if wd, ok := s.weapons[name]; ok {
		return wd
	}
	wd := &weaponData{}
	s.weapons[name] = wd
	return wd
}
