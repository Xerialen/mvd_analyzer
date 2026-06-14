package view

import (
	"fmt"
	"math"
	"sort"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// BucketsOptions controls the windowing, field selection, and reducer
// override for a single Buckets call. Every field is optional; the
// zero value asks for "every standard field, default reducers, 50 ms
// windows over the whole match."
type BucketsOptions struct {
	WindowMs    int               // 50 if zero
	StartTime   float64           // match start if zero
	EndTime     float64           // match end if zero
	Players     []string          // all if empty
	Fields      []string          // AllStandardFields if empty
	Reducers    map[string]string // per-field overrides; merged with DefaultReducers
	IncludeTeam bool              // emit per-team aggregates on each bucket
	// LocIndex selects the loc representation in each bucket's player
	// map: false (default) resolves the reduced loc value to a name
	// under key "loc"; true leaves the raw LocTable index under "li"
	// (decode via /loc-table). The legacy WASM bridge needs the index,
	// so getDefaultBuckets sets this true.
	LocIndex bool
	// Layout selects the output shape. "" / "row" build the bucket-major
	// BucketsView via Buckets; "column" builds the column-major
	// ColumnarBuckets via BucketsColumnar. The field documents caller
	// intent — dispatch happens in the transport layer (handlers / WASM
	// bridge), not inside Buckets itself. Columnar always emits the raw
	// loc index ("li"); LocIndex does not apply to it.
	Layout string
}

// BucketsView is the result of a Buckets call. WindowMs echoes back
// the resolved window size (so callers reading 0 above can still see
// what they got); Buckets is sorted by T ascending.
//
// Loc rendering follows BucketsOptions.LocIndex: by default each
// bucket's player map carries a "loc" name; in index mode it carries
// the raw "li" index, which a consumer decodes against /loc-table.
type BucketsView struct {
	WindowMs int          `json:"windowMs"`
	Buckets  []ViewBucket `json:"buckets"`
}

// ViewBucket is the row-major (bucket-major) per-player map shape. The
// column-major counterpart is ColumnarBuckets (columnar.go), selected
// via BucketsOptions.Layout / the API+MCP layout param.
//
// Players maps player name → field code → reduced value. Field codes
// match the canonical vocabulary in fields.go. Values' Go types
// depend on the reducer used: numeric reducers (mean/min/max) emit
// float64; "last"/"first" preserve the underlying stream type;
// boolean reducers (held-any/majority/any) emit bool.
//
// Team, when populated, is keyed by team name and carries the
// IncludeTeam aggregate counters.
type ViewBucket struct {
	T       float64                       `json:"t"`
	Players map[string]map[string]any     `json:"p"`
	// Team is keyed by team name → field → value. Most fields are
	// int counters (rl, lg, w, th, ta, …); the special key "abt"
	// holds an armor-by-type map (string → int, "ra"/"ya"/"ga"
	// to count). The mixed-type value is why this is map[string]any.
	Team    map[string]map[string]any     `json:"team,omitempty"`
	Partial bool                          `json:"partial,omitempty"`
}

// Buckets walks the streams in r and emits one ViewBucket per window
// across [StartTime, EndTime). The final bucket is marked Partial when
// the window doesn't divide evenly.
func Buckets(r *result.Result, opts BucketsOptions) (*BucketsView, error) {
	if r == nil || r.Streams == nil {
		return &BucketsView{WindowMs: bucketWindowMsOrDefault(opts.WindowMs)}, nil
	}
	windowMs, windowSec, err := resolveWindow(opts.WindowMs)
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
	reducers := make(map[string]Reducer, len(fields))
	for _, f := range fields {
		red, err := resolveReducerName(f, opts.Reducers)
		if err != nil {
			return nil, err
		}
		reducers[f] = red
	}
	// Hoist the per-field reducer and its name out of the bucket loop:
	// indexing these parallel slices by field position on the hot path
	// avoids one map lookup per field × player × bucket (millions on a
	// 50ms full-field run).
	fieldReds := make([]Reducer, len(fields))
	fieldRedNames := make([]string, len(fields))
	for i, f := range fields {
		fieldReds[i] = reducers[f]
		fieldRedNames[i] = fieldReds[i].Name()
	}

	// Public API uses float64 seconds; the schema stores Global.Match*
	// as int32 ms (schema v8) — convert once at the entry.
	start := opts.StartTime
	end := opts.EndTime
	if end == 0 {
		end = float64(r.Streams.Global.MatchEnd) * 0.001
	}
	if start == 0 {
		start = float64(r.Streams.Global.MatchStart) * 0.001
	}
	if end <= start {
		return &BucketsView{WindowMs: windowMs, Buckets: nil}, nil
	}

	playerFilter := newPlayerFilter(opts.Players)
	playerStreams := selectPlayers(r.Streams.Players, playerFilter)

	totalSpan := end - start
	full := int(math.Floor((totalSpan + 1e-12) / windowSec))
	remainder := totalSpan - float64(full)*windowSec
	bucketCount := full
	hasPartial := remainder > 1e-9
	if hasPartial {
		bucketCount++
	}
	if bucketCount == 0 {
		return &BucketsView{WindowMs: windowMs}, nil
	}

	buckets := make([]ViewBucket, bucketCount)
	for i := 0; i < bucketCount; i++ {
		bStart := start + float64(i)*windowSec
		bEnd := bStart + windowSec
		if hasPartial && i == bucketCount-1 {
			bEnd = end
		}
		bucket := ViewBucket{
			T:       bStart,
			Players: make(map[string]map[string]any),
		}
		if hasPartial && i == bucketCount-1 {
			bucket.Partial = true
		}

		for pi := range playerStreams {
			p := &playerStreams[pi]
			if !playerActiveInWindow(p, bStart, bEnd) {
				continue
			}
			pdata := reducePlayer(p, fields, fieldReds, fieldRedNames, bStart, bEnd)
			if pdata != nil {
				bucket.Players[p.Name] = pdata
			}
		}

		if opts.IncludeTeam {
			bucket.Team = aggregateTeams(playerStreams, bucket.Players)
		}
		buckets[i] = bucket
	}

	if !opts.LocIndex && reducers[FieldLoc] != nil {
		resolveBucketLocNames(buckets, locTableOf(r))
	}
	return &BucketsView{WindowMs: windowMs, Buckets: buckets}, nil
}

// resolveBucketLocNames rewrites each bucket's reduced loc value from
// the raw LocTable index (key "li") to the resolved name (key "loc").
// A no-loc index (name "") drops the key entirely so clean buckets stay
// quiet. Index mode skips this and leaves "li" in place.
func resolveBucketLocNames(buckets []ViewBucket, locTable []string) {
	for i := range buckets {
		for _, pdata := range buckets[i].Players {
			v, ok := pdata[FieldLoc]
			if !ok {
				continue
			}
			delete(pdata, FieldLoc)
			idx, ok := numericFromAny(v)
			if !ok {
				continue
			}
			if name := locNameAt(locTable, int16(idx)); name != "" {
				pdata["loc"] = name
			}
		}
	}
}

// playerActiveInWindow returns true if the player should appear in a
// bucket spanning [bStart, bEnd). Mirrors v6's position-driven
// sampler: a player gets a bucket entry only while they are alive.
//
// "Alive" is determined from the spawn / death streams:
//
//   - If both streams are empty and the player has no position track,
//     the player has no concrete activity signal — treat as active to
//     keep synthetic test fixtures working without spawn/death markers.
//   - If the spawn / death streams or position track are populated,
//     consult them: a player is alive iff their latest spawn before
//     bEnd is later than their latest death before bEnd, OR a spawn
//     happens in [bStart, bEnd) (mid-window respawn).
//   - If the player has positions but no spawn/death streams (e.g.,
//     already alive at match start in a demo where parser never
//     emitted a synthetic SpawnEvent), fall back to position presence.
func playerActiveInWindow(p *result.PlayerStream, bStart, bEnd float64) bool {
	hasSpawnDeath := len(p.Spawns) > 0 || len(p.Deaths) > 0
	hasPositions := p.Position != nil && len(p.Position.T) > 0

	if !hasSpawnDeath && !hasPositions {
		// No concrete liveness signal — assume active (test fixtures).
		return true
	}

	// Spawns / Deaths are int32 ms (schema v8); convert the window
	// bounds once and stay in int32 for the comparison loop.
	bStartMs := int32(bStart * 1000)
	bEndMs := int32(bEnd * 1000)

	if hasSpawnDeath {
		latestKind := ""
		latestT := int32(-1)
		seen := false
		for _, t := range p.Spawns {
			if t >= bStartMs && t < bEndMs {
				return true // spawned inside the window
			}
			if t < bStartMs && (!seen || t > latestT) {
				latestT = t
				latestKind = "spawn"
				seen = true
			}
			if t >= bEndMs {
				break
			}
		}
		for _, t := range p.Deaths {
			if t < bStartMs && (!seen || t > latestT) {
				latestT = t
				latestKind = "death"
				seen = true
			}
			if t >= bEndMs {
				break
			}
		}
		if latestKind == "spawn" {
			return true
		}
		if latestKind == "death" {
			return false
		}
		// No spawn/death before bStart — fall through to position check.
	}

	return positionTouchesWindow(p.Position, bStart, bEnd)
}

func positionTouchesWindow(pt *result.PositionTrack, bStart, bEnd float64) bool {
	if pt == nil || len(pt.T) == 0 {
		return false
	}
	const fudge = 0.1 // slightly more than one 50 ms bucket
	// pt.T is int32 ms (schema v8); convert window bounds once.
	loMs := int32((bStart - fudge) * 1000)
	bEndMs := int32(bEnd * 1000)
	// Binary-search for the first sample >= loMs; if it lands before
	// bEndMs we have a touch.
	i := sort.Search(len(pt.T), func(i int) bool { return pt.T[i] >= loMs })
	return i < len(pt.T) && pt.T[i] < bEndMs
}

func bucketWindowMsOrDefault(ms int) int {
	if ms <= 0 {
		return 50
	}
	return ms
}

func resolveWindow(ms int) (int, float64, error) {
	if ms < 0 {
		return 0, 0, fmt.Errorf("WindowMs must be > 0, got %d", ms)
	}
	if ms == 0 {
		ms = 50
	}
	return ms, float64(ms) / 1000.0, nil
}

// playerFilter enforces an opt-in name filter. nil pointer → accept
// everyone; otherwise the set is checked.
type playerFilter struct{ allow map[string]bool }

func newPlayerFilter(names []string) *playerFilter {
	if len(names) == 0 {
		return nil
	}
	pf := &playerFilter{allow: make(map[string]bool, len(names))}
	for _, n := range names {
		pf.allow[n] = true
	}
	return pf
}

func (pf *playerFilter) accepts(name string) bool {
	if pf == nil {
		return true
	}
	return pf.allow[name]
}

func selectPlayers(all []result.PlayerStream, pf *playerFilter) []result.PlayerStream {
	if pf == nil {
		return all
	}
	out := make([]result.PlayerStream, 0, len(all))
	for _, p := range all {
		if pf.accepts(p.Name) {
			out = append(out, p)
		}
	}
	return out
}

// reducePlayer collects samples for each requested field over [bStart,
// bEnd) and runs the reducer. Returns nil when no field produced a
// non-nil value (i.e. player wasn't active in this window).
func reducePlayer(
	p *result.PlayerStream,
	fields []string,
	reds []Reducer,
	redNames []string,
	bStart, bEnd float64,
) map[string]any {
	// out is allocated lazily on the first non-nil field so that inactive
	// players (no field produced a value) cost no map allocation — the
	// common case for the thousands of 50ms buckets where a given player
	// has no samples.
	var out map[string]any
	for i, f := range fields {
		var val any
		if v, ok := fastReduce(p, f, redNames[i], bStart, bEnd); ok {
			val = v
		} else {
			// General path: materialise the in-window samples and run the
			// reducer. Used for any reducer the fast path does not
			// specialise (mean/min/max/last/dominant/held-any/majority).
			val = reds[i].Apply(collectSamples(p, f, bStart, bEnd))
		}
		if val == nil {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(fields))
		}
		out[f] = val
	}
	return out
}

