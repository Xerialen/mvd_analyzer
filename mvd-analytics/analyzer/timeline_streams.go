package analyzer

import (
	"math"
	"sort"

	"github.com/mvd-analyzer/mvd-analytics/bspvis"
	"github.com/mvd-analyzer/mvd-analytics/mapclip"
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-reader/events"
)

// msTime converts a float64-seconds event timestamp into the canonical
// int32 milliseconds used throughout schema v8. Event Time is the
// derived view of the decoder's int32-ms accumulator
// (msg.Time = float64(d.timeMs)*0.001); math.Round inverts that exactly
// for any integer-ms value representable in float64 (up to ~285 years
// — comfortably more than any conceivable match).
//
// Events that carry an explicit TimeMs field (PlayerPositionEvent,
// SpawnEvent, DeathEvent) bypass this helper and use TimeMs directly;
// the other event types haven't been plumbed yet, so producers convert
// here at the analyzer write site. The round-trip is mathematically
// lossless when math.Round is used.
func msTime(t float64) int32 {
	return int32(math.Round(t * 1000))
}

// Streams emission for the timeline analyzer.
//
// Every OnEvent dispatch updates the running cursor (timelinePlayerState
// fields) AND the historical record (the streamBuilder substruct).
// The cursor is the analyser's "value right now"; the builder is the
// append-only ledger that becomes result.PlayerStream at finalize.
//
// Append rules (D11 in PLAN-v3): change streams dedup against last
// value; position never dedups; intervals open on false→true and close
// on true→false (or at match end).

// recordHealth dedups against the last seen value before appending.
// All record* functions take integer milliseconds (schema v8).
func (b *streamBuilder) recordHealth(tMs int32, v int16) {
	if n := len(b.health); n > 0 && b.health[n-1].v == v {
		return
	}
	b.health = append(b.health, changeI16{t: tMs, v: v})
}

func (b *streamBuilder) recordArmor(tMs int32, v int16) {
	if n := len(b.armor); n > 0 && b.armor[n-1].v == v {
		return
	}
	b.armor = append(b.armor, changeI16{t: tMs, v: v})
}

func (b *streamBuilder) recordArmorType(tMs int32, v string) {
	if n := len(b.armorType); n > 0 && b.armorType[n-1].v == v {
		return
	}
	b.armorType = append(b.armorType, changeStr{t: tMs, v: v})
}

func (b *streamBuilder) recordLoc(tMs int32, v int16) {
	if n := len(b.loc); n > 0 && b.loc[n-1].v == v {
		return
	}
	b.loc = append(b.loc, changeI16{t: tMs, v: v})
}

func (b *streamBuilder) recordShells(tMs int32, v int16) {
	if n := len(b.shells); n > 0 && b.shells[n-1].v == v {
		return
	}
	b.shells = append(b.shells, changeI16{t: tMs, v: v})
}

func (b *streamBuilder) recordNails(tMs int32, v int16) {
	if n := len(b.nails); n > 0 && b.nails[n-1].v == v {
		return
	}
	b.nails = append(b.nails, changeI16{t: tMs, v: v})
}

func (b *streamBuilder) recordRockets(tMs int32, v int16) {
	if n := len(b.rockets); n > 0 && b.rockets[n-1].v == v {
		return
	}
	b.rockets = append(b.rockets, changeI16{t: tMs, v: v})
}

func (b *streamBuilder) recordCells(tMs int32, v int16) {
	if n := len(b.cells); n > 0 && b.cells[n-1].v == v {
		return
	}
	b.cells = append(b.cells, changeI16{t: tMs, v: v})
}

// recordPosition appends every native sample (no dedup; D11
// asymmetry). Time is integer milliseconds — the canonical wire-native
// unit; we never narrow it back to float to avoid drift across the
// boundary comparisons in locgraph / blip filter. x/y/z are kept as the
// wire-native float32 origin (no truncation to whole units). vp/vya are
// the raw angle16 view pitch/yaw shorts off the wire, stored losslessly.
func (b *streamBuilder) recordPosition(tMs int32, x, y, z float32, vp, vya int16) {
	b.posT = append(b.posT, tMs)
	b.posX = append(b.posX, x)
	b.posY = append(b.posY, y)
	b.posZ = append(b.posZ, z)
	b.posVP = append(b.posVP, vp)
	b.posVYa = append(b.posVYa, vya)
}

func (b *streamBuilder) recordSpawn(tMs int32) {
	b.spawns = append(b.spawns, tMs)
}

func (b *streamBuilder) recordDeath(tMs int32) {
	b.deaths = append(b.deaths, tMs)
}

// updateInterval drives an interval stream based on a boolean flip.
// On false→true, opens an anchor at tMs. On true→false, closes the
// previous anchor as [anchor, tMs) and appends to the closed list.
// Same-state events are no-ops (dedup invariant for booleans). All
// times are integer milliseconds (schema v8).
func (s *intervalState) updateInterval(tMs int32, held bool) {
	if held == s.held {
		return
	}
	if held {
		s.anchor = tMs
		s.held = true
		return
	}
	// true → false: close the open interval.
	if s.held {
		s.closed = append(s.closed, intervalRecord{start: s.anchor, end: tMs})
	}
	s.held = false
}

// closeAtMatchEnd flushes any still-open interval at match end so the
// caller doesn't get half-built records. After this no further
// updateInterval calls should arrive.
func (s *intervalState) closeAtMatchEnd(tMs int32) {
	if s.held {
		s.closed = append(s.closed, intervalRecord{start: s.anchor, end: tMs})
		s.held = false
	}
}

