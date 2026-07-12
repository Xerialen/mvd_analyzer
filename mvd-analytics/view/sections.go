package view

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// ErrUnavailable signals that a result section could not be produced for
// this demo because the enabling signal was absent — no KTX demoinfo /
// damage stream, no frag log, etc. HTTP callers map it to 422
// "<section>_unavailable"; in-process callers test errors.Is(err,
// ErrUnavailable).
//
// The convention these functions encode (the R3 rule): an object-shaped
// section that requires a specific demo capability returns ErrUnavailable
// when that capability is absent (Frags, Damage). An always-computable
// object (Items — the layout is derived from the entity stream on any MVD
// source) and the list-shaped sections (Backpacks, WeaponPickups, Chat)
// never return ErrUnavailable; they return an empty value instead.
var ErrUnavailable = errors.New("result section unavailable")

// ErrBoundedUnavailable signals that the caller asked view.Damage for the
// bounded family (dmg=bounded) on a demo whose server mode made the bounded
// reconstruction impossible (DamageResult.BoundedMode "skipped:*") — there is
// no bounded figure to materialize. HTTP callers map it to 422; it wraps
// ErrUnavailable so errors.Is(err, ErrUnavailable) still holds for the generic
// unavailability handler, while a bounded-aware handler can single it out.
var ErrBoundedUnavailable = fmt.Errorf("bounded damage family unavailable: %w", ErrUnavailable)

// Filtering note: player names are matched case-sensitively (QW names are
// case-significant); weapon / item / kind tokens are matched
// case-insensitively against their canonical lowercase form.

// FragOptions filters FragResult. Empty fields mean "no filter". From/To
// are match-relative SECONDS (0 disables that bound), matching getEvents.
type FragOptions struct {
	Players []string // killer or victim in this set
	Weapons []string // weapon token (rl, lg, ...); case-insensitive
	From    float64  // window start, match-relative seconds (0 = no bound)
	To      float64  // window end, match-relative seconds (0 = no bound)
	Summary bool     // drop the per-event Frags log; keep only aggregates
}

// Frags returns the demo's FragResult, optionally narrowed to the named
// players / weapons / time window. Returns ErrUnavailable when the demo has
// no frag log.
//
// ALIASING: on the unfiltered path (and the unfiltered Summary path) the
// returned value shares the stored Result's aggregate maps/slices by
// reference — it is a read-only view. Callers MUST NOT mutate the returned
// FragResult or anything reachable from it (all current callers
// marshal-and-discard). The filtered path returns freshly-allocated data.
//
// Two paths, by design:
//
//   - UNFILTERED (no players AND no weapon AND no from AND no to): return the
//     STORED authoritative aggregates unchanged — byte-identical to what the
//     analyzer produced. These are NOT a pure function of the frag log
//     (per-player Deaths come from the protocol DeathEvent, and top-level
//     ByWeapon counts some generic-killer obituaries the log excludes), so a
//     recompute could not reproduce them. Summary alone still takes this path;
//     it only drops the log.
//
//   - SCOPING FILTER ACTIVE (players OR weapon OR from OR to): RECOMPUTE every
//     aggregate from the FILTERED frag log so the response is internally
//     consistent with the entries shown. These log-sourced aggregates reflect
//     exactly the shown entries and may differ from the authoritative
//     unfiltered totals for reconnect / unresolved-name edge cases — that is
//     expected and intended.
func Frags(r *result.Result, opts FragOptions) (*result.FragResult, error) {
	if r.Frags == nil {
		return nil, ErrUnavailable
	}
	players := toSet(opts.Players)
	weapons := toLowerSet(opts.Weapons)
	if len(players) == 0 && len(weapons) == 0 && opts.From == 0 && opts.To == 0 {
		if opts.Summary {
			// Shallow copy so we can drop the log without mutating the shared
			// stored Result; the aggregate maps stay shared by reference.
			cp := *r.Frags
			cp.Frags = nil
			return &cp, nil
		}
		return r.Frags, nil
	}

	startMs := secToMs(opts.From)
	endMs := secToMs(opts.To)

	// Filter the log to the entries the caller asked for.
	var filtered []result.FragEntry
	for _, fe := range r.Frags.Frags {
		if len(weapons) > 0 && !weapons[strings.ToLower(fe.Weapon)] {
			continue
		}
		if len(players) > 0 && !players[fe.Killer] && !players[fe.Victim] {
			continue
		}
		if startMs != 0 && fe.Time < startMs {
			continue
		}
		if endMs != 0 && fe.Time > endMs {
			continue
		}
		filtered = append(filtered, fe)
	}

	// Recompute all aggregates from the filtered log, mirroring the frag
	// analyzer's rules (frag.go handleObituaryPrint + Finalize):
	//   - TotalFrags = count of log entries (includes suicides + teamkills).
	//   - top-level ByWeapon = enemy kills only (!suicide && !teamkill).
	//   - per-player Kills = killer==P && !suicide && !teamkill.
	//   - per-player Deaths = victim==P (all deaths, incl. suicide/teamkill).
	//   - per-player TeamKills = killer==P && teamkill.
	//   - per-player ByWeapon = as Kills, split by weapon.
	out := &result.FragResult{
		TotalFrags: len(filtered),
		ByWeapon:   map[string]int{},
		ByPlayer:   map[string]*result.PlayerFrags{},
	}
	get := func(name string) *result.PlayerFrags {
		if len(players) > 0 && !players[name] {
			return nil
		}
		p, ok := out.ByPlayer[name]
		if !ok {
			p = &result.PlayerFrags{ByWeapon: map[string]int{}}
			out.ByPlayer[name] = p
		}
		return p
	}
	for _, fe := range filtered {
		if !fe.IsSuicide && !fe.IsTeamKill {
			out.ByWeapon[fe.Weapon]++
		}
		if v := get(fe.Victim); v != nil {
			v.Deaths++
		}
		if k := get(fe.Killer); k != nil {
			switch {
			case fe.IsTeamKill:
				k.TeamKills++
			case !fe.IsSuicide:
				k.Kills++
				k.ByWeapon[fe.Weapon]++
			}
		}
	}
	// PlayerFrags.ByWeapon has no omitempty, so it must serialize as {} not
	// null; every player created via get() gets an allocated (possibly empty)
	// map, matching the analyzer's eager getOrCreatePlayer allocation.
	// TeamKills carries omitempty, so leaving it 0 is the right shape.

	if !opts.Summary {
		if filtered == nil {
			// Log included but the filter matched nothing: serialize as [], so
			// null stays exclusively the summary-mode "log dropped" signal
			// (consistent with the {} aggregates and the damage matrix).
			filtered = []result.FragEntry{}
		}
		out.Frags = filtered
	}
	return out, nil
}

