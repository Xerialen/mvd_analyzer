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

	// vitals tracks each wire slot's health and armor value. Under the v55
	// death-value model the NORMAL-hit bounded value no longer reads shadow
	// health (a survived hit is bounded == raw by identity; a killing hit's
	// overkill comes from the end-of-frame death broadcast, see deaths). The
	// shadow is kept ONLY for the paths that still need it: the armor share
	// (save) for pent/tp-nullified hits and the cascade floor, and the
	// telefrag reconstruction's armor+remaining-health + its respawn
	// inference (a non-positive health shadow on a live tele victim means the
	// respawn beat the end-of-frame stat broadcast). KTX multicasts dmgdone
	// mid-frame inside T_Damage while stat broadcasts land at end of frame,
	// so the last-seen stat at DamageEvent time is the pre-hit value; the
	// per-hit shadow decrement in OnEvent keeps same-frame multi-hits'
	// armor/health sequential, and every accepted stat update checkpoints it.
	vitals map[int]*slotVitals

	// deaths records, per wire slot, the end-of-frame StatHealth broadcasts
	// that carry a death value (<= 0 — KTX sets health to the negative
	// leftover, or -1 when it lands exactly on 0; combat.c:983-985). This is
	// the exact overkill signal the wire otherwise hides: for a killing hit
	// bounded = raw + deathValue (the armor share cancels out of the
	// save+take identity). Empirically the death broadcast shares the
	// killing DamageEvent's demo timestamp exactly (10923/10923 across the
	// corpus), so hits are matched to a marker by identical tMs.
	deaths map[int][]deathMarker

	// deathFrames records, per wire slot, the demo timestamps of authoritative
	// DeathEvents. It is the masked-death fallback: a tight death→respawn cycle
	// broadcasts the respawn's positive health at end of frame, hiding the
	// negative death value, but the obituary-driven DeathEvent still fires
	// (parser forceEmitDeath). A killing hit with a DeathEvent but no death
	// value is capped by the (approximate) shadow instead of left at raw.
	deathFrames map[int][]int32

	// serverInfo collects the serverinfo cvars (fullserverinfo stufftext +
	// mid-game key updates, same sources as MetadataAnalyzer) that the
	// bounded arithmetic depends on: teamplay for the KTX team-damage
	// nullification rules, and k_midair / k_instagib / k_dmgfrags to detect
	// modes whose T_Damage rewrites are not reconstructable from the wire.
	serverInfo map[string]string

	raw []rawDamage
}

// slotVitals is one slot's tracked health/armor for the bounded
// reconstruction — shadow-decremented per hit and checkpointed by
// authoritative stat updates (see the analyzer's vitals field).
type slotVitals struct {
	health int
	armor  int
}

// deathMarker is one accepted end-of-frame StatHealth <= 0 broadcast: the
// victim's post-frame leftover health (never 0 — combat.c:985 coerces to
// -1), pinned to its demo timestamp. Consumed at most once, matched to the
// same-frame killing hit(s) by identical tMs.
type deathMarker struct {
	tMs   int32
	value int
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
	victimHealth int // victim's pre-hit health (shadow) — telefrag path + respawn inference only
	victimArmor  int // victim's pre-hit armor (shadow) — save share for pent/tp + cascade floor + telefrag
}