// recordItemFlags is a one-shot helper called from the analyzer's
// stat-update path. It folds the parsed boolean state for every
// inventory field into the corresponding interval streams.
func (b *streamBuilder) recordItemFlags(tMs int32, w weaponLoadout, p powerupLoadout) {
	b.rl.updateInterval(tMs, w.rl)
	b.lg.updateInterval(tMs, w.lg)
	b.gl.updateInterval(tMs, w.gl)
	b.ssg.updateInterval(tMs, w.ssg)
	b.sng.updateInterval(tMs, w.sng)
	b.quad.updateInterval(tMs, p.quad)
	b.pent.updateInterval(tMs, p.pent)
	b.ring.updateInterval(tMs, p.ring)
}

// finalize closes any open intervals at matchEnd and converts internal
// records to the public result types.
func (b *streamBuilder) finalize(matchEndMs int32) {
	b.rl.closeAtMatchEnd(matchEndMs)
	b.lg.closeAtMatchEnd(matchEndMs)
	b.gl.closeAtMatchEnd(matchEndMs)
	b.ssg.closeAtMatchEnd(matchEndMs)
	b.sng.closeAtMatchEnd(matchEndMs)
	b.quad.closeAtMatchEnd(matchEndMs)
	b.pent.closeAtMatchEnd(matchEndMs)
	b.ring.closeAtMatchEnd(matchEndMs)
}

// toPlayerStream converts the builder into result.PlayerStream,
// suitable for appending to result.Streams.Players.
func (b *streamBuilder) toPlayerStream(name, team string) result.PlayerStream {
	ps := result.PlayerStream{Name: name, Team: team}
	if len(b.health) > 0 {
		ps.Health = make([]result.ChangeI16, len(b.health))
		for i, c := range b.health {
			ps.Health[i] = result.ChangeI16{T: c.t, V: c.v}
		}
	}
	if len(b.armor) > 0 {
		ps.Armor = make([]result.ChangeI16, len(b.armor))
		for i, c := range b.armor {
			ps.Armor[i] = result.ChangeI16{T: c.t, V: c.v}
		}
	}
	if len(b.armorType) > 0 {
		ps.ArmorType = make([]result.ChangeStr, len(b.armorType))
		for i, c := range b.armorType {
			ps.ArmorType[i] = result.ChangeStr{T: c.t, V: c.v}
		}
	}
	if len(b.loc) > 0 {
		ps.Loc = make([]result.ChangeI16, len(b.loc))
		for i, c := range b.loc {
			ps.Loc[i] = result.ChangeI16{T: c.t, V: c.v}
		}
	}
	ps.RL = intervalsToResult(b.rl.closed)
	ps.LG = intervalsToResult(b.lg.closed)
	ps.GL = intervalsToResult(b.gl.closed)
	ps.SSG = intervalsToResult(b.ssg.closed)
	ps.SNG = intervalsToResult(b.sng.closed)
	ps.Quad = intervalsToResult(b.quad.closed)
	ps.Pent = intervalsToResult(b.pent.closed)
	ps.Ring = intervalsToResult(b.ring.closed)
	if len(b.shells) > 0 {
		ps.Shells = make([]result.ChangeI16, len(b.shells))
		for i, c := range b.shells {
			ps.Shells[i] = result.ChangeI16{T: c.t, V: c.v}
		}
	}
	if len(b.nails) > 0 {
		ps.Nails = make([]result.ChangeI16, len(b.nails))
		for i, c := range b.nails {
			ps.Nails[i] = result.ChangeI16{T: c.t, V: c.v}
		}
	}
	if len(b.rockets) > 0 {
		ps.Rockets = make([]result.ChangeI16, len(b.rockets))
		for i, c := range b.rockets {
			ps.Rockets[i] = result.ChangeI16{T: c.t, V: c.v}
		}
	}
	if len(b.cells) > 0 {
		ps.Cells = make([]result.ChangeI16, len(b.cells))
		for i, c := range b.cells {
			ps.Cells[i] = result.ChangeI16{T: c.t, V: c.v}
		}
	}
	if len(b.posT) > 0 {
		pos := &result.PositionTrack{
			T: append([]int32(nil), b.posT...),
			X: append([]float32(nil), b.posX...),
			Y: append([]float32(nil), b.posY...),
			Z: append([]float32(nil), b.posZ...),
		}
		if len(b.posLi) == len(b.posT) {
			pos.Li = append([]int16(nil), b.posLi...)
		}
		if len(b.posH) == len(b.posT) {
			pos.H = append([]float32(nil), b.posH...)
		}
		if len(b.posLq) == len(b.posT) {
			pos.Lq = append([]int8(nil), b.posLq...)
		}
		if len(b.posVP) == len(b.posT) {
			pos.VP = append([]int16(nil), b.posVP...)
		}
		if len(b.posVYa) == len(b.posT) {
			pos.VYa = append([]int16(nil), b.posVYa...)
		}
		if len(b.posVX) == len(b.posT) {
			pos.VX = append([]float32(nil), b.posVX...)
		}
		if len(b.posVY) == len(b.posT) {
			pos.VY = append([]float32(nil), b.posVY...)
		}
		if len(b.posVZ) == len(b.posT) {
			pos.VZ = append([]float32(nil), b.posVZ...)
		}
		ps.Position = pos
	}
	if len(b.spawns) > 0 {
		ps.Spawns = append([]int32(nil), b.spawns...)
	}
	if len(b.deaths) > 0 {
		ps.Deaths = append([]int32(nil), b.deaths...)
	}
	return ps
}

