package analyzer

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// DamageAnalyzer reconstructs per-hit damage and its aggregates from the
// KTX mvdhidden_dmgdone stream (events.DamageEvent). It mirrors the frag
// analyzer: raw events are collected during OnEvent and resolved to player
// identities in Finalize via CoreOutputs.
//
// Both the aggregates (Given/Taken/Matrix/ByWeapon/EWep buckets) AND the
// per-hit Events log are gated to match time, matching KTX's scoreboard
// semantics so the reconciliation against demoInfo.players[].dmg is
// meaningful. Out-of-match (warmup / post-match) damage is dropped at the
// source — it is not exposed anywhere and consumers never need to re-window.
type DamageAnalyzer struct {
	ctx    *Context
	core   *CoreOutputs
	timing MatchTimingDetector

	// items tracks each wire slot's current weapon bitfield (StatItems),
	// so a DamageEvent can be classified by the VICTIM's held weapons at
	// hit time (KTX "ewep" semantics — see ktx/src/combat.c:1084-1089).
	items map[int]int

	// vitals tracks each wire slot's health and armor value for the bounded
	// reconstruction. The wire carries only the UNBOUND damage; the bounded
	// (KTX-scoreboard) value is re-derived per hit from the victim's pre-hit
	// state. KTX multicasts dmgdone mid-frame inside T_Damage while stat
	// broadcasts land at end of frame, so the last-seen stat at DamageEvent
	// time IS the pre-hit value — the same wire-order guarantee the items
	// snapshot above relies on. Between authoritative stat updates the shadow
	// decrement in OnEvent keeps same-frame multi-hits sequentially capped;
	// every accepted stat update overwrites (checkpoints) the shadow value,
	// so any drift self-corrects within a frame.
	vitals map[int]*slotVitals

	// serverInfo collects the serverinfo cvars (fullserverinfo stufftext +
	// mid-game key updates, same sources as MetadataAnalyzer) that the
	// bounded arithmetic depends on: teamplay for the KTX team-damage
	// nullification rules, and k_midair / k_instagib / k_dmgfrags to detect
	// modes whose T_Damage rewrites are not reconstructable from the wire.
	serverInfo map[string]string

	raw []rawDamage
}

// slotVitals is one slot's tracked health/armor. known distinguishes "never
// saw a stat for this slot" (snapshot falls back to the 100/0 spawn state)
// from a legitimately tracked value.
type slotVitals struct {
	health int
	armor  int
	known  bool
}

// rawDamage is one mvdhidden_dmgdone record pinned to wire slots + time,
// plus the victim's weapon bitfield and vitals snapshots. The match-time
// gate is applied in Finalize from the demo-clock timestamp (tMs), not
// sampled here, to avoid the match-start-frame race (see Finalize).
// Names/teams are resolved in Finalize too.
type rawDamage struct {
	attacker     int // wire slot, or -1 for world / non-player inflictor
	victim       int // wire slot
	damage       int
	deathType    int
	isSplash     bool
	tMs          int32
	victimItem   int // victim's StatItems bitfield at hit time
	victimHealth int // victim's pre-hit health (shadow-tracked)
	victimArmor  int // victim's pre-hit armor value (shadow-tracked)
}

// NewDamageAnalyzer creates a new damage analyzer.
func NewDamageAnalyzer() *DamageAnalyzer {
	return &DamageAnalyzer{
		items:      make(map[int]int),
		vitals:     make(map[int]*slotVitals),
		serverInfo: make(map[string]string),
	}
}

func (a *DamageAnalyzer) Name() string { return "damage" }