// DamageOptions filters DamageResult. Empty fields mean "no filter". From/To
// are match-relative SECONDS (0 disables that bound), matching getEvents.
type DamageOptions struct {
	Players []string // attacker or victim in this set
	Weapons []string // attacker weapon token; "tele"/"stomp" select positional kills; case-insensitive
	From    float64  // window start, match-relative seconds (0 = no bound)
	To      float64  // window end, match-relative seconds (0 = no bound)
	Summary bool     // drop the per-hit Events log; keep only aggregates

	// Dmg selects the damage family: "" / "raw" (the default) strip the
	// bounded additions down to the v53 FIELD layout — but with v54 fold
	// semantics (given/taken include the tele/stomp folds on standard-mode
	// demos), the kill entries' bounded/damage/victimWep retained, and
	// boundedMode surviving as the explainer; "both" serves the stored
	// shape (raw fields + bounded nests); "bounded" materializes the
	// bounded family into the raw field names. The view does NOT validate
	// this — the REST layer does — and treats anything but "bounded"/"both"
	// as raw.
	Dmg string
}

// Damage returns the demo's DamageResult, optionally narrowed to the named
// players / weapons / time window. Telefrags and stomps carry no weapon; a
// weapon filter treats their implicit weapon as "tele" / "stomp". Returns
// ErrUnavailable when the demo has no KTX mvdhidden_dmgdone stream.
//
// ALIASING: on the unfiltered path (and the unfiltered Summary path) the
// returned value shares the stored Result's aggregate maps/slices by
// reference — it is a read-only view. Callers MUST NOT mutate the returned
// DamageResult or anything reachable from it (all current callers
// marshal-and-discard). The filtered path returns freshly-allocated data.
//
// Two paths, matching Frags():
//
//   - UNFILTERED (no players AND no weapon AND no from AND no to): return the
//     STORED aggregates unchanged. Summary alone still takes this path; it
//     only drops the Events log.
//
//   - SCOPING FILTER ACTIVE (players OR weapon OR from OR to): RECOMPUTE every
//     aggregate (TotalDamage, ByPlayer given/taken/byWeapon/EWep buckets,
//     ByWeapon, Matrix) from the FILTERED per-hit Events, mirroring the damage
//     analyzer's rules. Damage aggregates are a pure function of Events plus the
//     telefrag/stomp fold-in, and both are match-gated at the source (the
//     analyzer drops out-of-match hits), so an all-players/full-window recompute
//     reproduces the stored numbers exactly — the two are built from the same
//     in-match hit set. The kill entries carry everything the fold needs
//     (bounded, the raw damage when it differs, victimWep for the buckets).
//
// FAMILY (opts.Dmg): the stored shape carries two damage families (schema v54).
// "both" serves it as stored; "raw"/"" strips every bounded addition to the v53
// shape; "bounded" materializes the bounded family into the raw field names.
// See DamageOptions.Dmg. On a skipped:* demo the bounded family was never
// reconstructed, so dmg=bounded returns ErrBoundedUnavailable; "both"/"raw"
// serve normally. All family transforms operate on COPIES — the stored Result
// is never mutated (aimcore/airgibs read its raw values in place).
func Damage(r *result.Result, opts DamageOptions) (*result.DamageResult, error) {
	if r.Damage == nil {
		return nil, ErrUnavailable
	}
	d := r.Damage

	// Family selection. hasBounded is true exactly when the analyzer
	// reconstructed the bounded family; a skipped:* demo carries none. Derive
	// it from BoundedMode — the field that NAMES the state ("standard" vs
	// "skipped:*") — rather than the Dmg echo the analyzer sets in lockstep
	// with it. The view does NOT validate opts.Dmg (the REST layer does) —
	// anything but "bounded"/"both" is the raw (v53) shape.
	fam := strings.ToLower(strings.TrimSpace(opts.Dmg))
	hasBounded := d.BoundedMode == "standard"
	if fam == "bounded" && !hasBounded {
		return nil, ErrBoundedUnavailable
	}
	// wantBounded gates the bounded-nest accumulation in the filtered recompute
	// below: only "both"/"bounded" need the nests built. The RAW totals fold
	// telefrag/stomp kill values in regardless (they are part of the stored raw
	// numbers), so that fold stays gated on hasBounded alone.
	wantBounded := hasBounded && (fam == "both" || fam == "bounded")

	players := toSet(opts.Players)
	weapons := toLowerSet(opts.Weapons)

	if len(players) == 0 && len(weapons) == 0 && opts.From == 0 && opts.To == 0 {
		// Unfiltered: serve the stored aggregates, transformed to the requested
		// family. "both" (and raw on a bounded-less demo — nothing to strip) may
		// alias the stored Result by reference; raw/bounded on a bounded-carrying
		// demo return owned copies.
		var out *result.DamageResult
		switch {
		case fam == "bounded":
			out = materializeBounded(d, !opts.Summary)
		case fam == "both":
			out = d
		default: // raw / ""
			if hasBounded {
				out = stripBounded(d, !opts.Summary)
			} else {
				out = d // already the v53 raw shape
			}
		}
		if opts.Summary {
			cp := *out
			cp.Events = nil
			return &cp, nil
		}
		return out, nil
	}

	startMs := secToMs(opts.From)
	endMs := secToMs(opts.To)

	matchEvent := func(attacker, victim, weapon string, tMs int32) bool {
		if len(weapons) > 0 && !weapons[strings.ToLower(weapon)] {
			return false
		}
		if len(players) > 0 && !players[attacker] && !players[victim] {
			return false
		}
		if startMs != 0 && tMs < startMs {
			return false
		}
		if endMs != 0 && tMs > endMs {
			return false
		}
		return true
	}

	// Filter the per-hit log first, then recompute the aggregates from it so
	// every figure is consistent with exactly the hits shown. Both families are
	// summed in parallel (the bounded nests) when the demo carries bounded; the
	// family transform below strips or materializes them.
	var events []result.DamageEntry
	for _, de := range d.Events {
		if matchEvent(de.Attacker, de.Victim, de.Weapon, de.Time) {
			events = append(events, de)
		}
	}

	out := &result.DamageResult{
		ByWeapon: map[string]int{},
		ByPlayer: map[string]*result.PlayerDamage{},
	}
	matrix := map[string]*result.DamagePair{}
	getP := func(name string) *result.PlayerDamage {
		if len(players) > 0 && !players[name] {
			return nil
		}
		p, ok := out.ByPlayer[name]
		if !ok {
			p = &result.PlayerDamage{ByWeapon: map[string]int{}}
			out.ByPlayer[name] = p
		}
		return p
	}
	for _, de := range events {
		// Match-level aggregates (TotalDamage, top-level ByWeapon, Matrix) count
		// every SHOWN hit — they describe the entries, not a player role, so
		// they are NOT gated by the players set (a hit shown because its victim
		// is in the set still counts its enemy pair in the matrix). Only the
		// per-player ByPlayer map is scoped to the set, via getP.
		out.TotalDamage += de.Damage
		// Per-hit bounded effective value: nil Bounded means "equal to Damage".
		bdmg := de.Damage
		if de.Bounded != nil {
			bdmg = *de.Bounded
		}
		enemy := de.Attacker != "world" && !de.IsSelf && !de.IsTeam
		if enemy {
			out.ByWeapon[de.Weapon] += de.Damage
			addPair(matrix, de.Attacker, de.Victim, de.Weapon, de.Damage)
		}

		if vp := getP(de.Victim); vp != nil {
			vp.Taken += de.Damage
			if de.IsEnv {
				vp.TakenEnv += de.Damage
			}
			if wantBounded {
				vb := vp.BoundedNest()
				vb.Taken += bdmg
				if de.IsEnv {
					vb.TakenEnv += bdmg
				}
			}
		}
		if de.Attacker == "world" {
			// World-sourced hit: no attacker to credit (mirrors the analyzer's
			// `if isWorld { continue }`; Attacker=="world" iff the wire slot
			// was <0). Note a non-world environmental hit still credits its
			// player attacker, matching the analyzer.
			continue
		}
		if ap := getP(de.Attacker); ap != nil {
			switch {
			case de.IsSelf:
				ap.GivenSelf += de.Damage
				if wantBounded {
					ap.BoundedNest().GivenSelf += bdmg
				}
			case de.IsTeam:
				ap.GivenTeam += de.Damage
				if wantBounded {
					ap.BoundedNest().GivenTeam += bdmg
				}
			default:
				ap.Given += de.Damage
				ap.ByWeapon[de.Weapon] += de.Damage
				addVictimBucket(ap, de.VictimWep, de.Damage)
				if wantBounded {
					ab := ap.BoundedNest()
					ab.Given += bdmg
					ab.ByWeapon[de.Weapon] += bdmg
					addVictimBucket(ab, de.VictimWep, bdmg)
				}
			}
		}
	}
	out.Matrix = flattenDamageMatrix(matrix)

	// Positional kills (telefrags/stomps) aren't in Events. Filter the stored
	// lists directly, treating their implicit weapon as "tele"/"stomp", recompute
	// the per-player counts from what survives, and fold their value into the
	// given/taken aggregates — the raw family now DEPENDS on that fold (v54), so
	// an all-players recompute must reproduce it or it wouldn't equal the stored
	// numbers. Fold only when the bounded family exists: a skipped:* demo folds
	// nothing (v53 exclusion semantics), matching the analyzer.
	foldKill := func(k result.PositionalKill) {
		// Mirror the analyzer's fold exactly: the raw family folds the kill's
		// Damage (present only when it differs from Bounded — a stomp whose
		// bounded arithmetic capped below the wire value), the bounded family
		// folds Bounded. Telefrags fold the same number into both. A nil
		// Bounded never reaches here (the fold is gated on hasBounded, and
		// the analyzer sets it on every kill it folds).
		b := 0
		if k.Bounded != nil {
			b = *k.Bounded
		}
		raw := k.Damage
		if raw == 0 {
			raw = b
		}
		if vp := getP(k.Victim); vp != nil {
			vp.Taken += raw
			if wantBounded {
				vp.BoundedNest().Taken += b
			}
		}
		if k.Attacker == "world" {
			return
		}
		ap := getP(k.Attacker)
		if ap == nil {
			return
		}
		switch {
		case k.Attacker == k.Victim:
			ap.GivenSelf += raw
			if wantBounded {
				ap.BoundedNest().GivenSelf += b
			}
		case k.IsTeam:
			ap.GivenTeam += raw
			if wantBounded {
				ap.BoundedNest().GivenTeam += b
			}
		default:
			ap.Given += raw
			addVictimBucket(ap, k.VictimWep, raw)
			if wantBounded {
				ab := ap.BoundedNest()
				ab.Given += b
				addVictimBucket(ab, k.VictimWep, b)
			}
		}
	}
	for _, tf := range d.Telefrags {
		if !matchEvent(tf.Attacker, tf.Victim, "tele", tf.Time) {
			continue
		}
		out.Telefrags = append(out.Telefrags, tf)
		if !tf.IsTeam && tf.Attacker != "world" && tf.Attacker != tf.Victim {
			if ap := getP(tf.Attacker); ap != nil {
				ap.Telefrags++
			}
		}
		if hasBounded {
			foldKill(tf)
		}
	}
	for _, st := range d.Stomps {
		if !matchEvent(st.Attacker, st.Victim, "stomp", st.Time) {
			continue
		}
		out.Stomps = append(out.Stomps, st)
		if !st.IsTeam && st.Attacker != "world" && st.Attacker != st.Victim {
			if ap := getP(st.Attacker); ap != nil {
				ap.Stomps++
			}
		}
		if hasBounded {
			foldKill(st)
		}
	}

	// Scoreboard is a KTX end-of-match cross-check keyed by player; it has no
	// per-event provenance to recompute, so narrow it by the players filter
	// (as before) and pass the deltas through unchanged.
	if d.Scoreboard != nil {
		sb := &result.DamageReconciliation{ByPlayer: map[string]*result.DamageDelta{}}
		for name, dd := range d.Scoreboard.ByPlayer {
			if len(players) > 0 && !players[name] {
				continue
			}
			// Value copy so the raw strip below can nil the bounded nest
			// without writing through to the stored Result.
			cp := *dd
			sb.ByPlayer[name] = &cp
		}
		out.Scoreboard = sb
	}

	// Carry the stored family echo, then apply the requested family transform.
	// out currently holds the "both" shape (raw fields + bounded nests). The
	// transforms recompute over out.Events, so it must be set first.
	out.Dmg = d.Dmg
	out.BoundedMode = d.BoundedMode
	out.Events = events
	switch {
	case fam == "bounded":
		out = materializeBounded(out, !opts.Summary)
	case fam == "both":
		// keep as built
	default: // raw / "" — out is fully owned here, so strip in place instead
		// of the unfiltered path's copy-strip: the nests were never built
		// (wantBounded), leaving only the event/scoreboard pointers and the
		// family echo. The event entries are value copies; nil-ing their
		// Bounded does not write through to the stored log.
		if hasBounded {
			out.Dmg = ""
			for i := range out.Events {
				out.Events[i].Bounded = nil
			}
			if out.Scoreboard != nil {
				for _, dd := range out.Scoreboard.ByPlayer {
					dd.Bounded = nil
				}
			}
		}
	}

	if opts.Summary {
		out.Events = nil
	} else if out.Events == nil {
		// Same rule as Frags: an included-but-empty log is [], never null.
		out.Events = []result.DamageEntry{}
	}
	return out, nil
}