// fastReduce computes the reduced value for the two reducers the default
// bucketing uses — "first" and "any" — directly from the stream, without
// allocating the intermediate []Sample that collectSamples would. This is
// the hot path: a 50ms full-field bucketing of a 20-minute 4on4 otherwise
// allocates ~4M tiny slices (one per field × player × bucket) and the
// resulting GC churn dominates the run.
//
// It returns ok=false for any (reducer, field-kind) pair it does not
// specialise, so the caller falls back to collectSamples + Reducer.Apply
// and exact semantics are preserved for custom reducers. The values
// returned here are byte-identical to the slice path — same boxed types,
// same carry-forward rules — locked by TestFastReduceParity.
func fastReduce(p *result.PlayerStream, field, reducer string, bStart, bEnd float64) (any, bool) {
	kind, ok := FieldKindFor(field)
	if !ok {
		return nil, false
	}
	switch reducer {
	case "first":
		switch kind {
		case KindChangeI16:
			return firstChangeI16(streamI16(p, field), bStart, bEnd), true
		case KindChangeStr:
			return firstChangeStr(streamStr(p, field), bStart, bEnd), true
		case KindInterval:
			s := streamInterval(p, field)
			if s == nil {
				return nil, true
			}
			// Matches intervalSamples[0]: held at exactly bStart.
			return intervalContains(s, int32(bStart*1000)), true
		case KindPosition:
			return firstPosition(p.Position, bStart, bEnd), true
		case KindView:
			return firstView(p.Position, bStart, bEnd), true
		case KindHeight:
			return firstHeight(p.Position, bStart, bEnd), true
		case KindLiquid:
			return firstLiquid(p.Position, bStart, bEnd), true
		case KindVelocity:
			return firstVelocity(p.Position, bStart, bEnd), true
		}
	case "any":
		if kind == KindEventList {
			// Matches AnyReducer over eventListSamples: a bool (never nil).
			return anyEventInWindow(streamEventList(p, field), bStart, bEnd), true
		}
	}
	return nil, false
}

