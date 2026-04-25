package analyzer

import (
	"strings"

	"github.com/mvd-analyzer/qwdemo/events"
)

// FragAnalyzer detects frags from print messages
type FragAnalyzer struct {
	ctx      *Context
	core     *CoreOutputs
	frags    []FragEntry
	byWeapon map[string]int
	byPlayer map[string]*PlayerFrags
}

// UseCoreOutputs is part of the CoreConsumer contract — Frag consumes
// co.DemoInfo during its Finalize to re-evaluate teamkill status with
// authoritative team membership.
func (a *FragAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

// NewFragAnalyzer creates a new frag analyzer
func NewFragAnalyzer() *FragAnalyzer {
	return &FragAnalyzer{
		byWeapon: make(map[string]int),
		byPlayer: make(map[string]*PlayerFrags),
	}
}

func (a *FragAnalyzer) Name() string { return "frag" }

func (a *FragAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

func (a *FragAnalyzer) OnEvent(event events.Event) error {
	printEvent, ok := event.(*events.PrintEvent)
	if !ok {
		return nil
	}

	// Obituary messages are typically at level 1 (PRINT_MEDIUM) in MVD
	// But we'll check levels 1, 2, and 3 to be safe
	if printEvent.Level > 3 {
		return nil
	}

	// Try to parse as a frag
	frag := a.parseObituary(printEvent.Message, printEvent.Time)
	if frag != nil {
		// Skip generic killers/victims that can't be resolved
		killerIsGeneric := isGenericPlayer(frag.Killer)
		victimIsGeneric := isGenericPlayer(frag.Victim)

		// Only add to frags list if both parties are identifiable
		if !killerIsGeneric && !victimIsGeneric {
			a.frags = append(a.frags, *frag)
		}
		a.byWeapon[frag.Weapon]++

		// Update killer stats
		// Don't count teamkills as kills (teamkiller loses a frag, doesn't gain one)
		if !frag.IsSuicide && !frag.IsTeamKill && !killerIsGeneric {
			killer := a.getOrCreatePlayer(frag.Killer)
			killer.Kills++
			killer.ByWeapon[frag.Weapon]++
		}

		// Update victim stats
		if !victimIsGeneric {
			victim := a.getOrCreatePlayer(frag.Victim)
			victim.Deaths++
		}
	}

	return nil
}

func (a *FragAnalyzer) Finalize() (interface{}, error) {
	// Re-evaluate teamkill status using DemoInfo. During OnEvent,
	// isTeamKill() compared obituary display names against ctx.Players
	// which may have had auth names, causing misses.
	if a.core != nil && a.core.DemoInfo != nil {
		names := a.core.Names
		for i := range a.frags {
			f := &a.frags[i]
			if f.IsSuicide {
				continue
			}
			killerTeam := names.TeamForName(f.Killer)
			victimTeam := names.TeamForName(f.Victim)
			wasTeamKill := f.IsTeamKill
			f.IsTeamKill = killerTeam != "" && victimTeam != "" && killerTeam == victimTeam

			// Fix kill counts if teamkill status changed
			if f.IsTeamKill != wasTeamKill && !isGenericPlayer(f.Killer) {
				if killer, ok := a.byPlayer[f.Killer]; ok {
					if f.IsTeamKill && !wasTeamKill {
						killer.Kills--
					} else if !f.IsTeamKill && wasTeamKill {
						killer.Kills++
					}
				}
			}
		}
	}

	return &FragResult{
		TotalFrags: len(a.frags),
		Frags:      a.frags,
		ByWeapon:   a.byWeapon,
		ByPlayer:   a.byPlayer,
	}, nil
}

func (a *FragAnalyzer) getOrCreatePlayer(name string) *PlayerFrags {
	// Skip generic teammate references - these are unresolvable
	if isGenericPlayer(name) {
		return &PlayerFrags{ByWeapon: make(map[string]int)} // Return a throw-away entry
	}

	if p, ok := a.byPlayer[name]; ok {
		return p
	}
	p := &PlayerFrags{ByWeapon: make(map[string]int)}
	a.byPlayer[name] = p
	return p
}

// isGenericPlayer returns true for placeholder names that shouldn't be tracked
func isGenericPlayer(name string) bool {
	nameLower := strings.ToLower(name)
	return nameLower == "teammate" ||
		nameLower == "his teammate" ||
		nameLower == "her teammate" ||
		strings.HasSuffix(nameLower, "'s quad") ||
		strings.Contains(nameLower, "'s quad rocket") ||
		strings.Contains(nameLower, "'s quad shaft")
}

// parseObituary attempts to parse a print message as a frag
func (a *FragAnalyzer) parseObituary(msg string, time float64) *FragEntry {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return nil
	}

	// Check for suicide patterns first
	if frag := a.checkSuicide(msg, time); frag != nil {
		return frag
	}

	// Check for "ate X loads" patterns (SSG kills)
	if frag := a.checkAtePattern(msg, time); frag != nil {
		return frag
	}

	// Check for killer-first patterns (X_FRAGS_Y format from KTX)
	if frag := a.checkKillerFirstPatterns(msg, time); frag != nil {
		return frag
	}

	// Check for kill patterns
	return a.checkKill(msg, time)
}

// checkSuicide checks for suicide patterns
func (a *FragAnalyzer) checkSuicide(msg string, time float64) *FragEntry {
	suicidePatterns := []struct {
		pattern string
		weapon  string
	}{
		// Suicide command
		{" suicides", "suicide"},

		// Rocket Launcher self-damage (from KTX client.c)
		// These are suicides, counted under "suicide" not "rl" to avoid double-counting
		{" discovers blast radius", "suicide"},
		{" becomes bored with life", "suicide"},

		// Grenade Launcher self-damage (counted as "suicide" not "gl")
		{" tries to put the pin back in", "suicide"},

		// Lightning Gun discharge self-damage (counted as "suicide" not "lg")
		{" electrocutes himself", "suicide"},
		{" electrocutes herself", "suicide"},
		{" heats up the water", "suicide"},
		{" discharges into the water", "suicide"},
		{" discharges into the slime", "suicide"},
		{" discharges into the lava", "suicide"},

		// Water drowning (from KTX client.c)
		{" sleeps with the fishes", "water"},
		{" sucks it down", "water"},

		// Slime damage (from KTX client.c)
		{" gulped a load of slime", "slime"},
		{" can't exist on slime alone", "slime"},

		// Lava damage (from KTX client.c)
		{" burst into flames", "lava"},
		{" turned into hot slag", "lava"},
		{" visits the Volcano God", "lava"},

		// Fall damage (from KTX client.c)
		{" cratered", "fall"},
		{" fell to his death", "fall"},
		{" fell to her death", "fall"},

		// Environmental deaths (from KTX client.c)
		{" was spiked", "world"},       // nails from world
		{" was zapped", "world"},       // laser
		{" ate a lavaball", "world"},   // fireball
		{" blew up", "world"},          // explosive box
		{" was squished", "squish"},    // squish
		{" tried to leave", "world"},   // changelevel
		// NOTE: " died" pattern removed - too generic, matches KTX stats messages

		// Legacy patterns
		{" blew himself up", "rl"},
		{" blew herself up", "rl"},
		{" finds a way out", "suicide"},
	}

	for _, p := range suicidePatterns {
		if idx := strings.Index(msg, p.pattern); idx > 0 {
			victim := strings.TrimSpace(msg[:idx])
			if victim != "" {
				return &FragEntry{
					Time:      time,
					Killer:    victim,
					Victim:    victim,
					Weapon:    p.weapon,
					IsSuicide: true,
				}
			}
		}
	}

	return nil
}

// checkKill checks for kill patterns
func (a *FragAnalyzer) checkKill(msg string, time float64) *FragEntry {
	// Check for teamkill patterns first
	if frag := a.checkTeamKill(msg, time); frag != nil {
		return frag
	}

	// Kill patterns: victim <pattern> killer
	// Order matters - more specific patterns should come before generic ones
	killPatterns := []struct {
		pattern string
		weapon  string
	}{
		// Telefrag (from KTX client.c dtTELE1)
		{" was telefragged by ", "tele"},

		// Lightning Gun (from KTX client.c dtLG_BEAM, dtLG_DIS)
		{" accepts ", "lg"},                     // "accepts X's shaft"
		{" gets a natural disaster from ", "lg"}, // quad gib
		{" drains ", "lg"},                      // "drains X's batteries" (discharge kill)

		// Rocket Launcher (from KTX client.c dtRL)
		{" rides ", "rl"},             // "rides X's rocket"
		{" was brutalized by ", "rl"}, // quad gib variant
		{" was smeared by ", "rl"},    // quad gib variant
		// NOTE: " was gibbed by " handled specially below (grenade vs rocket)

		// Grenade Launcher (from KTX client.c dtGL)
		{" eats ", "gl"}, // "eats X's pineapple"

		// Nailgun (from KTX client.c dtNG) - these come before SNG!
		{" was body pierced by ", "ng"},
		{" was nailed by ", "ng"},

		// Super Nailgun (from KTX client.c dtSNG)
		{" was straw-cuttered by ", "sng"}, // quad gib
		{" was perforated by ", "sng"},
		{" was punctured by ", "sng"},
		{" was ventilated by ", "sng"},

		// Shotgun (from KTX client.c dtSG)
		{" chewed on ", "sg"},           // "chewed on X's boomstick"
		{" was lead poisoned by ", "sg"}, // gib
		{" was instagibbed by ", "sg"},   // instagib mode

		// Axe (from KTX client.c dtAXE)
		{" was ax-murdered by ", "axe"},
		{" was axed to pieces by ", "axe"}, // instagib

		// Grappling Hook (from KTX client.c dtHOOK)
		{" was hooked by ", "hook"},

		// Rail Gun (from KTX sv_mod_frags.h, DMM8/TF)
		{" was railed by ", "rail"},

		// Stomp kills (from KTX client.c dtSTOMP)
		{" softens ", "stomp"},     // "X softens Y's fall"
		{" tried to catch ", "stomp"},
		{" was literally stomped into particles by ", "stomp"}, // instagib
		{" was jumped by ", "stomp"},
		{" was crushed by ", "stomp"},

		// Generic patterns
		{" was killed by ", "unknown"},
		{" was fragged by ", "unknown"},
	}

	for _, p := range killPatterns {
		if idx := strings.Index(msg, p.pattern); idx > 0 {
			victim := strings.TrimSpace(msg[:idx])
			rest := msg[idx+len(p.pattern):]

			// Extract killer name (may have trailing text)
			killer := extractKillerName(rest)

			if victim != "" && killer != "" {
				// Check for team kill
				isTeamKill := a.isTeamKill(victim, killer)

				return &FragEntry{
					Time:       time,
					Killer:     killer,
					Victim:     victim,
					Weapon:     p.weapon,
					IsTeamKill: isTeamKill,
				}
			}
		}
	}

	// "was gibbed by" needs special handling: weapon depends on suffix
	// "was gibbed by X's grenade" = gl, "was gibbed by X's rocket" = rl
	if frag := a.checkGibbedBy(msg, time); frag != nil {
		return frag
	}

	return nil
}

// checkTeamKill checks for team kill patterns
func (a *FragAnalyzer) checkTeamKill(msg string, time float64) *FragEntry {
	// These patterns must be checked BEFORE regular kill patterns
	// because e.g. "was telefragged by his teammate" would otherwise match "was telefragged by"

	// Killer-only teamkill patterns (victim is generic "teammate")
	tkPatterns := []string{
		" gets a frag for the other team",
		" mows down a teammate",
		" squished a teammate",
		" checks his glasses",
		" checks her glasses",
		" loses another friend",
	}
	for _, pattern := range tkPatterns {
		if idx := strings.Index(msg, pattern); idx > 0 {
			player := strings.TrimSpace(msg[:idx])
			isSuicide := pattern == " gets a frag for the other team"
			return &FragEntry{
				Time:       time,
				Killer:     player,
				Victim:     "teammate",
				Weapon:     "teamkill",
				IsSuicide:  isSuicide,
				IsTeamKill: true,
			}
		}
	}

	// Victim-first teammate patterns: "X was <verb> by his/her teammate"
	tkVictimPatterns := []string{
		" was telefragged by his teammate",
		" was telefragged by her teammate",
		" was crushed by his teammate",
		" was crushed by her teammate",
		" was jumped by his teammate",
		" was jumped by her teammate",
	}
	for _, pattern := range tkVictimPatterns {
		if idx := strings.Index(msg, pattern); idx > 0 {
			victim := strings.TrimSpace(msg[:idx])
			return &FragEntry{
				Time:       time,
				Killer:     "teammate",
				Victim:     victim,
				Weapon:     "teamkill",
				IsTeamKill: true,
			}
		}
	}

	return nil
}

// checkKillerFirstPatterns checks patterns where killer comes first (X_FRAGS_Y)
func (a *FragAnalyzer) checkKillerFirstPatterns(msg string, time float64) *FragEntry {
	// Pattern: "X rips Y a new one" (quad RL from KTX client.c)
	if idx := strings.Index(msg, " rips "); idx > 0 {
		if strings.Contains(msg, " a new one") {
			killer := strings.TrimSpace(msg[:idx])
			rest := msg[idx+6:]
			victimEnd := strings.Index(rest, " a new one")
			if victimEnd > 0 {
				victim := strings.TrimSpace(rest[:victimEnd])
				return &FragEntry{
					Time:       time,
					Killer:     killer,
					Victim:     victim,
					Weapon:     "rl",
					IsTeamKill: a.isTeamKill(victim, killer),
				}
			}
		}
	}

	// Pattern: "X stomps Y" (stomp kill from KTX client.c)
	if idx := strings.Index(msg, " stomps "); idx > 0 {
		killer := strings.TrimSpace(msg[:idx])
		victim := strings.TrimSpace(msg[idx+8:])
		if killer != "" && victim != "" {
			return &FragEntry{
				Time:       time,
				Killer:     killer,
				Victim:     victim,
				Weapon:     "stomp",
				IsTeamKill: a.isTeamKill(victim, killer),
			}
		}
	}

	// Pattern: "X squishes Y" (squish kill from KTX client.c)
	if idx := strings.Index(msg, " squishes "); idx > 0 {
		killer := strings.TrimSpace(msg[:idx])
		victim := strings.TrimSpace(msg[idx+10:])
		if killer != "" && victim != "" {
			return &FragEntry{
				Time:       time,
				Killer:     killer,
				Victim:     victim,
				Weapon:     "squish",
				IsTeamKill: a.isTeamKill(victim, killer),
			}
		}
	}

	return nil
}

// checkGibbedBy handles "was gibbed by X's grenade/rocket" with weapon detection
func (a *FragAnalyzer) checkGibbedBy(msg string, time float64) *FragEntry {
	idx := strings.Index(msg, " was gibbed by ")
	if idx <= 0 {
		return nil
	}
	victim := strings.TrimSpace(msg[:idx])
	rest := msg[idx+15:] // after " was gibbed by "

	// Determine weapon from suffix
	weapon := "rl" // default
	if strings.Contains(rest, "'s grenade") || strings.HasSuffix(strings.TrimSpace(rest), "' grenade") {
		weapon = "gl"
	}

	killer := extractKillerName(rest)
	if victim == "" || killer == "" {
		return nil
	}

	return &FragEntry{
		Time:       time,
		Killer:     killer,
		Victim:     victim,
		Weapon:     weapon,
		IsTeamKill: a.isTeamKill(victim, killer),
	}
}

// checkAtePattern handles "ate X loads of Y's buckshot" patterns
func (a *FragAnalyzer) checkAtePattern(msg string, time float64) *FragEntry {
	// Pattern: "victim ate N loads of killer's buckshot"
	if idx := strings.Index(msg, " ate "); idx > 0 {
		victim := strings.TrimSpace(msg[:idx])
		rest := msg[idx+5:]

		// Look for "'s buckshot" - this is SUPER SHOTGUN (ssg)!
		// According to fragfile.dat:
		// - "ate 2 loads of X's buckshot" = SUPER_SHOTGUN
		// - "ate 8 loads of X's buckshot" = Q_SUPER_SHOTGUN (quad)
		// - "chewed on X's boomstick" = SHOTGUN (sg)
		if strings.Contains(rest, "'s buckshot") {
			killerEnd := strings.Index(rest, "'s buckshot")
			// Skip the "N loads of " part
			loadsIdx := strings.Index(rest, " loads of ")
			if loadsIdx >= 0 && loadsIdx < killerEnd {
				killer := strings.TrimSpace(rest[loadsIdx+10 : killerEnd])
				return &FragEntry{
					Time:       time,
					Killer:     killer,
					Victim:     victim,
					Weapon:     "ssg", // Buckshot = super shotgun
					IsTeamKill: a.isTeamKill(victim, killer),
				}
			}
		}
		if strings.Contains(rest, "'s rocket") || strings.Contains(rest, " rockets from ") {
			loadsIdx := strings.Index(rest, " rockets from ")
			if loadsIdx >= 0 {
				killer := strings.TrimSpace(rest[loadsIdx+14:])
				killer = stripQuadSuffix(killer)
				return &FragEntry{
					Time:       time,
					Killer:     killer,
					Victim:     victim,
					Weapon:     "rl",
					IsTeamKill: a.isTeamKill(victim, killer),
				}
			}
		}
	}
	return nil
}

// isTeamKill checks if killer and victim are on the same team
func (a *FragAnalyzer) isTeamKill(victim, killer string) bool {
	var victimTeam, killerTeam string

	for i := 0; i < len(a.ctx.Players); i++ {
		p := a.ctx.Players[i]
		if p == nil {
			continue
		}
		if p.Name == victim {
			victimTeam = p.Team
		}
		if p.Name == killer {
			killerTeam = p.Team
		}
	}

	return victimTeam != "" && killerTeam != "" && victimTeam == killerTeam
}
