package view

import (
	"errors"
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

// FragOptions filters FragResult. Empty fields mean "no filter".
type FragOptions struct {
	Players []string // killer or victim in this set
	Weapons []string // weapon token (rl, lg, ...); case-insensitive
}

// Frags returns the demo's FragResult, optionally narrowed to the named
// players / weapons. Returns ErrUnavailable when the demo has no frag log.
func Frags(r *result.Result, opts FragOptions) (*result.FragResult, error) {
	if r.Frags == nil {
		return nil, ErrUnavailable
	}
	players := toSet(opts.Players)
	weapons := toLowerSet(opts.Weapons)
	if len(players) == 0 && len(weapons) == 0 {
		return r.Frags, nil
	}

	out := &result.FragResult{TotalFrags: r.Frags.TotalFrags}
	if r.Frags.ByPlayer != nil {
		out.ByPlayer = make(map[string]*result.PlayerFrags, len(r.Frags.ByPlayer))
		for name, pf := range r.Frags.ByPlayer {
			if len(players) > 0 && !players[name] {
				continue
			}
			if len(weapons) > 0 {
				filtered := &result.PlayerFrags{Kills: pf.Kills, Deaths: pf.Deaths, ByWeapon: map[string]int{}}
				for wpn, n := range pf.ByWeapon {
					if weapons[strings.ToLower(wpn)] {
						filtered.ByWeapon[wpn] = n
					}
				}
				out.ByPlayer[name] = filtered
			} else {
				out.ByPlayer[name] = pf
			}
		}
	}
	if r.Frags.ByWeapon != nil {
		out.ByWeapon = make(map[string]int, len(r.Frags.ByWeapon))
		for wpn, n := range r.Frags.ByWeapon {
			if len(weapons) > 0 && !weapons[strings.ToLower(wpn)] {
				continue
			}
			out.ByWeapon[wpn] = n
		}
	}
	for _, fe := range r.Frags.Frags {
		if len(weapons) > 0 && !weapons[strings.ToLower(fe.Weapon)] {
			continue
		}
		if len(players) > 0 && !players[fe.Killer] && !players[fe.Victim] {
			continue
		}
		out.Frags = append(out.Frags, fe)
	}
	return out, nil
}

// DamageOptions filters DamageResult. Empty fields mean "no filter".
type DamageOptions struct {
	Players []string // attacker or victim in this set
	Weapons []string // attacker weapon token; "tele"/"stomp" select positional kills; case-insensitive
}

// Damage returns the demo's DamageResult, optionally narrowed to the named
// players / weapons. Telefrags and stomps carry no weapon; a weapon filter
// treats their implicit weapon as "tele" / "stomp". Returns ErrUnavailable
// when the demo has no KTX mvdhidden_dmgdone stream.
func Damage(r *result.Result, opts DamageOptions) (*result.DamageResult, error) {
	if r.Damage == nil {
		return nil, ErrUnavailable
	}
	players := toSet(opts.Players)
	weapons := toLowerSet(opts.Weapons)
	if len(players) == 0 && len(weapons) == 0 {
		return r.Damage, nil
	}

	d := r.Damage
	out := &result.DamageResult{TotalDamage: d.TotalDamage}
	if d.ByPlayer != nil {
		out.ByPlayer = make(map[string]*result.PlayerDamage, len(d.ByPlayer))
		for name, pd := range d.ByPlayer {
			if len(players) > 0 && !players[name] {
				continue
			}
			if len(weapons) > 0 {
				clone := *pd
				clone.ByWeapon = map[string]int{}
				for wpn, n := range pd.ByWeapon {
					if weapons[strings.ToLower(wpn)] {
						clone.ByWeapon[wpn] = n
					}
				}
				out.ByPlayer[name] = &clone
			} else {
				out.ByPlayer[name] = pd
			}
		}
	}
	if d.ByWeapon != nil {
		out.ByWeapon = make(map[string]int, len(d.ByWeapon))
		for wpn, n := range d.ByWeapon {
			if len(weapons) > 0 && !weapons[strings.ToLower(wpn)] {
				continue
			}
			out.ByWeapon[wpn] = n
		}
	}
	for _, mp := range d.Matrix {
		if len(players) > 0 && !players[mp.Attacker] && !players[mp.Victim] {
			continue
		}
		if len(weapons) > 0 {
			pair := result.DamagePair{Attacker: mp.Attacker, Victim: mp.Victim, ByWeapon: map[string]int{}}
			for wpn, n := range mp.ByWeapon {
				if weapons[strings.ToLower(wpn)] {
					pair.ByWeapon[wpn] = n
					pair.Damage += n
				}
			}
			if pair.Damage == 0 {
				continue
			}
			out.Matrix = append(out.Matrix, pair)
		} else {
			out.Matrix = append(out.Matrix, mp)
		}
	}
	for _, de := range d.Events {
		if len(weapons) > 0 && !weapons[strings.ToLower(de.Weapon)] {
			continue
		}
		if len(players) > 0 && !players[de.Attacker] && !players[de.Victim] {
			continue
		}
		out.Events = append(out.Events, de)
	}
	// Telefrags / stomps carry no weapon; treat their implicit weapon as
	// "tele" / "stomp" so weapon=tele|stomp retrieves them and any other
	// weapon filter excludes them.
	for _, tf := range d.Telefrags {
		if len(weapons) > 0 && !weapons["tele"] {
			continue
		}
		if len(players) > 0 && !players[tf.Attacker] && !players[tf.Victim] {
			continue
		}
		out.Telefrags = append(out.Telefrags, tf)
	}
	for _, st := range d.Stomps {
		if len(weapons) > 0 && !weapons["stomp"] {
			continue
		}
		if len(players) > 0 && !players[st.Attacker] && !players[st.Victim] {
			continue
		}
		out.Stomps = append(out.Stomps, st)
	}
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
