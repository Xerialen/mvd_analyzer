package view

import (
	"errors"
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

	startMs := int32(opts.From * 1000)
	endMs := int32(opts.To * 1000)

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
}

// Damage returns the demo's DamageResult, optionally narrowed to the named
// players / weapons / time window. Telefrags and stomps carry no weapon; a
// weapon filter treats their implicit weapon as "tele" / "stomp". Returns
// ErrUnavailable when the demo has no KTX mvdhidden_dmgdone stream.
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
//     analyzer's rules. Damage aggregates are a pure function of Events, so on
//     a fully in-match stream the recompute reproduces the stored numbers;
//     they can differ only where the stored aggregates gate out warmup hits
//     that the Events log (ungated) still carries.
func Damage(r *result.Result, opts DamageOptions) (*result.DamageResult, error) {
	if r.Damage == nil {
		return nil, ErrUnavailable
	}
	players := toSet(opts.Players)
	weapons := toLowerSet(opts.Weapons)
	if len(players) == 0 && len(weapons) == 0 && opts.From == 0 && opts.To == 0 {
		if opts.Summary {
			cp := *r.Damage
			cp.Events = nil
			return &cp, nil
		}
		return r.Damage, nil
	}

	d := r.Damage
	startMs := int32(opts.From * 1000)
	endMs := int32(opts.To * 1000)

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
	// every figure is consistent with exactly the hits shown.
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
			case de.IsTeam:
				ap.GivenTeam += de.Damage
			default:
				ap.Given += de.Damage
				ap.ByWeapon[de.Weapon] += de.Damage
				addVictimBucket(ap, de.VictimWep, de.Damage)
			}
		}
	}
	out.Matrix = flattenDamageMatrix(matrix)

	// Positional kills (telefrags/stomps) aren't in Events. Filter the stored
	// lists directly, treating their implicit weapon as "tele"/"stomp", and
	// recompute the per-player counts from what survives.
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
	}

	if !opts.Summary {
		out.Events = events
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
			sb.ByPlayer[name] = dd
		}
		out.Scoreboard = sb
	}
	return out, nil
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
}

// Items returns the demo's per-item pickup/respawn timeline, optionally
// filtered. The item layout is derived from the entity stream on any MVD
// source, so this is always available — an absent section yields an empty
// list, never ErrUnavailable. Phases with no TakenBy survive a players
// filter (they represent the item's availability state).
func Items(r *result.Result, opts ItemOptions) *result.ItemsResult {
	if r.Items == nil {
		return &result.ItemsResult{Items: []result.ItemTimeline{}}
	}
	itemSet := toLowerSet(opts.Items)
	players := toSet(opts.Players)
	kindSet := toLowerSet(opts.Kinds)
	if len(itemSet) == 0 && len(players) == 0 && len(kindSet) == 0 {
		return r.Items
	}

	out := &result.ItemsResult{Items: make([]result.ItemTimeline, 0, len(r.Items.Items))}
	for _, it := range r.Items.Items {
		if len(itemSet) > 0 && !itemSet[strings.ToLower(it.Name)] && !itemSet[strings.ToLower(it.Kind)] {
			continue
		}
		if len(kindSet) > 0 && !kindSet[it.Category()] && !kindSet[strings.ToLower(it.Kind)] {
			continue
		}
		if len(players) > 0 {
			kept := it
			kept.Phases = make([]result.ItemPhase, 0, len(it.Phases))
			for _, ph := range it.Phases {
				if ph.TakenBy == "" || players[ph.TakenBy] {
					kept.Phases = append(kept.Phases, ph)
				}
			}
			if len(kept.Phases) == 0 {
				continue
			}
			out.Items = append(out.Items, kept)
			continue
		}
		out.Items = append(out.Items, it)
	}
	return out
}

// BackpackOptions filters the backpack-drop list. Empty fields mean "no
// filter".
type BackpackOptions struct {
	Players []string // dropper name (case-sensitive)
	Weapons []string // "rl"/"lg"; case-insensitive (CSV — multiple accepted)
}

// Backpacks returns the demo's RL/LG backpack drops, optionally filtered.
// Always available; an empty list when the demo has none.
func Backpacks(r *result.Result, opts BackpackOptions) []result.BackpackDrop {
	out := []result.BackpackDrop{}
	if len(r.Backpacks) == 0 {
		return out
	}
	players := toSet(opts.Players)
	weapons := toLowerSet(opts.Weapons)
	for _, b := range r.Backpacks {
		if len(players) > 0 && !players[b.Player] {
			continue
		}
		if len(weapons) > 0 && !weapons[strings.ToLower(b.Weapon)] {
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
}

// WeaponPickups returns the demo's slot-weapon acquisitions, optionally
// filtered. Always available; an empty list when the demo has none.
func WeaponPickups(r *result.Result, opts WeaponPickupOptions) []result.WeaponPickup {
	out := []result.WeaponPickup{}
	if len(r.WeaponPickups) == 0 {
		return out
	}
	players := toSet(opts.Players)
	weapons := toLowerSet(opts.Weapons)
	source := strings.ToLower(strings.TrimSpace(opts.Source))
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
	startMs := int32(opts.From * 1000)
	endMs := int32(opts.To * 1000)
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