// firstChangeI16 returns changeSamplesI16(stream, bStart, bEnd)[0].V
// without building the slice: the carry value (last change at/before
// bStart) if any, else the first in-window change, else nil.
func firstChangeI16(stream []result.ChangeI16, bStart, bEnd float64) any {
	if stream == nil {
		return nil
	}
	if carry := indexI16AtOrBefore(stream, int32(bStart*1000)); carry >= 0 {
		return stream[carry].V
	}
	// carry < 0 ⇒ stream[0].T > bStartMs; it's the first in-window sample
	// when it also lands before bEnd.
	if stream[0].T < int32(bEnd*1000) {
		return stream[0].V
	}
	return nil
}

// firstChangeStr is firstChangeI16 for string change streams.
func firstChangeStr(stream []result.ChangeStr, bStart, bEnd float64) any {
	if stream == nil {
		return nil
	}
	if carry := indexStrAtOrBefore(stream, int32(bStart*1000)); carry >= 0 {
		return stream[carry].V
	}
	if stream[0].T < int32(bEnd*1000) {
		return stream[0].V
	}
	return nil
}

// firstPosition returns positionSamples(p, bStart, bEnd)[0].V without the
// slice: the first in-window sample, else the carry-forward sample, else nil.
func firstPosition(pt *result.PositionTrack, bStart, bEnd float64) any {
	if pt == nil {
		return nil
	}
	n := len(pt.T)
	if n == 0 {
		return nil
	}
	bStartMs := int32(bStart * 1000)
	firstIn := sort.Search(n, func(i int) bool { return pt.T[i] >= bStartMs })
	if firstIn < n && pt.T[firstIn] < int32(bEnd*1000) {
		return positionTriple(pt, firstIn)
	}
	if firstIn > 0 {
		return positionTriple(pt, firstIn-1)
	}
	return nil
}