// NewDamageAnalyzer creates a new damage analyzer.
func NewDamageAnalyzer() *DamageAnalyzer {
	return &DamageAnalyzer{
		items:       make(map[int]int),
		vitals:      make(map[int]*slotVitals),
		deaths:      make(map[int][]deathMarker),
		deathFrames: make(map[int][]int32),
		serverInfo:  make(map[string]string),
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
		// Captured locally per the established per-analyzer convention
		// (weaponstay.go's OnStuffText/OnServerInfo pair does the same):
		// CoreOutputs carries no serverinfo to reuse, and these cvars are read
		// only in Finalize.
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
				// A non-positive accepted health is a death broadcast (the
				// negative leftover, or -1 for an exact-0 landing). Record it
				// as the frame's overkill signal for the bounded arithmetic.
				if e.Value <= 0 {
					a.deaths[e.PlayerNum] = append(a.deaths[e.PlayerNum],
						deathMarker{tMs: msTime(e.Time), value: e.Value})
				}
			}
		case events.StatArmor:
			// Real armor caps at 200 (RA); larger values are KTX feedback
			// sentinels. Same filter as timeline.go.
			if e.Value <= 200 && e.Value >= 0 {
				a.vitalsFor(e.PlayerNum).armor = e.Value
			}
		}
	case *events.DeathEvent:
		// Authoritative death signal (StatHealth edge or obituary). Used only
		// as the masked-death fallback when no death value was broadcast.
		a.deathFrames[e.PlayerNum] = append(a.deathFrames[e.PlayerNum], e.TimeMs)
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
	// tp_num() (g_utils.c:1586): the raw teamplay cvar counts ONLY in
	// team/CTF/coop modes — a duel's colour-team artifact, or an FFA demo
	// with a leftover teamplay cvar from a previous team game, must not
	// trigger the teamplay nullification rules. The KTX demoinfo mode
	// string carries the gt* verdict ("team"/"ctf" vs "duel"/"ffa"); with
	// no demoinfo we fall back to the non-duel gate alone (team
	// classification still requires matching non-empty team strings).
	boundedSkip := a.boundedSkipReason()
	tp := 0
	if !duel && a.tpModeApplies() {
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

	// Per-hit bounded values (v55 death-value model), indexed by raw hit.
	// Computed once up front so same-frame multi-hit deaths can cascade the
	// shared overkill across their hits (see computeBounded). Only produced
	// for the standard mode; nil when the bounded family is skipped.
	var boundedNew []int
	if boundedSkip == "" {
		boundedNew = a.computeBounded(tp, duel, inMatchWindow)
	}

	for i := range a.raw {
		d := a.raw[i]
		hc := a.classifyHit(d, duel)
		if hc.victim == "" {
			// Can't attribute the hit to a known victim; skip rather than
			// inventing a slot-numbered name.
			continue
		}
		isWorld, isSelf, isEnv := hc.isWorld, hc.isSelf, hc.isEnv
		isTele, isStomp, isTeam := hc.isTele, hc.isStomp, hc.isTeam
		attacker, victim, weapon := hc.attacker, hc.victim, hc.weapon

		// Telefrags and stomps are positional instant kills, not weapon
		// damage — a telefrag's wire value is the 9999 sentinel, a stomp is
		// a movement kill. They stay out of the Events log / ByWeapon /
		// Matrix / EWep / TotalDamage (KTX maps them to wpNONE, so its
		// weapons[].damage excludes them too) and are surfaced on their own.
		// The kill itself is still in FragResult.
		if isTele || isStomp {
			// Positional instant kills are match-only, like all damage output:
			// out-of-match telefrags/stomps are dropped everywhere (of no
			// interest, unreconcilable). Team telefrags/stomps are not credited
			// to the attacker COUNTER, mirroring the team-kill convention (and
			// matching view.Damage's recompute).
			if !inMatchWindow(d.tMs) {
				continue
			}
			kill := PositionalKill{Time: d.tMs, Attacker: attacker, Victim: victim, IsTeam: isTeam}

			// Their damage DOES fold into Given/GivenTeam/Taken in both
			// families — KTX's accumulation has no tele/stomp exclusion
			// (combat.c:1046-1076), which is exactly why demoInfo dmg.given/
			// team run above a fold-free reconstruction. The raw family
			// folds the bounded value for a telefrag (the wire 9999 is a
			// kill guarantee, not a measurement; armor + remaining health is
			// the only honest number) and the wire value for a stomp. Only
			// when the bounded reconstruction is active: in skipped modes
			// the shadow vitals these values depend on are polluted by the
			// unmodeled take rewrites, so v53 exclusion semantics apply.
			if boundedSkip == "" {
				// A tele/stomp victim is alive by definition (spawn_tdeath
				// and the stomp path only touch live players), so a
				// non-positive health shadow means the victim respawned
				// THIS frame and the respawn beat the end-of-frame stat
				// broadcast — the same wire-invisibility as the v51
				// match-start spawn. KTX saw the spawn state (100 health,
				// no armor, spawn inventory); the stale corpse values
				// mis-credit in both directions (dead health says 0,
				// pre-death armor says too much — both corpus-measured on
				// the dm3 spawn-deflects). Reconstruct from spawn state.
				dd := d
				if dd.victimHealth <= 0 {
					dd.victimHealth, dd.victimArmor, dd.victimItem = 100, 0, 0
				}
				var b, raw int
				if isTele {
					// Full armor consumed: take = newceil(50000 - save)
					// overwhelms every cap (combat.c:627-632). The teamplay
					// rules deliberately exclude TELEDEATH (combat.c:742,747)
					// so tp nullification never applies here; the pent rule
					// does NOT exclude it (combat.c:728-737), but a telefrag
					// on a pent holder deflects into dtTELE2 server-side —
					// the one wire-visible pent-victim telefrag is dtTELE3
					// (pent vs pent), where KTX zeroes the health share and
					// credits the armor alone.
					b = dd.victimArmor + dd.victimHealth
					if d.deathType == events.DtTele3 {
						b = dd.victimArmor
					}
					raw = b
				} else {
					// Stomp keeps the normal path under the death-value model
					// (its ~10 HP wire value is honest): bounded == raw unless
					// it killed, then raw + deathValue via computeBounded.
					b = boundedNew[i]
					raw = d.damage
				}
				bv := b
				kill.Bounded = &bv
				if raw != b {
					// Only a stomp can diverge (its raw fold is the wire
					// value); carried so the view's filtered recompute
					// reproduces the raw totals exactly.
					kill.Damage = raw
				}

				vp := getOrCreateDamage(out.ByPlayer, victim)
				vp.Taken += raw
				vp.BoundedNest().Taken += b
				if !isWorld {
					ap := getOrCreateDamage(out.ByPlayer, attacker)
					switch {
					case isSelf:
						ap.GivenSelf += raw
						ap.BoundedNest().GivenSelf += b
					case isTeam:
						ap.GivenTeam += raw
						ap.BoundedNest().GivenTeam += b
					default:
						ap.Given += raw
						ap.BoundedNest().Given += b
						// KTX's enemy branch accumulates dmg_eweapon with
						// no deathtype gate (combat.c:1073), so tele/stomp
						// damage lands in the EWep buckets when the victim
						// held RL/LG (corpus-measured: ewep under-reported
						// by exactly the tele bounded values without this).
						// It also keeps "EnemyVs* sums to Given" true now
						// that Given includes the fold.
						pvw := victimWeaponClass(dd.victimItem)
						addVictimWeaponBucket(ap, pvw, raw)
						addVictimWeaponBucket(ap.BoundedNest(), pvw, b)
						enemyTakenBounded[victim] += b
						// Record the victim-weapon class the enemy fold used so
						// the view's filtered recompute can reproduce the EWep
						// bucket fold exactly (view can't re-derive the victim's
						// hit-time inventory). Enemy branch only — team/self/world
						// telefrags don't touch the buckets.
						kill.VictimWep = pvw
					}
				}
			}

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
			b = boundedNew[i]
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
			vb := vp.BoundedNest()
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
				ap.BoundedNest().GivenSelf += b
			}
		case isTeam:
			ap.GivenTeam += d.damage
			if boundedSkip == "" {
				ap.BoundedNest().GivenTeam += b
			}
		default:
			// Enemy damage — the "useful" number.
			ap.Given += d.damage
			ap.ByWeapon[weapon] += d.damage
			out.ByWeapon[weapon] += d.damage
			addToMatrix(matrix, attacker, victim, weapon, d.damage)
			addVictimWeaponBucket(ap, vw, d.damage)
			if boundedSkip == "" {
				ab := ap.BoundedNest()
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

// hitInfo is one raw hit's resolved identity + classification, shared by the
// bounded pre-pass (computeBounded) and the aggregation loop so the two never
// diverge on how a hit is classified. victim == "" marks an unattributable
// hit (dropped).
type hitInfo struct {
	isWorld  bool
	isSelf   bool
	isEnv    bool
	isTele   bool
	isStomp  bool
	isTeam   bool
	attacker string
	victim   string
	weapon   string
}

// classifyHit resolves a raw hit's attacker/victim identities at hit time and
// derives the world/self/env/tele/stomp/team flags + the resolved weapon
// (environmental category folded in). duel forces enemy classification for a
// colour-team 1v1 (F20).
func (a *DamageAnalyzer) classifyHit(d rawDamage, duel bool) hitInfo {
	h := hitInfo{}
	h.isWorld = d.attacker < 0
	h.isSelf = !h.isWorld && d.attacker == d.victim
	h.isEnv = h.isWorld || events.IsEnvironmentalDamage(d.deathType)

	var attackerTeam string
	if !h.isWorld {
		id := a.resolveAt(d.attacker, d.tMs)
		h.attacker, attackerTeam = id.Name, id.Team
	} else {
		h.attacker = "world"
	}
	victimID := a.resolveAt(d.victim, d.tMs)
	h.victim = victimID.Name
	victimTeam := victimID.Team
	if h.victim == "" {
		return h
	}

	h.weapon = events.DeathTypeToWeapon(d.deathType)
	h.isTele = h.weapon == "tele"
	h.isStomp = h.weapon == "stomp"
	if h.isEnv && !h.isTele && !h.isStomp {
		if env := events.EnvironmentalDamageType(d.deathType); env != "" {
			h.weapon = env
		}
	}
	h.isTeam = !duel && !h.isWorld && !h.isSelf && attackerTeam != "" &&
		victimTeam != "" && attackerTeam == victimTeam
	return h
}

// computeBounded reconstructs each raw hit's KTX-scoreboard value (dmg_dealt,
// ktx/src/combat.c:783) under the v55 death-value model, returned indexed by
// raw hit. The wire carries save+virtual_take (unbound, combat.c:795); the
// bounded value differs only by the overkill on a KILLING hit, which the wire
// hides but the end-of-frame death broadcast reveals exactly:
//
//   - A survived hit has no overkill: bounded == raw, identically. No health
//     knowledge needed — only liveliness (no death this frame).
//   - A killing hit: bounded = raw + deathValue. Since raw = save+take and
//     bounded = save+min(take,health_pre) = save+health_pre while
//     deathValue = health_pre-take, the armor share cancels — the identity is
//     exact (modulo the -1 coercion when health lands exactly on 0, at most a
//     1-low residual on that hit).
//   - Same-frame multi-hit death: one deathValue covers the frame's hits.
//     Wire order is application order, so the overkill |deathValue| is
//     deducted from each hit's health share from LAST to FIRST, flooring each
//     hit's bounded at its save share (the one case where the save split —
//     re-derived from the wire int, approximate — enters a normal hit; it is
//     exact everywhere else because it cancels).
//
// Two wire limitations force an approximate shadow-cap fallback (see
// shadowFallback): KTX clamps a corpse's health at -99 (combat.c:257-260), so
// a death value AT that floor hides a deeper overkill; and a tight
// death→respawn broadcasts the respawn's positive health, masking the death
// value entirely (detected via the authoritative DeathEvent).
//
// The nullification paths are unchanged and do NOT read the death signal (a
// nullified hit deals no health damage, so it can't kill): a pent or tp1/tp3
// hit is bounded to its armor share (save), a tp4 team hit to 0, all skipped
// for dtSUICIDE (combat.c:722). Telefrags are handled separately (the wire
// 9999 clamp breaks the raw+deathValue identity) and excluded here.
func (a *DamageAnalyzer) computeBounded(tp int, duel bool, inMatchWindow func(int32) bool) []int {
	b := make([]int, len(a.raw))

	type frameKey struct {
		slot int
		t    int32
	}
	// Non-nullified normal-path hits grouped by (victim, frame) — the cascade
	// candidates. And the frames that also carry a telefrag, whose overwhelming
	// -50000 health sink would corrupt the deathValue; a normal hit sharing a
	// tele's frame landed before the tele killed, so it stays bounded == raw.
	groups := map[frameKey][]int{}
	teleFrames := map[frameKey]bool{}

	for i := range a.raw {
		d := a.raw[i]
		hc := a.classifyHit(d, duel)
		if hc.victim == "" || !inMatchWindow(d.tMs) {
			continue
		}
		key := frameKey{d.victim, d.tMs}
		if hc.isTele {
			teleFrames[key] = true
			continue
		}
		save, _ := damageSplit(d.damage, d.victimItem, d.victimArmor)

		// Nullification (mirrors T_Damage combat.c:620-753 minus the health
		// cap). The wire still carries save+virtual_take, so a nullified hit
		// is bounded to its armor share, not zero — except tp4, which touches
		// neither armor nor health.
		if tp == 4 && hc.isTeam {
			b[i] = 0
			continue
		}
		if d.deathType != events.DtSuicide {
			pent := d.victimItem&events.ITInvulnerability != 0
			if pent || (tp == 1 && (hc.isTeam || hc.isSelf)) || (tp == 3 && hc.isTeam) {
				b[i] = save // health share zeroed; armor still consumed
				continue
			}
		}

		// Normal path: bounded == raw unless a same-frame death caps it.
		b[i] = d.damage
		groups[key] = append(groups[key], i)
	}

	// shadowFallback caps each hit in a frame by the sequentially-decremented
	// shadow-health (the pre-v55 arithmetic): approximate, but bounded by the
	// victim's real capacity. Used when the exact death value is unavailable —
	// the -99 clamp or a masked (respawn-hidden) death.
	shadowFallback := func(idxs []int) {
		for _, i := range idxs {
			d := a.raw[i]
			save, take := damageSplit(d.damage, d.victimItem, d.victimArmor)
			h := d.victimHealth
			if h < 0 {
				h = 0
			}
			if take > h {
				take = h
			}
			b[i] = save + take
		}
	}

	deathByKey := map[frameKey]int{}
	for slot, ms := range a.deaths {
		for _, dm := range ms {
			deathByKey[frameKey{slot, dm.tMs}] = dm.value // one marker per (slot,frame)
		}
	}
	// Frames with an authoritative DeathEvent — the masked-death fallback.
	deathFrame := map[frameKey]bool{}
	for slot, ts := range a.deathFrames {
		for _, t := range ts {
			deathFrame[frameKey{slot, t}] = true
		}
	}

	for key, idxs := range groups {
		if teleFrames[key] {
			continue
		}
		dv, ok := deathByKey[key]
		if !ok {
			// No death value this frame. If an authoritative DeathEvent still
			// fired, a tight death→respawn hid the negative broadcast behind
			// the respawn's positive health — cap by the shadow rather than
			// leaving the killing hit at raw. Otherwise the victim survived,
			// so bounded == raw stands (the invisible-heal fix — a stale-low
			// shadow must NOT re-cap a survived hit).
			if deathFrame[key] {
				shadowFallback(idxs)
			}
			continue
		}
		delete(deathByKey, key) // consume at most once

		if dv <= deathClamp {
			// KTX clamps a corpse's health at -99 (Killed(), combat.c:257-260),
			// so an overkill deeper than 99 HP is unrecoverable from the wire —
			// raw + deathValue would over-credit by (overkill − 99). Fall back
			// to the shadow-health cap, bounded by the victim's real capacity.
			shadowFallback(idxs)
			continue
		}

		remaining := -dv // |deathValue| = the frame's total overkill (≤ 99)
		for j := len(idxs) - 1; j >= 0 && remaining > 0; j-- {
			i := idxs[j]
			d := a.raw[i]
			_, take := damageSplit(d.damage, d.victimItem, d.victimArmor)
			ded := take
			if ded > remaining {
				ded = remaining
			}
			b[i] = d.damage - ded // floors at the save share when ded == take
			remaining -= ded
		}
	}
	return b
}

// deathClamp is KTX's corpse-health floor (Killed(), ktx/src/combat.c:259):
// health below -99 is pinned to -99 before the end-of-frame stat broadcast, so
// a death value AT the floor may hide a deeper overkill and the exact raw +
// deathValue identity no longer holds.
const deathClamp = -99

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

// tpModeApplies reports whether the demo's mode lets the teamplay cvar
// count, mirroring tp_num()'s isTeam()||isCTF()||coop gate
// (ktx/src/g_utils.c:1586). The KTX demoinfo mode string carries the
// verdict; without demoinfo we can't tell and let the caller's other
// gates decide (returns true).
func (a *DamageAnalyzer) tpModeApplies() bool {
	if a.core == nil || a.core.DemoInfo == nil || a.core.DemoInfo.Mode == "" {
		return true
	}
	switch strings.ToLower(a.core.DemoInfo.Mode) {
	case "team", "ctf", "coop":
		return true
	}
	return false
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
