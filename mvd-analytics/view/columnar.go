package view

import (
	"encoding/json"
	"math"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// ColumnarBuckets is the column-major counterpart to BucketsView. Where
// BucketsView is bucket-major (one map per bucket, repeating every
// player name and field key per bucket), this layout stores, for each
// (player, field), a single dense typed array. On a 50 ms full-field
// 4on4 that collapses ~1.5M allocations / ~36 MB JSON to a few hundred
// slices and an order-of-magnitude smaller payload.
//
// The per-bucket time axis is implicit: time(i) = StartMs + i*WindowMs.
// PartialLastMs, when non-zero, is the (shorter) end of the final
// bucket; its start is still regular.
type ColumnarBuckets struct {
	WindowMs      int                        `json:"windowMs"`
	StartMs       int32                      `json:"startMs"`
	Count         int                        `json:"count"`
	PartialLastMs int32                      `json:"partialLastMs,omitempty"`
	Players       map[string]*ColumnarPlayer `json:"players,omitempty"`
	Teams         map[string]*ColumnarTeam   `json:"teams,omitempty"`
}

// ColumnarPlayer holds one player's dense per-field columns over their
// active span [First, First+N). Each field array has length N and is
// indexed by (absoluteBucket - First). Values carry forward through
// dead buckets so the arrays stay typed and dense; Alive marks which
// buckets in the span the player is actually alive (row-major omits a
// player from dead buckets, so consumers and the parity transform use
// Alive to reproduce that).
//
// There is deliberately no per-life table here: it would be a
// bucket-resolution approximation that undercounts a death+respawn
// landing in one window. The authoritative life enumeration is the
// per-player Spawns/Deaths event streams (getEvents / raw streams); a
// same-bucket death+respawn surfaces here as that bucket carrying both
// the "d" and "sp" markers while Alive stays set.
//
// A field whose first non-nil value lands after First (e.g. armor
// before the first pickup) records that absolute index in ValidFrom;
// the leading slots hold a typed zero and must be treated as absent.
// Cols values are typed slices: []int16 (h,a,li,sh,nl,rk,cl under
// first/last), []int32 (x,y,z), []float64 (mean/min/max overrides),
// []string (at), or []bool (weapons/powerups/spawn/death).
type ColumnarPlayer struct {
	First     int
	N         int
	Alive     []bool
	ValidFrom map[string]int
	Cols      map[string]any
}

// MarshalJSON inlines First/N/Alive/ValidFrom and every Cols key at the
// top level of the player object (field codes never collide with the
// reserved keys). Boolean columns (and the alive mask) marshal as 0/1
// int8 rather than true/false: on a 50 ms full-field 4on4 the boolean
// fields are ~half the payload, and "false"/"true" cost 5–6 bytes per
// element versus 1 for a digit (~6.5 MB saved). The arrays stay []bool
// in Go so the row/columnar parity comparison is exact; only the wire
// bytes change. Consumers read 0/1 as a truthy series.
func (cp *ColumnarPlayer) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(cp.Cols)+4)
	m["first"] = cp.First
	m["n"] = cp.N
	m["alive"] = bools01(cp.Alive)
	if len(cp.ValidFrom) > 0 {
		m["validFrom"] = cp.ValidFrom
	}
	for k, v := range cp.Cols {
		if bs, ok := v.([]bool); ok {
			m[k] = bools01(bs)
			continue
		}
		m[k] = v
	}
	return json.Marshal(m)
}

// bools01 converts a boolean slice to 0/1 int8 for compact, still-
// readable JSON.
func bools01(bs []bool) []int8 {
	out := make([]int8, len(bs))
	for i, b := range bs {
		if b {
			out[i] = 1
		}
	}
	return out
}

// ColumnarTeam holds one team's per-bucket aggregate counters as dense
// []int columns of length Count (the full match grid). A counter array
// is present only when the team had a non-zero value for it at some
// bucket. ABT carries the armor-by-type sub-columns ("ra"/"ya"/"ga").
type ColumnarTeam struct {
	Cols map[string][]int
	ABT  map[string][]int
}

// MarshalJSON inlines the counter columns and nests ABT under "abt".
func (ct *ColumnarTeam) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(ct.Cols)+1)
	for k, v := range ct.Cols {
		m[k] = v
	}
	if len(ct.ABT) > 0 {
		m["abt"] = ct.ABT
	}
	return json.Marshal(m)
}

