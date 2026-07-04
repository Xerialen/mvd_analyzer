package analyzer

import (
	"math"
	"sort"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// Aim analytics (schema v41). aimPost derives per-player aim metrics from the
// already-assembled Shots + Streams (interpolated position/view at fire time)
// + Damage + LG beams. It is a post-processor so it sees match-relative times
// and stable team labels (registered after duelTeamNormalize). It reuses the
// los* hull/eye/target constants — aim and line-of-sight share the same
// eye→target geometry — and result.PositionTrack.SampleAt for fire-time
// interpolation.
//
// Truthfulness: hit/miss is Shot.Hit (never re-derived); the crosshair-error
// heatmap is hitscan-only; a hit is attributed to its server-confirmed victim
// (nearest by crosshair error when a pellet fire hit several), bypassing the
// liveness gate — a killing blow lands in the same frame the victim dies, so
// losAliveAt reads the victim as already dead at the fire time — and the
// enemy filter — a team-damage hit is a confirmed target too; a miss is
// attributed only to an enemy whose track brackets the fire time AND who is
// alive at it (dead players keep streaming position samples — the death-anim
// body — so a corpse would otherwise stay a candidate; same liveness rule as
// LOS, losAliveAt); team-game miss attribution is a labeled nearest-crosshair
// heuristic; rocket "direct" is a non-splash-damage heuristic.
const (
	aimShaftGapMs     = 150  // LG fires more than this apart start a new shaft
	aimReachNearUnits = 48.0 // beam endpoint within this of an enemy hull = near miss
	aimBeamMatchUnits = 64.0 // beam muzzle within this of the shooter muzzle = same shot

	// Crosshair geometry. The shot traces from the weapon muzzle (≈ origin+16,
	// the LG/SG fire origin) — not the +22 eye — toward the enemy hull center
	// (origin+4, the −24..+32 box midpoint). aimHalfW/aimHalfH are the hull
	// half-extents (±16 wide, 56 tall → 28). The error is normalized by the
	// target's angular half-extent per axis, so the hull maps to the unit
	// square; the horizontal extent uses the box silhouette at the viewing
	// angle (an axis-aligned box is up to √2 wider seen corner-on).
	aimMuzzleZ = 16.0
	aimTargetZ = 4.0
	aimHalfW   = 16.0
	aimHalfH   = 28.0

	// LG beam max length (KTX W_FireLightning traces v_forward·600). A missed
	// beam within aimLGRangeSlack of this reached open space (out of range);
	// shorter means it stopped on geometry (blocked).
	aimLGRange      = 600.0
	aimLGRangeSlack = 12.0
)

var aimHitscan = map[string]bool{"sg": true, "ssg": true, "lg": true}

func aimPost(res *Result, co *CoreOutputs) {
	if res == nil || res.Shots == nil || res.Streams == nil || len(res.Shots.Shots) == 0 {
		return
	}

	// Position tracks keyed by canonical name, plus the spawn/death streams
	// backing the per-shot alive test (aliveAt below).
	tracks := make(map[string]*result.PositionTrack)
	spawnsOf := make(map[string][]int32)
	deathsOf := make(map[string][]int32)
	for i := range res.Streams.Players {
		p := &res.Streams.Players[i]
		if p.Position != nil && len(p.Position.T) > 0 {
			tracks[p.Name] = p.Position
		}
		spawnsOf[p.Name] = p.Spawns
		deathsOf[p.Name] = p.Deaths
	}
	aliveAt := func(name string, t int32) bool {
		return losAliveAt(spawnsOf[name], deathsOf[name], t)
	}

	// Best-effort team per player from the (same-namespace) shot stream. Empty
	// when teamplay is off (duels) — handled by the enemy rule below.
	teamOf := make(map[string]string)
	for _, ps := range res.Shots.ByPlayer {
		if ps.Team != "" {
			teamOf[ps.Player] = ps.Team
		}
	}

	// Group shots per player, time-sorted (ramp + shaft grouping need order).
	// In-match fires only: the shot stream keeps warmup/prewar fires, but aim
	// is a match-time view like shots.ByPlayer, so skip Warmup-flagged shots.
	byPlayer := make(map[string][]Shot)
	var order []string
	for _, s := range res.Shots.Shots {
		if s.Warmup {
			continue
		}
		if _, ok := byPlayer[s.Player]; !ok {
			order = append(order, s.Player)
		}
		byPlayer[s.Player] = append(byPlayer[s.Player], s)
		if teamOf[s.Player] == "" && s.Team != "" {
			teamOf[s.Player] = s.Team
		}
	}
	sort.Strings(order)
	for _, p := range order {
		shots := byPlayer[p]
		sort.SliceStable(shots, func(i, j int) bool { return shots[i].Time < shots[j].Time })
	}

	// Per-attacker damage events (non-self), used to size SG/SSG pellet hits
	// (Σ damage / 4) and RL/GL direct contacts (non-splash).
	dmgByPlayer := make(map[string][]*dmgRec)
	if res.Damage != nil {
		for _, d := range res.Damage.Events {
			if d.IsSelf || d.Attacker == "" {
				continue
			}
			dmgByPlayer[d.Attacker] = append(dmgByPlayer[d.Attacker],
				&dmgRec{t: d.Time, weapon: d.Weapon, dmg: d.Damage, splash: d.IsSplash, team: d.IsTeam})
		}
	}

	out := &AimResult{}
	for _, player := range order {
		if pa := computePlayerAim(player, byPlayer[player], tracks, teamOf, dmgByPlayer[player], res.Streams, aliveAt); pa != nil {
			out.Players = append(out.Players, *pa)
		}
	}
	if len(out.Players) > 0 {
		res.Aim = out
	}
}

// dmgRec is one damage event by the player (for sizing pellet hits and direct
// contacts); used marks it consumed by a same-frame hitscan fire. team splits
// the pellet/direct counters by victim class (self damage is excluded at
// collection, so enemy is simply !team).
type dmgRec struct {
	t      int32
	weapon string
	dmg    int
	splash bool
	team   bool
	used   bool
}

// aimSplitAgg accumulates one weapon's per-victim-class counter slices while
// the top-level WeaponAim counters are built; attached at emission only when
// a team or self hit makes them differ from the top-level counters.
type aimSplitAgg struct {
	enemy, team, self WeaponAimSplit
}

// shotKindOf returns the victim class of name on sh ("enemy"/"team"/"self").
// A nil VictimKinds means every victim is an enemy — the wire omits the
// all-enemy case (see result.Shot).
func shotKindOf(sh *Shot, name string) string {
	if sh.VictimKinds == nil {
		return "enemy"
	}
	for i, v := range sh.Victims {
		if v == name && i < len(sh.VictimKinds) {
			return sh.VictimKinds[i]
		}
	}
	return "enemy"
}

// shotHasKind reports whether any of sh's victims is of the given class.
func shotHasKind(sh *Shot, kind string) bool {
	if len(sh.Victims) == 0 {
		return false
	}
	if sh.VictimKinds == nil {
		return kind == "enemy"
	}
	for _, k := range sh.VictimKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func computePlayerAim(player string, shots []Shot, tracks map[string]*result.PositionTrack, teamOf map[string]string, dmg []*dmgRec, streams *Streams, aliveAt func(string, int32) bool) *PlayerAim {
	shooterTrack := tracks[player]
	sTeam := teamOf[player]

	// Enemies: other tracked players, excluding only a known-same team (so a
	// teamless duel correctly treats the one opponent as the enemy).
	var enemies []string
	for name := range tracks {
		if name == player {
			continue
		}
		if sTeam != "" && teamOf[name] == sTeam {
			continue
		}
		enemies = append(enemies, name)
	}
	sort.Strings(enemies)
	if len(enemies) == 0 {
		return nil
	}
	mode := "team"
	if len(enemies) == 1 {
		mode = "duel"
	}
	pa := &PlayerAim{Player: player, Team: sTeam, Mode: mode}

	// ── Crosshair (hitscan) error to the attributed target ──
	cs := &CrosshairSamples{}
	var csTeam []bool
	csAnyTeam := false
	for _, sh := range shots {
		if !aimHitscan[sh.Weapon] || !trackCovers(shooterTrack, sh.Time) {
			continue
		}
		ss, ok := shooterTrack.SampleAt(sh.Time)
		if !ok || !ss.HasView {
			continue
		}
		// A hit names its own target: the server-confirmed victims are
		// authoritative, so the error is measured to the victim nearest the
		// crosshair — with no liveness gate (a killing blow lands in the
		// same frame the victim dies, so aliveAt reads the victim as already
		// dead at the fire time) and no enemy filter (a team-damage hit is a
		// confirmed target too). Misses have no confirmed target and keep
		// the nearest-crosshair heuristic over live enemies.
		var tgt string
		var e aimErr
		var found bool
		fromVictims := false
		if len(sh.Victims) > 0 {
			tgt, e, found = aimAttribute(ss, sh.Time, sh.Victims, tracks, nil)
			fromVictims = found
		}
		if !found {
			tgt, e, found = aimAttribute(ss, sh.Time, enemies, tracks, aliveAt)
		}
		if !found || e.dist <= 0 {
			continue
		}
		cs.T = append(cs.T, sh.Time)
		cs.Weapon = append(cs.Weapon, sh.Weapon)
		cs.DYaw = append(cs.DYaw, float32(e.dYaw))
		cs.DPitch = append(cs.DPitch, float32(e.dPitch))
		cs.NYaw = append(cs.NYaw, float32(e.nYaw))
		cs.NPitch = append(cs.NPitch, float32(e.nPitch))
		cs.Dist = append(cs.Dist, float32(e.dist))
		cs.Hit = append(cs.Hit, sh.Hit)
		cs.Target = append(cs.Target, tgt)
		// The victim class is only knowable for a confirmed victim; the
		// miss/fallback heuristic attributes to enemies by construction.
		isTeam := fromVictims && shotKindOf(&sh, tgt) == "team"
		csTeam = append(csTeam, isTeam)
		csAnyTeam = csAnyTeam || isTeam
	}
	if len(cs.T) > 0 {
		if csAnyTeam {
			cs.Team = csTeam
		}
		pa.Crosshair = cs
	}

	// ── LG ramp onto target ──
	ramp := &LGRampSamples{}
	var rampTeam []bool
	rampAnyTeam := false
	var shaftStart, prev int32
	started := false
	for _, sh := range shots {
		if sh.Weapon != "lg" {
			continue
		}
		if !started || sh.Time-prev > aimShaftGapMs {
			shaftStart = sh.Time
			started = true
		}
		ramp.Since = append(ramp.Since, sh.Time-shaftStart)
		ramp.Hit = append(ramp.Hit, sh.Hit)
		// A fire that connected but hit no enemy is a teammate-only hit —
		// flagged so consumers can score the ramp per victim class.
		teamOnly := sh.Hit && !shotHasKind(&sh, "enemy") && shotHasKind(&sh, "team")
		rampTeam = append(rampTeam, teamOnly)
		rampAnyTeam = rampAnyTeam || teamOnly
		prev = sh.Time
	}
	if len(ramp.Since) > 0 {
		if rampAnyTeam {
			ramp.Team = rampTeam
		}
		pa.LGRamp = ramp
	}

	// ── Rich per-weapon effectiveness ──
	wagg := make(map[string]*WeaponAim)
	var worder []string
	getW := func(w string) *WeaponAim {
		if wagg[w] == nil {
			wagg[w] = &WeaponAim{Weapon: w}
			worder = append(worder, w)
		}
		return wagg[w]
	}
	// Per-weapon victim-class slices, accumulated alongside the top-level
	// counters and attached at emission only when they differ from them.
	splits := make(map[string]*aimSplitAgg)
	getS := func(w string) *aimSplitAgg {
		if splits[w] == nil {
			splits[w] = &aimSplitAgg{}
		}
		return splits[w]
	}
	for _, sh := range shots {
		wa := getW(sh.Weapon)
		wa.Shots++
		if sh.Hit {
			wa.Hits++
			sp := getS(sh.Weapon)
			if shotHasKind(&sh, "enemy") {
				sp.enemy.Hits++
			}
			if shotHasKind(&sh, "team") {
				sp.team.Hits++
			}
			if shotHasKind(&sh, "self") {
				sp.self.Hits++
			}
		}
	}

	// SG/SSG: size pellet hits from same-frame damage (Σ/4) and split each
	// fire into full / partial / whiff — overall and per victim class (the
	// per-fire damage sum splits exactly by dmgRec.team, except when the
	// perFire clamp triggers, e.g. quad-multiplied damage, where the
	// enemy/team allocation within that fire is approximate).
	for wn, perFire := range aimPellets {
		wa := wagg[wn]
		if wa == nil {
			continue
		}
		wa.Pellets = wa.Shots * perFire
		sp := getS(wn)
		classify := func(ph int, full, partial, miss *int) {
			switch {
			case ph <= 0:
				(*miss)++
			case ph >= perFire:
				(*full)++
			default:
				(*partial)++
			}
		}
		for _, sh := range shots {
			if sh.Weapon != wn {
				continue
			}
			sum, sumTeam := 0, 0
			for _, d := range dmg {
				if d.used || d.weapon != wn || absInt32(d.t-sh.Time) > hitscanLinkWindowMs {
					continue
				}
				d.used = true
				sum += d.dmg
				if d.team {
					sumTeam += d.dmg
				}
			}
			ph := sum / aimPelletDamage
			if ph > perFire {
				ph = perFire
			}
			phTeam := sumTeam / aimPelletDamage
			if phTeam > ph {
				phTeam = ph
			}
			phEnemy := ph - phTeam
			wa.PelletHits += ph
			sp.enemy.PelletHits += phEnemy
			sp.team.PelletHits += phTeam
			classify(ph, &wa.Full, &wa.Partial, &wa.Miss)
			classify(phEnemy, &sp.enemy.Full, &sp.enemy.Partial, &sp.enemy.Miss)
			classify(phTeam, &sp.team.Full, &sp.team.Partial, &sp.team.Miss)
		}
	}

	// RL/GL: direct (non-splash contacts) vs splash-only vs missed. Only when
	// projectile linking ran, else Hit is unreliable. Direct splits by victim
	// class from the damage records (self direct is protocol-impossible — a
	// missile ignores its owner for collision — so self hits are splash-only).
	if streams.Projectiles != nil {
		for _, wn := range []string{"rl", "gl"} {
			wa := wagg[wn]
			if wa == nil {
				continue
			}
			directE, directT := 0, 0
			for _, d := range dmg {
				if d.weapon == wn && !d.splash {
					if d.team {
						directT++
					} else {
						directE++
					}
				}
			}
			direct := directE + directT
			if direct > wa.Hits {
				direct = wa.Hits
			}
			wa.Direct = direct
			wa.Splash = wa.Hits - direct
			wa.Missed = wa.Shots - wa.Hits
			sp := getS(wn)
			if directE > sp.enemy.Hits {
				directE = sp.enemy.Hits
			}
			if directT > sp.team.Hits {
				directT = sp.team.Hits
			}
			sp.enemy.Direct = directE
			sp.team.Direct = directT
		}
	}

	// LG: classify each missed fire by where the beam ended (needs the beams).
	if streams.Beams != nil && shooterTrack != nil {
		if wa := wagg["lg"]; wa != nil {
			for _, sh := range shots {
				if sh.Weapon != "lg" || sh.Hit {
					continue
				}
				ss, ok := shooterTrack.SampleAt(sh.Time)
				if !ok {
					wa.Unresolved++
					continue
				}
				bi := matchBeam(streams.Beams, sh.Time, ss.X, ss.Y, ss.Z+aimMuzzleZ)
				if bi < 0 {
					wa.Unresolved++
					continue
				}
				sx, sy, sz := float64(streams.Beams.Sx[bi]), float64(streams.Beams.Sy[bi]), float64(streams.Beams.Sz[bi])
				ex, ey, ez := float64(streams.Beams.Ex[bi]), float64(streams.Beams.Ey[bi]), float64(streams.Beams.Ez[bi])
				best := math.MaxFloat64
				for _, e := range enemies {
					if !trackCovers(tracks[e], sh.Time) || !aliveAt(e, sh.Time) {
						continue
					}
					es, ok := tracks[e].SampleAt(sh.Time)
					if !ok {
						continue
					}
					if d := bboxDist(ex, ey, ez, es.X, es.Y, es.Z); d < best {
						best = d
					}
				}
				beamLen := math.Sqrt((ex-sx)*(ex-sx) + (ey-sy)*(ey-sy) + (ez-sz)*(ez-sz))
				switch {
				case best <= aimReachNearUnits:
					wa.NearMiss++ // aim error — beam ended on/near the hull
				case beamLen >= aimLGRange-aimLGRangeSlack:
					wa.OutOfRange++ // beam ran its full length into open space
				default:
					wa.Blocked++ // beam stopped on geometry short of max range
				}
			}
		}
	}

	sort.SliceStable(worder, func(i, j int) bool { return aimWeaponRank(worder[i]) < aimWeaponRank(worder[j]) })
	for _, w := range worder {
		wa := wagg[w]
		// Attach the victim-class slices only when they differ from the
		// top-level counters (≥1 team or self hit): Team/Self iff their
		// bucket connected, Enemy alongside so consumers never mix a split
		// with an unsplit fallback.
		if sp := splits[w]; sp != nil && (sp.team.Hits > 0 || sp.self.Hits > 0) {
			enemy, team, self := sp.enemy, sp.team, sp.self
			wa.Enemy = &enemy
			if team.Hits > 0 {
				wa.Team = &team
			}
			if self.Hits > 0 {
				wa.Self = &self
			}
		}
		pa.Weapons = append(pa.Weapons, *wa)
	}

	// A player with no computable block adds nothing.
	if pa.Crosshair == nil && pa.LGRamp == nil && len(pa.Weapons) == 0 {
		return nil
	}
	return pa
}

// aimPellets is the standard pellet count per trigger pull (KTX classic);
// aimPelletDamage is the per-pellet damage. Used to size SG/SSG pellet hits.
var aimPellets = map[string]int{"sg": 6, "ssg": 14}

const aimPelletDamage = 4

// aimWeaponRank orders the per-weapon rows (lg, sg, ssg, rl, gl, then rest).
func aimWeaponRank(w string) int {
	for i, x := range []string{"lg", "sg", "ssg", "rl", "gl", "sng", "ng"} {
		if x == w {
			return i
		}
	}
	return 99
}

// aimErr is one shot's crosshair error to a target: signed degrees (yaw
// right+, pitch up+), the same normalized by the target's angular half-extent
// per axis (hull → unit square), and the muzzle→target distance.
type aimErr struct {
	dYaw, dPitch float64
	nYaw, nPitch float64
	dist         float64
}

// aimAttribute picks the candidate whose hull is nearest the crosshair at the
// fire time, among those whose track brackets the fire time and — when aliveAt
// is non-nil — who are alive at it (nil for a hit's confirmed victims: the
// damage proves they were shootable at the fire time), and returns the error
// to it. "Nearest" uses the normalized error, so it is range-aware.
func aimAttribute(ss result.TrackSample, t int32, candidates []string, tracks map[string]*result.PositionTrack, aliveAt func(string, int32) bool) (tgt string, out aimErr, ok bool) {
	bestMag := math.MaxFloat64
	for _, e := range candidates {
		et := tracks[e]
		if !trackCovers(et, t) || (aliveAt != nil && !aliveAt(e, t)) {
			continue
		}
		es, ok2 := et.SampleAt(t)
		if !ok2 {
			continue
		}
		er := aimError(ss, es.X, es.Y, es.Z)
		if er.dist <= 0 {
			continue
		}
		if mag := math.Hypot(er.nYaw, er.nPitch); mag < bestMag {
			bestMag = mag
			tgt, out, ok = e, er, true
		}
	}
	return
}

// aimError computes the crosshair error from a shooter sample to an enemy
// origin. The shot traces from the weapon muzzle (origin + aimMuzzleZ) toward
// the hull center (origin + aimTargetZ); angles use the Quake forward
// convention (+pitch = down). The normalized error divides each axis by the
// target's angular half-extent, so the hull maps to the unit square: the yaw
// half-extent is the box silhouette at the viewing angle
// (aimHalfW·(|cosθ|+|sinθ|)), the pitch half-extent is aimHalfH.
func aimError(ss result.TrackSample, ox, oy, oz float64) aimErr {
	const deg = 180 / math.Pi
	dx := ox - ss.X
	dy := oy - ss.Y
	dz := (oz + aimTargetZ) - (ss.Z + aimMuzzleZ)
	dh := math.Hypot(dx, dy)
	var e aimErr
	e.dist = math.Hypot(dh, dz)
	if e.dist <= 0 {
		return e
	}
	f := aimForward(ss.VP, ss.VYa)
	e.dYaw = wrapPi(math.Atan2(dy, dx)-math.Atan2(f[1], f[0])) * deg
	e.dPitch = (math.Atan2(dz, dh) - math.Atan2(f[2], math.Hypot(f[0], f[1]))) * deg
	theta := math.Atan2(dy, dx)
	wEff := aimHalfW * (math.Abs(math.Cos(theta)) + math.Abs(math.Sin(theta)))
	if halfW := math.Atan2(wEff, dh) * deg; halfW > 0 {
		e.nYaw = e.dYaw / halfW
	}
	if halfH := math.Atan2(aimHalfH, e.dist) * deg; halfH > 0 {
		e.nPitch = e.dPitch / halfH
	}
	return e
}

// aimForward is the Quake AngleVectors forward from raw angle16 view angles
// (vp/vya in [0,65536) turn units): F = (cos p·cos y, cos p·sin y, −sin p).
func aimForward(vp, vya float64) [3]float64 {
	const a16 = math.Pi * 2 / 65536
	p := vp * a16
	y := vya * a16
	cp := math.Cos(p)
	return [3]float64{cp * math.Cos(y), cp * math.Sin(y), -math.Sin(p)}
}

// matchBeam returns the index of the beam fired at time t whose muzzle is
// closest to (and within aimBeamMatchUnits of) the shooter eye — robustly
// resolving the owner the BeamStreams columns don't carry. -1 if none.
func matchBeam(b *result.BeamStreams, t int32, ex, ey, ez float64) int {
	best := -1
	bestD := aimBeamMatchUnits * aimBeamMatchUnits
	for i, bt := range b.T {
		if bt != t {
			continue
		}
		dx, dy, dz := float64(b.Sx[i])-ex, float64(b.Sy[i])-ey, float64(b.Sz[i])-ez
		if d := dx*dx + dy*dy + dz*dz; d <= bestD {
			bestD = d
			best = i
		}
	}
	return best
}

// bboxDist is the distance from point p to the player hull centered at origin o
// (losBoxMin..losBoxMax). 0 inside the hull.
func bboxDist(px, py, pz, ox, oy, oz float64) float64 {
	cx := clampF(px, ox+float64(losBoxMin[0]), ox+float64(losBoxMax[0]))
	cy := clampF(py, oy+float64(losBoxMin[1]), oy+float64(losBoxMax[1]))
	cz := clampF(pz, oz+float64(losBoxMin[2]), oz+float64(losBoxMax[2]))
	dx, dy, dz := px-cx, py-cy, pz-cz
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func trackCovers(pt *result.PositionTrack, t int32) bool {
	return pt != nil && len(pt.T) > 0 && t >= pt.T[0] && t <= pt.T[len(pt.T)-1]
}

func wrapPi(a float64) float64 {
	for a > math.Pi {
		a -= 2 * math.Pi
	}
	for a < -math.Pi {
		a += 2 * math.Pi
	}
	return a
}
