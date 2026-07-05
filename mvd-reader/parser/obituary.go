package parser

import "strings"

// ObituaryPattern is one death-print marker. KTX's obituaries always
// follow the form "<victim> <marker> [<killer suffix>]" — `Marker` is
// the substring that identifies the obit kind; `Weapon` is the
// canonical short code for downstream attribution (rl, lg, sg, …, plus
// the synthetic "suicide", "water", "lava", "world", "fall", "squish",
// "teamkill"); `Suicide` and `TeamKill` flag the variants the
// FragAnalyzer needs to bucket separately.
//
// The list below is the single source of truth for victim-prefix
// obituaries. Both this parser (to fire DeathEvent via maybeEmitDeath
// when neither STAT_HEALTH nor DF_DEAD caught the transition) and the
// FragAnalyzer downstream walk the same table — keeping them aligned
// removes the silent drift risk of two parallel copies.
//
// Pattern order matters: more specific patterns precede their generic
// supersets so the more informative attribution wins (e.g. " was
// telefragged by his teammate" must be checked before " was telefragged
// by "). The composite slice ObituaryVictimPatterns is the
// canonically-ordered list a caller iterates left-to-right.
type ObituaryPattern struct {
	Marker   string
	Weapon   string
	Suicide  bool
	TeamKill bool
}

// teamKillVictimObituaries are the "<victim> was X by his/her teammate"
// forms — must be matched before the corresponding non-teammate kill
// pattern so the more specific obit wins.
var teamKillVictimObituaries = []ObituaryPattern{
	{Marker: " was telefragged by his teammate", Weapon: "teamkill", TeamKill: true},
	{Marker: " was telefragged by her teammate", Weapon: "teamkill", TeamKill: true},
	{Marker: " was crushed by his teammate", Weapon: "teamkill", TeamKill: true},
	{Marker: " was crushed by her teammate", Weapon: "teamkill", TeamKill: true},
	{Marker: " was jumped by his teammate", Weapon: "teamkill", TeamKill: true},
	{Marker: " was jumped by her teammate", Weapon: "teamkill", TeamKill: true},
}

// suicideObituaries: the player killed themselves. Self-RL, self-GL,
// LG discharge in liquid, environmental damage (lava/slime/water/fall),
// and the explicit `kill` console command. Every entry produces a
// death and a respawn for the player.
var suicideObituaries = []ObituaryPattern{
	{Marker: " suicides", Weapon: "suicide", Suicide: true},

	{Marker: " discovers blast radius", Weapon: "suicide", Suicide: true},
	// KTX's catch-all self-kill (ktx/src/client.c:5254). Must precede the
	// shorter " becomes bored with life" it contains, or that pattern
	// would match first and leave "… somehow" stuck on the victim name.
	{Marker: " somehow becomes bored with life", Weapon: "suicide", Suicide: true},
	{Marker: " becomes bored with life", Weapon: "suicide", Suicide: true},

	{Marker: " tries to put the pin back in", Weapon: "suicide", Suicide: true},

	{Marker: " electrocutes himself", Weapon: "suicide", Suicide: true},
	{Marker: " electrocutes herself", Weapon: "suicide", Suicide: true},
	{Marker: " heats up the water", Weapon: "suicide", Suicide: true},
	{Marker: " discharges into the water", Weapon: "suicide", Suicide: true},
	{Marker: " discharges into the slime", Weapon: "suicide", Suicide: true},
	{Marker: " discharges into the lava", Weapon: "suicide", Suicide: true},

	{Marker: " sleeps with the fishes", Weapon: "water", Suicide: true},
	{Marker: " sucks it down", Weapon: "water", Suicide: true},

	{Marker: " gulped a load of slime", Weapon: "slime", Suicide: true},
	{Marker: " can't exist on slime alone", Weapon: "slime", Suicide: true},

	{Marker: " burst into flames", Weapon: "lava", Suicide: true},
	{Marker: " turned into hot slag", Weapon: "lava", Suicide: true},
	{Marker: " visits the Volcano God", Weapon: "lava", Suicide: true},

	{Marker: " cratered", Weapon: "fall", Suicide: true},
	{Marker: " fell to his death", Weapon: "fall", Suicide: true},
	{Marker: " fell to her death", Weapon: "fall", Suicide: true},

	{Marker: " was spiked", Weapon: "world", Suicide: true},
	{Marker: " was zapped", Weapon: "world", Suicide: true},
	{Marker: " ate a lavaball", Weapon: "world", Suicide: true},
	{Marker: " blew up", Weapon: "world", Suicide: true},
	{Marker: " was squished", Weapon: "squish", Suicide: true},
	{Marker: " tried to leave", Weapon: "world", Suicide: true},

	{Marker: " blew himself up", Weapon: "rl", Suicide: true},
	{Marker: " blew herself up", Weapon: "rl", Suicide: true},
	{Marker: " finds a way out", Weapon: "suicide", Suicide: true},

	// KTX k_spawnicide variants (dtTELE4 — ktx/src/client.c:5164).
	// Fire only when k_spawnicide is enabled; otherwise the server uses
	// the regular " was telefragged by " pattern. Rare in pickup
	// matches; common in some pug rulesets.
	{Marker: " couldn't resist the shiny spawn point", Weapon: "tele", Suicide: true},
	{Marker: " got too close to the baby factory", Weapon: "tele", Suicide: true},
	{Marker: " was fragged by poor life choices", Weapon: "tele", Suicide: true},
}