// bucketGrid is the shared time→bucket mapping. start/end are seconds
// (public view units); the columnar wire form converts to int32 ms.
type bucketGrid struct {
	windowMs   int
	windowSec  float64
	start, end float64
	count      int
	hasPartial bool
}

// bounds returns [bStart, bEnd) for bucket i, matching the loop in
// Buckets (the final bucket is clamped to end when partial).
func (g bucketGrid) bounds(i int) (float64, float64) {
	bStart := g.start + float64(i)*g.windowSec
	bEnd := bStart + g.windowSec
	if g.hasPartial && i == g.count-1 {
		bEnd = g.end
	}
	return bStart, bEnd
}

// resolveBucketGrid computes the same window/start/end/count that
// Buckets derives inline (buckets.go). Mirrored here so the columnar
// grid is byte-identical to the row grid; the parity test deep-equals
// the two builders, so any drift is caught immediately.
func resolveBucketGrid(r *result.Result, opts BucketsOptions) (bucketGrid, error) {
	windowMs, windowSec, err := resolveWindow(opts.WindowMs)
	if err != nil {
		return bucketGrid{}, err
	}
	g := bucketGrid{windowMs: windowMs, windowSec: windowSec}
	g.end = opts.EndTime
	g.start = opts.StartTime
	if g.end == 0 {
		g.end = float64(r.Streams.Global.MatchEnd) * 0.001
	}
	if g.start == 0 {
		g.start = float64(r.Streams.Global.MatchStart) * 0.001
	}
	if g.end <= g.start {
		return g, nil // count stays 0
	}
	totalSpan := g.end - g.start
	full := int(math.Floor((totalSpan + 1e-12) / windowSec))
	remainder := totalSpan - float64(full)*windowSec
	g.count = full
	g.hasPartial = remainder > 1e-9
	if g.hasPartial {
		g.count++
	}
	return g, nil
}

// BucketsColumnar builds the column-major ColumnarBuckets. It reuses
// every reducer/stream helper from buckets.go (fastReduce /
// collectSamples / playerActiveInWindow / aggregateTeams) so per-bucket
// values are byte-identical to Buckets; only the storage layout
// differs. Loc is always emitted as the raw int16 index ("li").
func BucketsColumnar(r *result.Result, opts BucketsOptions) (*ColumnarBuckets, error) {
	if r == nil || r.Streams == nil {
		return &ColumnarBuckets{WindowMs: bucketWindowMsOrDefault(opts.WindowMs)}, nil
	}
	g, err := resolveBucketGrid(r, opts)
	if err != nil {
		return nil, err
	}

	fields := opts.Fields
	if len(fields) == 0 {
		fields = AllStandardFields
	}
	if err := validateFields(fields); err != nil {
		return nil, err
	}
	reds := make([]Reducer, len(fields))
	redNames := make([]string, len(fields))
	for i, f := range fields {
		red, err := resolveReducerName(f, opts.Reducers)
		if err != nil {
			return nil, err
		}
		reds[i] = red
		redNames[i] = red.Name()
	}

	cb := &ColumnarBuckets{WindowMs: g.windowMs, StartMs: int32(g.start * 1000), Count: g.count}
	if g.count == 0 {
		return cb, nil
	}
	if g.hasPartial {
		cb.PartialLastMs = int32(g.end * 1000)
	}

	playerFilter := newPlayerFilter(opts.Players)
	playerStreams := selectPlayers(r.Streams.Players, playerFilter)

	cps := make(map[string]*ColumnarPlayer)
	for pi := range playerStreams {
		p := &playerStreams[pi]
		cp := buildColumnarPlayer(p, fields, reds, redNames, g)
		if cp != nil {
			cps[p.Name] = cp
		}
	}
	if len(cps) > 0 {
		cb.Players = cps
	}

	if opts.IncludeTeam {
		if teams := aggregateTeamsColumnar(cps, playerStreams, g.count); teams != nil {
			cb.Teams = teams
		}
	}
	return cb, nil
}

