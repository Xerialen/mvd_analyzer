package analyzer

import "strings"

// parsedObituary is the neutral, weapon-and-name result of matching one
// KTX / mvdsv obituary or suicide print line, independent of how a consumer
// represents it. FragAnalyzer maps it to a FragEntry (adding the
// team-membership teamkill test it has always done); MessagesAnalyzer maps it
// to a MatchEvent.
//
// This is the single source of truth for obituary pattern matching. Both
// analyzers previously carried near-identical ~250-line pattern tables that
// had drifted apart — drowning code (water vs drown), the "somehow becomes
// bored with life" catch-all, the CRMod and k_spawnicide variants, and the
// gibbed-by suffix test all disagreed. frag.go's behavior is the reference:
// the tables and walker order below reproduce it exactly, and the messages
// output now matches it.
type parsedObituary struct {
	Killer string
	Victim string
	Weapon string
	// Suicide marks a self-kill: checkSuicide patterns, the Satan's-power
	// self-telefrag, and "X gets a frag for the other team" (frag.go books
	// that one as a suicide until recoverTeamkills finds the real victim).
	Suicide bool
	// TeamKill marks a phrasing-based teamkill — one of Killer/Victim is the
	// generic "teammate" placeholder because the obituary named only one
	// party. The membership-based teamkill test (killer and victim resolve
	// to the same team) is deliberately NOT done here: frag.go applies it in
	// its own mapper against ctx.Players, and messages.go doesn't need it.
	TeamKill bool
}

// parseObituaryLine matches msg against the full obituary/suicide pattern set
// in frag.go's canonical order: suicide, "ate N loads" (SSG/RL), killer-first
// (rips / stomps / squishes), then the kill group — phrasing teamkills,
// victim-first kill patterns, gibbed-by, Satan-deflect. Returns nil when msg
// is not an obituary.
//
// Order is semantic and load-bearing (Phase 1 F3 fixed the CRMod "eats 2
// scoops" vs generic " eats " ordering within killPatterns); do not reorder a
// group or a within-group entry without re-checking the golden corpus.
func parseObituaryLine(msg string) *parsedObituary {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return nil
	}
	if o := matchSuicide(msg); o != nil {
		return o
	}
	if o := matchAte(msg); o != nil {
		return o
	}
	if o := matchKillerFirst(msg); o != nil {
		return o
	}
	// The kill group. checkTeamKill runs before the victim-first kill
	// patterns so "X was telefragged by his teammate" matches the teamkill
	// form before the shorter " was telefragged by " marker.
	if o := matchTeamKill(msg); o != nil {
		return o
	}
	if o := matchKill(msg); o != nil {
		return o
	}
	if o := matchGibbedBy(msg); o != nil {
		return o
	}
	if o := matchSatanDeflect(msg); o != nil {
		return o
	}
	return nil
}

// matchSuicide scans the self-kill phrases. See the pattern comments for the
// KTX client.c origins.
func matchSuicide(msg string) *parsedObituary {
	for _, p := range suicidePatterns {
		if idx := strings.Index(msg, p.pattern); idx > 0 {
			victim := strings.TrimSpace(msg[:idx])
			if victim != "" {
				return &parsedObituary{Killer: victim, Victim: victim, Weapon: p.weapon, Suicide: true}
			}
		}
	}
	return nil
}

// matchAte handles "victim ate N loads of killer's buckshot" (SSG) and the
// rarer "... N rockets from killer" (RL) splash attribution.
func matchAte(msg string) *parsedObituary {
	idx := strings.Index(msg, " ate ")
	if idx <= 0 {
		return nil
	}
	victim := strings.TrimSpace(msg[:idx])
	rest := msg[idx+5:]

	// "ate N loads of X's buckshot" = SUPER SHOTGUN.
	if strings.Contains(rest, "'s buckshot") {
		killerEnd := strings.Index(rest, "'s buckshot")
		loadsIdx := strings.Index(rest, " loads of ")
		if loadsIdx >= 0 && loadsIdx < killerEnd {
			killer := strings.TrimSpace(rest[loadsIdx+10 : killerEnd])
			return &parsedObituary{Killer: killer, Victim: victim, Weapon: "ssg"}
		}
	}
	if strings.Contains(rest, "'s rocket") || strings.Contains(rest, " rockets from ") {
		if loadsIdx := strings.Index(rest, " rockets from "); loadsIdx >= 0 {
			killer := stripQuadSuffix(strings.TrimSpace(rest[loadsIdx+14:]))
			return &parsedObituary{Killer: killer, Victim: victim, Weapon: "rl"}
		}
	}
	return nil
}

