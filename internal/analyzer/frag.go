package analyzer

import (
	"strings"

	"github.com/mvd-analyzer/internal/parser"
)

// FragAnalyzer detects frags from print messages
type FragAnalyzer struct {
	ctx      *Context
	frags    []FragEntry
	byWeapon map[string]int
	byPlayer map[string]*PlayerFrags
}

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

func (a *FragAnalyzer) OnEvent(event parser.Event) error {
	printEvent, ok := event.(*parser.PrintEvent)
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
		if !frag.IsSuicide && !killerIsGeneric {
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

	// Check for kill patterns
	return a.checkKill(msg, time)
}

// checkSuicide checks for suicide patterns
func (a *FragAnalyzer) checkSuicide(msg string, time float64) *FragEntry {
	suicidePatterns := []struct {
		pattern string
		weapon  string
	}{
		{" suicides", "suicide"},
		{" tries to leave", "suicide"},
		{" becomes bored with life", "suicide"},
		{" sleeps with the fishes", "water"},
		{" sucks it down", "slime"},
		{" gulps a load of slime", "slime"},
		{" can't exist on slime alone", "slime"},
		{" burst into flames", "lava"},
		{" turned into hot slag", "lava"},
		{" visits the Volcano God", "lava"},
		{" cratered", "fall"},
		{" fell to his death", "fall"},
		{" fell to her death", "fall"},
		{" blew himself up", "rl"},
		{" blew herself up", "rl"},
		{" becomes an idealist", "gl"},
		{" finds a way out", "suicide"},
		{" tries to put the pin back in", "gl"},
		{" becomes bored with life", "suicide"},
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
	killPatterns := []struct {
		pattern string
		weapon  string
	}{
		// Telefrag
		{" was telefragged by ", "tele"},

		// Lightning Gun
		{" accepts ", "lg"}, // "accepts X's shaft"
		{" gets a natural disaster from ", "lg"},
		{" rides ", "lg"}, // "rides X's lightning"

		// Rocket Launcher
		{" was gibbed by ", "rl"}, // Most common
		{" was railed by ", "rl"},
		{" almost dodged ", "rl"},
		{" was brutalized by ", "rl"},
		{" was smeared by ", "rl"},
		{" eats ", "rl"},          // "eats X's rocket"
		{" chews on ", "rl"},      // "chews on X's rocket"
		{" was fried by ", "rl"},  // "was fried by X"

		// Grenade Launcher
		{" didn't see ", "gl"},
		{" was blown away by ", "gl"},

		// Super Nailgun
		{" was body pierced by ", "sng"},
		{" was nailed by ", "sng"},
		{" was straw-cuttered by ", "sng"},
		{" was ripped by ", "sng"},
		{" was perforated by ", "ng"},
		{" was punctured by ", "ng"},
		{" was ventilated by ", "ng"},

		// Super Shotgun
		{" was lead poisoned by ", "ssg"},
		{" was gunned by ", "ssg"},
		{" chewed on ", "ssg"}, // "chewed on X's boomstick"

		// Shotgun
		{" was shot by ", "sg"},

		// Axe
		{" was ax-murdered by ", "axe"},
		{" was axed by ", "axe"},
		{" was chopped up by ", "axe"},

		// Generic patterns
		{" was killed by ", "unknown"},
		{" was fragged by ", "unknown"},
		{" becomes ", "unknown"}, // "becomes X's bitch"
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

	return nil
}

// checkTeamKill checks for team kill patterns
func (a *FragAnalyzer) checkTeamKill(msg string, time float64) *FragEntry {
	// Pattern: "X gets a frag for the other team" (suicidal teamkill)
	if idx := strings.Index(msg, " gets a frag for the other team"); idx > 0 {
		player := strings.TrimSpace(msg[:idx])
		return &FragEntry{
			Time:       time,
			Killer:     player,
			Victim:     player, // Self-damage resulting in -1 frag
			Weapon:     "teamkill",
			IsSuicide:  true,
			IsTeamKill: true,
		}
	}

	// Pattern: "X mows down a teammate"
	if idx := strings.Index(msg, " mows down a teammate"); idx > 0 {
		player := strings.TrimSpace(msg[:idx])
		return &FragEntry{
			Time:       time,
			Killer:     player,
			Victim:     "teammate",
			Weapon:     "teamkill",
			IsTeamKill: true,
		}
	}

	return nil
}

// checkAtePattern handles "ate X loads of Y's buckshot" patterns
func (a *FragAnalyzer) checkAtePattern(msg string, time float64) *FragEntry {
	// Pattern: "victim ate N loads of killer's buckshot"
	if idx := strings.Index(msg, " ate "); idx > 0 {
		victim := strings.TrimSpace(msg[:idx])
		rest := msg[idx+5:]

		// Look for "'s buckshot" or "'s rocket"
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
					Weapon:     "ssg",
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

// extractKillerName extracts killer name from the rest of the message
func extractKillerName(rest string) string {
	// Common suffixes to remove (check quad variants first, then normal)
	suffixes := []string{
		// Quad damage variants (must be before regular variants)
		"'s quad shaft",
		"'s quad lightning",
		"'s quad rocket",
		"'s quad pineapple",
		"'s quad boomstick",
		"'s quad grenade",
		"'s quad axe",
		// Regular variants
		"'s shaft",
		"'s lightning",
		"'s rocket",
		"'s pineapple",
		"'s boomstick",
		"'s grenade",
		"'s axe",
	}

	for _, suffix := range suffixes {
		if idx := strings.Index(rest, suffix); idx > 0 {
			return strings.TrimSpace(rest[:idx])
		}
	}

	// Check for " rockets from " pattern
	if idx := strings.Index(rest, " rockets from "); idx >= 0 {
		killer := strings.TrimSpace(rest[idx+len(" rockets from "):])
		// Strip "'s quad rocket" suffix if present
		killer = stripQuadSuffix(killer)
		return killer
	}

	// Check for trailing newline or period
	rest = strings.TrimSuffix(rest, "\n")
	rest = strings.TrimSuffix(rest, ".")
	rest = stripQuadSuffix(rest)

	return strings.TrimSpace(rest)
}

// stripQuadSuffix removes quad-related suffixes from killer names
func stripQuadSuffix(name string) string {
	quadSuffixes := []string{
		"'s quad rocket",
		"'s quad shaft",
		"'s quad lightning",
		"'s quad pineapple",
		"'s quad boomstick",
		"'s quad grenade",
		"'s quad axe",
		"'s quad",
	}
	for _, suffix := range quadSuffixes {
		name = strings.TrimSuffix(name, suffix)
	}
	return strings.TrimSpace(name)
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
