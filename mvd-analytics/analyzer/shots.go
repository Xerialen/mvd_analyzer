package analyzer

import (
	"math"
	"sort"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// ShotsAnalyzer derives a per-shot weapon-fire stream from the raw wire
// signals: svc_sound fire sounds (which carry the firing entity on
// CHAN_WEAPON) for SG/SSG/RL/GL/NG/SNG, and TE_LIGHTNING2 beams for LG —
// the one weapon with no per-shot fire sound. Instantaneous hitscan fires
// (sg/ssg/lg) are linked to the damage they caused in the same server frame
// via the KTX damage stream; projectile fires are linked through their
// tracked entity flight (linkProjectiles) — rl/gl on every parse, ng/sng
// when nail decoding is enabled.
//
// It mirrors DamageAnalyzer: raw events are collected during OnEvent and
// resolved to player identities in Finalize via CoreOutputs. The Shots
// stream is ungated (warmup fires included, windowed by consumers);
// aggregates are match-gated for KTX scoreboard parity.
type ShotsAnalyzer struct {
	ctx    *Context
	core   *CoreOutputs
	timing MatchTimingDetector

	// pos is each slot's last-seen origin (svc_playerinfo) — the muzzle
	// reference that disambiguates which same-frame fire launched a rocket.
	pos map[int][3]float32

	// openProj tracks in-flight projectile entities by entnum; on despawn
	// the completed [spawn,despawn] bracket is appended to projectiles.
	openProj    map[int]*rawProjectile
	projectiles []rawProjectile

	// beams holds every LG bolt's geometry for the spatial beam stream.
	beams []rawBeam

	// Nail tracking (opt-in, ctx.Nails). svc_nails2 ids are stable while a
	// nail lives, so openNail brackets each flight (spawn → despawn) the same
	// way projectile entnums bracket rockets. nailFlights are the completed
	// brackets, linked to ng/sng fires and exported as the nail map stream.
	openNail    map[int]*rawProjectile // nail id -> open flight
	nailLastPos map[int][3]float32     // nail id -> last seen origin
	nailFlights []rawProjectile

	shots  []rawShot
	dmgs   []rawShotDmg
	hadDmg bool // any DamageEvent seen — distinguishes 0% accuracy from no-stream
}

// rawShot is one detected fire pinned to a wire slot + time, plus whether
// the match was running (for gated aggregates) and whether the weapon is
// instantaneous hitscan (linkable to same-frame damage). shooterPos is the
// firer's last-seen origin, used as the muzzle reference when matching a
// rocket spawn back to its fire. The resolved identity and link result are
// filled in Finalize.
type rawShot struct {
	slot       int
	weapon     string
	source     string // "sound" | "beam"
	tMs        int32
	inMatch    bool
	hitscan    bool
	shooterPos [3]float32

	name        string
	team        string
	hit         bool
	victims     []string
	victimKinds []string // parallel to victims: "enemy" | "team" | "self"
	linked      bool     // a projectile has already claimed this fire
}

// rawShotDmg is a DamageEvent (hitscan sg/ssg/lg, or projectile rl/gl) kept
// for shot linking.
type rawShotDmg struct {
	attacker int // wire slot
	victim   int // wire slot
	weapon   string
	tMs      int32
	used     bool
}

// rawProjectile is one tracked rocket/grenade flight: its weapon kind, the
// muzzle spawn (time + origin) and the despawn (time + last origin). The
// entity number bracketed the flight; only these endpoints are kept.
type rawProjectile struct {
	kind          string
	spawnTMs      int32
	spawnOrigin   [3]float32
	despawnTMs    int32
	despawnOrigin [3]float32
}

// rawBeam is one LG TE_LIGHTNING2 bolt: when it flashed and its muzzle→impact
// segment. Kept only for the spatial beam stream (the LG shot itself is a
// rawShot); collected regardless of the ShotStreams flag (cheap, bounded).
type rawBeam struct {
	tMs   int32
	start [3]float32
	end   [3]float32
}

const (
	chanWeapon = 1 // CHAN_WEAPON — the channel KTX fires weapons on

	// hitscanLinkWindowMs bounds how far a hitscan damage event may sit
	// from its shot and still be linked. Hitscan damage lands in the same
	// server frame as the fire; a couple of frames of slack covers wire
	// jitter while staying far below any weapon's refire interval.
	hitscanLinkWindowMs = 26

	// beamLightningLG is the TE_LIGHTNING2 type — the player Lightning Gun
	// bolt KTX emits once per fire tick (TE_LIGHTNING1/3 are non-player).
	beamLightningLG = 6

	// projSpawnWindowMs bounds how far a rocket/grenade spawn may sit from
	// the fire sound that launched it — they are emitted in the same server
	// frame, so a few frames of slack is ample.
	projSpawnWindowMs = 50

	// projImpactWindowMs bounds the projectile's despawn frame to its impact
	// damage (T_MissileTouch fires the explosion + damage as it is removed).
	projImpactWindowMs = 34
)

// NewShotsAnalyzer creates a new shots analyzer.
func NewShotsAnalyzer() *ShotsAnalyzer {
	return &ShotsAnalyzer{
		pos:         make(map[int][3]float32),
		openProj:    make(map[int]*rawProjectile),
		openNail:    make(map[int]*rawProjectile),
		nailLastPos: make(map[int][3]float32),
	}
}

func (a *ShotsAnalyzer) Name() string { return "shots" }

func (a *ShotsAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

// UseCoreOutputs is the CoreConsumer contract — Shots consumes co for
// slot→identity resolution and co.DemoInfo for the accuracy cross-check.
func (a *ShotsAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

func (a *ShotsAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.PrintEvent:
		a.timing.OnPrint(e)
	case *events.IntermissionEvent:
		a.timing.OnIntermission(e.Time)
	case *events.PlayerPositionEvent:
		a.pos[e.PlayerNum] = e.Origin
	case *events.SoundEvent:
		a.onSound(e)
	case *events.BeamEvent:
		a.onBeam(e)
	case *events.ProjectileSpawnEvent:
		a.openProj[e.EntNum] = &rawProjectile{kind: e.Kind, spawnTMs: e.TimeMs, spawnOrigin: e.Origin}
	case *events.ProjectileDespawnEvent:
		if p := a.openProj[e.EntNum]; p != nil {
			p.despawnTMs = e.TimeMs
			p.despawnOrigin = e.Origin
			// Spikes (sv_nailhack packet entities) are tagged "nail" and feed
			// the nail flights; rockets/grenades feed the projectile flights.
			if p.kind == "nail" {
				a.nailFlights = append(a.nailFlights, *p)
			} else {
				a.projectiles = append(a.projectiles, *p)
			}
			delete(a.openProj, e.EntNum)
		}
	case *events.NailsFrameEvent:
		a.onNails(e)
	case *events.DamageEvent:
		a.hadDmg = true
		if e.Attacker >= 0 {
			if w := events.DeathTypeToWeapon(e.DeathType); a.canLink(w) {
				a.dmgs = append(a.dmgs, rawShotDmg{
					attacker: e.Attacker,
					victim:   e.Victim,
					weapon:   w,
					tMs:      msTime(e.Time),
				})
			}
		}
	}
	return nil
}

// onNails brackets each nail's flight by its svc_nails2 id: a new id opens a
// flight at the muzzle, an id that vanishes from the frame closes it at its
// last position. Completed flights feed ng/sng → damage linking and the nail
// map stream. Only runs when nail tracking is enabled.
func (a *ShotsAnalyzer) onNails(e *events.NailsFrameEvent) {
	if !a.ctx.Nails {
		return
	}
	present := make(map[int]bool, len(e.Nails))
	for _, n := range e.Nails {
		present[n.ID] = true
		a.nailLastPos[n.ID] = n.Origin
		if a.openNail[n.ID] == nil {
			a.openNail[n.ID] = &rawProjectile{kind: "nail", spawnTMs: e.TimeMs, spawnOrigin: n.Origin}
		}
	}
	for id, fl := range a.openNail {
		if present[id] {
			continue
		}
		fl.despawnTMs = e.TimeMs
		fl.despawnOrigin = a.nailLastPos[id]
		a.nailFlights = append(a.nailFlights, *fl)
		delete(a.openNail, id)
		delete(a.nailLastPos, id)
	}
}

// onSound records a shot when the sound is a weapon-fire sound played on a
// player's CHAN_WEAPON. The svc_sound entity is the shooter (slot = Ent-1).
func (a *ShotsAnalyzer) onSound(e *events.SoundEvent) {
	if e.Channel != chanWeapon || e.Ent < 1 || e.Ent > events.MaxClients {
		return
	}
	w, ok := fireSoundWeapon(e.Name)
	if !ok {
		return
	}
	slot := e.Ent - 1
	a.shots = append(a.shots, rawShot{
		slot:       slot,
		weapon:     w,
		source:     "sound",
		tMs:        e.TimeMs,
		inMatch:    a.timing.Started && !a.timing.Ended,
		hitscan:    isHitscanWeapon(w),
		shooterPos: a.pos[slot],
	})
}

// onBeam records an LG fire from a TE_LIGHTNING2 beam. KTX emits exactly one
// such beam per LG fire tick, carrying the firing entity, so the beam is the
// authoritative per-shot LG signal (one beam == one attack == one cell;
// discharge emits no beam). TE_LIGHTNING1/3 are non-player bolts and are
// ignored. LG is hitscan, so its same-frame damage links like sg/ssg.
func (a *ShotsAnalyzer) onBeam(e *events.BeamEvent) {
	if e.Type != beamLightningLG || e.Ent < 1 || e.Ent > events.MaxClients {
		return
	}
	slot := e.Ent - 1
	a.shots = append(a.shots, rawShot{
		slot:       slot,
		weapon:     "lg",
		source:     "beam",
		tMs:        e.TimeMs,
		inMatch:    a.timing.Started && !a.timing.Ended,
		hitscan:    true,
		shooterPos: a.pos[slot],
	})
	a.beams = append(a.beams, rawBeam{tMs: e.TimeMs, start: e.Start, end: e.End})
}

func (a *ShotsAnalyzer) Finalize(result *Result) error {
	if len(a.shots) == 0 {
		return nil
	}

	// Index damage by attacker slot so linking is per-slot, not a full scan
	// per shot. Holds both hitscan (sg/ssg/lg) and projectile (rl/gl) damage.
	dmgBySlot := make(map[int][]*rawShotDmg)
	for i := range a.dmgs {
		d := &a.dmgs[i]
		dmgBySlot[d.attacker] = append(dmgBySlot[d.attacker], d)
	}

	sort.SliceStable(a.shots, func(i, j int) bool { return a.shots[i].tMs < a.shots[j].tMs })

	// 1. Resolve each fire's shooter identity.
	for i := range a.shots {
		s := &a.shots[i]
		id := a.resolveAt(s.slot, s.tMs)
		s.name, s.team = id.Name, id.Team
	}

	// 2. Hitscan: link each sg/ssg/lg fire to its same-frame damage.
	for i := range a.shots {
		s := &a.shots[i]
		if s.name == "" || !s.hitscan {
			continue
		}
		if v, k := a.linkHitscan(dmgBySlot[s.slot], s); len(v) > 0 {
			s.hit, s.victims, s.victimKinds = true, v, k
		}
	}

	// 3. Projectiles: bracket each rocket/grenade flight back to its
	//    launching fire (by muzzle) and forward to its impact damage. Nail
	//    flights (opt-in) link the same way for ng/sng.
	a.linkProjectiles(a.projectiles, dmgBySlot)
	a.linkProjectiles(a.nailFlights, dmgBySlot)

	// 4. Emit the stream + match-time aggregates from the resolved state.
	out := &ShotsResult{}
	aggByName := make(map[string]*shotAgg)
	var aggOrder []string
	for i := range a.shots {
		s := &a.shots[i]
		if s.name == "" {
			continue // can't attribute the fire to a known player
		}
		out.Shots = append(out.Shots, Shot{
			Time: s.tMs, Player: s.name, Team: s.team,
			Weapon: s.weapon, Source: s.source, Hit: s.hit, Victims: s.victims,
			VictimKinds: emitKinds(s.victimKinds),
			Warmup:      !s.inMatch,
		})
		if !s.inMatch {
			continue
		}
		ag := aggByName[s.name]
		if ag == nil {
			ag = &shotAgg{team: s.team, weapons: make(map[string]*weaponAgg)}
			aggByName[s.name] = ag
			aggOrder = append(aggOrder, s.name)
		}
		wa := ag.weapons[s.weapon]
		if wa == nil {
			wa = &weaponAgg{}
			ag.weapons[s.weapon] = wa
		}
		wa.shots++
		if s.hit {
			wa.hits++
			var hasEnemy, hasTeam, hasSelf bool
			for _, k := range s.victimKinds {
				switch k {
				case "self":
					hasSelf = true
				case "team":
					hasTeam = true
				default:
					hasEnemy = true
				}
			}
			if hasEnemy {
				wa.enemyHits++
			}
			if hasTeam {
				wa.teamHits++
			}
			if hasSelf {
				wa.selfHits++
			}
		}
	}

	out.ByPlayer = a.buildByPlayer(aggByName, aggOrder)
	out.Reconciliation = a.reconcile(aggByName)

	result.Shots = out
	a.buildSpatialStreams(result)
	return nil
}

// buildSpatialStreams attaches the projectile-flight and LG-beam streams to
// result.Streams for the map view. Opt-in (ShotStreams) and a no-op when the
// streams result is absent — these are sizeable (thousands of beams in a
// team game) so they are off by default. Times are demo-relative ms here;
// normalizeMatchRelativeTimes rebases them to match time like the other
// streams.
func (a *ShotsAnalyzer) buildSpatialStreams(result *Result) {
	if result.Streams == nil {
		return
	}
	// Rocket/grenade flights + LG beams ride the shot-streams gate.
	if a.ctx.ShotStreams {
		if len(a.projectiles) > 0 {
			result.Streams.Projectiles = flightsToStream(a.projectiles)
		}
		if len(a.beams) > 0 {
			bs := &BeamStreams{}
			for i := range a.beams {
				b := &a.beams[i]
				bs.T = append(bs.T, b.tMs)
				bs.Sx = append(bs.Sx, b.start[0])
				bs.Sy = append(bs.Sy, b.start[1])
				bs.Sz = append(bs.Sz, b.start[2])
				bs.Ex = append(bs.Ex, b.end[0])
				bs.Ey = append(bs.Ey, b.end[1])
				bs.Ez = append(bs.Ez, b.end[2])
			}
			result.Streams.Beams = bs
		}
	}
	// Nails are their own opt-in (high volume), independent of the other
	// shot streams.
	if a.ctx.Nails && len(a.nailFlights) > 0 {
		result.Streams.Nails = flightsToStream(a.nailFlights)
	}
}

// linkProjectiles matches each tracked flight (rocket/grenade, or a nail when
// kind is "nail") to the fire that launched it and the damage it caused. The
// launching fire is the unclaimed sound shot in the spawn frame nearest the
// spawn origin by muzzle (proximity only matters when two players fire the
// same weapon in one frame — otherwise there is a single candidate). The
// impact is that shooter's same-weapon damage at the despawn frame; the
// damage's attacker is authoritative, the flight's [spawn,despawn] bracket is
// what pins *which* shot caused *which* impact when several are in flight.
// A "nail" flight is weapon-agnostic (svc_nails does not tag ng vs sng), so it
// matches either ng or sng fires and the matched fire's weapon drives the
// impact lookup.
func (a *ShotsAnalyzer) linkProjectiles(flights []rawProjectile, dmgBySlot map[int][]*rawShotDmg) {
	if len(flights) == 0 {
		return
	}
	sort.SliceStable(flights, func(i, j int) bool {
		return flights[i].spawnTMs < flights[j].spawnTMs
	})
	for pi := range flights {
		p := &flights[pi]
		var best *rawShot
		bestDist := float32(math.MaxFloat32)
		for si := range a.shots {
			s := &a.shots[si]
			if s.linked || s.source != "sound" || s.name == "" || !flightMatchesFire(p.kind, s.weapon) {
				continue
			}
			if dt := s.tMs - p.spawnTMs; dt < -projSpawnWindowMs || dt > projSpawnWindowMs {
				continue
			}
			if d := dist2(s.shooterPos, p.spawnOrigin); d < bestDist {
				bestDist, best = d, s
			}
		}
		if best == nil {
			continue
		}
		best.linked = true
		var victims, kinds []string
		seen := make(map[string]bool)
		for _, d := range dmgBySlot[best.slot] {
			if d.used || d.weapon != best.weapon || absInt32(d.tMs-p.despawnTMs) > projImpactWindowMs {
				continue
			}
			d.used = true
			if id := a.resolveAt(d.victim, d.tMs); id.Name != "" && !seen[id.Name] {
				seen[id.Name] = true
				victims = append(victims, id.Name)
				kinds = append(kinds, victimKindOf(best.slot, best.team, d.victim, id.Team))
			}
		}
		if len(victims) > 0 {
			best.hit, best.victims, best.victimKinds = true, victims, kinds
		}
	}
}

// flightsToStream packs a flight slice into the columnar ProjectileStreams
// shape (one entry per flight). Shared by the rocket/grenade and nail streams.
func flightsToStream(flights []rawProjectile) *ProjectileStreams {
	ps := &ProjectileStreams{}
	for i := range flights {
		p := &flights[i]
		ps.Weapon = append(ps.Weapon, p.kind)
		ps.Spawn = append(ps.Spawn, p.spawnTMs)
		ps.End = append(ps.End, p.despawnTMs)
		ps.Sx = append(ps.Sx, p.spawnOrigin[0])
		ps.Sy = append(ps.Sy, p.spawnOrigin[1])
		ps.Sz = append(ps.Sz, p.spawnOrigin[2])
		ps.Ex = append(ps.Ex, p.despawnOrigin[0])
		ps.Ey = append(ps.Ey, p.despawnOrigin[1])
		ps.Ez = append(ps.Ez, p.despawnOrigin[2])
	}
	return ps
}

// flightMatchesFire reports whether a flight of the given kind could have been
// launched by a fire of weapon w. A "nail" flight matches ng or sng (svc_nails
// is not weapon-tagged); every other flight matches its own weapon.
func flightMatchesFire(kind, w string) bool {
	if kind == "nail" {
		return isNailWeapon(w)
	}
	return w == kind
}

// linkHitscan returns the names of players damaged by this hitscan fire —
// same attacker slot, same weapon, within the same server frame — and each
// victim's class relative to the shooter (parallel slices). Each matched
// damage record is consumed so a later shot cannot reclaim it.
func (a *ShotsAnalyzer) linkHitscan(dmgs []*rawShotDmg, s *rawShot) (victims, kinds []string) {
	seen := make(map[string]bool)
	for _, d := range dmgs {
		if d.used || d.weapon != s.weapon {
			continue
		}
		if absInt32(d.tMs-s.tMs) > hitscanLinkWindowMs {
			continue
		}
		d.used = true
		if id := a.resolveAt(d.victim, d.tMs); id.Name != "" && !seen[id.Name] {
			seen[id.Name] = true
			victims = append(victims, id.Name)
			kinds = append(kinds, victimKindOf(s.slot, s.team, d.victim, id.Team))
		}
	}
	return victims, kinds
}

// victimKindOf classifies a damage victim relative to the shooter, mirroring
// the damage layer's isSelf/isTeam semantics (damage.go): "self" when the
// victim is the shooter's own wire slot, "team" when both teams are non-empty
// and equal, else "enemy".
func victimKindOf(shooterSlot int, shooterTeam string, victimSlot int, victimTeam string) string {
	switch {
	case victimSlot == shooterSlot:
		return "self"
	case shooterTeam != "" && victimTeam != "" && shooterTeam == victimTeam:
		return "team"
	default:
		return "enemy"
	}
}

// emitKinds returns kinds only when it carries information: every victim
// being an enemy is the common case and is encoded as absence on the wire.
func emitKinds(kinds []string) []string {
	for _, k := range kinds {
		if k != "enemy" {
			return kinds
		}
	}
	return nil
}

// buildByPlayer flattens the match-time aggregates into the result shape,
// sorted by player then by a stable weapon order. Hits/Accuracy are emitted
// only for linkable weapons (hitscan sg/ssg/lg + projectile rl/gl) and only
// when a damage stream was present.
func (a *ShotsAnalyzer) buildByPlayer(aggByName map[string]*shotAgg, order []string) []PlayerShots {
	sort.Strings(order)
	out := make([]PlayerShots, 0, len(order))
	for _, name := range order {
		ag := aggByName[name]
		ps := PlayerShots{Player: name, Team: ag.team}
		for _, w := range weaponOrder {
			wa := ag.weapons[w]
			if wa == nil || wa.shots == 0 {
				continue
			}
			ws := WeaponShots{Weapon: w, Shots: wa.shots}
			if a.hadDmg && a.canLink(w) {
				ws.Hits = wa.hits
				ws.Accuracy = float64(wa.hits) / float64(wa.shots)
				ws.EnemyHits, ws.TeamHits, ws.SelfHits = wa.enemyHits, wa.teamHits, wa.selfHits
			}
			ps.Total += wa.shots
			ps.ByWeapon = append(ps.ByWeapon, ws)
		}
		if ps.Total > 0 {
			out = append(out, ps)
		}
	}
	return out
}

// reconcile cross-checks detected per-weapon shot counts against the KTX
// end-of-match accuracy block. Diagnostic only — divergence is reported,
// never used to adjust the detected stream.
func (a *ShotsAnalyzer) reconcile(aggByName map[string]*shotAgg) *ShotsReconciliation {
	if a.core == nil || a.core.DemoInfo == nil || len(a.core.DemoInfo.Players) == 0 {
		return nil
	}
	rec := &ShotsReconciliation{ByPlayer: make(map[string][]ShotsDelta)}
	for i := range a.core.DemoInfo.Players {
		p := &a.core.DemoInfo.Players[i]
		ag := aggByName[p.Name]
		var rows []ShotsDelta
		for _, w := range weaponOrder {
			streamShots := 0
			if ag != nil {
				if wa := ag.weapons[w]; wa != nil {
					streamShots = wa.shots
				}
			}
			ktxAtt, ktxHits := 0, 0
			if dw := p.Weapons[w]; dw != nil && dw.Acc != nil {
				ktxAtt, ktxHits = dw.Acc.Attacks, dw.Acc.Hits
			}
			if streamShots == 0 && ktxAtt == 0 && ktxHits == 0 {
				continue
			}
			rows = append(rows, ShotsDelta{
				Weapon:        w,
				StreamShots:   streamShots,
				StreamAttacks: streamShots * ktxAttackMultiplier(w),
				KtxAttacks:    ktxAtt,
				KtxHits:       ktxHits,
			})
		}
		if len(rows) > 0 {
			rec.ByPlayer[p.Name] = rows
		}
	}
	if len(rec.ByPlayer) == 0 {
		return nil
	}
	return rec
}

// resolveAt maps a wire slot to its identity at tMs via the canonical
// ResolveSlotAt chain (session table → userinfo → name→team backfill),
// same as the damage and frag analyzers.
func (a *ShotsAnalyzer) resolveAt(slot int, tMs int32) SlotInfo {
	return ResolveSlotAt(a.core, a.ctx.Players, slot, tMs)
}

type shotAgg struct {
	team    string
	weapons map[string]*weaponAgg
}

type weaponAgg struct {
	shots     int
	hits      int
	enemyHits int
	teamHits  int
	selfHits  int
}

// weaponOrder is the stable output order for per-weapon aggregates and
// reconciliation rows (matches the KTX WpName ordering).
var weaponOrder = []string{"sg", "ssg", "ng", "sng", "gl", "rl", "lg"}

// fireSoundWeapon maps a precached sound path to the weapon it fires, or
// (",false) when the sound is not a weapon-fire sound. The Quake sound
// filenames are historically mismatched with the weapons that play them —
// the rocket launcher fires "weapons/sgun1.wav" and the nailgun fires
// "weapons/rocket1i.wav" (ktx/src/weapons.c W_FireRocket / W_FireSpikes).
// Non-fire weapon sounds (the grenade bounce, the LG hit/start, ricochets,
// spike tinks, the axe) are deliberately excluded.
func fireSoundWeapon(name string) (string, bool) {
	switch name {
	case "weapons/guncock.wav":
		return "sg", true // shotgun (W_FireShotgun)
	case "weapons/shotgn2.wav":
		return "ssg", true // super shotgun (W_FireSuperShotgun)
	case "weapons/sgun1.wav":
		return "rl", true // rocket launcher (W_FireRocket)
	case "weapons/grenade.wav":
		return "gl", true // grenade launcher (W_FireGrenade)
	case "weapons/rocket1i.wav":
		return "ng", true // nailgun (W_FireSpikes)
	case "weapons/spike2.wav":
		return "sng", true // super nailgun (W_FireSuperSpikes)
	case "weapons/coilgun.wav":
		return "sg", true // instagib shotgun-slot railgun
	}
	return "", false
}

// isHitscanWeapon reports whether a weapon's shot and its damage land in the
// same server frame (so they link via same-frame matching). Nail/rocket/
// grenade fires are projectiles and are excluded.
func isHitscanWeapon(w string) bool {
	return w == "sg" || w == "ssg" || w == "lg"
}

// isProjectileWeapon reports whether a fire launches a tracked slow
// projectile (rocket / grenade), linked via the entity flight bracket.
func isProjectileWeapon(w string) bool {
	return w == "rl" || w == "gl"
}

// isNailWeapon reports whether a fire launches nails (ng / sng), linked via
// svc_nails id brackets — only when nail tracking is enabled.
func isNailWeapon(w string) bool {
	return w == "ng" || w == "sng"
}

// canLink reports whether a weapon's fires can be linked to damage: same-frame
// hitscan, entity-tracked rl/gl projectiles, or — when nail tracking is on —
// ng/sng nails. Gated on ctx.Nails so ng/sng don't show a bogus 0% accuracy
// in the default (un-tracked) output.
func (a *ShotsAnalyzer) canLink(w string) bool {
	if isHitscanWeapon(w) || isProjectileWeapon(w) {
		return true
	}
	return a.ctx != nil && a.ctx.Nails && isNailWeapon(w)
}

// dist2 is the squared Euclidean distance between two points (no sqrt — only
// used for nearest-muzzle comparison).
func dist2(a, b [3]float32) float32 {
	dx, dy, dz := a[0]-b[0], a[1]-b[1], a[2]-b[2]
	return dx*dx + dy*dy + dz*dz
}

// ktxAttackMultiplier converts a discrete fire count into KTX's acc.attacks
// unit: the shotguns count one attack per pellet (6 / 14), every other
// weapon counts one attack per fire. See ktx/src/weapons.c.
func ktxAttackMultiplier(w string) int {
	switch w {
	case "sg":
		return 6
	case "ssg":
		return 14
	default:
		return 1
	}
}

func absInt32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