// stripBounded returns an owned copy of d in the v53 raw shape: every v54
// bounded addition removed (events[].Bounded, byPlayer nests' Bounded,
// scoreboard deltas' Bounded, Dmg). BoundedMode survives (on a skipped:* demo
// it explains why no bounded family exists). Telefrags/stomps keep their
// Bounded (+VictimWep): the raw given/taken now DEPEND on the folded kill
// value, so the kill entries are what make the raw totals explainable. Caller
// must never pass stored memory expecting it left untouched — the copies here
// protect it, but only the parts stripBounded rewrites.
func stripBounded(d *result.DamageResult, includeEvents bool) *result.DamageResult {
	cp := *d
	cp.Dmg = ""
	if !includeEvents {
		// A Summary caller drops the log anyway — don't clone it just to
		// strip pointers it will never serialize.
		cp.Events = nil
	} else if d.Events != nil {
		ev := make([]result.DamageEntry, len(d.Events))
		copy(ev, d.Events)
		for i := range ev {
			ev[i].Bounded = nil
		}
		cp.Events = ev
	}
	if d.ByPlayer != nil {
		bp := make(map[string]*result.PlayerDamage, len(d.ByPlayer))
		for k, v := range d.ByPlayer {
			pv := *v // shallow copy: nil the nest without writing through
			pv.Bounded = nil
			bp[k] = &pv
		}
		cp.ByPlayer = bp
	}
	if d.Scoreboard != nil {
		sb := &result.DamageReconciliation{ByPlayer: make(map[string]*result.DamageDelta, len(d.Scoreboard.ByPlayer))}
		for k, v := range d.Scoreboard.ByPlayer {
			dv := *v
			dv.Bounded = nil
			sb.ByPlayer[k] = &dv
		}
		cp.Scoreboard = sb
	}
	// ByWeapon, Matrix, Telefrags, Stomps stay shared by reference — read-only
	// downstream, never mutated here.
	return &cp
}

