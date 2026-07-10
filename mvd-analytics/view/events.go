package view

import (
	"sort"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// EventsFilter narrows an Events query by time, player set, or event
// type. An empty Types filter selects the discrete-event set (D15);
// an explicit list — even with one entry — overrides that default.
type EventsFilter struct {
	StartTime float64
	EndTime   float64
	Players   []string
	Types     []string
	// LocIndex selects the loc-event representation: false (default)
	// puts the resolved name under Detail["loc"]; true puts the raw
	// LocTable index under Detail["li"] (decode via /loc-table).
	LocIndex bool
}

// EventsView is the response shape: a flat list of TaggedEvent in
// time order.
type EventsView struct {
	Events []TaggedEvent `json:"events"`
}

// TaggedEvent is a uniform time-ordered event record. Detail is
// always non-nil for the types that have details (frag, weapon, …)
// and may be nil for spawn / death where the timestamp is the whole
// signal.
type TaggedEvent struct {
	T      float64        `json:"t"`
	Type   string         `json:"type"`
	Player string         `json:"player,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
}

// Default Types when EventsFilter.Types is empty (D15: omit the
// high-frequency change events that drown the discrete-event story).
var defaultEventTypes = []string{
	"frag", "powerup", "streak", "spawn", "death", "weapon", "item", "chat",
	"pickup",
}

// Events returns a time-ordered list of events matching the filter.
// Synthesised from result.TimelineAnalysis.{FragEvents, PowerupEvents,
// FragStreaks}, result.Messages, result.Streams change entries, and —
// for the pickup type — result.Items / result.WeaponPickups.
func Events(r *result.Result, filter EventsFilter) (*EventsView, error) {
	if r == nil {
		return &EventsView{}, nil
	}
	types := filter.Types
	if len(types) == 0 {
		types = defaultEventTypes
	}
	want := make(map[string]bool, len(types))
	for _, t := range types {
		want[t] = true
	}
	pf := newPlayerFilter(filter.Players)
	// Public end is float64 seconds; schema stores int32 ms. Convert at
	// the boundary.
	end := filter.EndTime
	if end == 0 && r.Streams != nil {
		end = float64(r.Streams.Global.MatchEnd) * 0.001
	}
	if end == 0 {
		end = inferMatchEnd(r)
	}

	// Helper: convert int32-ms timestamp from a result-schema field
	// into the float64-seconds TaggedEvent.T, plus window check.
	msToSec := func(tMs int32) float64 { return float64(tMs) * 0.001 }

	var events []TaggedEvent
	if want["frag"] && r.TimelineAnalysis != nil {
		for _, fe := range r.TimelineAnalysis.FragEvents {
			ts := msToSec(fe.Time)
			if !inWindow(ts, filter.StartTime, end) {
				continue
			}
			if !pf.accepts(fe.Player) {
				continue
			}
			detail := map[string]any{"team": fe.Team, "delta": fe.Delta}
			events = append(events, TaggedEvent{
				T: ts, Type: "frag", Player: fe.Player, Detail: detail,
			})
		}
	}
	if want["powerup"] && r.TimelineAnalysis != nil {
		for _, pe := range r.TimelineAnalysis.PowerupEvents {
			ts := msToSec(pe.Time)
			if !inWindow(ts, filter.StartTime, end) {
				continue
			}
			if !pf.accepts(pe.PlayerName) {
				continue
			}
			detail := map[string]any{
				"powerup":  pe.PowerupType,
				"endTime":  msToSec(pe.EndTime),
				"duration": msToSec(pe.Duration),
				"frags":    pe.Frags,
				"team":     pe.Team,
			}
			events = append(events, TaggedEvent{
				T: ts, Type: "powerup", Player: pe.PlayerName, Detail: detail,
			})
		}
	}
	if want["streak"] && r.TimelineAnalysis != nil {
		for _, fs := range r.TimelineAnalysis.FragStreaks {
			ts := msToSec(fs.Time)
			if !inWindow(ts, filter.StartTime, end) {
				continue
			}
			if !pf.accepts(fs.PlayerName) {
				continue
			}
			detail := map[string]any{
				"length":   fs.Frags,
				"endTime":  msToSec(fs.EndTime),
				"duration": msToSec(fs.Duration),
				"weapon":   fs.Ewep,
				"team":     fs.Team,
			}
			events = append(events, TaggedEvent{
				T: ts, Type: "streak", Player: fs.PlayerName, Detail: detail,
			})
		}
	}
	if want["damage"] && r.Damage != nil {
		for _, d := range r.Damage.Events {
			ts := msToSec(d.Time)
			if !inWindow(ts, filter.StartTime, end) {
				continue
			}
			// A player filter matches damage they dealt OR received.
			if !pf.accepts(d.Attacker) && !pf.accepts(d.Victim) {
				continue
			}
			detail := map[string]any{
				"victim": d.Victim,
				"damage": d.Damage,
				"weapon": d.Weapon,
			}
			if d.IsSplash {
				detail["isSplash"] = true
			}
			if d.IsEnv {
				detail["isEnv"] = true
			}
			if d.IsSelf {
				detail["isSelf"] = true
			}
			if d.IsTeam {
				detail["isTeam"] = true
			}
			if d.VictimWep != "" {
				detail["victimWep"] = d.VictimWep
			}
			events = append(events, TaggedEvent{
				T: ts, Type: "damage", Player: d.Attacker, Detail: detail,
			})
		}
	}
	if want["telefrag"] && r.Damage != nil {
		// Telefrags also appear as "frag" events (from obituaries); this is
		// the dedicated lens, opt-in so the default kill feed isn't doubled.
		for _, tf := range r.Damage.Telefrags {
			ts := msToSec(tf.Time)
			if !inWindow(ts, filter.StartTime, end) {
				continue
			}
			if !pf.accepts(tf.Attacker) && !pf.accepts(tf.Victim) {
				continue
			}
			detail := map[string]any{"victim": tf.Victim}
			if tf.IsTeam {
				detail["isTeam"] = true
			}
			events = append(events, TaggedEvent{
				T: ts, Type: "telefrag", Player: tf.Attacker, Detail: detail,
			})
		}
	}
	if want["stomp"] && r.Damage != nil {
		// Like telefrag: stomp kills also appear as "frag" events, so this
		// dedicated lens is opt-in to avoid doubling the kill feed.
		for _, st := range r.Damage.Stomps {
			ts := msToSec(st.Time)
			if !inWindow(ts, filter.StartTime, end) {
				continue
			}
			if !pf.accepts(st.Attacker) && !pf.accepts(st.Victim) {
				continue
			}
			detail := map[string]any{"victim": st.Victim}
			if st.IsTeam {
				detail["isTeam"] = true
			}
			events = append(events, TaggedEvent{
				T: ts, Type: "stomp", Player: st.Attacker, Detail: detail,
			})
		}
	}
	if want["chat"] && r.Messages != nil {
		for _, msg := range r.Messages.Events {
			ts := msToSec(msg.Time)
			if !inWindow(ts, filter.StartTime, end) {
				continue
			}
			if !pf.accepts(msg.Player) {
				continue
			}
			detail := map[string]any{"text": msg.Message}
			if msg.MessageClean != "" {
				detail["clean"] = msg.MessageClean
			}
			if msg.Team != "" {
				detail["team"] = msg.Team
			}
			events = append(events, TaggedEvent{
				T: ts, Type: "chat", Player: msg.Player, Detail: detail,
			})
		}
	}

	if want["pickup"] {
		// Pickups with identity, joined from the authoritative Result
		// sections rather than the held-interval streams (which only say
		// "gained rl", not which spawner). Two sources, split so no take
		// is double-reported:
		//   - world-spawner takes (any kind, weapons included) come from
		//     the per-spawner item timelines — Name disambiguates twin
		//     spawners (ya_1 vs ya_2), EntNum/Loc pin the map entity;
		//   - backpack / unknown-source weapon grants come from
		//     WeaponPickups (a backpack grab never flips the world
		//     spawner's entity state, so it has no item phase).
		if r.Items != nil {
			for _, it := range r.Items.Items {
				for _, ph := range it.Phases {
					if ph.TakenAt == 0 && ph.TakenBy == "" {
						continue // untaken availability phase
					}
					ts := msToSec(ph.TakenAt)
					if !inWindow(ts, filter.StartTime, end) {
						continue
					}
					if !pf.accepts(ph.TakenBy) {
						continue
					}
					detail := map[string]any{
						"item":   it.Name,
						"kind":   it.Kind,
						"entNum": it.EntNum,
						"source": "world",
					}
					if it.Loc != "" {
						detail["loc"] = it.Loc
					}
					if ph.Team != "" {
						detail["team"] = ph.Team
					}
					events = append(events, TaggedEvent{
						T: ts, Type: "pickup", Player: ph.TakenBy, Detail: detail,
					})
				}
			}
		}
		for _, wp := range r.WeaponPickups {
			if wp.Source == "world" {
				continue // world takes are covered by the item timelines above
			}
			ts := msToSec(wp.Time)
			if !inWindow(ts, filter.StartTime, end) {
				continue
			}
			if !pf.accepts(wp.Player) {
				continue
			}
			detail := map[string]any{
				"item":   wp.Weapon,
				"kind":   wp.Weapon,
				"source": wp.Source,
			}
			if wp.Team != "" {
				detail["team"] = wp.Team
			}
			if wp.BackpackEnt != 0 {
				detail["entNum"] = wp.BackpackEnt
			}
			if wp.Dropper != "" {
				detail["dropper"] = wp.Dropper
			}
			events = append(events, TaggedEvent{
				T: ts, Type: "pickup", Player: wp.Player, Detail: detail,
			})
		}
	}

	if r.Streams != nil {
		for _, p := range r.Streams.Players {
			if !pf.accepts(p.Name) {
				continue
			}
			// Spawns / Deaths are int32 ms (schema v8); the TaggedEvent
			// public schema is float64 seconds. Convert per-entry; the
			// outer filter / window is in seconds.
			if want["spawn"] {
				for _, tMs := range p.Spawns {
					ts := float64(tMs) * 0.001
					if !inWindow(ts, filter.StartTime, end) {
						continue
					}
					var detail map[string]any
					if li, ok := locAtSpawn(p.Loc, tMs); ok {
						if filter.LocIndex {
							detail = map[string]any{"li": int(li)}
						} else if r.TimelineAnalysis != nil {
							if name := locNameAt(r.TimelineAnalysis.LocTable, li); name != "" {
								detail = map[string]any{"loc": name}
							}
						}
					}
					events = append(events, TaggedEvent{T: ts, Type: "spawn", Player: p.Name, Detail: detail})
				}
			}
			if want["death"] {
				for _, tMs := range p.Deaths {
					ts := float64(tMs) * 0.001
					if !inWindow(ts, filter.StartTime, end) {
						continue
					}
					events = append(events, TaggedEvent{T: ts, Type: "death", Player: p.Name})
				}
			}
			if want["weapon"] {
				weaponIntervals := map[string][]result.Interval{
					"rl":  p.RL,
					"lg":  p.LG,
					"gl":  p.GL,
					"ssg": p.SSG,
					"sng": p.SNG,
				}
				events = appendIntervalEvents(events, p.Name, "weapon", weaponIntervals, filter.StartTime, end)
			}
			if want["item"] {
				powerupIntervals := map[string][]result.Interval{
					"q":  p.Quad,
					"pe": p.Pent,
					"r":  p.Ring,
				}
				events = appendIntervalEvents(events, p.Name, "item", powerupIntervals, filter.StartTime, end)
			}
			if want["health"] {
				prev := int16(0)
				for i, c := range p.Health {
					ts := msToSec(c.T)
					if !inWindow(ts, filter.StartTime, end) {
						continue
					}
					detail := map[string]any{"value": c.V}
					if i > 0 {
						detail["delta"] = int(c.V) - int(prev)
					}
					events = append(events, TaggedEvent{T: ts, Type: "health", Player: p.Name, Detail: detail})
					prev = c.V
				}
			}
			if want["armor"] {
				prev := int16(0)
				for i, c := range p.Armor {
					ts := msToSec(c.T)
					if !inWindow(ts, filter.StartTime, end) {
						continue
					}
					detail := map[string]any{"value": c.V}
					if i > 0 {
						detail["delta"] = int(c.V) - int(prev)
					}
					events = append(events, TaggedEvent{T: ts, Type: "armor", Player: p.Name, Detail: detail})
					prev = c.V
				}
			}
			if want["loc"] && r.TimelineAnalysis != nil {
				locTable := r.TimelineAnalysis.LocTable
				for _, c := range p.Loc {
					ts := msToSec(c.T)
					if !inWindow(ts, filter.StartTime, end) {
						continue
					}
					var detail map[string]any
					if filter.LocIndex {
						detail = map[string]any{"li": int(c.V)}
					} else {
						detail = map[string]any{"loc": locNameAt(locTable, c.V)}
					}
					events = append(events, TaggedEvent{
						T: ts, Type: "loc", Player: p.Name, Detail: detail,
					})
				}
			}
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		if events[i].T != events[j].T {
			return events[i].T < events[j].T
		}
		return events[i].Type < events[j].Type
	})
	return &EventsView{Events: events}, nil
}

// locAtSpawnWindowMs bounds how far past a spawn timestamp the loc
// change stream is searched. The spawn teleport lands in the loc
// stream at the first post-spawn position sample (native samples are
// ≤~100ms apart), so a couple of frames is plenty.
const locAtSpawnWindowMs = 500

// locAtSpawn resolves where a player spawned from their loc change
// stream. The first change entry strictly after tMs (within the
// window) is the spawn teleport landing — change streams record the
// post-transition value, and the teleport lands at the next position
// sample. When no entry changed in that window the loc didn't change
// across the spawn, so the value in effect at tMs is correct. The
// strictly-after preference matters for the synthesized t=0 spawn:
// the rebase's carry-forward entry AT t=0 holds the countdown-end
// location, while the match-start respawn teleport lands just after.
func locAtSpawn(stream []result.ChangeI16, tMs int32) (int16, bool) {
	for _, c := range stream {
		if c.T <= tMs {
			continue
		}
		if c.T <= tMs+locAtSpawnWindowMs {
			return c.V, true
		}
		break
	}
	if idx := indexI16AtOrBefore(stream, tMs); idx >= 0 {
		return stream[idx].V, true
	}
	return 0, false
}

func inWindow(t, start, end float64) bool {
	if start != 0 && t < start {
		return false
	}
	if end != 0 && t > end {
		return false
	}
	return true
}

func appendIntervalEvents(
	events []TaggedEvent,
	player, kindLabel string,
	streams map[string][]result.Interval,
	start, end float64,
) []TaggedEvent {
	// Interval.Start/End are int32 ms (schema v8); TaggedEvent.T is
	// float64 seconds — convert each emission. Iterate codes in sorted
	// order (not Go map-range order) so same-(T,Type) events across codes
	// append deterministically; the caller's final (T,Type) sort is stable
	// and leaves these ties in this order, giving byte-stable output.
	codes := make([]string, 0, len(streams))
	for code := range streams {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		ivs := streams[code]
		for _, iv := range ivs {
			startSec := float64(iv.Start) * 0.001
			endSec := float64(iv.End) * 0.001
			if inWindow(startSec, start, end) {
				events = append(events, TaggedEvent{
					T: startSec, Type: kindLabel, Player: player,
					Detail: map[string]any{kindLabel: code, "kind": "gain"},
				})
			}
			if inWindow(endSec, start, end) {
				events = append(events, TaggedEvent{
					T: endSec, Type: kindLabel, Player: player,
					Detail: map[string]any{kindLabel: code, "kind": "lose"},
				})
			}
		}
	}
	return events
}

// inferMatchEnd is a fallback when r.Streams is absent. Reads
// Match.Duration (match-relative coords ⇒ end == duration) and converts
// to float64 seconds (public view API unit; result schema stores ms).
func inferMatchEnd(r *result.Result) float64 {
	if r.Match != nil {
		return float64(r.Match.Duration) * 0.001
	}
	return 0
}