// buildColumnarPlayer fills one player's columns. Returns nil if the
// player is never active over the grid, or if no requested field
// produced any value (a stats-less observer) — the latter matches the
// row builder, which omits a player whose reducePlayer yields nothing,
// so column and row carry the same player set for any field selection.
func buildColumnarPlayer(p *result.PlayerStream, fields []string, reds []Reducer, redNames []string, g bucketGrid) *ColumnarPlayer {
	first, last := -1, -1
	for i := 0; i < g.count; i++ {
		bStart, bEnd := g.bounds(i)
		if playerActiveInWindow(p, bStart, bEnd) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return nil
	}
	n := last - first + 1
	cp := &ColumnarPlayer{First: first, N: n, Cols: make(map[string]any)}
	cp.Alive = make([]bool, n)
	for j := 0; j < n; j++ {
		bStart, bEnd := g.bounds(first + j)
		cp.Alive[j] = playerActiveInWindow(p, bStart, bEnd)
	}

	for fi, f := range fields {
		if f == FieldPosition {
			buildPositionCols(p, redNames[fi], reds[fi], g, cp)
			continue
		}
		if f == FieldView {
			buildViewCols(p, redNames[fi], reds[fi], g, cp)
			continue
		}
		if f == FieldVelocity {
			buildVelocityCols(p, redNames[fi], reds[fi], g, cp)
			continue
		}
		col, validFrom, present := buildColumn(p, f, redNames[fi], reds[fi], g, cp)
		if !present {
			continue
		}
		cp.Cols[f] = col
		if validFrom > first {
			if cp.ValidFrom == nil {
				cp.ValidFrom = make(map[string]int)
			}
			cp.ValidFrom[f] = validFrom
		}
	}
	if len(cp.Cols) == 0 {
		return nil // no field data — row omits these too
	}
	return cp
}

// buildColumn materialises one non-position field's typed column over
// [First, First+N). validFrom is the absolute index of the first
// non-nil value; present is false when the field is nil for the whole
// span (then the column is omitted, matching row-major's omitempty).
func buildColumn(p *result.PlayerStream, field, redName string, red Reducer, g bucketGrid, cp *ColumnarPlayer) (col any, validFrom int, present bool) {
	elem := columnElemType(field, redName)
	if elem == "none" {
		return nil, 0, false
	}
	n := cp.N
	firstNonNil := -1
	mark := func(i int) {
		if firstNonNil < 0 {
			firstNonNil = i
		}
	}
	switch elem {
	case "i16":
		s := make([]int16, n)
		for j := 0; j < n; j++ {
			v := reduceFieldValue(p, field, redName, red, g, cp.First+j)
			if v == nil {
				continue
			}
			mark(cp.First + j)
			if f, ok := numericToFloat(v); ok {
				s[j] = int16(f)
			}
		}
		col = s
	case "i32":
		s := make([]int32, n)
		for j := 0; j < n; j++ {
			v := reduceFieldValue(p, field, redName, red, g, cp.First+j)
			if v == nil {
				continue
			}
			mark(cp.First + j)
			if f, ok := numericToFloat(v); ok {
				s[j] = int32(f)
			}
		}
		col = s
	case "f64":
		s := make([]float64, n)
		for j := 0; j < n; j++ {
			v := reduceFieldValue(p, field, redName, red, g, cp.First+j)
			if v == nil {
				continue
			}
			mark(cp.First + j)
			if f, ok := numericToFloat(v); ok {
				s[j] = f
			}
		}
		col = s
	case "str":
		s := make([]string, n)
		for j := 0; j < n; j++ {
			v := reduceFieldValue(p, field, redName, red, g, cp.First+j)
			if v == nil {
				continue
			}
			mark(cp.First + j)
			if str, ok := v.(string); ok {
				s[j] = str
			}
		}
		col = s
	case "bool":
		s := make([]bool, n)
		for j := 0; j < n; j++ {
			v := reduceFieldValue(p, field, redName, red, g, cp.First+j)
			if v == nil {
				continue
			}
			mark(cp.First + j)
			if b, ok := v.(bool); ok {
				s[j] = b
			}
		}
		col = s
	default:
		return nil, 0, false
	}
	if firstNonNil < 0 {
		return nil, 0, false
	}
	return col, firstNonNil, true
}

