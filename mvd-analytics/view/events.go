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
}

// Events returns a time-ordered list of events matching the filter.
// Synthesised from result.TimelineAnalysis.{FragEvents, PowerupEvents,
// FragStreaks}, result.Messages, and result.Streams change entries.
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
					events = append(events, TaggedEvent{T: ts, Type: "spawn", Player: p.Name})
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
	// float64 seconds — convert each emission.
	for code, ivs := range streams {
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
// Match.EndTime if present and converts to float64 seconds (public
// view API unit; result schema stores ms).
func inferMatchEnd(r *result.Result) float64 {
	if r.Match != nil {
		return float64(r.Match.EndTime) * 0.001
	}
	return 0
}
