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
	// KTX emits every obituary at PRINT_MEDIUM (level 1) — the whole
	// ClientObituary broadcast region does (ktx/src/client.c:5100–5720, all
	// G_bprint(PRINT_MEDIUM, ...); ktx/include/g_consts.h documents
	// PRINT_MEDIUM=1 as "death messages"). Accept levels 0–2 (bprint range)
	// but reject PRINT_CHAT (level 3): a say/say_team line containing an
	// obituary verb like " rides " must not inject a phantom frag.
	if e.Level > 2 {
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
// identity active at the death time via the shared ResolveSlotAt chain
// (session table → userinfo name). Only the name is used; the name→team
// backfill in ResolveSlotAt doesn't affect it.
func (a *FragAnalyzer) resolveDeathName(slot int, tMs int32) string {
	return ResolveSlotAt(a.core, a.ctx.Players, slot, tMs).Name
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

// parseObituary parses a print message as a frag, mapping the shared neutral
// obituary parse (obituary_parse.go) onto a FragEntry. The frag side is the
// reference behavior for that parser; the mapping here reproduces what the
// old per-checker code did — IsSuicide comes straight from the parse, and a
// non-phrasing kill's teamkill flag is decided by the team-membership test
// against ctx.Players.
func (a *FragAnalyzer) parseObituary(msg string, time float64) *FragEntry {
	o := parseObituaryLine(msg)
	if o == nil {
		return nil
	}
	f := &FragEntry{
		Time:      msTime(time),
		Killer:    o.Killer,
		Victim:    o.Victim,
		Weapon:    o.Weapon,
		IsSuicide: o.Suicide,
	}
	switch {
	case o.TeamKill:
		// Phrasing-based teamkill ("X loses another friend", "X was
		// telefragged by his teammate") — the obituary itself asserts it.
		f.IsTeamKill = true
	case !o.Suicide:
		// Regular kill — teamkill iff killer and victim resolve to the same
		// team in the live userinfo table.
		f.IsTeamKill = a.isTeamKill(o.Victim, o.Killer)
	}
	return f
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