// buildPositionCols splits the [3]int32 position reduction into the
// dense x/y/z int32 columns. All three share one validFrom.
func buildPositionCols(p *result.PlayerStream, redName string, red Reducer, g bucketGrid, cp *ColumnarPlayer) {
	n := cp.N
	xs := make([]int32, n)
	ys := make([]int32, n)
	zs := make([]int32, n)
	firstNonNil := -1
	for j := 0; j < n; j++ {
		v := reduceFieldValue(p, FieldPosition, redName, red, g, cp.First+j)
		if v == nil {
			continue
		}
		if firstNonNil < 0 {
			firstNonNil = cp.First + j
		}
		if pos, ok := v.([3]int32); ok {
			xs[j], ys[j], zs[j] = pos[0], pos[1], pos[2]
		}
	}
	if firstNonNil < 0 {
		return
	}
	cp.Cols["x"] = xs
	cp.Cols["y"] = ys
	cp.Cols["z"] = zs
	if firstNonNil > cp.First {
		if cp.ValidFrom == nil {
			cp.ValidFrom = make(map[string]int)
		}
		cp.ValidFrom["x"] = firstNonNil
		cp.ValidFrom["y"] = firstNonNil
		cp.ValidFrom["z"] = firstNonNil
	}
}

// buildViewCols splits the [2]int16 view reduction into the dense
// vp/vya int16 columns, mirroring buildPositionCols. Both share one
// validFrom. Under a numeric reducer (mean/min/max) the pair doesn't
// coerce, so no value is produced and the columns are omitted — exactly
// as the row builder omits a nil field.
func buildViewCols(p *result.PlayerStream, redName string, red Reducer, g bucketGrid, cp *ColumnarPlayer) {
	n := cp.N
	vps := make([]int16, n)
	vyas := make([]int16, n)
	firstNonNil := -1
	for j := 0; j < n; j++ {
		v := reduceFieldValue(p, FieldView, redName, red, g, cp.First+j)
		if v == nil {
			continue
		}
		if firstNonNil < 0 {
			firstNonNil = cp.First + j
		}
		if pair, ok := v.([2]int16); ok {
			vps[j], vyas[j] = pair[0], pair[1]
		}
	}
	if firstNonNil < 0 {
		return
	}
	cp.Cols["vp"] = vps
	cp.Cols["vya"] = vyas
	if firstNonNil > cp.First {
		if cp.ValidFrom == nil {
			cp.ValidFrom = make(map[string]int)
		}
		cp.ValidFrom["vp"] = firstNonNil
		cp.ValidFrom["vya"] = firstNonNil
	}
}

// buildVelocityCols splits the [3]int32 velocity reduction into the
// dense vx/vy/vz int32 columns, mirroring buildPositionCols. All three
// share one validFrom.
func buildVelocityCols(p *result.PlayerStream, redName string, red Reducer, g bucketGrid, cp *ColumnarPlayer) {
	n := cp.N
	vxs := make([]int32, n)
	vys := make([]int32, n)
	vzs := make([]int32, n)
	firstNonNil := -1
	for j := 0; j < n; j++ {
		v := reduceFieldValue(p, FieldVelocity, redName, red, g, cp.First+j)
		if v == nil {
			continue
		}
		if firstNonNil < 0 {
			firstNonNil = cp.First + j
		}
		if vel, ok := v.([3]int32); ok {
			vxs[j], vys[j], vzs[j] = vel[0], vel[1], vel[2]
		}
	}
	if firstNonNil < 0 {
		return
	}
	cp.Cols["vx"] = vxs
	cp.Cols["vy"] = vys
	cp.Cols["vz"] = vzs
	if firstNonNil > cp.First {
		if cp.ValidFrom == nil {
			cp.ValidFrom = make(map[string]int)
		}
		cp.ValidFrom["vx"] = firstNonNil
		cp.ValidFrom["vy"] = firstNonNil
		cp.ValidFrom["vz"] = firstNonNil
	}
}

// reduceFieldValue computes the reduced value for one (field, bucket),
// mirroring reducePlayer's per-field branch exactly (fast path with
// fallback to collectSamples + Reducer.Apply).
func reduceFieldValue(p *result.PlayerStream, field, redName string, red Reducer, g bucketGrid, i int) any {
	bStart, bEnd := g.bounds(i)
	if v, ok := fastReduce(p, field, redName, bStart, bEnd); ok {
		return v
	}
	return red.Apply(collectSamples(p, field, bStart, bEnd))
}