// anyEventInWindow reports whether the event-list stream has any timestamp
// in [bStart, bEnd) — AnyReducer's result over eventListSamples, computed
// with a binary search instead of a scan. The stream is sorted ascending.
func anyEventInWindow(stream []int32, bStart, bEnd float64) any {
	bStartMs := int32(bStart * 1000)
	i := sort.Search(len(stream), func(i int) bool { return stream[i] >= bStartMs })
	return i < len(stream) && stream[i] < int32(bEnd*1000)
}

// collectSamples walks the appropriate stream of p and returns Samples
// suitable for reduction. Carry-forward semantics: for change streams,
// the last entry with T <= bStart is included so reducers like "last"
// behave as "value at end of window even when nothing changed inside."
func collectSamples(p *result.PlayerStream, field string, bStart, bEnd float64) []Sample {
	kind, ok := FieldKindFor(field)
	if !ok {
		return nil
	}
	switch kind {
	case KindChangeI16:
		return changeI16Samples(p, field, bStart, bEnd)
	case KindChangeStr:
		return changeStrSamples(p, field, bStart, bEnd)
	case KindInterval:
		return intervalSamples(p, field, bStart, bEnd)
	case KindPosition:
		return positionSamples(p, bStart, bEnd)
	case KindView:
		return viewSamples(p, bStart, bEnd)
	case KindHeight:
		return heightSamples(p, bStart, bEnd)
	case KindLiquid:
		return liquidSamples(p, bStart, bEnd)
	case KindVelocity:
		return velocitySamples(p, bStart, bEnd)
	case KindEventList:
		return eventListSamples(p, field, bStart, bEnd)
	}
	return nil
}