// matchKillerFirst handles the "killer <verb> victim" forms (X_FRAGS_Y).
func matchKillerFirst(msg string) *parsedObituary {
	// "X rips Y a new one" (quad RL).
	if idx := strings.Index(msg, " rips "); idx > 0 && strings.Contains(msg, " a new one") {
		killer := strings.TrimSpace(msg[:idx])
		rest := msg[idx+6:]
		if victimEnd := strings.Index(rest, " a new one"); victimEnd > 0 {
			victim := strings.TrimSpace(rest[:victimEnd])
			return &parsedObituary{Killer: killer, Victim: victim, Weapon: "rl"}
		}
	}
	// "X stomps Y".
	if idx := strings.Index(msg, " stomps "); idx > 0 {
		killer := strings.TrimSpace(msg[:idx])
		victim := strings.TrimSpace(msg[idx+8:])
		if killer != "" && victim != "" {
			return &parsedObituary{Killer: killer, Victim: victim, Weapon: "stomp"}
		}
	}
	// "X squishes Y".
	if idx := strings.Index(msg, " squishes "); idx > 0 {
		killer := strings.TrimSpace(msg[:idx])
		victim := strings.TrimSpace(msg[idx+10:])
		if killer != "" && victim != "" {
			return &parsedObituary{Killer: killer, Victim: victim, Weapon: "squish"}
		}
	}
	return nil
}

// matchTeamKill handles the phrasing-based teamkills where the obituary names
// only one party; the other becomes the generic "teammate" placeholder.
func matchTeamKill(msg string) *parsedObituary {
	// Killer-named ("X loses another friend"): victim is generic.
	for _, pattern := range teamkillKillerPatterns {
		if idx := strings.Index(msg, pattern); idx > 0 {
			player := strings.TrimSpace(msg[:idx])
			return &parsedObituary{
				Killer: player,
				Victim: "teammate",
				Weapon: "teamkill",
				// "X gets a frag for the other team" is a self-inflicted
				// team frag; frag.go tags it suicide until the victim is
				// recovered (see recoverTeamkills).
				Suicide:  pattern == " gets a frag for the other team",
				TeamKill: true,
			}
		}
	}
	// Victim-named ("X was telefragged by his teammate"): killer is generic.
	for _, pattern := range teamkillVictimPatterns {
		if idx := strings.Index(msg, pattern); idx > 0 {
			victim := strings.TrimSpace(msg[:idx])
			return &parsedObituary{
				Killer:   "teammate",
				Victim:   victim,
				Weapon:   "teamkill",
				TeamKill: true,
			}
		}
	}
	return nil
}

// matchKill walks the victim-first kill patterns and disambiguates the
// shared " was blown to chunks by " verb by weapon suffix.
func matchKill(msg string) *parsedObituary {
	for _, p := range killPatterns {
		if idx := strings.Index(msg, p.pattern); idx > 0 {
			victim := strings.TrimSpace(msg[:idx])
			rest := msg[idx+len(p.pattern):]
			killer := extractKillerName(rest)

			weapon := p.weapon
			// "X was blown to chunks by Y's rocket" (rl) vs "... Y's grenade"
			// (gl) share the verb — disambiguate via the suffix.
			if p.pattern == " was blown to chunks by " {
				if strings.Contains(rest, "'s grenade") || strings.HasSuffix(strings.TrimSpace(rest), "' grenade") {
					weapon = "gl"
				}
			}

			if victim != "" && killer != "" {
				return &parsedObituary{Killer: killer, Victim: victim, Weapon: weapon}
			}
		}
	}
	return nil
}

