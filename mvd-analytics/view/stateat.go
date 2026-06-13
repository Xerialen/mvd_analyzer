package view

import (
	"fmt"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// StateAtOptions specifies the moment in time to interrogate plus the
// optional player / field filter. Time is required.
type StateAtOptions struct {
	Time    float64
	Players []string
	Fields  []string
	// LocIndex selects the loc representation: false (default) resolves
	// to loc names (PlayerStateAt.Loc); true emits the raw LocTable
	// index (PlayerStateAt.Li) for index-based computation. Decode the
	// index with the demo's loc-table.
	LocIndex bool
}

// StateAtView returns each requested player's state at Time. Empty
// players slice → no players matched the filter.
type StateAtView struct {
	Time    float64                  `json:"t"`
	Players map[string]PlayerStateAt `json:"players"`
}

// PlayerStateAt holds each requested field at Time. Pointers (and
// omitempty) make it possible for JSON to omit fields that weren't
// requested AND fields that have no data yet at Time.
type PlayerStateAt struct {
	Health    *int16      `json:"h,omitempty"`
	Armor     *int16      `json:"a,omitempty"`
	ArmorType *string     `json:"at,omitempty"`
	// Loc / Li carry the player's location, one or the other depending
	// on StateAtOptions.LocIndex. Loc (default) is the resolved name
	// (e.g. "RA"), so consumers don't carry the table; empty string =
	// "no loc". Li is the raw LocTable index (opt-in, for index math;
	// decode via /loc-table). Both nil ⇒ no loc sample at or before Time.
	Loc *string     `json:"loc,omitempty"`
	Li  *int16      `json:"li,omitempty"`
	Pos *Position3D `json:"pos,omitempty"`
	// View / Hgt / Lq are the point-in-time view direction, height above
	// floor, and liquid state — snapped to the nearest position sample,
	// like Pos. Hgt / Lq are present only when the map's BSP supplied
	// those columns.
	View *ViewAngles `json:"view,omitempty"`
	Hgt  *int32      `json:"hgt,omitempty"`
	Lq   *int8       `json:"lq,omitempty"`
	Vel  *Velocity3D `json:"vel,omitempty"`

	RL  *bool `json:"rl,omitempty"`
	LG  *bool `json:"lg,omitempty"`
	GL  *bool `json:"gl,omitempty"`
	SSG *bool `json:"ssg,omitempty"`
	SNG *bool `json:"sng,omitempty"`

	Quad *bool `json:"q,omitempty"`
	Pent *bool `json:"pe,omitempty"`
	Ring *bool `json:"r,omitempty"`

	Shells  *int16 `json:"sh,omitempty"`
	Nails   *int16 `json:"nl,omitempty"`
	Rockets *int16 `json:"rk,omitempty"`
	Cells   *int16 `json:"cl,omitempty"`
}

// Position3D is the JSON-friendly companion to PositionTrack for
// point-in-time results. Snapped to the nearest sample.
type Position3D struct {
	X int32 `json:"x"`
	Y int32 `json:"y"`
	Z int32 `json:"z"`
}

// ViewAngles is the point-in-time view direction: raw angle16 pitch/yaw
// (decode deg = uint16(v)*360/65536; pitch > 180 = up).
type ViewAngles struct {
	VP  int16 `json:"vp"`
	VYa int16 `json:"vya"`
}

// Velocity3D is the point-in-time velocity vector in Quake units/sec,
// snapped to the nearest sample (see PositionTrack.VX for derivation).
type Velocity3D struct {
	VX int32 `json:"vx"`
	VY int32 `json:"vy"`
	VZ int32 `json:"vz"`
}

// StateAt resolves each requested field at Time per player. For
// change streams, returns the latest entry with T <= Time. For
// intervals, returns true iff Time falls inside any interval. For
// position, returns the nearest sample by T (no interpolation).
//
// Spawns/Deaths are explicitly rejected — they're discrete events
// without a "state at time" notion. Use Events() for those.
func StateAt(r *result.Result, opts StateAtOptions) (*StateAtView, error) {
	if r == nil || r.Streams == nil {
		return &StateAtView{Time: opts.Time, Players: map[string]PlayerStateAt{}}, nil
	}
	fields := opts.Fields
	if len(fields) == 0 {
		fields = stateAtDefaultFields()
	}
	if err := validateFields(fields); err != nil {
		return nil, err
	}
	// Spawns/Deaths are not state — reject the request rather than
	// silently dropping them.
	for _, f := range fields {
		if f == FieldSpawns || f == FieldDeaths {
			return nil, fmt.Errorf("field %q has no point-in-time meaning; use view.Events() instead", f)
		}
	}
	requested := make(map[string]bool, len(fields))
	for _, f := range fields {
		requested[f] = true
	}
	pf := newPlayerFilter(opts.Players)
	locTable := locTableOf(r)

	// Public opts.Time is float64 seconds; schema stores int32 ms.
	// Convert once at the entry; every index/contains lookup below
	// takes int32 ms.
	tMs := int32(opts.Time * 1000)
	out := &StateAtView{Time: opts.Time, Players: make(map[string]PlayerStateAt)}
	for _, p := range r.Streams.Players {
		if !pf.accepts(p.Name) {
			continue
		}
		ps := PlayerStateAt{}
		if requested[FieldHealth] {
			if idx := indexI16AtOrBefore(p.Health, tMs); idx >= 0 {
				v := p.Health[idx].V
				ps.Health = &v
			}
		}
		if requested[FieldArmor] {
			if idx := indexI16AtOrBefore(p.Armor, tMs); idx >= 0 {
				v := p.Armor[idx].V
				ps.Armor = &v
			}
		}
		if requested[FieldArmorType] {
			if idx := indexStrAtOrBefore(p.ArmorType, tMs); idx >= 0 {
				v := p.ArmorType[idx].V
				ps.ArmorType = &v
			}
		}
		if requested[FieldLoc] {
			if idx := indexI16AtOrBefore(p.Loc, tMs); idx >= 0 {
				v := p.Loc[idx].V
				if opts.LocIndex {
					ps.Li = &v
				} else {
					name := locNameAt(locTable, v)
					ps.Loc = &name
				}
			}
		}
		if requested[FieldShells] {
			if idx := indexI16AtOrBefore(p.Shells, tMs); idx >= 0 {
				v := p.Shells[idx].V
				ps.Shells = &v
			}
		}
		if requested[FieldNails] {
			if idx := indexI16AtOrBefore(p.Nails, tMs); idx >= 0 {
				v := p.Nails[idx].V
				ps.Nails = &v
			}
		}
		if requested[FieldRockets] {
			if idx := indexI16AtOrBefore(p.Rockets, tMs); idx >= 0 {
				v := p.Rockets[idx].V
				ps.Rockets = &v
			}
		}
		if requested[FieldCells] {
			if idx := indexI16AtOrBefore(p.Cells, tMs); idx >= 0 {
				v := p.Cells[idx].V
				ps.Cells = &v
			}
		}

		if requested[FieldRL] {
			ps.RL = boolPtr(intervalContains(p.RL, tMs))
		}
		if requested[FieldLG] {
			ps.LG = boolPtr(intervalContains(p.LG, tMs))
		}
		if requested[FieldGL] {
			ps.GL = boolPtr(intervalContains(p.GL, tMs))
		}
		if requested[FieldSSG] {
			ps.SSG = boolPtr(intervalContains(p.SSG, tMs))
		}
		if requested[FieldSNG] {
			ps.SNG = boolPtr(intervalContains(p.SNG, tMs))
		}
		if requested[FieldQuad] {
			ps.Quad = boolPtr(intervalContains(p.Quad, tMs))
		}
		if requested[FieldPent] {
			ps.Pent = boolPtr(intervalContains(p.Pent, tMs))
		}
		if requested[FieldRing] {
			ps.Ring = boolPtr(intervalContains(p.Ring, tMs))
		}

		if (requested[FieldPosition] || requested[FieldView] || requested[FieldHeight] ||
			requested[FieldLiquid] || requested[FieldVelocity]) &&
			p.Position != nil && len(p.Position.T) > 0 {
			pt := p.Position
			idx := nearestPositionIndex(pt, opts.Time)
			if idx >= 0 {
				if requested[FieldPosition] {
					ps.Pos = &Position3D{X: pt.X[idx], Y: pt.Y[idx], Z: pt.Z[idx]}
				}
				if requested[FieldView] && len(pt.VP) == len(pt.T) && len(pt.VYa) == len(pt.T) {
					ps.View = &ViewAngles{VP: pt.VP[idx], VYa: pt.VYa[idx]}
				}
				if requested[FieldHeight] && len(pt.H) == len(pt.T) {
					v := pt.H[idx]
					ps.Hgt = &v
				}
				if requested[FieldLiquid] && len(pt.Lq) == len(pt.T) {
					v := pt.Lq[idx]
					ps.Lq = &v
				}
				if requested[FieldVelocity] && len(pt.VX) == len(pt.T) && len(pt.VY) == len(pt.T) && len(pt.VZ) == len(pt.T) {
					ps.Vel = &Velocity3D{VX: pt.VX[idx], VY: pt.VY[idx], VZ: pt.VZ[idx]}
				}
			}
		}

		out.Players[p.Name] = ps
	}
	return out, nil
}

func boolPtr(b bool) *bool { return &b }

// stateAtDefaultFields excludes spawn / death (no state-at meaning).
func stateAtDefaultFields() []string {
	out := make([]string, 0, len(AllStandardFields))
	for _, f := range AllStandardFields {
		if f == FieldSpawns || f == FieldDeaths {
			continue
		}
		out = append(out, f)
	}
	return out
}

// nearestPositionIndex finds the position sample closest to t. If t
// is between two samples, the closer one wins; ties go to the earlier
// sample. -1 if pt is empty. t is float64 seconds (public view API);
// pt.T is int32 ms (schema v8) — convert the query once and stay in
// int32 for the loop.
func nearestPositionIndex(pt *result.PositionTrack, t float64) int {
	if len(pt.T) == 0 {
		return -1
	}
	tMs := int32(t * 1000)
	best := -1
	bestDiff := int32(0)
	for i := range pt.T {
		diff := pt.T[i] - tMs
		if diff < 0 {
			diff = -diff
		}
		if best == -1 || diff < bestDiff {
			best = i
			bestDiff = diff
		}
	}
	return best
}