// columnElemType resolves the storage type of a (field, reducer) pair.
// Numeric reducers force float64; boolean reducers force bool;
// first/last/dominant preserve the underlying stream type.
func columnElemType(field, redName string) string {
	switch redName {
	case "any", "held-any", "majority":
		return "bool"
	case "mean", "min", "max":
		return "f64"
	}
	kind, ok := FieldKindFor(field)
	if !ok {
		return "none"
	}
	switch kind {
	case KindChangeI16:
		return "i16"
	case KindChangeStr:
		return "str"
	case KindInterval:
		return "bool"
	case KindPosition:
		return "pos" // handled by buildPositionCols
	case KindView:
		return "view" // handled by buildViewCols
	case KindHeight:
		return "i32" // single floor-height column (first/last)
	case KindLiquid:
		return "i16" // single liquid-state column (first/last); lq fits int8
	case KindVelocity:
		return "vel" // handled by buildVelocityCols
	case KindEventList:
		return "f64" // first/last on an event list yields a timestamp
	}
	return "none"
}

// valAt returns the boxed value of a column at absolute bucket i, or
// nil when the field is absent (no column, or before its validFrom).
func (cp *ColumnarPlayer) valAt(field string, i int) any {
	col, ok := cp.Cols[field]
	if !ok {
		return nil
	}
	if vf, ok := cp.ValidFrom[field]; ok && i < vf {
		return nil
	}
	j := i - cp.First
	switch s := col.(type) {
	case []int16:
		return s[j]
	case []int32:
		return s[j]
	case []float64:
		return s[j]
	case []string:
		return s[j]
	case []bool:
		return s[j]
	}
	return nil
}

// aggregateTeamsColumnar re-derives the per-team counters that
// aggregateTeams produces, but writes them into dense []int columns of
// length count directly from the columnar player data — avoiding the
// per-bucket map churn a reconstruct-then-aggregate path would cost.
// The bump logic mirrors aggregateTeams (buckets.go) exactly.
func aggregateTeamsColumnar(cps map[string]*ColumnarPlayer, streams []result.PlayerStream, count int) map[string]*ColumnarTeam {
	teams := make(map[string]*ColumnarTeam)
	teamOf := func(name string) *ColumnarTeam {
		ct := teams[name]
		if ct == nil {
			ct = &ColumnarTeam{Cols: make(map[string][]int)}
			teams[name] = ct
		}
		return ct
	}
	bump := func(ct *ColumnarTeam, key string, i int) {
		arr := ct.Cols[key]
		if arr == nil {
			arr = make([]int, count)
			ct.Cols[key] = arr
		}
		arr[i]++
	}
	add := func(ct *ColumnarTeam, key string, i, n int) {
		arr := ct.Cols[key]
		if arr == nil {
			arr = make([]int, count)
			ct.Cols[key] = arr
		}
		arr[i] += n
	}
	asBool := func(v any) bool { b, ok := v.(bool); return ok && b }

	for pi := range streams {
		p := &streams[pi]
		if p.Team == "" {
			continue
		}
		cp := cps[p.Name]
		if cp == nil {
			continue
		}
		ct := teamOf(p.Team)
		for j := 0; j < cp.N; j++ {
			if !cp.Alive[j] {
				continue
			}
			i := cp.First + j
			hasRL := asBool(cp.valAt(FieldRL, i))
			hasLG := asBool(cp.valAt(FieldLG, i))
			hasGL := asBool(cp.valAt(FieldGL, i))
			switch {
			case hasRL && hasLG:
				bump(ct, "rllg", i)
				bump(ct, "w", i)
			case hasRL:
				bump(ct, "rl", i)
				bump(ct, "w", i)
			case hasLG:
				bump(ct, "lg", i)
				bump(ct, "w", i)
			}
			if hasGL {
				bump(ct, "gl", i)
			}
			if asBool(cp.valAt(FieldQuad, i)) {
				bump(ct, "q", i)
				bump(ct, "pw", i)
			}
			if asBool(cp.valAt(FieldPent, i)) {
				bump(ct, "pe", i)
				bump(ct, "pw", i)
			}
			if asBool(cp.valAt(FieldRing, i)) {
				bump(ct, "r", i)
				bump(ct, "pw", i)
			}
			if h, ok := numericFromAny(cp.valAt(FieldHealth, i)); ok {
				add(ct, "th", i, int(h))
			}
			if a, ok := numericFromAny(cp.valAt(FieldArmor, i)); ok {
				add(ct, "ta", i, int(a))
			}
			if at, ok := cp.valAt(FieldArmorType, i).(string); ok && at != "" {
				if ct.ABT == nil {
					ct.ABT = make(map[string][]int)
				}
				arr := ct.ABT[at]
				if arr == nil {
					arr = make([]int, count)
					ct.ABT[at] = arr
				}
				arr[i]++
			}
		}
	}
	if len(teams) == 0 {
		return nil
	}
	return teams
}