// killObituaries: another player killed the victim. Marker order
// matches the KTX client.c death-type table; weapons follow KTX's
// canonical short codes.
var killObituaries = []ObituaryPattern{
	{Marker: " was telefragged by ", Weapon: "tele"},

	{Marker: " accepts ", Weapon: "lg"},
	{Marker: " gets a natural disaster from ", Weapon: "lg"},
	{Marker: " drains ", Weapon: "lg"},

	{Marker: " rides ", Weapon: "rl"},
	{Marker: " was brutalized by ", Weapon: "rl"},
	{Marker: " was smeared by ", Weapon: "rl"},

	{Marker: " eats ", Weapon: "gl"},

	{Marker: " was body pierced by ", Weapon: "ng"},
	{Marker: " was nailed by ", Weapon: "ng"},

	{Marker: " was straw-cuttered by ", Weapon: "sng"},
	{Marker: " was perforated by ", Weapon: "sng"},
	{Marker: " was punctured by ", Weapon: "sng"},
	{Marker: " was ventilated by ", Weapon: "sng"},

	{Marker: " chewed on ", Weapon: "sg"},
	{Marker: " was lead poisoned by ", Weapon: "sg"},
	{Marker: " was instagibbed by ", Weapon: "sg"},

	{Marker: " was ax-murdered by ", Weapon: "axe"},
	{Marker: " was axed to pieces by ", Weapon: "axe"},

	{Marker: " was hooked by ", Weapon: "hook"},

	{Marker: " was railed by ", Weapon: "rail"},

	{Marker: " softens ", Weapon: "stomp"},
	{Marker: " tried to catch ", Weapon: "stomp"},
	{Marker: " was literally stomped into particles by ", Weapon: "stomp"},
	{Marker: " was jumped by ", Weapon: "stomp"},
	{Marker: " was crushed by ", Weapon: "stomp"},

	{Marker: " was killed by ", Weapon: "unknown"},
	{Marker: " was fragged by ", Weapon: "unknown"},

	// "was gibbed by" handled specially below — weapon depends on
	// whether the suffix is "'s rocket" (rl) or "'s grenade" (gl).
	{Marker: " was gibbed by ", Weapon: "rl"},

	// CRMod-added obituary variants (kept here because servers running
	// the CR ruleset still produce these strings; KTX retains them in
	// the fragfile table). For each, the victim is the prefix before
	// Marker; the suffix that disambiguates the weapon is handled in
	// the analyzer's frag.go (parser-side only needs the victim).
	{Marker: " was disembowled by ", Weapon: "sg"},             // CRMod misspelling [sic]; suffix "'s shotgun"
	{Marker: " eats 2 scoops of ", Weapon: "ssg"},              // suffix "'s lead shot"
	{Marker: " is shish-kebabed by ", Weapon: "rl"},            // suffix "'s rocket"
	{Marker: " was blown to chunks by ", Weapon: "rl"},         // suffix "'s rocket" or "'s grenade" (disambiguate in analyzer)
	{Marker: " gets intimate with ", Weapon: "gl"},             // suffix "'s grenade"
	{Marker: " gets a warm fuzzy feeling from ", Weapon: "lg"}, // no suffix
}