// matchGibbedBy handles "was gibbed by X's grenade/rocket" where the weapon
// depends on the suffix.
func matchGibbedBy(msg string) *parsedObituary {
	idx := strings.Index(msg, " was gibbed by ")
	if idx <= 0 {
		return nil
	}
	victim := strings.TrimSpace(msg[:idx])
	rest := msg[idx+15:] // after " was gibbed by "

	weapon := "rl" // default
	if strings.Contains(rest, "'s grenade") || strings.HasSuffix(strings.TrimSpace(rest), "' grenade") {
		weapon = "gl"
	}

	killer := extractKillerName(rest)
	if victim == "" || killer == "" {
		return nil
	}
	return &parsedObituary{Killer: killer, Victim: victim, Weapon: weapon}
}

// matchSatanDeflect handles the "Satan's power deflects X's telefrag"
// self-telefrag suicide (KTX dtTELE2), an infix form the prefix suicide loop
// can't catch.
func matchSatanDeflect(msg string) *parsedObituary {
	victim := satanDeflectVictim(msg)
	if victim == "" {
		return nil
	}
	return &parsedObituary{Killer: victim, Victim: victim, Weapon: "tele", Suicide: true}
}

// suicidePatterns are the self-kill phrases in KTX client.c order. The
// weapon/cause is preserved (with IsSuicide set) so a weapon self-detonation
// stays distinguishable from a /kill; only the /kill console command keeps
// weapon "suicide".
var suicidePatterns = []struct {
	pattern string
	weapon  string
}{
	// The /kill console command (dtSUICIDE, −2 frags).
	{" suicides", "suicide"},

	// Rocket Launcher self-damage.
	{" discovers blast radius", "rl"},
	// KTX catch-all self-kill of unknown cause (client.c:5254). Must precede
	// the shorter " becomes bored with life" substring it contains; cause
	// unknown, so it stays "suicide".
	{" somehow becomes bored with life", "suicide"},
	{" becomes bored with life", "rl"},

	// Grenade Launcher self-damage.
	{" tries to put the pin back in", "gl"},

	// Lightning Gun discharge self-damage.
	{" electrocutes himself", "lg"},
	{" electrocutes herself", "lg"},
	{" heats up the water", "lg"},
	{" discharges into the water", "lg"},
	{" discharges into the slime", "lg"},
	{" discharges into the lava", "lg"},

	// Water drowning.
	{" sleeps with the fishes", "water"},
	{" sucks it down", "water"},

	// Slime.
	{" gulped a load of slime", "slime"},
	{" can't exist on slime alone", "slime"},

	// Lava.
	{" burst into flames", "lava"},
	{" turned into hot slag", "lava"},
	{" visits the Volcano God", "lava"},

	// Fall.
	{" cratered", "fall"},
	{" fell to his death", "fall"},
	{" fell to her death", "fall"},

	// Environmental.
	{" was spiked", "world"},     // nails from world
	{" was zapped", "world"},     // laser
	{" ate a lavaball", "world"}, // fireball
	{" blew up", "world"},        // explosive box
	{" was squished", "squish"},  // squish
	{" tried to leave", "world"}, // changelevel
	// NOTE: " died" pattern deliberately absent — too generic, matches KTX
	// stats messages.

	// Legacy.
	{" blew himself up", "rl"},
	{" blew herself up", "rl"},
	{" finds a way out", "suicide"},

	// KTX k_spawnicide variants (client.c:5164, dtTELE4). Only emitted when
	// k_spawnicide is enabled; counted as a suicide (KTX logfrag(targ, targ)).
	{" couldn't resist the shiny spawn point", "tele"},
	{" got too close to the baby factory", "tele"},
	{" was fragged by poor life choices", "tele"},
}