// materializeBounded returns an owned copy of d with the bounded family promoted
// into the raw field names: each event carries its bounded amount, per-player
// figures come from the bounded nest (Telefrags/Stomps counts kept from the
// parent), and TotalDamage/ByWeapon/Matrix are recomputed from the materialized
// events. Scoreboard is kept as stored (it already carries both sides).
// Telefrags/stomps stay as-is (their Bounded already IS the bounded fold value).
// Requires the bounded family to exist — the caller checks hasBounded.
func materializeBounded(d *result.DamageResult, includeEvents bool) *result.DamageResult {
	cp := *d
	cp.Dmg = "bounded"

	var ev []result.DamageEntry
	if includeEvents && d.Events != nil {
		ev = make([]result.DamageEntry, len(d.Events))
		copy(ev, d.Events)
	}
	total := 0
	byWeapon := map[string]int{}
	matrix := map[string]*result.DamagePair{}
	for i := range d.Events {
		// Iterate the source read-only: a Summary caller drops the log, so
		// the aggregates are computed without cloning 2-3k entries.
		dmg := d.Events[i].Damage
		if b := d.Events[i].Bounded; b != nil {
			dmg = *b
		}
		if ev != nil {
			ev[i].Damage = dmg
			ev[i].Bounded = nil
		}
		total += dmg
		e := &d.Events[i]
		if e.Attacker != "world" && !e.IsSelf && !e.IsTeam {
			byWeapon[e.Weapon] += dmg
			addPair(matrix, e.Attacker, e.Victim, e.Weapon, dmg)
		}
	}
	cp.Events = ev
	cp.TotalDamage = total
	cp.ByWeapon = byWeapon
	cp.Matrix = flattenDamageMatrix(matrix)

	if d.ByPlayer != nil {
		bp := make(map[string]*result.PlayerDamage, len(d.ByPlayer))
		for k, v := range d.ByPlayer {
			bp[k] = materializePlayer(v)
		}
		cp.ByPlayer = bp
	}
	return &cp
}