func (a *DamageAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

// UseCoreOutputs is part of the CoreConsumer contract — Damage consumes
// co for slot→identity+team resolution and co.DemoInfo for the
// scoreboard cross-check.
func (a *DamageAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

func (a *DamageAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.PrintEvent:
		a.timing.OnPrint(e)
	case *events.IntermissionEvent:
		a.timing.OnIntermission(e.Time)
	case *events.StuffTextEvent:
		// Serverinfo capture for the bounded arithmetic (teamplay,
		// k_midair/k_instagib/k_dmgfrags) — same sources as MetadataAnalyzer.
		// Captured here rather than read from CoreOutputs because the shadow
		// decrement below runs during OnEvent, before Finalize wiring.
		if strings.HasPrefix(e.Command, "fullserverinfo ") {
			for k, v := range parseInfoString(e.Command) {
				a.serverInfo[k] = v
			}
		}
	case *events.ServerInfoEvent:
		if e.Key != "" {
			a.serverInfo[e.Key] = e.Value
		}
	case *events.StatUpdateEvent:
		switch e.StatIndex {
		case events.StatItems:
			// Track weapon inventory ungated so a victim's loadout is known
			// from the first stat update, regardless of match phase.
			a.items[e.PlayerNum] = e.Value
		case events.StatHealth:
			// Authoritative checkpoint for the vitals shadow. KTX reuses the
			// health stat as a damage indicator (1000+damage, combat.c:1001);
			// only plausible values are real health (≤ 250, the mega cap;
			// negative death values are genuine). Same filter as timeline.go.
			if e.Value <= 250 {
				a.vitalsFor(e.PlayerNum).health = e.Value
			}
		case events.StatArmor:
			// Real armor caps at 200 (RA); larger values are KTX feedback
			// sentinels. Same filter as timeline.go.
			if e.Value <= 200 && e.Value >= 0 {
				a.vitalsFor(e.PlayerNum).armor = e.Value
			}
		}
	case *events.DamageEvent:
		v := a.vitalsFor(e.Victim)
		a.raw = append(a.raw, rawDamage{
			attacker:     e.Attacker,
			victim:       e.Victim,
			damage:       e.Damage,
			deathType:    e.DeathType,
			isSplash:     e.IsSplash,
			tMs:          msTime(e.Time),
			victimItem:   a.items[e.Victim],
			victimHealth: v.health,
			victimArmor:  v.armor,
		})
		// Shadow decrement so a same-frame follow-up hit (no stat update in
		// between — stats broadcast at end of frame) sees sequentially
		// reduced vitals. Mirrors KTX: armor is only consumed in a live
		// match (combat.c:636-639). Teamplay nullification is deliberately
		// NOT modeled here — team classification needs Finalize's identity
		// resolution; the rare same-frame drift on a tp1/3-nullified hit is
		// corrected by the next end-of-frame stat checkpoint.
		if a.timing.Started && !a.timing.Ended {
			if isTeleDeathType(e.DeathType) {
				// Telefrag: armor fully consumed, health overwhelmed.
				v.armor = 0
				v.health -= 50000
			} else {
				save, take := damageSplit(e.Damage, a.items[e.Victim], v.armor)
				if a.items[e.Victim]&events.ITInvulnerability != 0 && e.DeathType != events.DtSuicide {
					take = 0 // pent: armor still consumed, health untouched
				}
				v.armor -= save
				v.health -= take
			}
		}
	}
	return nil
}

// vitalsFor returns the tracked vitals for a slot, creating the entry at the
// 100/0 spawn state when no stat has been seen yet.
func (a *DamageAnalyzer) vitalsFor(slot int) *slotVitals {
	v, ok := a.vitals[slot]
	if !ok {
		v = &slotVitals{health: 100}
		a.vitals[slot] = v
	}
	return v
}

// isTeleDeathType reports whether dt is one of the four KTX telefrag
// deathtypes (dtTELE1..4 — normal, pent-deflect, pent-vs-pent, unused).
func isTeleDeathType(dt int) bool {
	return dt >= events.DtTele1 && dt <= events.DtTele4
}

func (a *DamageAnalyzer) Finalize(result *Result) error {
	if len(a.raw) == 0 {
		return nil
	}

	out := &DamageResult{
		ByWeapon: make(map[string]int),
		ByPlayer: make(map[string]*PlayerDamage),
	}
	// matrix is keyed by attacker\x00victim for stable aggregation, then
	// flattened + sorted for deterministic output.
	matrix := make(map[string]*DamagePair)

	// In a 1v1 any non-self hit is enemy damage by definition, but two
	// duelers sharing a non-empty colour team would classify every hit as
	// IsTeam — silently emptying airgibs, zeroing the aim enemy splits and
	// contradicting the duel-classified Shots.VictimKinds (F20). Read the duel
	// verdict from the roster (the shared CoreOutputs table every producer reads), so
	// the victim-weapon buckets and the matrix are built once, correctly,
	// instead of being rebuilt after the fact.
	duel := a.core.IsDuel()

	// Bounded reconstruction setup. boundedSkip names a server mode whose
	// T_Damage rewrites are not observable per hit (see boundedSkipReason);
	// when set, no bounded figure is produced anywhere. tp mirrors KTX
	// tp_num() (g_utils.c:1586): the raw teamplay cvar counts only in team
	// modes — a duel's colour-team artifact must not trigger the teamplay
	// nullification rules.
	boundedSkip := a.boundedSkipReason()
	tp := 0
	if !duel {
		tp, _ = strconv.Atoi(a.serverInfo["teamplay"])
	}
	// enemyTakenBounded feeds DamageDeltaBounded.StreamTaken: KTX dmg_t
	// accumulates only in the enemy branch (combat.c:1069), unlike our
	// all-sources Taken.
	enemyTakenBounded := make(map[string]int)

	// Match window on the demo clock. Gate on the timestamp range, not the live
	// match-phase flag sampled in OnEvent: a DamageEvent on the match-start
	// frame is decoded before the same-frame "Fight" print that flips the
	// detector, so the flag was still false and the hit — e.g. a telefrag at
	// match-relative t=0 — was wrongly dropped (v50 start-frame race). The
	// window keeps every hit whose demo time lands in [start, end], from the
	// detector's final state: not started keeps nothing (aborted demos
	// unchanged); started with no detected end (demo cut before intermission) is
	// unbounded above, so late in-match hits survive as they did under the flag.
	started := a.timing.Started
	matchStartMs := msTime(a.timing.StartTime)
	ended := a.timing.Ended
	matchEndMs := msTime(a.timing.EndTime)
	inMatchWindow := func(tMs int32) bool {
		if !started || tMs < matchStartMs {
			return false
		}
		return !ended || tMs <= matchEndMs
	}

	for _, d := range a.raw {
		isWorld := d.attacker < 0
		isSelf := !isWorld && d.attacker == d.victim
		isEnv := isWorld || events.IsEnvironmentalDamage(d.deathType)

		attacker := ""
		var attackerTeam string
		if !isWorld {
			id := a.resolveAt(d.attacker, d.tMs)
			attacker, attackerTeam = id.Name, id.Team
		} else {
			attacker = "world"
		}
		victimID := a.resolveAt(d.victim, d.tMs)
		victim, victimTeam := victimID.Name, victimID.Team
		if victim == "" {
			// Can't attribute the hit to a known victim; skip rather than
			// inventing a slot-numbered name.
			continue
		}

		weapon := events.DeathTypeToWeapon(d.deathType)
		isTele := weapon == "tele"
		isStomp := weapon == "stomp"
		if isEnv && !isTele && !isStomp {
			if env := events.EnvironmentalDamageType(d.deathType); env != "" {
				weapon = env
			}
		}

		isTeam := !duel && !isWorld && !isSelf && attackerTeam != "" &&
			victimTeam != "" && attackerTeam == victimTeam

		// Telefrags and stomps are positional instant kills, not weapon
		// damage — a telefrag is the 9999 sentinel, a stomp is a movement
		// kill. Keep them out of every damage figure and surface them on
		// their own. The kill itself is still in FragResult.
		if isTele || isStomp {
			// Positional instant kills are match-only, like all damage output:
			// out-of-match telefrags/stomps are dropped everywhere (of no
			// interest, unreconcilable). Team telefrags/stomps are not credited
			// to the attacker, mirroring the team-kill convention (and matching
			// view.Damage's recompute).
			if !inMatchWindow(d.tMs) {
				continue
			}
			kill := PositionalKill{Time: d.tMs, Attacker: attacker, Victim: victim, IsTeam: isTeam}
			credit := !isWorld && !isSelf && !isTeam
			if isTele {
				out.Telefrags = append(out.Telefrags, kill)
				if credit {
					getOrCreateDamage(out.ByPlayer, attacker).Telefrags++
				}
			} else {
				out.Stomps = append(out.Stomps, kill)
				if credit {
					getOrCreateDamage(out.ByPlayer, attacker).Stomps++
				}
			}
			continue
		}

		// Everything below — the per-hit Events log AND the aggregates — is
		// match-time only. Out-of-match (warmup / post-match) damage is not
		// exposed anywhere: it can't be reconciled against KTX's scoreboard
		// and downstream consumers (aim splits, airgib detection) would only
		// ever want to filter it back out. Drop it at the source so every
		// damage figure and the Events log agree.
		if !inMatchWindow(d.tMs) {
			continue
		}

		vw := ""
		if !isWorld && !isSelf && !isTeam {
			vw = victimWeaponClass(d.victimItem)
		}

		entry := DamageEntry{
			Time:      d.tMs,
			Attacker:  attacker,
			Victim:    victim,
			Weapon:    weapon,
			Damage:    d.damage,
			IsSplash:  d.isSplash,
			IsEnv:     isEnv,
			IsSelf:    isSelf,
			IsTeam:    isTeam,
			VictimWep: vw,
		}

		// Bounded reconstruction for this hit (KTX dmg_dealt semantics).
		// Omitted from the entry when equal to the raw value — the common
		// non-overkill case — so the log only grows where the families
		// actually differ.
		b := 0
		if boundedSkip == "" {
			b = boundedDamage(d, tp, isTeam, isSelf)
			if b != d.damage {
				bv := b
				entry.Bounded = &bv
			}
		}
		out.Events = append(out.Events, entry)

		out.TotalDamage += d.damage

		// Victim's damage-taken (all sources).
		vp := getOrCreateDamage(out.ByPlayer, victim)
		vp.Taken += d.damage
		if isEnv {
			vp.TakenEnv += d.damage
		}
		if boundedSkip == "" {
			vb := boundedNest(vp)
			vb.Taken += b
			if isEnv {
				vb.TakenEnv += b
			}
		}

		if isWorld {
			continue // no attacker to credit
		}

		ap := getOrCreateDamage(out.ByPlayer, attacker)
		switch {
		case isSelf:
			ap.GivenSelf += d.damage
			if boundedSkip == "" {
				boundedNest(ap).GivenSelf += b
			}
		case isTeam:
			ap.GivenTeam += d.damage
			if boundedSkip == "" {
				boundedNest(ap).GivenTeam += b
			}
		default:
			// Enemy damage — the "useful" number.
			ap.Given += d.damage
			ap.ByWeapon[weapon] += d.damage
			out.ByWeapon[weapon] += d.damage
			addToMatrix(matrix, attacker, victim, weapon, d.damage)
			addVictimWeaponBucket(ap, vw, d.damage)
			if boundedSkip == "" {
				ab := boundedNest(ap)
				ab.Given += b
				ab.ByWeapon[weapon] += b
				addVictimWeaponBucket(ab, vw, b)
				enemyTakenBounded[victim] += b
			}
		}
	}

	out.Matrix = flattenMatrix(matrix)
	out.Scoreboard = a.reconcile(out.ByPlayer, enemyTakenBounded, boundedSkip == "")
	if boundedSkip == "" {
		out.Dmg = "both"
		out.BoundedMode = "standard"
	} else {
		out.BoundedMode = "skipped:" + boundedSkip
	}

	result.Damage = out

	// Born-correct timestamps: rebase the whole damage log to the match clock.
	// Events, Telefrags and Stomps all carry a match-relative Time — the schema
	// (result/damage.go PositionalKill/DamageEntry) documents all three as
	// match-relative ms, the view from/to window (view/sections.go) and the
	// getEvents telefrag/stomp lens (view/events.go) compare them against
	// match-relative bounds, and no consumer reads them on the demo clock.
	// Identity resolution above used the demo-time d.tMs, so this runs last. The
	// shift equals matchStartMs (co.MatchStartMs() derives from the same
	// detector StartTime), so the gated in-match window rebases to [0, ...].
	if ms := a.core.MatchStartMs(); ms > 0 {
		for i := range out.Events {
			out.Events[i].Time -= ms
		}
		for i := range out.Telefrags {
			out.Telefrags[i].Time -= ms
		}
		for i := range out.Stomps {
			out.Stomps[i].Time -= ms
		}
	}
	return nil
}

// resolveAt maps a wire slot to its identity at tMs via the canonical
// ResolveSlotAt chain (session table → userinfo → name→team backfill).
func (a *DamageAnalyzer) resolveAt(slot int, tMs int32) SlotInfo {
	return ResolveSlotAt(a.core, a.ctx.Players, slot, tMs)
}

// victimWeaponClass classifies a victim's StatItems bitfield into the
// EWep buckets, keyed on the TARGET's inventory (KTX combat.c:1084-1089).
// Priority RL+LG > RL > LG > mid > sg; NG counts as shotgun-tier, not mid.
func victimWeaponClass(items int) string {
	hasRL := items&events.ITRocketLauncher != 0
	hasLG := items&events.ITLightning != 0
	const midMask = events.ITSuperShotgun | events.ITSuperNailgun | events.ITGrenadeLauncher
	switch {
	case hasRL && hasLG:
		return "both"
	case hasRL:
		return "rl"
	case hasLG:
		return "lg"
	case items&midMask != 0:
		return "mid"
	default:
		return "sg"
	}
}

func addVictimWeaponBucket(p *PlayerDamage, class string, dmg int) {
	switch class {
	case "both":
		p.EnemyVsBoth += dmg
		p.EWep += dmg
	case "rl":
		p.EnemyVsRL += dmg
		p.EWep += dmg
	case "lg":
		p.EnemyVsLG += dmg
		p.EWep += dmg
	case "mid":
		p.EnemyVsMid += dmg
	default:
		p.EnemyVsSG += dmg
	}
}

func getOrCreateDamage(m map[string]*PlayerDamage, name string) *PlayerDamage {
	if p, ok := m[name]; ok {
		return p
	}
	p := &PlayerDamage{ByWeapon: make(map[string]int)}
	m[name] = p
	return p
}

// boundedNest lazily creates a player's bounded-family aggregate. The nest
// is itself a PlayerDamage (same field names, same helpers) with the
// invariant that its Telefrags/Stomps/Bounded stay zero/nil.
func boundedNest(p *PlayerDamage) *PlayerDamage {
	if p.Bounded == nil {
		p.Bounded = &PlayerDamage{ByWeapon: make(map[string]int)}
	}
	return p.Bounded
}

// boundedDamage reconstructs one hit's KTX-scoreboard value (dmg_dealt,
// ktx/src/combat.c:783): armor absorbed + health damage capped to the
// victim's remaining health, after the nullification rules the wire value
// deliberately ignores (virtual_take is captured pre-nullification at
// combat.c:719). Godmode is unobservable from the demo and ignored — it
// does not occur in real matches.
func boundedDamage(d rawDamage, tp int, isTeam, isSelf bool) int {
	if tp == 4 && isTeam {
		// tp4teamdmg: neither armor nor health is touched (combat.c:554,
		// 622-625, 749-752); only velocity applies. The wire still carries
		// save+virtual_take, so bounded is 0, not the wire value.
		return 0
	}
	save, take := damageSplit(d.damage, d.victimItem, d.victimArmor)
	// Nullification rules zero the health share only — armor was already
	// consumed above them (combat.c:620-639 precede 722-753). All are
	// skipped for dtSUICIDE (combat.c:722).
	if d.deathType != events.DtSuicide {
		switch {
		case d.victimItem&events.ITInvulnerability != 0:
			take = 0 // pent (combat.c:728-737)
		case tp == 1 && (isTeam || isSelf):
			take = 0 // tp1: no damage to mates or self (combat.c:738-748)
		case tp == 3 && isTeam:
			take = 0 // tp3: no damage to mates; self still takes
		}
	}
	h := d.victimHealth
	if h < 0 {
		h = 0 // hit on a corpse: only armor (if any) absorbs
	}
	if take > h {
		take = h // the overkill cap — the whole point of the bounded family
	}
	return save + take
}

// damageSplit mirrors T_Damage's armor absorption (ktx/src/combat.c:618-641):
// save is the armor-absorbed share of one wire damage value, capped at the
// victim's remaining armor; take is the health share. The wire value is
// save+take of the true float damage (each newceil'd), so re-deriving the
// split from the wire int instead of the unobservable float can differ by
// ±1 on armor-absorbing hits — the documented reconstruction slop.
func damageSplit(damage, victimItems, victimArmor int) (save, take int) {
	save = newceil(armorFraction(victimItems) * float64(damage))
	if save > victimArmor {
		save = victimArmor
	}
	if save < 0 {
		save = 0
	}
	return save, damage - save
}

// newceil mirrors KTX's QVM ceil shim (ktx/src/combat.c:353-356): ceiling
// with a 1e-3 truncation guard against float noise.
func newceil(f float64) int { return int(math.Ceil(math.Trunc(f*1000) / 1000)) }

// armorFraction maps the victim's armor item bit to KTX's armortype
// absorption fraction (GA 0.3 / YA 0.6 / RA 0.8).
func armorFraction(items int) float64 {
	switch {
	case items&events.ITArmor3 != 0:
		return 0.8
	case items&events.ITArmor2 != 0:
		return 0.6
	case items&events.ITArmor1 != 0:
		return 0.3
	}
	return 0
}

// boundedSkipReason names the server mode that makes the bounded
// reconstruction impossible, or "" when the standard arithmetic applies.
// k_midair rewrites take from the victim's height above ground (combat.c:
// 644-694), k_instagib flattens it to 5000 (698-709), k_dmgfrags inverts
// the pent/telefrag accumulation (758-777) — none observable per hit.
func (a *DamageAnalyzer) boundedSkipReason() string {
	for _, m := range [...]struct{ cvar, mode string }{
		{"k_midair", "midair"},
		{"k_instagib", "instagib"},
		{"k_dmgfrags", "dmgfrags"},
	} {
		if v := a.serverInfo[m.cvar]; v != "" && v != "0" {
			return m.mode
		}
	}
	return ""
}

func addToMatrix(m map[string]*DamagePair, attacker, victim, weapon string, dmg int) {
	key := attacker + "\x00" + victim
	p, ok := m[key]
	if !ok {
		p = &DamagePair{Attacker: attacker, Victim: victim, ByWeapon: make(map[string]int)}
		m[key] = p
	}
	p.Damage += dmg
	p.ByWeapon[weapon] += dmg
}

func flattenMatrix(m map[string]*DamagePair) []DamagePair {
	out := make([]DamagePair, 0, len(m))
	for _, p := range m {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Attacker != out[j].Attacker {
			return out[i].Attacker < out[j].Attacker
		}
		return out[i].Victim < out[j].Victim
	})
	return out
}

