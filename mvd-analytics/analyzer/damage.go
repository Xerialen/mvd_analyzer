package analyzer

import (
	"math"

	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-reader/events"
	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// damageSentinel is the value KTX clamps unbound_dmg_dealt to when it would
// overflow a short (telefrag / instagib / prewar 99999 kill — combat.c:809).
// It is not a real damage amount, so it is dropped from every total.
const damageSentinel = 9999

// DamageAnalyzer consumes the per-hit DamageEvent stream (decoded from KTX's
// mvdhidden_dmgdone 0x0007 blocks) and produces result.DamageResult.
//
// The values it sums are UNBOUND (overkill-inclusive) — a different quantity
// from KTX's health-capped demoInfo scoreboard. See result/damage.go for the
// full semantics and the directional invariant that relates the two.
//
// It is a derived analyzer + CoreConsumer: it resolves attacker/victim wire
// slots to reconnect-unified identities via co.SlotIdentityAt and classifies
// enemy-vs-team with the demoInfo-authoritative co.Names, mirroring the
// resolution the frag analyzer does in its Finalize.
type DamageAnalyzer struct {
	ctx    *Context
	core   *CoreOutputs
	timing MatchTimingDetector
	raw    []rawDamage

	// health/armor/items track each player's current vitals, fed by
	// StatHealth / StatArmor / StatItems updates (authoritative — they fire for
	// every player in an MVD). They reconstruct KTX's effective-damage credit
	// per hit: save = min(ceil(armortype·D), armorvalue), take = D − save,
	// credit = save + bound(take, health) (combat.c:634,648,655,796). The
	// armortype fraction (GA .3 / YA .6 / RA .8) comes from the armor bit in
	// items. Vitals are advanced per in-match hit (armor−=save, health−=take)
	// so several hits in one frame each see the running value; the next stat
	// update re-anchors them to the authoritative figures.
	health [maxPlayerSlots]int
	armor  [maxPlayerSlots]int
	items  [maxPlayerSlots]int
	known  [maxPlayerSlots]bool
}

// armorFraction is KTX's armortype for the armor the player is holding:
// red .8, yellow .6, green .3 (mvd-reader MVD_FORMAT.md; combat.c uses it as
// save = ceil(armortype*damage)). Returns 0 when no armor is held.
func armorFraction(items int) float64 {
	switch {
	case items&events.ITArmor3 != 0:
		return 0.8
	case items&events.ITArmor2 != 0:
		return 0.6
	case items&events.ITArmor1 != 0:
		return 0.3
	default:
		return 0
	}
}

// maxPlayerSlots bounds the per-slot health arrays. events.MaxClients is the
// wire ceiling; reuse it so slot indices from the parser are always in range.
const maxPlayerSlots = events.MaxClients

// rawDamage is one in-match per-hit record captured during the event pass,
// resolved to identities only in Finalize (so SlotIdentityAt sees the
// fully-built session table). capped is the hit's damage clamped at the
// victim's health before the hit (computed live, since health state is gone
// by Finalize).
type rawDamage struct {
	attacker  int
	victim    int
	damage    int
	capped    int
	deathType int
	splash    bool
	tMs       int32
}

// NewDamageAnalyzer creates a damage analyzer.
func NewDamageAnalyzer() *DamageAnalyzer { return &DamageAnalyzer{} }

func (a *DamageAnalyzer) Name() string { return "damage" }

func (a *DamageAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

// UseCoreOutputs wires the resolved identity / name tables in before Finalize.
func (a *DamageAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

func (a *DamageAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.StatUpdateEvent:
		// Track health + armor + items for every player, always (incl. warmup)
		// so the values are warm and authoritative by the time match damage
		// starts. Spawns, deaths and pickups all arrive as stat updates and
		// re-anchor any per-hit decrement drift.
		s := e.PlayerNum
		if s < 0 || s >= maxPlayerSlots {
			break
		}
		switch e.StatIndex {
		case events.StatHealth:
			a.health[s] = e.Value
			a.known[s] = true
		case events.StatArmor:
			a.armor[s] = e.Value
		case events.StatItems:
			a.items[s] = e.Value
		}
	case *events.PrintEvent:
		a.timing.OnPrint(e)
	case *events.IntermissionEvent:
		a.timing.OnIntermission(e.Time)
	case *events.DamageEvent:
		// Match-gate exactly as the frag analyzer gates deaths, so damage
		// windows align with the death/frag log and warmup/prewar hits are
		// excluded — the same window KTX's match-only scoreboard covers.
		if !a.timing.Started || a.timing.Ended {
			return nil
		}
		if e.Damage <= 0 {
			return nil
		}

		// Reconstruct KTX's effective-damage credit for this hit:
		//   save   = min(ceil(armortype·D), armorvalue)   armor absorbs a fraction
		//   take   = D − save                              the rest hits health
		//   credit = save + bound(0, take, health)         health overkill dropped
		// then advance the victim's armor/health for same-frame later hits. This
		// is the exact KTX accounting (combat.c:634,648,655,796) — clamping at
		// health+armor would over-credit, because armor only absorbs its
		// fraction, not the full overkill.
		dmg, capped := e.Damage, e.Damage
		v := e.Victim
		if v >= 0 && v < maxPlayerSlots && a.known[v] {
			save := int(math.Ceil(armorFraction(a.items[v]) * float64(e.Damage)))
			if save > a.armor[v] {
				save = a.armor[v]
			}
			if save < 0 {
				save = 0
			}
			take := e.Damage - save
			h := a.health[v]
			if h < 0 {
				h = 0
			}
			healthCredit := take
			if healthCredit > h {
				healthCredit = h
			}
			capped = save + healthCredit
			a.armor[v] -= save
			a.health[v] -= take

			// Telefrag / instakill: the wire 9999 is a non-physical clamp
			// (combat.c:809), not real damage. KTX credits what it removed
			// (the capped value above); use that for the raw side too rather
			// than the bogus 9999.
			if e.Damage >= damageSentinel {
				dmg = capped
			}
		} else if e.Damage >= damageSentinel {
			return nil // can't reconstruct a sentinel without the victim's vitals
		}

		a.raw = append(a.raw, rawDamage{
			attacker:  e.Attacker,
			victim:    v,
			damage:    dmg,
			capped:    capped,
			deathType: e.DeathType,
			splash:    e.IsSplash,
			tMs:       msTime(e.Time),
		})
	}
	return nil
}

func (a *DamageAnalyzer) Finalize(res *Result) error {
	if len(a.raw) == 0 {
		return nil
	}

	byPlayer := make(map[string]*result.PlayerDamage)
	get := func(name string) *result.PlayerDamage {
		p, ok := byPlayer[name]
		if !ok {
			p = &result.PlayerDamage{ByWeapon: make(map[string]int)}
			byPlayer[name] = p
		}
		return p
	}

	hits := make([]result.DamageHit, 0, len(a.raw))
	for _, r := range a.raw {
		attacker := a.resolveName(r.attacker, r.tMs)
		victim := a.resolveName(r.victim, r.tMs)
		weapon := mvd.DeathTypeToWeapon(r.deathType)

		// Self damage: attacker entity == victim entity (KTX: attacker == targ,
		// combat.c:1060). Folded into Self* only; not an outgoing hit.
		if r.attacker == r.victim {
			if attacker != "" {
				p := get(attacker)
				p.SelfRaw += r.damage
				p.SelfEff += r.capped
			}
			continue
		}

		// Non-self accounting requires BOTH ends resolved so the given and
		// taken sides stay in lockstep — this is what makes the closed-system
		// invariant ΣgivenRaw==ΣtakenRaw and Σhits==Σ(givenRaw+teamRaw) hold by
		// construction. A hit with an unresolvable slot is dropped from every
		// total (it can't be attributed anyway); on KTX demos every slot
		// resolves — deaths reconcile exactly — so a drop here would itself be
		// a resolution-gap signal the validation harness surfaces.
		if attacker == "" || victim == "" {
			continue
		}

		// Enemy vs teammate, using the demoInfo-authoritative team table
		// first (same precedence as frag.Finalize), slot identity next,
		// live userinfo last. Unknown teams default to enemy so the total
		// still reconciles directionally against demoInfo.dmg.given.
		attackerTeam := a.teamFor(attacker, r.attacker, r.tMs)
		victimTeam := a.teamFor(victim, r.victim, r.tMs)
		isTeam := attackerTeam != "" && victimTeam != "" && attackerTeam == victimTeam

		pa := get(attacker)
		if isTeam {
			pa.TeamRaw += r.damage
			pa.TeamEff += r.capped
		} else {
			pa.GivenRaw += r.damage
			pa.GivenEff += r.capped
			pa.ByWeapon[weapon] += r.capped
			pa.Hits++
			// Enemy damage received only — mirrors KTX dmg_t (combat.c:1083),
			// which is not touched on team or self damage.
			pv := get(victim)
			pv.TakenRaw += r.damage
			pv.TakenEff += r.capped
		}

		hits = append(hits, result.DamageHit{
			Attacker:  attacker,
			Victim:    victim,
			Damage:    r.damage,
			DamageEff: r.capped,
			Weapon:    weapon,
			Splash:    r.splash,
			TimeMs:    r.tMs,
		})
	}

	res.Damage = &result.DamageResult{ByPlayer: byPlayer, Hits: hits}
	return nil
}

// resolveName maps a wire slot to the canonical identity active at tMs,
// falling back to the live userinfo name. Mirrors FragAnalyzer.resolveDeathName.
func (a *DamageAnalyzer) resolveName(slot int, tMs int32) string {
	if a.core != nil {
		if n := a.core.SlotIdentityAt(slot, tMs).Name; n != "" {
			return n
		}
	}
	if slot >= 0 && slot < len(a.ctx.Players) {
		if p := a.ctx.Players[slot]; p != nil {
			return p.Name
		}
	}
	return ""
}

// teamFor resolves the team for a (name, slot) at tMs: demoInfo name table
// first (authoritative), then the slot's identity-session team, then live
// userinfo. Returns "" when no source knows the team.
func (a *DamageAnalyzer) teamFor(name string, slot int, tMs int32) string {
	if a.core != nil {
		if t := a.core.Names.TeamForName(name); t != "" {
			return t
		}
		if t := a.core.SlotIdentityAt(slot, tMs).Team; t != "" {
			return t
		}
	}
	if slot >= 0 && slot < len(a.ctx.Players) {
		if p := a.ctx.Players[slot]; p != nil {
			return p.Team
		}
	}
	return ""
}