func changeI16Samples(p *result.PlayerStream, field string, bStart, bEnd float64) []Sample {
	stream := streamI16(p, field)
	if stream == nil {
		return nil
	}
	return changeSamplesI16(stream, bStart, bEnd)
}

func changeStrSamples(p *result.PlayerStream, field string, bStart, bEnd float64) []Sample {
	stream := streamStr(p, field)
	if stream == nil {
		return nil
	}
	return changeSamplesStr(stream, bStart, bEnd)
}

func intervalSamples(p *result.PlayerStream, field string, bStart, bEnd float64) []Sample {
	stream := streamInterval(p, field)
	if stream == nil {
		return nil
	}
	// Sample the interval state at a few points across the window so
	// majority / held-any have something meaningful to count. samples[0]
	// is at exactly bStart so the "first" reducer returns intervalContains(bStart),
	// matching v6's "held at first event of bucket" semantics.
	const samplesPerWindow = 8
	out := make([]Sample, samplesPerWindow)
	span := bEnd - bStart
	out[0] = Sample{T: bStart, V: intervalContains(stream, int32(bStart*1000))}
	for i := 1; i < samplesPerWindow; i++ {
		t := bStart + float64(i)*span/float64(samplesPerWindow)
		out[i] = Sample{T: t, V: intervalContains(stream, int32(t*1000))}
	}
	return out
}

// positionSamples emits the position samples in [bStart, bEnd). When
// the window contains no native samples (a "gap bucket" — typically
// during deaths or recording lag) it falls back to a single
// carry-forward sample so reducers still produce a value.
//
// Window membership uses inclusive lower bound: samples with
// T == bStart are in-window. This matches v6's `int(t/0.05)` integer
// division — a sample exactly at the bucket boundary is the first
// event of that bucket, not the last event of the previous one.
//
// Order: in-window samples chronologically; carry-forward only
// appended when no in-window sample exists. So "first" returns the
// first in-window sample (or carry in gap buckets); "last" returns
// the last in-window sample (or carry); "mean"/"min"/"max" don't
// include the carry unless the bucket is empty.
func positionSamples(p *result.PlayerStream, bStart, bEnd float64) []Sample {
	if p.Position == nil {
		return nil
	}
	pt := p.Position
	n := len(pt.T)
	if n == 0 {
		return nil
	}
	// pt.T is int32 ms (schema v8); window bounds arrive in float64
	// seconds (public view API). Convert window once; the comparison
	// loop stays in int32 ms. Sample.T is the public unit, float64
	// seconds, converted once per emitted sample.
	bStartMs := int32(bStart * 1000)
	bEndMs := int32(bEnd * 1000)
	// First sample with T >= bStartMs (inclusive). Treats samples
	// landing exactly on the bucket boundary as the bucket's first
	// event, matching v6's int-division semantics.
	firstIn := sort.Search(n, func(i int) bool { return pt.T[i] >= bStartMs })
	out := make([]Sample, 0, 4)
	for i := firstIn; i < n; i++ {
		t := pt.T[i]
		if t >= bEndMs {
			break
		}
		out = append(out, Sample{T: float64(t) * 0.001, V: positionTriple(pt, i)})
	}
	if len(out) == 0 && firstIn > 0 {
		// Gap bucket — fall back to the latest sample before bStart.
		idx := firstIn - 1
		out = append(out, Sample{T: float64(pt.T[idx]) * 0.001, V: positionTriple(pt, idx)})
	}
	return out
}

// positionTriple returns a [3]result.Coord array for sample i (rounded
// at JSON time; native float32 in memory).
func positionTriple(pt *result.PositionTrack, i int) [3]result.Coord {
	return [3]result.Coord{result.Coord(pt.X[i]), result.Coord(pt.Y[i]), result.Coord(pt.Z[i])}
}

// viewPair returns the [vp, vya] raw angle16 pair for sample i. int16 to
// match the schema columns (and the split vp/vya columnar columns).
func viewPair(pt *result.PositionTrack, i int) [2]int16 {
	return [2]int16{pt.VP[i], pt.VYa[i]}
}