// reconcile cross-checks the stream-derived per-player totals against the
// KTX end-of-match scoreboard. Diagnostic only — divergence is reported,
// never used to adjust the stream-derived numbers. When the bounded family
// was reconstructed, each delta also pairs it against the same scoreboard
// (near-equality is the reconstruction's correctness signal; the raw side
// keeps its expected overkill gap).
func (a *DamageAnalyzer) reconcile(byPlayer map[string]*PlayerDamage, enemyTakenBounded map[string]int, bounded bool) *DamageReconciliation {
	if a.core == nil || a.core.DemoInfo == nil || len(a.core.DemoInfo.Players) == 0 {
		return nil
	}
	rec := &DamageReconciliation{ByPlayer: make(map[string]*DamageDelta)}
	for _, p := range a.core.DemoInfo.Players {
		if p.Dmg == nil {
			continue
		}
		d := &DamageDelta{
			ScoreGiven: p.Dmg.Given,
			ScoreTaken: p.Dmg.Taken,
			ScoreEWep:  p.Dmg.EnemyWeapons,
		}
		pd := byPlayer[p.Name]
		if pd != nil {
			d.StreamGiven = pd.Given
			d.StreamTaken = pd.Taken
			d.StreamEWep = pd.EWep
		}
		if bounded {
			db := &DamageDeltaBounded{
				StreamTaken: enemyTakenBounded[p.Name],
				ScoreTeam:   p.Dmg.Team,
			}
			if pd != nil && pd.Bounded != nil {
				db.StreamGiven = pd.Bounded.Given
				db.StreamEWep = pd.Bounded.EWep
				db.StreamTeam = pd.Bounded.GivenTeam
			}
			d.Bounded = db
		}
		rec.ByPlayer[p.Name] = d
	}
	return rec
}