// ObituaryVictimPatterns is the canonically-ordered union of every
// victim-prefix obituary (team kills first to win the "telefragged by
// his teammate" disambiguation, then suicides, then kills). Callers
// scan left-to-right and accept the first match.
var ObituaryVictimPatterns = func() []ObituaryPattern {
	out := make([]ObituaryPattern, 0, len(teamKillVictimObituaries)+len(suicideObituaries)+len(killObituaries))
	out = append(out, teamKillVictimObituaries...)
	out = append(out, suicideObituaries...)
	out = append(out, killObituaries...)
	return out
}()

// ObituaryInfixPattern describes obit forms where the victim's name
// sits between a fixed prefix and a fixed suffix rather than at the
// start of the line. The canonical case is KTX's pentagram-deflection
// telefrag: when a player tries to telefrag someone wearing pent, the
// would-be telefragger dies and the obit is "Satan's power deflects
// <victim>'s telefrag\n" (ktx/src/client.c:5143 — the death is
// attributed to `targ`, i.e. the named player, with a frags -= 1
// penalty). The victim-prefix scan would never pick that up, so it
// rides on this separate list.
type ObituaryInfixPattern struct {
	Prefix   string
	Suffix   string
	Weapon   string
	Suicide  bool
	TeamKill bool
}

// ObituaryInfixPatterns is the canonical infix-form obit list. Kept
// short: only patterns confirmed against KTX source go here. Mirrors
// (with Weapon attribution) any matching pattern in the analyzer's
// FragAnalyzer.
var ObituaryInfixPatterns = []ObituaryInfixPattern{
	{Prefix: "Satan's power deflects ", Suffix: "'s telefrag", Weapon: "tele"},
}

// FindObituaryVictim scans `msg` against the canonical obituary
// patterns. On the first match it returns the victim's display name
// (everything in `msg` before the matched marker, with surrounding
// whitespace trimmed) and a pointer to the matched pattern. Returns
// ("", nil) when no obituary pattern fits.
//
// Callers that need only "did somebody die" can ignore the pattern
// pointer; callers building a frag log read Weapon / Suicide / TeamKill
// off the returned pattern.
//
// Lookups are tried in order: victim-prefix first (the bulk of KTX's
// fragfile lines), then infix patterns (Satan's-deflection-style
// obits where the victim is bracketed by a fixed prefix and suffix).
// Infix matches are synthesized into a returned *ObituaryPattern so
// callers don't need to branch on which list matched.
func FindObituaryVictim(msg string) (string, *ObituaryPattern) {
	for i := range ObituaryVictimPatterns {
		p := &ObituaryVictimPatterns[i]
		idx := strings.Index(msg, p.Marker)
		if idx <= 0 {
			continue
		}
		victim := strings.TrimSpace(msg[:idx])
		if victim == "" {
			continue
		}
		return victim, p
	}
	for i := range ObituaryInfixPatterns {
		ip := &ObituaryInfixPatterns[i]
		if !strings.HasPrefix(msg, ip.Prefix) {
			continue
		}
		rest := msg[len(ip.Prefix):]
		suffixIdx := strings.Index(rest, ip.Suffix)
		if suffixIdx <= 0 {
			continue
		}
		victim := strings.TrimSpace(rest[:suffixIdx])
		if victim == "" {
			continue
		}
		synthesized := ObituaryPattern{
			Marker:   ip.Prefix + ip.Suffix,
			Weapon:   ip.Weapon,
			Suicide:  ip.Suicide,
			TeamKill: ip.TeamKill,
		}
		return victim, &synthesized
	}
	return "", nil
}