// firstView / firstHeight / firstLiquid mirror firstPosition for the
// view-direction, floor-height, and liquid columns: the first in-window
// sample, else the carry-forward sample, else nil. Each returns nil when
// its column is absent (no view recorded, or no BSP for h/lq), so the
// field is omitted exactly as the row builder's omitempty would.
func firstView(pt *result.PositionTrack, bStart, bEnd float64) any {
	if pt == nil || len(pt.VP) != len(pt.T) || len(pt.VYa) != len(pt.T) {
		return nil
	}
	if i := firstPosIndex(pt, bStart, bEnd); i >= 0 {
		return viewPair(pt, i)
	}
	return nil
}

func firstHeight(pt *result.PositionTrack, bStart, bEnd float64) any {
	if pt == nil || len(pt.H) != len(pt.T) {
		return nil
	}
	if i := firstPosIndex(pt, bStart, bEnd); i >= 0 {
		return result.Coord(pt.H[i])
	}
	return nil
}

func firstLiquid(pt *result.PositionTrack, bStart, bEnd float64) any {
	if pt == nil || len(pt.Lq) != len(pt.T) {
		return nil
	}
	if i := firstPosIndex(pt, bStart, bEnd); i >= 0 {
		// int16 (not int8) so the value boxes identically in the row and
		// columnar paths — the parity test deep-equals the two, and the
		// columnar i16 column can't represent int8 distinctly. The lq
		// codes (0–15) are unchanged; decode with result.LqLevel/LqType.
		return int16(pt.Lq[i])
	}
	return nil
}

func firstVelocity(pt *result.PositionTrack, bStart, bEnd float64) any {
	if pt == nil || len(pt.VX) != len(pt.T) || len(pt.VY) != len(pt.T) || len(pt.VZ) != len(pt.T) {
		return nil
	}
	if i := firstPosIndex(pt, bStart, bEnd); i >= 0 {
		return velocityTriple(pt, i)
	}
	return nil
}

// firstPosIndex returns the index of the first sample in [bStart, bEnd),
// else the carry-forward sample before bStart, else -1. Shared liveness
// logic behind firstPosition/firstView/firstHeight/firstLiquid.
func firstPosIndex(pt *result.PositionTrack, bStart, bEnd float64) int {
	n := len(pt.T)
	if n == 0 {
		return -1
	}
	bStartMs := int32(bStart * 1000)
	firstIn := sort.Search(n, func(i int) bool { return pt.T[i] >= bStartMs })
	if firstIn < n && pt.T[firstIn] < int32(bEnd*1000) {
		return firstIn
	}
	if firstIn > 0 {
		return firstIn - 1
	}
	return -1
}

// viewSamples / heightSamples / liquidSamples mirror positionSamples for
// the view, height, and liquid columns: in-window samples chronological,
// or a single carry-forward sample in a gap bucket. Empty (nil) when the
// underlying column is absent.
func viewSamples(p *result.PlayerStream, bStart, bEnd float64) []Sample {
	pt := p.Position
	if pt == nil || len(pt.VP) != len(pt.T) || len(pt.VYa) != len(pt.T) {
		return nil
	}
	return columnSamples(pt, bStart, bEnd, func(i int) any { return viewPair(pt, i) })
}

func heightSamples(p *result.PlayerStream, bStart, bEnd float64) []Sample {
	pt := p.Position
	if pt == nil || len(pt.H) != len(pt.T) {
		return nil
	}
	return columnSamples(pt, bStart, bEnd, func(i int) any { return result.Coord(pt.H[i]) })
}

func liquidSamples(p *result.PlayerStream, bStart, bEnd float64) []Sample {
	pt := p.Position
	if pt == nil || len(pt.Lq) != len(pt.T) {
		return nil
	}
	return columnSamples(pt, bStart, bEnd, func(i int) any { return int16(pt.Lq[i]) })
}

func velocitySamples(p *result.PlayerStream, bStart, bEnd float64) []Sample {
	pt := p.Position
	if pt == nil || len(pt.VX) != len(pt.T) || len(pt.VY) != len(pt.T) || len(pt.VZ) != len(pt.T) {
		return nil
	}
	return columnSamples(pt, bStart, bEnd, func(i int) any { return velocityTriple(pt, i) })
}

// velocityTriple returns the [vx, vy, vz] units/sec vector for sample i
// (rounded at JSON time; native float32 in memory).
func velocityTriple(pt *result.PositionTrack, i int) [3]result.Coord {
	return [3]result.Coord{result.Coord(pt.VX[i]), result.Coord(pt.VY[i]), result.Coord(pt.VZ[i])}
}

