package result

import (
	"encoding/json"
	"math"
	"strconv"
)

// coordJSONScale rounds coordinates to 3 decimal places (10^3) when
// SERIALIZING positions, velocities, and floor heights to JSON. The
// values are kept at native float32 resolution in memory — only the text
// artifact is rounded. Three decimals is lossless for the wire's
// eighth-unit coordinates (.125 / .25 / .375 …) yet sheds the
// false-precision tail a float32 division leaves on derived velocity
// (e.g. -58.333332 → -58.333) and on the floor-height trace.
const coordJSONScale = 1000

// appendCoordJSON appends v as a JSON number rounded to 3 decimal places,
// dropping trailing zeros (192 → "192", -58.333332 → "-58.333"). It
// rounds in float64 then formats the shortest round-tripping decimal of
// the rounded value.
func appendCoordJSON(b []byte, v float32) []byte {
	r := math.Round(float64(v)*coordJSONScale) / coordJSONScale
	return strconv.AppendFloat(b, r, 'f', -1, 64)
}

// Coord is a float32 serialized to JSON at 3-decimal precision (see
// appendCoordJSON). It is used for the view-layer bucket outputs, whose
// values are boxed in map[string]any and so cannot be reached by a
// container MarshalJSON. The native-rate PositionTrack does not store
// this type — it keeps plain float32 fields and rounds via its own
// MarshalJSON below.
type Coord float32

// MarshalJSON renders the rounded text form.
func (c Coord) MarshalJSON() ([]byte, error) {
	return appendCoordJSON(nil, float32(c)), nil
}

// Coords is a []float32 serialized to JSON at 3-decimal precision in a
// single pass (cheaper than a per-element Coord for the dense columns).
// Conversion to/from []float32 is free (identical underlying type). Used
// by the columnar bucket columns and, internally, by
// PositionTrack.MarshalJSON.
type Coords []float32

// MarshalJSON renders the array with each element rounded.
func (cs Coords) MarshalJSON() ([]byte, error) {
	if cs == nil {
		return []byte("null"), nil
	}
	b := make([]byte, 0, len(cs)*8+2)
	b = append(b, '[')
	for i, v := range cs {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendCoordJSON(b, v)
	}
	return append(b, ']'), nil
}

// MarshalJSON keeps PositionTrack's coordinate columns at native float32
// in memory but rounds them to 3 decimals in the JSON text. It marshals
// through a shadow struct whose float columns are the Coords type, so the
// standard encoder still handles the field layout, omitempty, and the int
// columns (t/li/lq/vp/vya) unchanged — only x/y/z/h/vx/vy/vz get the
// rounded encoding. The Coords conversions are free (same underlying
// type). Mirrors the field set of PositionTrack; keep in sync when adding
// a column.
func (p PositionTrack) MarshalJSON() ([]byte, error) {
	type shadow struct {
		T   []int32 `json:"t"`
		X   Coords  `json:"x"`
		Y   Coords  `json:"y"`
		Z   Coords  `json:"z"`
		Li  []int16 `json:"li,omitempty"`
		H   Coords  `json:"h,omitempty"`
		Lq  []int8  `json:"lq,omitempty"`
		VP  []int16 `json:"vp,omitempty"`
		VYa []int16 `json:"vya,omitempty"`
		VX  Coords  `json:"vx,omitempty"`
		VY  Coords  `json:"vy,omitempty"`
		VZ  Coords  `json:"vz,omitempty"`
	}
	return json.Marshal(shadow{
		T: p.T, X: Coords(p.X), Y: Coords(p.Y), Z: Coords(p.Z),
		Li: p.Li, H: Coords(p.H), Lq: p.Lq, VP: p.VP, VYa: p.VYa,
		VX: Coords(p.VX), VY: Coords(p.VY), VZ: Coords(p.VZ),
	})
}
