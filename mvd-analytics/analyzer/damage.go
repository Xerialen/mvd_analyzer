package analyzer

import (
	"sort"

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

	raw []rawDamage
}

// rawDamage is one mvdhidden_dmgdone record pinned to wire slots + time,
// plus the victim's weapon bitfield snapshot. The match-time gate is applied
// in Finalize from the demo-clock timestamp (tMs), not sampled here, to avoid
// the match-start-frame race (see Finalize). Names/teams are resolved in
// Finalize too.
type rawDamage struct {
	attacker   int // wire slot, or -1 for world / non-player inflictor
	victim     int // wire slot
	damage     int
	deathType  int
	isSplash   bool
	tMs        int32
	victimItem int // victim's StatItems bitfield at hit time
}

// NewDamageAnalyzer creates a new damage analyzer.
func NewDamageAnalyzer() *DamageAnalyzer {
	return &DamageAnalyzer{items: make(map[int]int)}
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
	case *events.StatUpdateEvent:
		// Track weapon inventory ungated so a victim's loadout is known
		// from the first stat update, regardless of match phase.
		if e.StatIndex == events.StatItems {
			a.items[e.PlayerNum] = e.Value
		}
	case *events.DamageEvent:
		a.raw = append(a.raw, rawDamage{
			attacker:   e.Attacker,
			victim:     e.Victim,
			damage:     e.Damage,
			deathType:  e.DeathType,
			isSplash:   e.IsSplash,
			tMs:        msTime(e.Time),
			victimItem: a.items[e.Victim],
		})
	}
	return nil
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

		out.Events = append(out.Events, DamageEntry{
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
		})

		out.TotalDamage += d.damage

		// Victim's damage-taken (all sources).
		vp := getOrCreateDamage(out.ByPlayer, victim)
		vp.Taken += d.damage
		if isEnv {
			vp.TakenEnv += d.damage
		}

		if isWorld {
			continue // no attacker to credit
		}

		ap := getOrCreateDamage(out.ByPlayer, attacker)
		switch {
		case isSelf:
			ap.GivenSelf += d.damage
		case isTeam:
			ap.GivenTeam += d.damage
		default:
			// Enemy damage — the "useful" number.
			ap.Given += d.damage
			ap.ByWeapon[weapon] += d.damage
			out.ByWeapon[weapon] += d.damage
			addToMatrix(matrix, attacker, victim, weapon, d.damage)
			addVictimWeaponBucket(ap, vw, d.damage)
		}
	}

	out.Matrix = flattenMatrix(matrix)
	out.Scoreboard = a.reconcile(out.ByPlayer)

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
// never used to adjust the stream-derived numbers.
func (a *DamageAnalyzer) reconcile(byPlayer map[string]*PlayerDamage) *DamageReconciliation {
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
		if pd, ok := byPlayer[p.Name]; ok {
			d.StreamGiven = pd.Given
			d.StreamTaken = pd.Taken
			d.StreamEWep = pd.EWep
		}
		rec.ByPlayer[p.Name] = d
	}
	return rec
}