// columnSamples is positionSamples generalised over a per-index value
// accessor, so the view/height/liquid columns share the identical
// window-membership and gap-bucket carry-forward semantics.
func columnSamples(pt *result.PositionTrack, bStart, bEnd float64, valAt func(i int) any) []Sample {
	n := len(pt.T)
	if n == 0 {
		return nil
	}
	bStartMs := int32(bStart * 1000)
	bEndMs := int32(bEnd * 1000)
	firstIn := sort.Search(n, func(i int) bool { return pt.T[i] >= bStartMs })
	out := make([]Sample, 0, 4)
	for i := firstIn; i < n; i++ {
		t := pt.T[i]
		if t >= bEndMs {
			break
		}
		out = append(out, Sample{T: float64(t) * 0.001, V: valAt(i)})
	}
	if len(out) == 0 && firstIn > 0 {
		idx := firstIn - 1
		out = append(out, Sample{T: float64(pt.T[idx]) * 0.001, V: valAt(idx)})
	}
	return out
}

func eventListSamples(p *result.PlayerStream, field string, bStart, bEnd float64) []Sample {
	stream := streamEventList(p, field)
	if stream == nil {
		return nil
	}
	// Stream is int32 ms (schema v8); compare in ms then convert each
	// in-window timestamp to seconds for the public Sample.
	bStartMs := int32(bStart * 1000)
	bEndMs := int32(bEnd * 1000)
	out := make([]Sample, 0, 2)
	for _, t := range stream {
		if t < bStartMs || t >= bEndMs {
			continue
		}
		ts := float64(t) * 0.001
		out = append(out, Sample{T: ts, V: ts})
	}
	return out
}

func changeSamplesI16(stream []result.ChangeI16, bStart, bEnd float64) []Sample {
	bStartMs := int32(bStart * 1000)
	bEndMs := int32(bEnd * 1000)
	out := make([]Sample, 0, 4)
	carry := indexI16AtOrBefore(stream, bStartMs)
	if carry >= 0 {
		out = append(out, Sample{T: float64(stream[carry].T) * 0.001, V: stream[carry].V})
	}
	startIdx := carry + 1
	if startIdx < 0 {
		startIdx = 0
	}
	for i := startIdx; i < len(stream); i++ {
		c := stream[i]
		if c.T < bStartMs {
			continue
		}
		if c.T >= bEndMs {
			break
		}
		out = append(out, Sample{T: float64(c.T) * 0.001, V: c.V})
	}
	return out
}

func changeSamplesStr(stream []result.ChangeStr, bStart, bEnd float64) []Sample {
	bStartMs := int32(bStart * 1000)
	bEndMs := int32(bEnd * 1000)
	out := make([]Sample, 0, 4)
	carry := indexStrAtOrBefore(stream, bStartMs)
	if carry >= 0 {
		out = append(out, Sample{T: float64(stream[carry].T) * 0.001, V: stream[carry].V})
	}
	startIdx := carry + 1
	if startIdx < 0 {
		startIdx = 0
	}
	for i := startIdx; i < len(stream); i++ {
		c := stream[i]
		if c.T < bStartMs {
			continue
		}
		if c.T >= bEndMs {
			break
		}
		out = append(out, Sample{T: float64(c.T) * 0.001, V: c.V})
	}
	return out
}

// index*AtOrBefore returns the largest index i for which stream[i].T
// is <= the query tMs. The query is integer ms (schema v8); callers
// converting from seconds use int32(t * 1000) at the boundary.
func indexI16AtOrBefore(stream []result.ChangeI16, tMs int32) int {
	i := sort.Search(len(stream), func(i int) bool { return stream[i].T > tMs })
	return i - 1
}

func indexStrAtOrBefore(stream []result.ChangeStr, tMs int32) int {
	i := sort.Search(len(stream), func(i int) bool { return stream[i].T > tMs })
	return i - 1
}

// intervalContains tests whether tMs falls inside any half-open
// interval [Start, End). All times are integer milliseconds.
func intervalContains(stream []result.Interval, tMs int32) bool {
	for _, iv := range stream {
		if tMs >= iv.Start && tMs < iv.End {
			return true
		}
	}
	return false
}