// killPatterns are the victim-first "X <verb> Y" kill markers in KTX client.c
// order. Order matters: more specific patterns precede the generic ones they
// contain (CRMod "eats 2 scoops of" before " eats ").
var killPatterns = []struct {
	pattern string
	weapon  string
}{
	// Telefrag (dtTELE1).
	{" was telefragged by ", "tele"},

	// Lightning Gun (dtLG_BEAM, dtLG_DIS).
	{" accepts ", "lg"},                      // "accepts X's shaft"
	{" gets a natural disaster from ", "lg"}, // quad gib
	{" drains ", "lg"},                       // "drains X's batteries" (discharge kill)

	// Rocket Launcher (dtRL).
	{" rides ", "rl"},             // "rides X's rocket"
	{" was brutalized by ", "rl"}, // quad gib variant
	{" was smeared by ", "rl"},    // quad gib variant
	// NOTE: " was gibbed by " handled by matchGibbedBy (grenade vs rocket).

	// CRMod SSG ("X eats 2 scoops of Y's lead shot") must precede the generic
	// GL " eats " below: strings.Index would otherwise hit the shorter
	// " eats " first, mislabel the kill "gl", and leave extractKillerName to
	// return the phantom "2 scoops of Y" name.
	{" eats 2 scoops of ", "ssg"}, // suffix "'s lead shot"

	// Grenade Launcher (dtGL).
	{" eats ", "gl"}, // "eats X's pineapple"

	// Nailgun (dtNG) — before SNG.
	{" was body pierced by ", "ng"},
	{" was nailed by ", "ng"},

	// Super Nailgun (dtSNG).
	{" was straw-cuttered by ", "sng"}, // quad gib
	{" was perforated by ", "sng"},
	{" was punctured by ", "sng"},
	{" was ventilated by ", "sng"},

	// Shotgun (dtSG).
	{" chewed on ", "sg"},            // "chewed on X's boomstick"
	{" was lead poisoned by ", "sg"}, // gib
	{" was instagibbed by ", "sg"},   // instagib mode

	// Axe (dtAXE).
	{" was ax-murdered by ", "axe"},
	{" was axed to pieces by ", "axe"}, // instagib

	// Grappling Hook (dtHOOK).
	{" was hooked by ", "hook"},

	// Rail Gun (sv_mod_frags.h, DMM8/TF).
	{" was railed by ", "rail"},

	// Stomp kills (dtSTOMP).
	{" softens ", "stomp"}, // "X softens Y's fall"
	{" tried to catch ", "stomp"},
	{" was literally stomped into particles by ", "stomp"}, // instagib
	{" was jumped by ", "stomp"},
	{" was crushed by ", "stomp"},

	// CRMod obituary variants (parallel "X_FRAGGED_BY_Y" table). Suffix-based
	// weapon disambiguation happens via obituaryWeapons / extractKillerName,
	// except " was blown to chunks by " which is shared rl/gl and is fixed up
	// in matchKill.
	{" was disembowled by ", "sg"},             // [sic] CRMod misspelling; suffix "'s shotgun"
	{" is shish-kebabed by ", "rl"},            // suffix "'s rocket"
	{" was blown to chunks by ", "rl"},         // suffix "'s rocket" — fixed up to gl when suffix is "'s grenade"
	{" gets intimate with ", "gl"},             // suffix "'s grenade"
	{" gets a warm fuzzy feeling from ", "lg"}, // no weapon suffix; rest is just the killer name

	// Generic.
	{" was killed by ", "unknown"},
	{" was fragged by ", "unknown"},
}

// teamkillKillerPatterns name only the attacker; the victim is the generic
// "teammate". Checked before the victim-first kill patterns.
var teamkillKillerPatterns = []string{
	" gets a frag for the other team",
	" mows down a teammate",
	" squished a teammate",
	" checks his glasses",
	" checks her glasses",
	" loses another friend",
}

// teamkillVictimPatterns name only the victim; the killer is the generic
// "teammate". "X was <verb> by his/her teammate" — must be checked before the
// non-team " was telefragged by " / " was crushed by " / " was jumped by "
// markers so those don't steal the line.
var teamkillVictimPatterns = []string{
	" was telefragged by his teammate",
	" was telefragged by her teammate",
	" was crushed by his teammate",
	" was crushed by her teammate",
	" was jumped by his teammate",
	" was jumped by her teammate",
}
