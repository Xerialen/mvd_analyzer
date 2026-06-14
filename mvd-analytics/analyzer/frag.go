package analyzer

import (
	"sort"
	"strings"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// teamkillMatchWindowMs bounds how far a killer-named teamkill obituary
// may sit from the authoritative DeathEvent it caused when recovering the
// victim. Obituary print and DF_DEAD transition share the demo clock and
// land on the same frame in practice (observed Δ0); the small window only
// guards against clock jitter.
const teamkillMatchWindowMs int32 = 256

// FragAnalyzer detects frags from print messages
type FragAnalyzer struct {
	ctx      *Context
	core     *CoreOutputs
	timing   MatchTimingDetector
	frags    []FragEntry
	byWeapon map[string]int
	byPlayer map[string]*PlayerFrags
	// deathSlots are the authoritative match-time deaths (wire slot +
	// time), collected from the protocol DeathEvent and resolved to a
	// player identity in Finalize. See the DeathEvent case in OnEvent.
	deathSlots []slotDeath
	// genericTeamkills are killer-named teamkill obituaries ("X loses
	// another friend", "X checks his glasses", ...) whose victim is the
	// generic "teammate" and so were dropped from frags. Finalize counts
	// them against the killer and recovers the victim by matching the
	// coincident DeathEvent on the killer's team.
	genericTeamkills []FragEntry
	// victimNamedTeamkills are the mirror case ("X was telefragged by his
	// teammate") — victim known, killer generic. Exposed via CoreOutputs so
	// the recoverTelefragTeamkills post-processor can recover the killer
	// from position co-location + the teamkiller's frag-penalty.
	victimNamedTeamkills []FragEntry
}

// slotDeath is one match-time death pinned to the wire slot that died
// and the time it happened, so Finalize can resolve it to the
// reconnect-unified identity holding that slot then.
type slotDeath struct {
	slot int
	tMs  int32
}

// UseCoreOutputs is part of the CoreConsumer contract — Frag consumes
// co.DemoInfo during its Finalize to re-evaluate teamkill status with
// authoritative team membership.
func (a *FragAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

// PopulateCore exposes the resolved frag log to downstream analysers
// (timeline, weapon_pickups) via CoreOutputs.FragEntries.
func (a *FragAnalyzer) PopulateCore(co *CoreOutputs) {
	co.FragEntries = a.frags
	co.VictimNamedTeamkills = a.victimNamedTeamkills
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

func (a *FragAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.PrintEvent:
		a.timing.OnPrint(e)
		a.handleObituaryPrint(e)
	case *events.IntermissionEvent:
		a.timing.OnIntermission(e.Time)
	case *events.DeathEvent:
		// Count authoritative deaths during the match. KTX bumps
		// targ->deaths for every death (ktx/src/client.c:5124), but
		// several teamkill obituaries name only the *attacker* — e.g.
		// "X mows down a teammate", "X checks his glasses" — so the
		// victim-prefix obituary scan below can never attribute those
		// deaths to a victim. The protocol DeathEvent (health transition
		// / DF_DEAD, deduped in the parser) fires for every death
		// regardless of the message, so it's the authoritative death
		// signal. Gate it to match time with the same boundary the
		// timeline uses, and resolve the slot to a player in Finalize via
		// the reconnect-aware identity table.
		if a.timing.Started && !a.timing.Ended {
			a.deathSlots = append(a.deathSlots, slotDeath{slot: e.PlayerNum, tMs: e.TimeMs})
		}
	}
	return nil
}

// handleObituaryPrint mines a print line for kill / weapon attribution
// (the frag log + per-killer kills). Victim death counting is NOT done
// here — see the DeathEvent case in OnEvent for why deaths come from the
// protocol signal instead.
func (a *FragAnalyzer) handleObituaryPrint(e *events.PrintEvent) {
	// Obituary messages are typically at level 1 (PRINT_MEDIUM) in MVD
	// But we'll check levels 1, 2, and 3 to be safe
	if e.Level > 3 {
		return
	}

	frag := a.parseObituary(e.Message, e.Time)
	if frag == nil {
		return
	}

	// Skip generic killers/victims that can't be resolved
	killerIsGeneric := isGenericPlayer(frag.Killer)
	victimIsGeneric := isGenericPlayer(frag.Victim)

	// Only add to frags list if both parties are identifiable
	if !killerIsGeneric && !victimIsGeneric {
		a.frags = append(a.frags, *frag)
	} else if frag.IsTeamKill && !killerIsGeneric && victimIsGeneric {
		// Killer-named teamkill: attacker known, victim generic. Stash for
		// Finalize to count + recover the victim from the DeathEvent.
		a.genericTeamkills = append(a.genericTeamkills, *frag)
	} else if frag.IsTeamKill && killerIsGeneric && !victimIsGeneric {
		// Victim-named teamkill: victim known, attacker generic. Stash for
		// the post-processor to recover the killer (position + frag-delta).
		a.victimNamedTeamkills = append(a.victimNamedTeamkills, *frag)
	}

	// Global per-weapon tally is enemy kills only — exclude suicides and
	// teamkills so a weapon self-detonation (now under its real weapon,
	// rl/gl/lg) doesn't inflate that weapon's kills.
	if !frag.IsSuicide && !frag.IsTeamKill {
		a.byWeapon[frag.Weapon]++
	}

	// Update killer stats
	// Don't count teamkills as kills (teamkiller loses a frag, doesn't gain one)
	if !frag.IsSuicide && !frag.IsTeamKill && !killerIsGeneric {
		killer := a.getOrCreatePlayer(frag.Killer)
		killer.Kills++
		killer.ByWeapon[frag.Weapon]++
	}
}

func (a *FragAnalyzer) Finalize(result *Result) error {
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

	// Attribute authoritative match-time deaths to players. Sourced from
	// the protocol DeathEvent (see OnEvent), resolved to the reconnect-
	// unified identity that held the slot at death time — so a player's
	// deaths across a reconnect fold into one name, and teamkill victims
	// (whose obituary names only the attacker) are still counted.
	for _, d := range a.deathSlots {
		if name := a.resolveDeathName(d.slot, d.tMs); name != "" && !isGenericPlayer(name) {
			a.getOrCreatePlayer(name).Deaths++
		}
	}

	a.recoverTeamkills()

	result.Frags = &FragResult{
		TotalFrags: len(a.frags),
		Frags:      a.frags,
		ByWeapon:   a.byWeapon,
		ByPlayer:   a.byPlayer,
	}
	return nil
}

// recoverTeamkills counts each killer-named teamkill against its killer
// and recovers the victim by pairing the obituary with the authoritative
// DeathEvent it caused — a death at ~the same time whose victim resolves
// to a teammate of the killer. Recovered teamkills (now a complete
// killer↔victim pair) rejoin the frag log. Death totals are untouched:
// the death was already counted in the deathSlots loop above.
//
// A death is only eligible if it isn't already explained by a named-victim
// frag, so a teamkill can't steal a regular kill's death; each death is
// consumed at most once. Resolution needs core (team table), so this is a
// no-op without it — the count is then simply not recovered.
func (a *FragAnalyzer) recoverTeamkills() {
	if a.core == nil || len(a.genericTeamkills) == 0 {
		return
	}

	// Pre-resolve every death once and mark those already claimed by a
	// named-victim frag at ~the same time.
	type rd struct {
		name string
		tMs  int32
	}
	resolved := make([]rd, len(a.deathSlots))
	claimed := make([]bool, len(a.deathSlots))
	for i, d := range a.deathSlots {
		name := a.resolveDeathName(d.slot, d.tMs)
		resolved[i] = rd{name: name, tMs: d.tMs}
		if name == "" {
			continue
		}
		for _, f := range a.frags {
			if f.Victim == name && absI32(f.Time-d.tMs) <= teamkillMatchWindowMs {
				claimed[i] = true
				break
			}
		}
	}

	for _, tk := range a.genericTeamkills {
		a.getOrCreatePlayer(tk.Killer).TeamKills++
		killerTeam := a.core.Names.TeamForName(tk.Killer)

		best, bestGap := -1, teamkillMatchWindowMs+1
		for i := range resolved {
			if claimed[i] || resolved[i].name == "" ||
				resolved[i].name == tk.Killer || isGenericPlayer(resolved[i].name) {
				continue
			}
			// Require same team when both teams resolve; stay lenient when
			// the victim's team is unknown.
			if killerTeam != "" {
				if vt := a.core.Names.TeamForName(resolved[i].name); vt != "" && vt != killerTeam {
					continue
				}
			}
			if gap := absI32(resolved[i].tMs - tk.Time); gap < bestGap {
				bestGap, best = gap, i
			}
		}
		if best >= 0 {
			claimed[best] = true
			entry := tk
			entry.Victim = resolved[best].name
			// "X gets a frag for the other team" sets IsSuicide on the
			// killer-self frag-log convention; once we know the real victim
			// it's a teamkill, not a suicide (killer != victim).
			entry.IsSuicide = false
			a.frags = append(a.frags, entry)
		}
	}

	// Appended teamkills break the time ordering consumers assume (score
	// timeline, binary search); restore it. Stable so equal-time entries
	// keep their relative order.
	sort.SliceStable(a.frags, func(i, j int) bool { return a.frags[i].Time < a.frags[j].Time })
}

func absI32(x int32) int32 {
	if x < 0 {
		return -x
	}
	return x
}

func absF32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// resolveDeathName maps a death's wire slot to the canonical player
// identity active at the death time, falling back to the live userinfo
// name when no identity / demoinfo entry covers the slot. SlotIdentityAt
// is nil-safe, so this also works for registries without the identity or
// demoinfo analysers wired up.
func (a *FragAnalyzer) resolveDeathName(slot int, tMs int32) string {
	if name := a.core.SlotIdentityAt(slot, tMs).Name; name != "" {
		return name
	}
	if slot >= 0 && slot < len(a.ctx.Players) {
		if p := a.ctx.Players[slot]; p != nil {
			return p.Name
		}
	}
	return ""
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
		// The /kill console command (dtSUICIDE, −2 frags). "suicide" is
		// reserved for this — every other self-kill keeps the weapon/cause
		// that produced it (with IsSuicide set), so consumers can tell a
		// real /kill from a weapon self-detonation. IsSuicide already keeps
		// these out of per-weapon *kill* counts (see handleObituaryPrint).
		{" suicides", "suicide"},

		// Rocket Launcher self-damage (from KTX client.c)
		{" discovers blast radius", "rl"},
		// KTX catch-all self-kill of unknown cause (client.c:5254). Must
		// precede the shorter " becomes bored with life" substring it
		// contains; cause unknown, so it stays "suicide".
		{" somehow becomes bored with life", "suicide"},
		{" becomes bored with life", "rl"},

		// Grenade Launcher self-damage
		{" tries to put the pin back in", "gl"},

		// Lightning Gun discharge self-damage
		{" electrocutes himself", "lg"},
		{" electrocutes herself", "lg"},
		{" heats up the water", "lg"},
		{" discharges into the water", "lg"},
		{" discharges into the slime", "lg"},
		{" discharges into the lava", "lg"},

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

		// KTX k_spawnicide variants (ktx/src/client.c:5164, dtTELE4).
		// Only emitted when k_spawnicide is enabled; the regular
		// telefrag print is used otherwise. Counted as a suicide for
		// scoreboard purposes (KTX logfrag(targ, targ)).
		{" couldn't resist the shiny spawn point", "tele"},
		{" got too close to the baby factory", "tele"},
		{" was fragged by poor life choices", "tele"},
	}

	for _, p := range suicidePatterns {
		if idx := strings.Index(msg, p.pattern); idx > 0 {
			victim := strings.TrimSpace(msg[:idx])
			if victim != "" {
				return &FragEntry{
					Time:      msTime(time),
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

		// CRMod obituary variants. CRMod added a parallel "X_FRAGGED_BY_Y"
		// table on top of KTX's fragfile; servers running the CR ruleset
		// still emit these and KTX retains them. Suffix-based weapon
		// disambiguation happens via obituaryWeapons / extractKillerName,
		// except for " was blown to chunks by " which is shared between
		// rl ("'s rocket") and gl ("'s grenade") and is fixed up below.
		{" was disembowled by ", "sg"},     // [sic] CRMod misspelling; suffix "'s shotgun"
		{" eats 2 scoops of ", "ssg"},      // suffix "'s lead shot"
		{" is shish-kebabed by ", "rl"},    // suffix "'s rocket"
		{" was blown to chunks by ", "rl"}, // suffix "'s rocket" — fixed up to gl when suffix is "'s grenade"
		{" gets intimate with ", "gl"},     // suffix "'s grenade"
		{" gets a warm fuzzy feeling from ", "lg"}, // no weapon suffix; rest is just the killer name

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

			weapon := p.weapon
			// "X was blown to chunks by Y's rocket" (rl) vs "X was
			// blown to chunks by Y's grenade" (gl) share the same
			// verb — disambiguate via the suffix, same shape as
			// checkGibbedBy below.
			if p.pattern == " was blown to chunks by " {
				if strings.Contains(rest, "'s grenade") || strings.HasSuffix(strings.TrimSpace(rest), "' grenade") {
					weapon = "gl"
				}
			}

			if victim != "" && killer != "" {
				// Check for team kill
				isTeamKill := a.isTeamKill(victim, killer)

				return &FragEntry{
					Time:       msTime(time),
					Killer:     killer,
					Victim:     victim,
					Weapon:     weapon,
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

	// Satan's-power deflection (KTX dtTELE2 — ktx/src/client.c:5141).
	// Infix-form obit where the victim's name sits between the
	// "Satan's power deflects " prefix and the "'s telefrag" suffix.
	// The dying player is the one who attempted the telefrag; KTX
	// books this as a self-attributed suicide (logfrag(targ, targ)).
	if frag := a.checkSatanDeflect(msg, time); frag != nil {
		return frag
	}

	return nil
}

// checkSatanDeflect handles the "Satan's power deflects X's telefrag"
// obit — see KTX ktx/src/client.c:5141.
func (a *FragAnalyzer) checkSatanDeflect(msg string, time float64) *FragEntry {
	victim := satanDeflectVictim(msg)
	if victim == "" {
		return nil
	}
	return &FragEntry{
		Time:      msTime(time),
		Killer:    victim,
		Victim:    victim,
		Weapon:    "tele",
		IsSuicide: true,
	}
}

// satanDeflectVictim returns the dying player's name for a dtTELE2
// "Satan's power deflects X's telefrag" obituary (a self-telefrag booked
// as a suicide, ktx/src/client.c:5141), or "" if msg isn't that form. The
// victim sits between a fixed prefix and suffix (infix), so prefix-based
// suicide scans miss it. Shared by the messages analyzer.
func satanDeflectVictim(msg string) string {
	const prefix = "Satan's power deflects "
	const suffix = "'s telefrag"
	if !strings.HasPrefix(msg, prefix) {
		return ""
	}
	rest := msg[len(prefix):]
	end := strings.Index(rest, suffix)
	if end <= 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
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
				Time:       msTime(time),
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
				Time:       msTime(time),
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
					Time:       msTime(time),
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
				Time:       msTime(time),
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
				Time:       msTime(time),
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
		Time:       msTime(time),
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
					Time:       msTime(time),
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
					Time:       msTime(time),
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