// streamI16 / streamStr / streamInterval / streamEventList dispatch
// by field code on a PlayerStream. Returns nil for unknown codes —
// callers have already validated, so this is a runtime guardrail.
func streamI16(p *result.PlayerStream, field string) []result.ChangeI16 {
	switch field {
	case FieldHealth:
		return p.Health
	case FieldArmor:
		return p.Armor
	case FieldLoc:
		return p.Loc
	case FieldShells:
		return p.Shells
	case FieldNails:
		return p.Nails
	case FieldRockets:
		return p.Rockets
	case FieldCells:
		return p.Cells
	}
	return nil
}

func streamStr(p *result.PlayerStream, field string) []result.ChangeStr {
	if field == FieldArmorType {
		return p.ArmorType
	}
	return nil
}

func streamInterval(p *result.PlayerStream, field string) []result.Interval {
	switch field {
	case FieldRL:
		return p.RL
	case FieldLG:
		return p.LG
	case FieldGL:
		return p.GL
	case FieldSSG:
		return p.SSG
	case FieldSNG:
		return p.SNG
	case FieldQuad:
		return p.Quad
	case FieldPent:
		return p.Pent
	case FieldRing:
		return p.Ring
	}
	return nil
}

// streamEventList returns the raw int32-ms timestamp slice for a
// spawn/death-style event field. Callers wrap the seconds conversion
// where the public Sample type (float64 seconds) is materialised.
func streamEventList(p *result.PlayerStream, field string) []int32 {
	switch field {
	case FieldSpawns:
		return p.Spawns
	case FieldDeaths:
		return p.Deaths
	}
	return nil
}

// aggregateTeams produces the per-team counters for a bucket's Team
// map (IncludeTeam). We re-derive from each player's reduced
// values (booleans for weapons / powerups) so the team aggregate is
// always consistent with the per-player display. Most fields are
// int; the "abt" key carries an armor-by-type sub-map ("ra"/"ya"/"ga"
// → count) so the frontend can colour-segment the team-armor bar.
func aggregateTeams(
	players []result.PlayerStream,
	reduced map[string]map[string]any,
) map[string]map[string]any {
	teams := make(map[string]map[string]any)
	bumpInt := func(td map[string]any, key string) {
		if v, ok := td[key]; ok {
			td[key] = v.(int) + 1
		} else {
			td[key] = 1
		}
	}
	addInt := func(td map[string]any, key string, n int) {
		if v, ok := td[key]; ok {
			td[key] = v.(int) + n
		} else {
			td[key] = n
		}
	}
	for pi := range players {
		p := &players[pi]
		pdata, ok := reduced[p.Name]
		if !ok {
			continue
		}
		if p.Team == "" {
			continue
		}
		td := teams[p.Team]
		if td == nil {
			td = make(map[string]any)
			teams[p.Team] = td
		}
		hasRL := boolField(pdata, FieldRL)
		hasLG := boolField(pdata, FieldLG)
		hasGL := boolField(pdata, FieldGL)
		switch {
		case hasRL && hasLG:
			bumpInt(td, "rllg")
			bumpInt(td, "w")
		case hasRL:
			bumpInt(td, "rl")
			bumpInt(td, "w")
		case hasLG:
			bumpInt(td, "lg")
			bumpInt(td, "w")
		}
		if hasGL {
			bumpInt(td, "gl")
		}
		if boolField(pdata, FieldQuad) {
			bumpInt(td, "q")
			bumpInt(td, "pw")
		}
		if boolField(pdata, FieldPent) {
			bumpInt(td, "pe")
			bumpInt(td, "pw")
		}
		if boolField(pdata, FieldRing) {
			bumpInt(td, "r")
			bumpInt(td, "pw")
		}
		// Health / armor sums.
		if h, ok := numericFromAny(pdata[FieldHealth]); ok {
			addInt(td, "th", int(h))
		}
		if a, ok := numericFromAny(pdata[FieldArmor]); ok {
			addInt(td, "ta", int(a))
		}
		// Armor-by-type (abt) — sub-map keyed by armor type code.
		if atVal, ok := pdata[FieldArmorType]; ok {
			if at, ok := atVal.(string); ok && at != "" {
				abt, ok := td["abt"].(map[string]int)
				if !ok {
					abt = make(map[string]int)
					td["abt"] = abt
				}
				abt[at]++
			}
		}
	}
	if len(teams) == 0 {
		return nil
	}
	return teams
}

func boolField(pdata map[string]any, field string) bool {
	v, ok := pdata[field]
	if !ok {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func numericFromAny(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	return numericToFloat(v)
}