func intervalsToResult(in []intervalRecord) []result.Interval {
	if len(in) == 0 {
		return nil
	}
	out := make([]result.Interval, len(in))
	for i, r := range in {
		out[i] = result.Interval{Start: r.start, End: r.end}
	}
	return out
}

// disambiguatePlayerName resolves D12 (collision suffix). Given a slot
// and a name that may collide with another slot's resolved name in the
// same match, return the slot-suffixed form so each slot's stream is
// uniquely keyed.
func disambiguatePlayerName(name string, slot int, allNames map[string]int) string {
	if allNames[name] > 1 {
		return name + "#" + intToStr(slot)
	}
	return name
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	if negative {
		digits = append(digits, '-')
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

// appendSlice appends the entries of src that fall within the half-open
// window [startMs, endMs) onto b, preserving time order. It is the
// primitive behind reconnect-aware stream merging: one player's
// per-slot session fragments are stitched into a single stream by
// appending each fragment in time order, and a slot shared by two
// players over time is carved by appending only each player's window.
//
// Change streams dedup at the seam (so concatenation keeps the
// "no two adjacent equal values" invariant); the position track and
// spawn/death timestamps append verbatim; interval lists are clipped to
// the window. Callers append fragments in ascending startMs so the
// merged columns stay globally time-ordered.
func (b *streamBuilder) appendSlice(src *streamBuilder, startMs, endMs int32) {
	in := func(t int32) bool { return t >= startMs && t < endMs }

	appendI16 := func(dst *[]changeI16, col []changeI16) {
		for _, c := range col {
			if !in(c.t) {
				continue
			}
			if n := len(*dst); n > 0 && (*dst)[n-1].v == c.v {
				continue
			}
			*dst = append(*dst, c)
		}
	}
	appendI16(&b.health, src.health)
	appendI16(&b.armor, src.armor)
	appendI16(&b.loc, src.loc)
	appendI16(&b.shells, src.shells)
	appendI16(&b.nails, src.nails)
	appendI16(&b.rockets, src.rockets)
	appendI16(&b.cells, src.cells)

	for _, c := range src.armorType {
		if !in(c.t) {
			continue
		}
		if n := len(b.armorType); n > 0 && b.armorType[n-1].v == c.v {
			continue
		}
		b.armorType = append(b.armorType, c)
	}

	hasLi := len(src.posLi) == len(src.posT)
	hasH := len(src.posH) == len(src.posT)
	hasLq := len(src.posLq) == len(src.posT)
	hasVP := len(src.posVP) == len(src.posT)
	hasVYa := len(src.posVYa) == len(src.posT)
	hasVX := len(src.posVX) == len(src.posT)
	hasVY := len(src.posVY) == len(src.posT)
	hasVZ := len(src.posVZ) == len(src.posT)
	for i, t := range src.posT {
		if !in(t) {
			continue
		}
		b.posT = append(b.posT, t)
		b.posX = append(b.posX, src.posX[i])
		b.posY = append(b.posY, src.posY[i])
		b.posZ = append(b.posZ, src.posZ[i])
		if hasLi {
			b.posLi = append(b.posLi, src.posLi[i])
		}
		if hasH {
			b.posH = append(b.posH, src.posH[i])
		}
		if hasLq {
			b.posLq = append(b.posLq, src.posLq[i])
		}
		if hasVP {
			b.posVP = append(b.posVP, src.posVP[i])
		}
		if hasVYa {
			b.posVYa = append(b.posVYa, src.posVYa[i])
		}
		if hasVX {
			b.posVX = append(b.posVX, src.posVX[i])
		}
		if hasVY {
			b.posVY = append(b.posVY, src.posVY[i])
		}
		if hasVZ {
			b.posVZ = append(b.posVZ, src.posVZ[i])
		}
	}

	for _, t := range src.spawns {
		if in(t) {
			b.spawns = append(b.spawns, t)
		}
	}
	for _, t := range src.deaths {
		if in(t) {
			b.deaths = append(b.deaths, t)
		}
	}

	clip := func(dst *intervalState, s intervalState) {
		for _, r := range s.closed {
			start, end := r.start, r.end
			if start < startMs {
				start = startMs
			}
			if end > endMs {
				end = endMs
			}
			if start < end {
				dst.closed = append(dst.closed, intervalRecord{start: start, end: end})
			}
		}
	}
	clip(&b.rl, src.rl)
	clip(&b.lg, src.lg)
	clip(&b.gl, src.gl)
	clip(&b.ssg, src.ssg)
	clip(&b.sng, src.sng)
	clip(&b.quad, src.quad)
	clip(&b.pent, src.pent)
	clip(&b.ring, src.ring)
}

// fragStart returns the earliest in-window sample time for a fragment,
// used to order an identity's fragments chronologically across slots.
// Series are time-sorted, so the first in-window position / spawn /
// death is the earliest. Falls back to startMs when the window holds no
// sample (an empty fragment sorts harmlessly).
func fragStart(b *streamBuilder, startMs, endMs int32) int32 {
	best := endMs
	got := false
	first := func(col []int32) {
		for _, t := range col {
			if t >= startMs && t < endMs {
				if !got || t < best {
					best = t
					got = true
				}
				break
			}
		}
	}
	first(b.posT)
	first(b.spawns)
	first(b.deaths)
	if !got {
		return startMs
	}
	return best
}

// sessionHasPlay reports whether the builder recorded any actual play
// (a spawn, death, or position sample) inside [startMs, endMs). Used to
// tell a real occupancy from a phantom one (e.g. a vacated slot taken
// by someone who never spawned).
func sessionHasPlay(b *streamBuilder, startMs, endMs int32) bool {
	in := func(t int32) bool { return t >= startMs && t < endMs }
	for _, t := range b.spawns {
		if in(t) {
			return true
		}
	}
	for _, t := range b.deaths {
		if in(t) {
			return true
		}
	}
	for _, t := range b.posT {
		if in(t) {
			return true
		}
	}
	return false
}

// streamFragment is one slot-occupancy window contributing to a player
// identity's merged stream.
type streamFragment struct {
	slot    int
	startMs int32
	endMs   int32
}

// streamGroup accumulates every fragment belonging to one canonical
// player identity (keyed by ResolvedSession.IdentityKey).
type streamGroup struct {
	name    string
	team    string
	repSlot int // smallest contributing slot — stable ordering + collision suffix
	frags   []streamFragment
}

// buildStreamsResult assembles result.Streams, one PlayerStream per
// canonical player *identity* rather than per wire slot. A player who
// reconnected onto a different slot has their per-slot session fragments
// stitched into a single stream (and a slot reused by two players is
// carved at the handover), using the identity table on CoreOutputs.
// matchStart / matchEnd anchor GlobalStream.
//
// Disambiguation: if two distinct identities resolve to the same display
// name, the later one carries a "#slot" suffix (per D12).
func (a *TimelineAnalyzer) buildStreamsResult(slotToName map[int]string, slotToTeam map[int]string, matchStart, matchEnd float64) *result.Streams {
	if len(a.playerState) == 0 {
		return nil
	}
	matchStartMs := msTime(matchStart)
	matchEndMs := msTime(matchEnd)

	// Close any still-open intervals at match end before partitioning so
	// the per-slot builders carry complete interval lists; the slice
	// clip then trims them to each session window.
	slots := make([]int, 0, len(a.playerState))
	for slot := range a.playerState {
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	for _, slot := range slots {
		if state := a.playerState[slot]; state != nil {
			state.streams.finalize(matchEndMs)
		}
	}

	var sessionsFor func(slot int) []ResolvedSession
	if a.core != nil {
		sessionsFor = func(slot int) []ResolvedSession { return a.core.Sessions[slot] }
	} else {
		sessionsFor = func(int) []ResolvedSession { return nil }
	}

	groups := make(map[string]*streamGroup)
	var order []string // identity keys in first-seen slot order, for determinism
	add := func(key, name, team string, slot int, startMs, endMs int32) {
		if name == "" {
			return
		}
		g := groups[key]
		if g == nil {
			g = &streamGroup{name: name, team: team, repSlot: slot}
			groups[key] = g
			order = append(order, key)
		}
		if name != "" {
			g.name = name
		}
		if team != "" {
			g.team = team
		}
		if slot < g.repSlot {
			g.repSlot = slot
		}
		g.frags = append(g.frags, streamFragment{slot: slot, startMs: startMs, endMs: endMs})
	}

	for _, slot := range slots {
		if a.playerState[slot] == nil {
			continue
		}
		sessions := sessionsFor(slot)
		if len(sessions) == 0 {
			// No identity table (e.g. unit test without the registry, or
			// an untracked slot): fall back to one stream per slot named
			// by the demoinfo/userinfo resolution.
			add("slot:"+intToStr(slot), slotToName[slot], slotToTeam[slot], slot, minInt32, maxInt32)
			continue
		}
		for _, s := range sessions {
			add(s.IdentityKey, s.Name, s.Team, slot, s.StartMs, s.EndMs)
		}
	}

	// Count display names so colliding identities can be suffixed.
	nameCounts := make(map[string]int)
	for _, key := range order {
		nameCounts[groups[key].name]++
	}

	streams := &result.Streams{
		Global: result.GlobalStream{MatchStart: matchStartMs, MatchEnd: matchEndMs},
	}
	// Emit in (repSlot, name) order for deterministic output.
	sort.SliceStable(order, func(i, j int) bool {
		gi, gj := groups[order[i]], groups[order[j]]
		if gi.repSlot != gj.repSlot {
			return gi.repSlot < gj.repSlot
		}
		return gi.name < gj.name
	})
	for _, key := range order {
		g := groups[key]
		// Order fragments chronologically by their actual earliest in-window
		// sample, not by session.StartMs — the first session on every slot
		// is clamped to MinInt32, so two slots' fragments would otherwise be
		// ordered by slot index and could interleave the second half before
		// the first.
		sort.SliceStable(g.frags, func(i, j int) bool {
			return fragStart(&a.playerState[g.frags[i].slot].streams, g.frags[i].startMs, g.frags[i].endMs) <
				fragStart(&a.playerState[g.frags[j].slot].streams, g.frags[j].startMs, g.frags[j].endMs)
		})
		var merged streamBuilder
		for _, f := range g.frags {
			merged.appendSlice(&a.playerState[f.slot].streams, f.startMs, f.endMs)
		}
		if merged.isEmpty() {
			continue // phantom identity with no recorded play (e.g. a vacated slot's new occupant who never played)
		}
		uniqName := disambiguatePlayerName(g.name, g.repSlot, nameCounts)
		streams.Players = append(streams.Players, merged.toPlayerStream(uniqName, g.team))
	}
	if len(streams.Players) == 0 {
		return nil
	}
	return streams
}

const (
	minInt32 = int32(-1 << 31)
	maxInt32 = int32(1<<31 - 1)
)

// isEmpty reports whether the builder recorded no player activity at
// all — no vitals, positions, intervals, spawns or deaths. Used to drop
// phantom identities (a vacated slot taken by someone who never played).
func (b *streamBuilder) isEmpty() bool {
	return len(b.health) == 0 && len(b.armor) == 0 && len(b.armorType) == 0 &&
		len(b.loc) == 0 && len(b.shells) == 0 && len(b.nails) == 0 &&
		len(b.rockets) == 0 && len(b.cells) == 0 && len(b.posT) == 0 &&
		len(b.spawns) == 0 && len(b.deaths) == 0 &&
		len(b.rl.closed) == 0 && len(b.lg.closed) == 0 && len(b.gl.closed) == 0 &&
		len(b.ssg.closed) == 0 && len(b.sng.closed) == 0 &&
		len(b.quad.closed) == 0 && len(b.pent.closed) == 0 && len(b.ring.closed) == 0
}

// resolveLocsAndFilterBlips populates each player's PositionTrack.Li
// column from the loc finder, runs the blip filter on it (collapsing
// short-residence wall-bleed onto adjacent stable runs), and emits
// the resulting sparse Loc change stream into PlayerStream.Loc.
//
// Replaces the v6 path of populating per-bucket pData.location and
// running applyBlipFilter on `a.buckets`. The new approach operates
// directly on the native-rate position samples so the parse-time
// bucket data structure is no longer needed.
//
// Returns the loc-name → index map for any callers that need to
// resolve external loc references (e.g. the regions builder).
func (a *TimelineAnalyzer) resolveLocsAndFilterBlips() (locTable []string, locIndex map[string]int) {
	locTable = []string{""}
	locIndex = map[string]int{"": 0}
	if a.locFinder == nil {
		return locTable, locIndex
	}
	thresholdMs := int32(a.blipThresholdMs)

	// First pass: resolve every native sample's nearest loc, populate
	// PositionTrack.Li. Build the loc-name → index map incrementally
	// so finalize doesn't need a separate "collect names then assign
	// indices" pass; the index for a name is stable from first use.
	indexFor := func(name string) int16 {
		if name == "" {
			return 0
		}
		idx, ok := locIndex[name]
		if !ok {
			idx = len(locTable)
			locTable = append(locTable, name)
			locIndex[name] = idx
		}
		return int16(idx)
	}

	// Sort slots so iteration is deterministic — locTable indices are
	// assigned in order of first appearance, and a Go map iteration
	// order would shuffle them across runs.
	slots := make([]int, 0, len(a.playerState))
	for slot := range a.playerState {
		slots = append(slots, slot)
	}
	sort.Ints(slots)

	for _, slot := range slots {
		state := a.playerState[slot]
		b := &state.streams
		if len(b.posT) == 0 {
			continue
		}
		if cap(b.posLi) < len(b.posT) {
			b.posLi = make([]int16, len(b.posT))
		} else {
			b.posLi = b.posLi[:len(b.posT)]
		}
		for i := range b.posT {
			x, y, z := b.posX[i], b.posY[i], b.posZ[i]
			if x == 0 && y == 0 && z == 0 {
				b.posLi[i] = 0
				continue
			}
			b.posLi[i] = indexFor(a.locFinder.FindNearest(x, y, z))
		}
	}

	// Second pass: run the blip filter on each player's Li column,
	// using each player's spawn / death timestamps to split runs.
	if thresholdMs > 0 {
		for _, slot := range slots {
			state := a.playerState[slot]
			b := &state.streams
			if len(b.posT) == 0 {
				continue
			}
			boundaries := mergeBoundaries(b.spawns, b.deaths)
			filterPositionLiBlips(b, boundaries, thresholdMs)
		}
	}

	// Third pass: emit the sparse PlayerStream.Loc change stream from
	// the (now-smoothed) Li column. Both pt.T and the Loc change
	// stream are int32 ms in schema v8 — no conversion needed.
	for _, slot := range slots {
		state := a.playerState[slot]
		b := &state.streams
		for i := range b.posT {
			state.streams.recordLoc(b.posT[i], b.posLi[i])
		}
	}
	return locTable, locIndex
}

// resolveFloorHeights populates each player's PositionTrack.H column —
// the feet-above-floor height — by tracing straight down through the
// map's player clip hulls at every native-rate sample (schema v24).
// Runs per-slot before the reconnect merge — the same staging as
// resolveLocsAndFilterBlips — so appendSlice carries posH alongside
// posLi into the merged stream.
//
// The trace scene is the worldspawn hull plus every mover entity's
// submodel hull posed at its demo-streamed origin for the sample's
// timestamp (schema v27) — a player riding the dm2 RA lift stands on
// the lift, not the shaft floor far beneath it. Mover poses come from
// the moverTrack timelines via forward cursors (samples are
// time-ascending per slot); movers are scanned in entity-number order
// so the per-sample max is deterministic across runs.
//
// Liquids participate too (schema v28). Each sample's liquid state is
// classified against the hull-0 render BSP by mirroring the engine's
// PM_CategorizePosition probes (bspvis.WaterLevel) into posLq, packed
// (type<<2)|level. A sample in liquid (level >= 1) reads H = 0 by
// definition — the liquid surface is the support, so the dm3 pool no
// longer reports swimmers as airborne over the pool bottom. A dry
// sample's support is the higher of the solid scene floor and the
// liquid surface below the origin (bspvis.LiquidSurfaceBelow — one
// column, liquid surfaces are flat), so a jump over water measures to
// the water, not the floor beneath it.
//
// No-op when neither the clip hull nor the render BSP is loaded for
// the map (no provisioned BSP): posH / posLq stay nil and the columns
// are absent. Either can be present alone (a parse failure in one
// loader doesn't take the other down). Samples at the zero origin,
// over a void/pit, or with an embedded origin get the result.NoFloor
// sentinel rather than a fabricated value.
func (a *TimelineAnalyzer) resolveFloorHeights() {
	if a.clipHull == nil && a.visBSP == nil {
		return
	}

	// Movers whose submodel hull was built, in entity-number order.
	type posableMover struct {
		track *moverTrack
		hull  *mapclip.Hull
	}
	moverEnts := make([]int, 0, len(a.movers))
	for ent, mt := range a.movers {
		if a.moverHulls[mt.subModel] != nil {
			moverEnts = append(moverEnts, ent)
		}
	}
	sort.Ints(moverEnts)
	posable := make([]posableMover, len(moverEnts))
	for i, ent := range moverEnts {
		mt := a.movers[ent]
		posable[i] = posableMover{track: mt, hull: a.moverHulls[mt.subModel]}
	}
	cursors := make([]int, len(posable))
	scratch := make([]mapclip.PosedHull, 0, len(posable))

	slots := make([]int, 0, len(a.playerState))
	for slot := range a.playerState {
		slots = append(slots, slot)
	}
	sort.Ints(slots)

	for _, slot := range slots {
		b := &a.playerState[slot].streams
		if len(b.posT) == 0 {
			continue
		}
		for i := range cursors {
			cursors[i] = -1 // each slot rescans the tracks from the start
		}
		if a.clipHull != nil {
			b.posH = make([]float32, len(b.posT))
		}
		if a.visBSP != nil {
			b.posLq = make([]int8, len(b.posT))
		}
		for i := range b.posT {
			x, y, z := b.posX[i], b.posY[i], b.posZ[i]
			if x == 0 && y == 0 && z == 0 {
				if b.posH != nil {
					b.posH[i] = result.NoFloor
				}
				continue
			}

			// Liquid state first: a submerged sample is supported by the
			// liquid and skips the floor traces entirely.
			if a.visBSP != nil {
				level, cont := a.visBSP.WaterLevel(x, y, z)
				b.posLq[i] = lqValue(level, cont)
				if b.posLq[i] != 0 {
					if b.posH != nil {
						b.posH[i] = 0
					}
					continue
				}
			}
			if b.posH == nil {
				continue
			}

			scratch = scratch[:0]
			for mi := range posable {
				org, vis := posable[mi].track.atCursor(b.posT[i], &cursors[mi])
				if !vis {
					continue
				}
				scratch = append(scratch, mapclip.PosedHull{H: posable[mi].hull, Origin: org})
			}
			h, solidOk := mapclip.HeightAboveFloorBoxScene(a.clipHull, scratch, x, y, z)
			// A liquid surface below the origin competes as a support:
			// the higher of solid floor and surface wins. Heights are
			// feet-relative, so smaller h = higher support.
			if a.visBSP != nil {
				if surfZ, _, ok := a.visBSP.LiquidSurfaceBelow(x, y, z); ok {
					hSurf := (z - playerFeetOffset) - surfZ
					if hSurf < 0 {
						// Feet a sub-unit into the surface while the feet
						// probe still reads dry — supported by the liquid.
						hSurf = 0
					}
					if !solidOk || hSurf < h {
						h, solidOk = hSurf, true
					}
				}
			}
			if solidOk {
				b.posH[i] = h
			} else {
				b.posH[i] = result.NoFloor
			}
		}
	}
}

// playerFeetOffset mirrors mapclip's constant: -mins.z of the player
// hull, the distance the origin rides above the floor the feet rest on.
const playerFeetOffset = 24.0

// lqValue packs a bspvis.WaterLevel result into the PositionTrack.Lq
// encoding: 0 dry, else (type << 2) | level — water 5/6/7, slime
// 9/10/11, lava 13/14/15.
func lqValue(level int, contents int32) int8 {
	if level <= 0 {
		return 0
	}
	var typ int8
	switch contents {
	case bspvis.ContentsWater:
		typ = result.LqWater
	case bspvis.ContentsSlime:
		typ = result.LqSlime
	case bspvis.ContentsLava:
		typ = result.LqLava
	default:
		return 0
	}
	return typ<<2 | int8(level&3)
}

// velGapCapMs is the largest inter-sample time gap (ms) the velocity
// estimator will differentiate across. Native position samples arrive
// ~every 13 ms (virtually all gaps < 25 ms, MVD_FORMAT.md), so a gap
// beyond this means the player was dead, the game was paused, or the
// slot changed hands — not real motion. The two samples either side then
// bound separate motion segments and no velocity spans them.
const velGapCapMs = 250

// velTeleportSpeedUps is the speed (units/sec) above which a step between
// two adjacent samples is treated as a teleport / origin discontinuity
// rather than movement, and not differentiated across. The QW server
// clamps real player speed to sv_maxvelocity (2000 ups default), so a
// single step implying >2x that is a map teleporter, a forced setorigin,
// or some other relocation the spawn/gap checks don't see — across which
// a velocity would be a meaningless tens-of-thousands-ups spike. Generous
// (2.25x the default clamp) so legitimate knockback / high-maxvelocity
// servers are never clipped; teleports overshoot it by an order of
// magnitude.
const velTeleportSpeedUps = 4500.0

// resolveVelocities fills each slot's posVX/VY/VZ from the native-rate
// position columns with a central-difference estimator (second-order
// accurate: v[i] = (p[i+1]-p[i-1]) / (t[i+1]-t[i-1])), falling back to a
// one-sided difference at a segment end. It runs per-slot before the
// reconnect merge — like resolveFloorHeights — so it never differentiates
// across a reconnect seam. Within a slot it also refuses to span a
// respawn (the origin teleports to the spawn point — not movement) or an
// abnormal time gap (velGapCapMs); an isolated sample reads 0. Units are
// Quake units per second. No BSP needed — velocity is derived purely from
// the position/time columns.
func (a *TimelineAnalyzer) resolveVelocities() {
	slots := make([]int, 0, len(a.playerState))
	for slot := range a.playerState {
		slots = append(slots, slot)
	}
	sort.Ints(slots)

	for _, slot := range slots {
		b := &a.playerState[slot].streams
		n := len(b.posT)
		if n == 0 {
			continue
		}
		b.posVX = make([]float32, n)
		b.posVY = make([]float32, n)
		b.posVZ = make([]float32, n)
		for i := 0; i < n; i++ {
			usePrev := i > 0 && velConnected(b, i-1, i)
			useNext := i < n-1 && velConnected(b, i, i+1)
			var lo, hi int
			switch {
			case usePrev && useNext:
				lo, hi = i-1, i+1 // central difference
			case usePrev:
				lo, hi = i-1, i // backward difference at a segment end
			case useNext:
				lo, hi = i, i+1 // forward difference at a segment start
			default:
				continue // isolated sample: velocity stays 0
			}
			dt := float64(b.posT[hi] - b.posT[lo])
			if dt <= 0 {
				continue
			}
			s := 1000.0 / dt // ms delta → per-second
			b.posVX[i] = float32((float64(b.posX[hi]) - float64(b.posX[lo])) * s)
			b.posVY[i] = float32((float64(b.posY[hi]) - float64(b.posY[lo])) * s)
			b.posVZ[i] = float32((float64(b.posZ[hi]) - float64(b.posZ[lo])) * s)
		}
	}
}

// velConnected reports whether adjacent samples i and j (= i+1) belong to
// the same motion segment — close enough in time, with no respawn
// teleport between them — so a velocity may be differenced across them.
func velConnected(b *streamBuilder, i, j int) bool {
	dt := b.posT[j] - b.posT[i]
	if dt <= 0 || dt > velGapCapMs {
		return false
	}
	if spawnBetween(b.spawns, b.posT[i], b.posT[j]) {
		return false
	}
	// Reject a non-physical displacement (a map teleporter or other origin
	// discontinuity the spawn/gap checks miss): compare squared distance
	// against the furthest a player could travel at velTeleportSpeedUps.
	dx := float64(b.posX[j]) - float64(b.posX[i])
	dy := float64(b.posY[j]) - float64(b.posY[i])
	dz := float64(b.posZ[j]) - float64(b.posZ[i])
	maxStep := velTeleportSpeedUps * float64(dt) / 1000.0
	return dx*dx+dy*dy+dz*dz <= maxStep*maxStep
}

// spawnBetween reports whether any spawn timestamp falls in (lo, hi].
// spawns is ascending (recordSpawn appends in time order). A respawn at s
// teleports the player to a spawn point, so the sample at/after s sits at
// a new origin — differentiating across it would fabricate a velocity
// spike.
func spawnBetween(spawns []int32, lo, hi int32) bool {
	i := sort.Search(len(spawns), func(k int) bool { return spawns[k] > lo })
	return i < len(spawns) && spawns[i] <= hi
}

// mergeBoundaries returns a sorted list of timestamps where the blip
// filter must split runs: every spawn and every death. Spawns and
// deaths can interleave; merge sorts both into one ascending slice.
// Values are integer milliseconds — comparisons against b.posT are
// exact, no eps slack needed.
func mergeBoundaries(spawns, deaths []int32) []int32 {
	if len(spawns) == 0 && len(deaths) == 0 {
		return nil
	}
	out := make([]int32, 0, len(spawns)+len(deaths))
	i, j := 0, 0
	for i < len(spawns) && j < len(deaths) {
		if spawns[i] <= deaths[j] {
			out = append(out, spawns[i])
			i++
		} else {
			out = append(out, deaths[j])
			j++
		}
	}
	out = append(out, spawns[i:]...)
	out = append(out, deaths[j:]...)
	return out
}

// filterPositionLiBlips smooths short-residence Li runs in b.posLi.
// Mirrors v6's applyBlipFilter / filterBlipsInRun logic but operates
// on per-position-sample Li values rather than per-50ms buckets.
//
// Splits the sample stream into segments at boundary timestamps
// (spawn / death) and at Li=0 gaps (no resolved loc). Within each
// segment, groups consecutive same-Li samples; segments whose
// duration is below thresholdMs become "blips" that get reassigned
// to the surrounding stable Li values. Mutates b.posLi in place.
//
// All time arithmetic is integer milliseconds — boundaries and
// b.posT both use int32 ms so comparisons are exact (this is the
// site of the gib-respawn precision bug schema v8 fixed).
func filterPositionLiBlips(b *streamBuilder, boundaries []int32, thresholdMs int32) {
	if b == nil || len(b.posT) == 0 || len(b.posLi) != len(b.posT) {
		return
	}
	// Walk samples, break runs at boundary crossings or Li=0.
	runStart := 0
	bIdx := 0
	for runStart < len(b.posT) {
		// Skip leading Li=0 samples (no loc resolved).
		for runStart < len(b.posT) && b.posLi[runStart] == 0 {
			runStart++
		}
		if runStart >= len(b.posT) {
			return
		}
		runEnd := runStart + 1
		for runEnd < len(b.posT) && b.posLi[runEnd] != 0 {
			t := b.posT[runEnd]
			for bIdx < len(boundaries) && boundaries[bIdx] <= t {
				if boundaries[bIdx] > b.posT[runStart] {
					goto runComplete
				}
				bIdx++
			}
			runEnd++
		}
	runComplete:
		filterBlipsInPositionRun(b, runStart, runEnd, thresholdMs)
		runStart = runEnd
	}
}

// filterBlipsInPositionRun applies the blip-collapse rules to one
// contiguous Li run [runStart, runEnd) of b.posLi. Implementation
// follows v6's filterBlipsInRun (leading/trailing blips inherit
// nearest stable; blips between two stables split ceil/floor; blips
// between same-loc stables collapse).
func filterBlipsInPositionRun(b *streamBuilder, runStart, runEnd int, thresholdMs int32) {
	if runEnd-runStart < 2 {
		return
	}
	type segment struct {
		li         int16
		start, end int
		duration   int32 // ms
	}
	var segs []segment
	for i := runStart; i < runEnd; {
		li := b.posLi[i]
		j := i + 1
		for j < runEnd && b.posLi[j] == li {
			j++
		}
		var dur int32
		if j < runEnd {
			dur = b.posT[j] - b.posT[i]
		} else if runEnd < len(b.posT) {
			dur = b.posT[runEnd] - b.posT[i]
		} else if j-1 > i {
			dur = b.posT[j-1] - b.posT[i]
		}
		segs = append(segs, segment{li: li, start: i, end: j, duration: dur})
		i = j
	}
	if len(segs) == 0 {
		return
	}
	stable := make([]bool, len(segs))
	firstStable, lastStable := -1, -1
	for i, s := range segs {
		if s.duration >= thresholdMs {
			stable[i] = true
			if firstStable < 0 {
				firstStable = i
			}
			lastStable = i
		}
	}
	if firstStable < 0 {
		return
	}
	for i := 0; i < firstStable; i++ {
		setLiInRange(b.posLi, segs[i].start, segs[i].end, segs[firstStable].li)
	}
	prev := firstStable
	for next := firstStable + 1; next <= lastStable; next++ {
		if !stable[next] {
			continue
		}
		if prev+1 < next {
			aLi := segs[prev].li
			dLi := segs[next].li
			firstBlipSeg := prev + 1
			if aLi == dLi {
				for k := firstBlipSeg; k < next; k++ {
					setLiInRange(b.posLi, segs[k].start, segs[k].end, aLi)
				}
			} else {
				total := 0
				for k := firstBlipSeg; k < next; k++ {
					total += segs[k].end - segs[k].start
				}
				aCount := (total + 1) / 2
				assigned := 0
				for k := firstBlipSeg; k < next; k++ {
					for s := segs[k].start; s < segs[k].end; s++ {
						if assigned < aCount {
							b.posLi[s] = aLi
						} else {
							b.posLi[s] = dLi
						}
						assigned++
					}
				}
			}
		}
		prev = next
	}
	for i := lastStable + 1; i < len(segs); i++ {
		setLiInRange(b.posLi, segs[i].start, segs[i].end, segs[lastStable].li)
	}
}

func setLiInRange(li []int16, lo, hi int, v int16) {
	for i := lo; i < hi; i++ {
		li[i] = v
	}
}

// itemBitsToLoadouts decodes the raw item bitfield into the
// (weapons, powerups, armorType) tuple. Used by the stream emission
// path on every StatItems update.
func itemBitsToLoadouts(items int) (weaponLoadout, powerupLoadout, string) {
	w := weaponLoadout{
		rl:  items&events.ITRocketLauncher != 0,
		lg:  items&events.ITLightning != 0,
		ssg: items&events.ITSuperShotgun != 0,
		sng: items&events.ITSuperNailgun != 0,
		gl:  items&events.ITGrenadeLauncher != 0,
	}
	p := powerupLoadout{
		quad: items&events.ITQuad != 0,
		pent: items&events.ITInvulnerability != 0,
		ring: items&events.ITInvisibility != 0,
	}
	armorType := ""
	switch {
	case items&events.ITArmor3 != 0:
		armorType = "ra"
	case items&events.ITArmor2 != 0:
		armorType = "ya"
	case items&events.ITArmor1 != 0:
		armorType = "ga"
	}
	return w, p, armorType
}