// materializePlayer builds one player's bounded-materialized figure: the damage
// fields come from the bounded nest, the Telefrags/Stomps counts from the parent
// (those aren't part of the bounded nest), and the nested Bounded is dropped.
func materializePlayer(p *result.PlayerDamage) *result.PlayerDamage {
	out := &result.PlayerDamage{
		Telefrags: p.Telefrags,
		Stomps:    p.Stomps,
		ByWeapon:  map[string]int{}, // no omitempty — must serialize as {}
	}
	if b := p.Bounded; b != nil {
		out.Given = b.Given
		out.Taken = b.Taken
		out.GivenTeam = b.GivenTeam
		out.GivenSelf = b.GivenSelf
		out.TakenEnv = b.TakenEnv
		out.EnemyVsSG = b.EnemyVsSG
		out.EnemyVsMid = b.EnemyVsMid
		out.EnemyVsLG = b.EnemyVsLG
		out.EnemyVsRL = b.EnemyVsRL
		out.EnemyVsBoth = b.EnemyVsBoth
		out.EWep = b.EWep
		for k, v := range b.ByWeapon {
			out.ByWeapon[k] = v
		}
	}
	return out
}

// addPair aggregates one attacker→victim hit into the damage matrix, mirroring
// the analyzer's addToMatrix.
func addPair(m map[string]*result.DamagePair, attacker, victim, weapon string, dmg int) {
	key := attacker + "\x00" + victim
	p, ok := m[key]
	if !ok {
		p = &result.DamagePair{Attacker: attacker, Victim: victim, ByWeapon: map[string]int{}}
		m[key] = p
	}
	p.Damage += dmg
	p.ByWeapon[weapon] += dmg
}

// flattenDamageMatrix flattens + sorts the matrix deterministically, mirroring
// the analyzer's flattenMatrix (attacker, then victim).
func flattenDamageMatrix(m map[string]*result.DamagePair) []result.DamagePair {
	out := make([]result.DamagePair, 0, len(m))
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

// addVictimBucket credits enemy-given damage to the victim-weapon (EWep)
// buckets from the hit's recorded VictimWep class, mirroring the analyzer's
// addVictimWeaponBucket (which classifies the victim's StatItems bitfield into
// the same "both"/"rl"/"lg"/"mid"/sg classes).
func addVictimBucket(p *result.PlayerDamage, class string, dmg int) {
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

// ItemOptions filters ItemsResult. Empty fields mean "no filter".
type ItemOptions struct {
	Items   []string // instance Name ("ya_1") or kind token ("ya"); case-insensitive
	Players []string // keep only phases TakenBy one of these (case-sensitive)
	Kinds   []string // item category (armor, mega, ...) or raw kind; case-insensitive
	From    float64  // window start, match-relative seconds (0 = no bound)
	To      float64  // window end, match-relative seconds (0 = no bound)
}

// Items returns the demo's per-item pickup/respawn timeline, optionally
// filtered. The item layout is derived from the entity stream on any MVD
// source, so this is always available — an absent section yields an empty
// list, never ErrUnavailable. Phases with no TakenBy survive a players
// filter (they represent the item's availability state).
//
// The From/To window keeps phases that OVERLAP it: a phase covers
// [availableFrom, respawnAt), open-ended when respawnAt is 0 (item
// still up, or taken and not yet back). Overlap — not take-in-window —
// so the response still tells the item's state across the whole window;
// the summary shape (ItemsSummary) is the one that counts takes inside
// the window.
func Items(r *result.Result, opts ItemOptions) *result.ItemsResult {
	if r.Items == nil {
		return &result.ItemsResult{Items: []result.ItemTimeline{}}
	}
	itemSet := toLowerSet(opts.Items)
	players := toSet(opts.Players)
	kindSet := toLowerSet(opts.Kinds)
	startMs := secToMs(opts.From)
	endMs := secToMs(opts.To)
	if len(itemSet) == 0 && len(players) == 0 && len(kindSet) == 0 && startMs == 0 && endMs == 0 {
		return r.Items
	}

	// Boundary convention: the query window is CLOSED [from, to], like
	// every sibling endpoint (frags/damage/backpacks keep time == to).
	// Both bounds compare strictly so a phase touching the window at a
	// single boundary instant survives — this matters for weapon-stay
	// demos, whose zero-length phases (takenAt == respawnAt) would
	// otherwise vanish when the take lands exactly on `from`.
	keepPhase := func(ph result.ItemPhase) bool {
		if len(players) > 0 && ph.TakenBy != "" && !players[ph.TakenBy] {
			return false
		}
		if endMs > 0 && ph.AvailableFrom > endMs {
			return false
		}
		if startMs > 0 && ph.RespawnAt != 0 && ph.RespawnAt < startMs {
			return false
		}
		return true
	}

	out := &result.ItemsResult{Items: make([]result.ItemTimeline, 0, len(r.Items.Items))}
	for _, it := range r.Items.Items {
		if len(itemSet) > 0 && !itemSet[strings.ToLower(it.Name)] && !itemSet[strings.ToLower(it.Kind)] {
			continue
		}
		if len(kindSet) > 0 && !kindSet[it.Category()] && !kindSet[strings.ToLower(it.Kind)] {
			continue
		}
		if len(players) == 0 && startMs == 0 && endMs == 0 {
			out.Items = append(out.Items, it)
			continue
		}
		kept := it
		kept.Phases = make([]result.ItemPhase, 0, len(it.Phases))
		for _, ph := range it.Phases {
			if keepPhase(ph) {
				kept.Phases = append(kept.Phases, ph)
			}
		}
		if len(kept.Phases) == 0 {
			continue
		}
		out.Items = append(out.Items, kept)
	}
	return out
}

// ItemsSummaryView is the summary=true shape of /items: per-item take
// aggregates instead of the full phase timeline. Cheap enough to be the
// MCP-layer default (PLAN-api-usability D1).
type ItemsSummaryView struct {
	Items []ItemSummary `json:"items"`
}

// ItemSummary aggregates one spawner's takes. Counted takes are the
// attributed-or-timed ones whose TakenAt falls INSIDE the From/To
// window (unlike the full timeline, which keeps overlapping phases).
type ItemSummary struct {
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	EntNum     int            `json:"entNum"`
	Loc        string         `json:"loc,omitempty"`
	TakenCount int            `json:"takenCount"`
	ByPlayer   map[string]int `json:"byPlayer,omitempty"`
	FirstTake  *ItemTake      `json:"firstTake,omitempty"`
}

// ItemTake is one take: t in match-relative seconds (view surface unit).
type ItemTake struct {
	T       float64 `json:"t"`
	TakenBy string  `json:"takenBy,omitempty"`
	Team    string  `json:"team,omitempty"`
}

// ItemsSummary reduces the (filtered) item timelines to per-item take
// counts, a by-player split, and the first take in the window. Items
// that match the identity filters survive with TakenCount 0 when
// nothing took them in the window (their availability is still signal).
func ItemsSummary(r *result.Result, opts ItemOptions) *ItemsSummaryView {
	filtered := Items(r, opts)
	startMs := secToMs(opts.From)
	endMs := secToMs(opts.To)
	out := &ItemsSummaryView{Items: make([]ItemSummary, 0, len(filtered.Items))}
	for _, it := range filtered.Items {
		s := ItemSummary{Name: it.Name, Kind: it.Kind, EntNum: it.EntNum, Loc: it.Loc}
		for _, ph := range it.Phases {
			if ph.TakenAt == 0 && ph.TakenBy == "" {
				continue // untaken availability phase
			}
			// Closed window [from, to] on the take time — a take at
			// exactly `to` counts, matching getFrags/getBackpacks.
			if startMs > 0 && ph.TakenAt < startMs {
				continue
			}
			if endMs > 0 && ph.TakenAt > endMs {
				continue
			}
			s.TakenCount++
			if ph.TakenBy != "" {
				if s.ByPlayer == nil {
					s.ByPlayer = make(map[string]int)
				}
				s.ByPlayer[ph.TakenBy]++
			}
			if s.FirstTake == nil || secs(ph.TakenAt) < s.FirstTake.T {
				s.FirstTake = &ItemTake{T: secs(ph.TakenAt), TakenBy: ph.TakenBy, Team: ph.Team}
			}
		}
		out.Items = append(out.Items, s)
	}
	return out
}

// BackpackOptions filters the backpack-drop list. Empty fields mean "no
// filter".
type BackpackOptions struct {
	Players []string // dropper name (case-sensitive)
	Weapons []string // "rl"/"lg"; case-insensitive (CSV — multiple accepted)
	From    float64  // window start, match-relative seconds (0 = no bound)
	To      float64  // window end, match-relative seconds (0 = no bound)
}

// Backpacks returns the demo's RL/LG backpack drops, optionally filtered.
// Always available; an empty list when the demo has none. From/To window
// the drop time.
func Backpacks(r *result.Result, opts BackpackOptions) []result.BackpackDrop {
	out := []result.BackpackDrop{}
	if len(r.Backpacks) == 0 {
		return out
	}
	players := toSet(opts.Players)
	weapons := toLowerSet(opts.Weapons)
	startMs := secToMs(opts.From)
	endMs := secToMs(opts.To)
	for _, b := range r.Backpacks {
		if len(players) > 0 && !players[b.Player] {
			continue
		}
		if len(weapons) > 0 && !weapons[strings.ToLower(b.Weapon)] {
			continue
		}
		if (startMs > 0 && b.Time < startMs) || (endMs > 0 && b.Time > endMs) {
			continue
		}
		out = append(out, b)
	}
	return out
}

// WeaponPickupOptions filters the weapon-pickup list. Empty fields mean "no
// filter".
type WeaponPickupOptions struct {
	Players []string // picker name (case-sensitive)
	Weapons []string // weapon token; case-insensitive
	Source  string   // "world" | "backpack"; case-insensitive
	From    float64  // window start, match-relative seconds (0 = no bound)
	To      float64  // window end, match-relative seconds (0 = no bound)
}

// WeaponPickups returns the demo's slot-weapon acquisitions, optionally
// filtered. Always available; an empty list when the demo has none.
// From/To window the pickup time.
func WeaponPickups(r *result.Result, opts WeaponPickupOptions) []result.WeaponPickup {
	out := []result.WeaponPickup{}
	if len(r.WeaponPickups) == 0 {
		return out
	}
	players := toSet(opts.Players)
	weapons := toLowerSet(opts.Weapons)
	source := strings.ToLower(strings.TrimSpace(opts.Source))
	startMs := secToMs(opts.From)
	endMs := secToMs(opts.To)
	for _, wp := range r.WeaponPickups {
		if len(players) > 0 && !players[wp.Player] {
			continue
		}
		if len(weapons) > 0 && !weapons[strings.ToLower(wp.Weapon)] {
			continue
		}
		if source != "" && wp.Source != source {
			continue
		}
		if (startMs > 0 && wp.Time < startMs) || (endMs > 0 && wp.Time > endMs) {
			continue
		}
		out = append(out, wp)
	}
	return out
}

// ChatOptions filters the chat/teamsay event list. From/To are
// match-relative seconds (0 disables that bound); Types defaults to
// {chat, teamsay}.
type ChatOptions struct {
	From    float64
	To      float64
	Players []string // sender name (case-sensitive)
	Types   []string // defaults to chat,teamsay
}

// Chat returns the chat/teamsay slice of the messages stream, optionally
// filtered. Always available; an empty list when the demo has no messages.
func Chat(r *result.Result, opts ChatOptions) []result.MatchEvent {
	out := []result.MatchEvent{}
	if r.Messages == nil {
		return out
	}
	players := toSet(opts.Players)
	types := toSet(opts.Types)
	if len(types) == 0 {
		types = map[string]bool{"chat": true, "teamsay": true}
	}
	startMs := secToMs(opts.From)
	endMs := secToMs(opts.To)
	for _, ev := range r.Messages.Events {
		if !types[ev.Type] {
			continue
		}
		if startMs != 0 && ev.Time < startMs {
			continue
		}
		if endMs != 0 && ev.Time > endMs {
			continue
		}
		if len(players) > 0 && !players[ev.Player] {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// secToMs converts a match-relative seconds bound to int32 ms, rounding to
// the nearest ms (not truncating) so e.g. from=0.29s maps to 290ms, not 289.
func secToMs(sec float64) int32 {
	return int32(math.Round(sec * 1000))
}

// secs converts int32 ms to the view surface's float64 seconds.
// Division (not `*0.001`) matters: IEEE division is correctly rounded,
// so the result is the double nearest to the decimal value and JSON
// prints "13.155" — multiplying by the inexact 0.001 can land one ulp
// off and print "13.155000000000001" (PLAN-api-usability D8).
func secs(ms int32) float64 {
	return float64(ms) / 1000
}

// toSet builds a case-sensitive lookup set, trimming and dropping empties.
func toSet(vals []string) map[string]bool {
	if len(vals) == 0 {
		return nil
	}
	s := make(map[string]bool, len(vals))
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			s[v] = true
		}
	}
	return s
}

// toLowerSet builds a case-insensitive lookup set (lowercased), trimming
// and dropping empties.
func toLowerSet(vals []string) map[string]bool {
	if len(vals) == 0 {
		return nil
	}
	s := make(map[string]bool, len(vals))
	for _, v := range vals {
		if v = strings.TrimSpace(strings.ToLower(v)); v != "" {
			s[v] = true
		}
	}
	return s
}
