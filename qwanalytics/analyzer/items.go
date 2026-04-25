package analyzer

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/mvd-analyzer/qwanalytics/loc"
	"github.com/mvd-analyzer/qwdemo/events"
)

// ItemAnalyzer builds the per-item pickup / respawn timeline by
// listening to ItemSpawnEvent / ItemStateEvent (entity-state visibility)
// and a layered set of attribution signals: KTX `//ktx took` hints,
// per-client `svc_print` pickup messages, and per-slot stat deltas
// (STAT_ITEMS bit transitions, ammo / health / armor jumps). Distance
// to the item entity is consulted only as a corroborator of last
// resort — gated by a touch-plausible radius — because in QuakeWorld
// the `findradius` / `touch` ordering for simultaneous touches is
// effectively random rather than nearest-wins.
//
// Signal layers in priority order:
//   1. ItemPickupHintEvent (`//ktx took`) keyed by entNum.
//   2. ItemPickupPrintEvent ("You got the X" / "You receive N health"),
//      authoritative when present but absent for any player whose
//      client config has msg >= 1.
//   3. Per-slot stat deltas, computed by diffing StatUpdateEvents
//      against a per-slot snapshot. Universal fallback.
//   4. Distance corroborator gated by maxDistanceSqAccept and a
//      position-recency window; restricted to L3 candidates if L3 was
//      ambiguous.
//
// A pickup with no in-radius candidate and no other evidence gets
// TakenBy="" and source="none" rather than a forced guess.
//
// Player display-name resolution is deferred to Finalize via
// CoreOutputs.SlotName, so the demoinfo-resolved display name is used
// rather than the eager userinfo name (mirrors WeaponPickupsAnalyzer).
type ItemAnalyzer struct {
	ctx       *Context
	co        *CoreOutputs
	items     map[int]*itemEntity // entNum -> tracked item
	playerPos map[int][3]float32  // slot -> last known origin
	playerPosTime map[int]float64 // slot -> time of last position update
	playerPosHist map[int][]posSample // slot -> recent position samples (for synthesis)
	mapName   string
	locFinder *loc.Finder
	timing    MatchTimingDetector

	// Per-slot stat snapshots used to produce delta-based evidence.
	// Each field has an "initialized" flag so the first update for a
	// stat (post-spawn / post-death) seeds the baseline silently.
	playerStats map[int]*playerStatSnapshot

	// Per-slot rolling buffers populated by signal handlers and
	// drained at attribution time. Old entries are pruned
	// opportunistically.
	pendingStatEvidence map[int][]statEvidence
	pendingPrints       map[int][]pendingPrint
	pendingHints        map[int]pendingHint // keyed by item entNum

	// MH holder tracking — drives the rot-end RespawnAt computation.
	// The MH respawn timer only starts 20 s after the holder's health
	// drops to <= 100 (rot tick-down or death), with a 5 s
	// minimum-hold floor enforced by KTX's `item_megahealth_rot`
	// (ktx/src/items.c:353).
	mhPickup     map[int]float64 // MH entNum -> pickup time
	heldMHs      map[int][]int   // slot -> MH entNums they currently hold
	playerHealth map[int]int     // slot -> last seen StatHealth value

	// Per-source attribution counters surfaced by the diagnostic harness.
	attrCounts map[string]int

	// Synthetic pickup chain — predicted next pickup per entity. Populated
	// at every Taken=true (real or synthetic) and consumed when the
	// predicted time has passed without a wire-level Taken=false. This
	// closes the insta-regrab gap: when an item respawns and is touched
	// again in the same server frame, the entity-state stream shows no
	// transition, but the predicted time + a player at the spawn point
	// + a matching stat delta is enough to infer the pickup.
	syntheticEnabled bool
	syntheticChain   map[int]*syntheticSchedule // entNum -> next predicted pickup
}

type syntheticSchedule struct {
	predicted float64
	chainLen  int
}

// posSample is one sample of a player's origin used by the synthesis
// pass to ask "was this player at the item's spawn at time T", which
// the latest-only `playerPos` map can't answer once T is in the past.
type posSample struct {
	origin [3]float32
	time   float64
}

type itemEntity struct {
	kind    string
	origin  [3]float32
	phases  []ItemPhase
	pickups []phaseAttribution // index aligned with phases
}

type phaseAttribution struct {
	slot   int
	source string // "hint" | "print" | "stat" | "distance" | "none"
}

type playerStatSnapshot struct {
	healthSet, armorSet                          bool
	shellsSet, nailsSet, rocketsSet, cellsSet    bool
	itemsSet                                      bool
	health, armor                                 int
	shells, nails, rockets, cells                 int
	items                                         int
}

type pendingPrint struct {
	kind string
	time float64
}

type pendingHint struct {
	playerSlot int
	time       float64
}

type statEvidence struct {
	time     float64
	kinds    []string
	consumed bool
}

const (
	// Hint→state correlation window. KTX emits //ktx took in the
	// same touch frame; ItemStateEvent.Taken=true arrives at the
	// next baseline-diff packet (~14 ms). Allow 250 ms for safety.
	hintMatchWindow = 0.250

	// Print→state correlation window. svc_print is server-immediate,
	// same window as the hint.
	printMatchWindow = 0.250

	// Stat-delta correlation windows. Stat updates arrive at ~3 Hz
	// per player so they can lag the touch by up to ~330 ms; allow
	// generous forward window. Backward is small because pickups
	// don't trigger stat updates ahead of the touch instant.
	statForwardWindow  = 0.500
	statBackwardWindow = 0.100

	// Position recency — drop slots from distance consideration whose
	// last position update is older than this.
	positionRecencyWindow = 0.250

	// Distance gate. KTX touch radius is ~40 u once item bbox is
	// included; allow 256 u (squared) as the upper bound for accepting
	// a distance-only attribution. Anything farther is implausible
	// for a real pickup.
	maxDistanceSqAccept = float32(256 * 256)

	// Cap on how long pending evidence/print/hint entries are kept.
	// Anything older is pruned at attribution time so the buffers
	// don't grow unbounded across a 30-minute match.
	maxBufferAge = 1.0

	// Synthesis settling window. Wait this long after the predicted
	// respawn time before deciding to synthesize, so any stat update
	// that lags the touch instant has a chance to land.
	syntheticSettleWindow = 0.5

	// Cap chain length per entity to defend against runaway prediction
	// when wire-level termination signals are missing entirely.
	// Long-running matches with constant timing on the same item rarely
	// chain more than 30-40 in a row.
	syntheticMaxChain = 60
)

// Standard Quake 1 / KTX / ktpro respawn times in seconds, keyed by the
// compact item kind strings emitted by the parser
// (qwdemo/parser/entities.go). MH is 20 only as a fallback — the rot
// phase that precedes it is handled separately via holder health
// tracking.
var kindRespawnSec = map[string]float64{
	"rl":      30,
	"lg":      30,
	"ssg":     30,
	"sng":     30,
	"ng":      30,
	"gl":      30,
	"ra":      20,
	"ya":      20,
	"ga":      20,
	"h25":     20,
	"h15":     20,
	"shells":  30,
	"nails":   30,
	"rockets": 30,
	"cells":   30,
	"quad":    60,
	"suit":    60,
	"pent":    300,
	"ring":    300,
}

func NewItemAnalyzer() *ItemAnalyzer {
	return &ItemAnalyzer{
		items:               make(map[int]*itemEntity),
		playerPos:           make(map[int][3]float32),
		playerPosTime:       make(map[int]float64),
		playerPosHist:       make(map[int][]posSample),
		playerStats:         make(map[int]*playerStatSnapshot),
		pendingStatEvidence: make(map[int][]statEvidence),
		pendingPrints:       make(map[int][]pendingPrint),
		pendingHints:        make(map[int]pendingHint),
		mhPickup:            make(map[int]float64),
		heldMHs:             make(map[int][]int),
		playerHealth:        make(map[int]int),
		attrCounts:          make(map[string]int),
		syntheticEnabled:    true,
		syntheticChain:      make(map[int]*syntheticSchedule),
	}
}

// SetSyntheticPickups toggles the insta-regrab synthesis pass. Default
// is on; tests that want to compare against the wire-only baseline
// (e.g. golden-corpus parity with the prior behaviour) can disable it.
func (a *ItemAnalyzer) SetSyntheticPickups(enabled bool) { a.syntheticEnabled = enabled }

func (a *ItemAnalyzer) Name() string { return "items" }

func (a *ItemAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

// UseCoreOutputs is part of the CoreConsumer contract — items.go
// resolves picker display names via co.SlotName during Finalize, so
// demoinfo-overridden names land in the output instead of the eager
// userinfo name.
func (a *ItemAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.co = co }

func (a *ItemAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.PrintEvent:
		a.timing.OnPrint(e)
	case *events.IntermissionEvent:
		a.timing.OnIntermission(e.Time)
	case *events.StuffTextEvent:
		if strings.HasPrefix(e.Command, "fullserverinfo ") {
			a.extractMapName(e.Command)
		}
	case *events.PlayerPositionEvent:
		o := [3]float32{e.Origin[0], e.Origin[1], e.Origin[2]}
		a.playerPos[e.PlayerNum] = o
		a.playerPosTime[e.PlayerNum] = e.Time
		a.recordPositionSample(e.PlayerNum, o, e.Time)
	case *events.ItemSpawnEvent:
		a.handleItemSpawn(e)
	case *events.ItemStateEvent:
		a.handleItemState(e)
	case *events.StatUpdateEvent:
		a.handleStatUpdate(e)
	case *events.DeathEvent:
		a.handleDeath(e)
	case *events.SpawnEvent:
		a.handleSpawn(e)
	case *events.ItemPickupHintEvent:
		a.handleItemPickupHint(e)
	case *events.ItemPickupPrintEvent:
		a.handleItemPickupPrint(e)
	}
	a.processSyntheticRespawns(event.EventTime())
	return nil
}

func (a *ItemAnalyzer) extractMapName(cmd string) {
	rest := strings.TrimPrefix(cmd, "fullserverinfo ")
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, "\"")
	if i := strings.LastIndexByte(rest, '"'); i >= 0 {
		rest = rest[:i]
	}
	parts := strings.Split(rest, "\\")
	start := 0
	if len(parts) > 0 && parts[0] == "" {
		start = 1
	}
	for i := start; i+1 < len(parts); i += 2 {
		if parts[i] == "map" {
			a.mapName = parts[i+1]
			return
		}
	}
}

// handleItemSpawn records the item's identity and opens the initial
// available phase. Fires once per entity (or again if a baseline is
// resent mid-match, which is rare).
func (a *ItemAnalyzer) handleItemSpawn(e *events.ItemSpawnEvent) {
	// Backpacks are handled by BackpackAnalyzer (backpacks.go), which
	// emits one entry per RL/LG drop from KTX's //ktx drop hint. Skip
	// them here so the per-item phase model stays clean — the
	// entity-state stream for backpack edicts is noisy and not used
	// for tracking today.
	if e.Kind == "" || e.Kind == "backpack" {
		return
	}
	it := a.items[e.EntNum]
	if it == nil {
		it = &itemEntity{
			kind:    e.Kind,
			origin:  e.Origin,
			phases:  []ItemPhase{{AvailableFrom: 0}},
			pickups: []phaseAttribution{{slot: -1}},
		}
		a.items[e.EntNum] = it
		return
	}
	// Update position / kind if the baseline changed. Don't touch
	// phases — the existing timeline is authoritative.
	it.kind = e.Kind
	it.origin = e.Origin
}

// handleItemState closes or opens a phase for a tracked item.
//
// Taken=true → close the current available phase with TakenAt, attribute
// the picker via the layered signal pipeline, and stamp RespawnAt from
// the kind→seconds table. MH is the exception: it uses holder-health
// tracking (handleStatUpdate / handleDeath) to compute the real 20 s
// countdown that only begins after rot ends, so RespawnAt stays at 0
// here and the UI renders that as "pending".
//
// Taken=false → respawn: open a new available phase. We don't stamp
// RespawnAt from the wire time — the wire respawn can slip by a full
// cycle on insta-regrabs (see qwdemo/MVD_FORMAT.md's "insta-regrab
// invisibility" note).
func (a *ItemAnalyzer) handleItemState(e *events.ItemStateEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	it := a.items[e.EntNum]
	if it == nil {
		return
	}
	if len(it.phases) == 0 {
		it.phases = []ItemPhase{{AvailableFrom: 0}}
		it.pickups = []phaseAttribution{{slot: -1}}
	}
	last := &it.phases[len(it.phases)-1]

	if e.Taken {
		// Close the current available phase. If the last phase was
		// already closed (bug or duplicate state event), skip.
		if last.TakenAt > 0 {
			return
		}
		last.TakenAt = e.Time
		slot, source := a.attributeWithLayeredSignals(e.EntNum, it.kind, it.origin, e.Time)
		it.pickups[len(it.pickups)-1] = phaseAttribution{slot: slot, source: source}
		a.attrCounts[source]++

		if it.kind == "mh" {
			// Start holder tracking; RespawnAt stays 0 until the
			// holder's health drops to <= 100.
			a.mhPickup[e.EntNum] = e.Time
			if slot >= 0 {
				a.heldMHs[slot] = append(a.heldMHs[slot], e.EntNum)
			}
			return
		}
		if sec, ok := kindRespawnSec[it.kind]; ok {
			last.RespawnAt = e.Time + sec
			a.scheduleSyntheticRespawn(e.EntNum, e.Time+sec, 0)
		}
		return
	}

	// Wire respawn: open the next available phase. Cancel any pending
	// synthetic schedule for this entity — the wire just told us
	// nobody picked it up at the predicted moment.
	it.phases = append(it.phases, ItemPhase{AvailableFrom: e.Time})
	it.pickups = append(it.pickups, phaseAttribution{slot: -1})
	delete(a.syntheticChain, e.EntNum)
}

// recordPositionSample appends one positional sample for synthesis use
// and prunes anything older than the synthesis window cap. Cheap; the
// per-slot history rarely exceeds ~80 entries given the ~73 Hz sample
// rate and a 1 s prune horizon.
func (a *ItemAnalyzer) recordPositionSample(slot int, origin [3]float32, t float64) {
	hist := a.playerPosHist[slot]
	hist = append(hist, posSample{origin: origin, time: t})
	cutoff := t - 1.0
	keepFrom := 0
	for keepFrom < len(hist) && hist[keepFrom].time < cutoff {
		keepFrom++
	}
	if keepFrom > 0 {
		hist = hist[keepFrom:]
	}
	a.playerPosHist[slot] = hist
}

// positionAt returns the slot's position closest to time t (preferring
// the latest sample at or before t). Returns ok=false if no sample is
// within statForwardWindow on either side of t — meaning we don't have
// recent enough position data to assess proximity.
func (a *ItemAnalyzer) positionAt(slot int, t float64) ([3]float32, bool) {
	hist := a.playerPosHist[slot]
	if len(hist) == 0 {
		return [3]float32{}, false
	}
	bestIdx := -1
	bestDelta := 1e18
	for i := range hist {
		dt := hist[i].time - t
		if dt < 0 {
			dt = -dt
		}
		if dt < bestDelta {
			bestDelta = dt
			bestIdx = i
		}
	}
	if bestIdx < 0 || bestDelta > statForwardWindow {
		return [3]float32{}, false
	}
	return hist[bestIdx].origin, true
}

// scheduleSyntheticRespawn registers an expectation that entity ent
// will be picked up at time `predicted`. If the wire confirms a real
// transition before then, the schedule is cleared in handleItemState.
// Otherwise processSyntheticRespawns will try to synthesize a pickup
// once the predicted moment plus settle window has passed.
func (a *ItemAnalyzer) scheduleSyntheticRespawn(ent int, predicted float64, chainLen int) {
	if !a.syntheticEnabled {
		return
	}
	if chainLen >= syntheticMaxChain {
		delete(a.syntheticChain, ent)
		return
	}
	a.syntheticChain[ent] = &syntheticSchedule{predicted: predicted, chainLen: chainLen}
}

// processSyntheticRespawns walks the schedule and synthesizes a pickup
// for any entity whose predicted respawn passed at least
// syntheticSettleWindow ago. The settle window lets stat-update events
// that lag the touch instant land before we make the call.
func (a *ItemAnalyzer) processSyntheticRespawns(currentT float64) {
	if !a.syntheticEnabled || !a.timing.Started || a.timing.Ended {
		return
	}
	for _, ent := range sortedKeys(a.syntheticChain) {
		sched := a.syntheticChain[ent]
		if sched == nil {
			continue
		}
		if currentT < sched.predicted+syntheticSettleWindow {
			continue
		}
		it := a.items[ent]
		if it == nil {
			delete(a.syntheticChain, ent)
			continue
		}
		// MH chain skipped for now — the rot logic makes the next
		// predicted pickup time depend on holder health and is best
		// handled inside the existing rot pass rather than synthesis.
		if it.kind == "mh" {
			delete(a.syntheticChain, ent)
			continue
		}
		slot, ok := a.findSyntheticPicker(it.kind, it.origin, sched.predicted)
		if !ok {
			delete(a.syntheticChain, ent)
			continue
		}
		a.recordSyntheticPickup(ent, sched.predicted, slot, sched.chainLen+1)
	}
}

// findSyntheticPicker returns a unique slot whose stat evidence and
// position support a pickup of the given kind at time predicted.
// Stat-evidence match is required (the universal "the player's stats
// ticked up consistent with this kind" signal); position is checked as
// a sanity guard against false positives.
func (a *ItemAnalyzer) findSyntheticPicker(kind string, origin [3]float32, predicted float64) (int, bool) {
	type cand struct {
		slot     int
		evIdx    int
	}
	var candidates []cand
	for _, slot := range sortedKeys(a.pendingStatEvidence) {
		evs := a.pendingStatEvidence[slot]
		for i := range evs {
			if evs[i].consumed {
				continue
			}
			if !containsKind(evs[i].kinds, kind) {
				continue
			}
			if evs[i].time < predicted-statBackwardWindow || evs[i].time > predicted+statForwardWindow {
				continue
			}
			pos, ok := a.positionAt(slot, predicted)
			if !ok {
				continue
			}
			dx := pos[0] - origin[0]
			dy := pos[1] - origin[1]
			dz := pos[2] - origin[2]
			if dx*dx+dy*dy+dz*dz > maxDistanceSqAccept {
				continue
			}
			candidates = append(candidates, cand{slot: slot, evIdx: i})
			break
		}
	}
	if len(candidates) != 1 {
		return -1, false
	}
	c := candidates[0]
	a.pendingStatEvidence[c.slot][c.evIdx].consumed = true
	return c.slot, true
}

// recordSyntheticPickup mirrors what handleItemState does on a wire
// Taken=true: closes the implicitly-just-respawned available phase
// and stamps the next predicted respawn. The phase model still
// alternates available -> taken; we append both transitions at the
// same time (predicted), since the synthesis assumption is "respawn
// and pickup happen in the same server tick".
func (a *ItemAnalyzer) recordSyntheticPickup(ent int, t float64, slot int, chainLen int) {
	it := a.items[ent]
	if it == nil {
		return
	}
	it.phases = append(it.phases, ItemPhase{AvailableFrom: t, TakenAt: t})
	it.pickups = append(it.pickups, phaseAttribution{slot: slot, source: "synthetic"})
	last := &it.phases[len(it.phases)-1]
	if sec, ok := kindRespawnSec[it.kind]; ok {
		last.RespawnAt = t + sec
		a.scheduleSyntheticRespawn(ent, t+sec, chainLen)
	} else {
		delete(a.syntheticChain, ent)
	}
	a.attrCounts["synthetic"]++
}

// attributeWithLayeredSignals walks the four signal layers in priority
// order and returns the first hit. Returns (-1, "none") if no signal
// produces a candidate inside its window / radius.
func (a *ItemAnalyzer) attributeWithLayeredSignals(entNum int, kind string, itemPos [3]float32, t float64) (int, string) {
	a.pruneBuffers(t)

	// Layer 1: KTX `//ktx took` hint, keyed by entNum.
	if h, ok := a.pendingHints[entNum]; ok && absDelta(h.time, t) <= hintMatchWindow {
		delete(a.pendingHints, entNum)
		return h.playerSlot, "hint"
	}

	// Layer 2: per-client svc_print pickup message. Iterate slots in
	// sorted order so a tie returns deterministically.
	type printCandidate struct {
		slot     int
		entryIdx int
	}
	var prints []printCandidate
	for _, slot := range sortedKeys(a.pendingPrints) {
		entries := a.pendingPrints[slot]
		for i, entry := range entries {
			if entry.kind == kind && absDelta(entry.time, t) <= printMatchWindow {
				prints = append(prints, printCandidate{slot: slot, entryIdx: i})
				break
			}
		}
	}
	if len(prints) == 1 {
		c := prints[0]
		entries := a.pendingPrints[c.slot]
		a.pendingPrints[c.slot] = append(entries[:c.entryIdx], entries[c.entryIdx+1:]...)
		return c.slot, "print"
	}

	// Layer 3: stat-delta evidence.
	type statCandidate struct {
		slot       int
		evidenceIx int
	}
	var stats []statCandidate
	for _, slot := range sortedKeys(a.pendingStatEvidence) {
		evs := a.pendingStatEvidence[slot]
		for i := range evs {
			if evs[i].consumed {
				continue
			}
			if evs[i].time < t-statBackwardWindow || evs[i].time > t+statForwardWindow {
				continue
			}
			if !containsKind(evs[i].kinds, kind) {
				continue
			}
			stats = append(stats, statCandidate{slot: slot, evidenceIx: i})
			break
		}
	}
	if len(stats) == 1 {
		c := stats[0]
		a.pendingStatEvidence[c.slot][c.evidenceIx].consumed = true
		return c.slot, "stat"
	}

	// Layer 4: distance corroborator. If L3 produced multiple
	// candidates, restrict distance to those slots only — the contest
	// is real and at least we know who was actually picking up.
	var restrictTo map[int]bool
	if len(stats) > 1 {
		restrictTo = make(map[int]bool, len(stats))
		for _, c := range stats {
			restrictTo[c.slot] = true
		}
	}
	if slot := a.distanceBest(itemPos, restrictTo, t); slot >= 0 {
		// If we restricted to L3 candidates, mark the chosen one's
		// evidence consumed so a later attribution doesn't re-pick it.
		if restrictTo != nil {
			for _, c := range stats {
				if c.slot == slot {
					a.pendingStatEvidence[c.slot][c.evidenceIx].consumed = true
					break
				}
			}
		}
		return slot, "distance"
	}

	return -1, "none"
}

// distanceBest returns the slot with the smallest squared distance to
// itemPos, gated by maxDistanceSqAccept and the position recency
// window. If restrictTo is non-nil, only those slots are considered.
// Returns -1 when no candidate satisfies the gate.
func (a *ItemAnalyzer) distanceBest(itemPos [3]float32, restrictTo map[int]bool, t float64) int {
	bestSlot := -1
	bestDistSq := float32(1e18)
	slots := make([]int, 0, len(a.playerPos))
	for slot := range a.playerPos {
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	for _, slot := range slots {
		if restrictTo != nil && !restrictTo[slot] {
			continue
		}
		// Drop stale positions — a slot whose last update is older
		// than positionRecencyWindow is not considered.
		if posT, ok := a.playerPosTime[slot]; ok {
			if t-posT > positionRecencyWindow {
				continue
			}
		}
		pos := a.playerPos[slot]
		dx := pos[0] - itemPos[0]
		dy := pos[1] - itemPos[1]
		dz := pos[2] - itemPos[2]
		d := dx*dx + dy*dy + dz*dz
		if d < bestDistSq {
			bestDistSq = d
			bestSlot = slot
		}
	}
	if bestSlot < 0 {
		return -1
	}
	if bestDistSq > maxDistanceSqAccept {
		return -1
	}
	if bestSlot >= len(a.ctx.Players) || a.ctx.Players[bestSlot] == nil {
		return -1
	}
	return bestSlot
}

// handleItemPickupHint buffers a KTX `//ktx took` directive keyed by
// the picked item's entNum. Layer 1 of the attribution pipeline reads
// from this buffer at ItemStateEvent time.
func (a *ItemAnalyzer) handleItemPickupHint(e *events.ItemPickupHintEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	slot := e.PlayerEnt - 1
	if slot < 0 || slot >= len(a.ctx.Players) {
		return
	}
	a.pendingHints[e.ItemEnt] = pendingHint{playerSlot: slot, time: e.Time}
}

// handleItemPickupPrint buffers a per-client `svc_print` pickup
// message ("You got the X" / "You receive N health"). Only present for
// players whose client config has msg=0; competitive players commonly
// suppress these so this signal is partial in practice.
func (a *ItemAnalyzer) handleItemPickupPrint(e *events.ItemPickupPrintEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	a.pendingPrints[e.PlayerNum] = append(a.pendingPrints[e.PlayerNum], pendingPrint{
		kind: e.Kind,
		time: e.Time,
	})
}

// handleStatUpdate is the universal observation hook for per-slot
// stat changes. It performs three jobs:
//   - Diff the incoming value against the per-slot snapshot to
//     emit structured stat-delta evidence rows that Layer 3 of the
//     attribution pipeline reads.
//   - Maintain MH holder-health tracking so the rot-end RespawnAt
//     can be stamped at the >100→<=100 crossing.
//   - Mirror IT_SUPERHEALTH bit clearing as a backup rot-end signal.
func (a *ItemAnalyzer) handleStatUpdate(e *events.StatUpdateEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}

	a.classifyStatDelta(e)

	switch e.StatIndex {
	case events.StatHealth:
		// Mirror TimelineAnalyzer's sentinel filter (ktx/src/combat.c:1001
		// sets health = 1000 + damage as a damage-indicator hint; real
		// player health caps at 250). Treat sentinels as "no update" so
		// they don't mask the real rot-end transition.
		if e.Value > 250 {
			return
		}
		prev := a.playerHealth[e.PlayerNum]
		a.playerHealth[e.PlayerNum] = e.Value
		if prev > 100 && e.Value <= 100 {
			a.stampHeldMHs(e.PlayerNum, e.Time)
		}
	case events.StatItems:
		if e.Value&events.ITSuperHealth != 0 {
			return
		}
		// Player's IT_SUPERHEALTH bit just cleared. KTX clears it from
		// inside item_megahealth_rot at rot-end (items.c:401), so this
		// is redundant with the health crossing above in the normal
		// case but catches the path where the health stream is thin.
		a.stampHeldMHs(e.PlayerNum, e.Time)
	}
}

// classifyStatDelta diffs the incoming stat value against the per-slot
// snapshot, appends a structured statEvidence row when the change
// matches a known pickup pattern, and updates the snapshot to the new
// value. Each stat field carries an "initialized" flag so the first
// update post-spawn / post-death seeds the baseline silently.
func (a *ItemAnalyzer) classifyStatDelta(e *events.StatUpdateEvent) {
	snap := a.playerStats[e.PlayerNum]
	if snap == nil {
		snap = &playerStatSnapshot{}
		a.playerStats[e.PlayerNum] = snap
	}

	switch e.StatIndex {
	case events.StatHealth:
		if e.Value > 250 {
			return
		}
		if !snap.healthSet {
			snap.health, snap.healthSet = e.Value, true
			return
		}
		delta := e.Value - snap.health
		snap.health = e.Value
		// MH evidence is emitted on the IT_SUPERHEALTH bit transition
		// in StatItems; small healths emit on +15 / +25 deltas.
		switch delta {
		case 15:
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"h15"})
		case 25:
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"h25"})
		}
	case events.StatArmor:
		if !snap.armorSet {
			snap.armor, snap.armorSet = e.Value, true
			return
		}
		snap.armor = e.Value
		// Armor kind is determined by the IT_ARMOR1/2/3 bit transition
		// in StatItems below — armor magnitude alone is ambiguous (a
		// YA over GA increases armor and flips the bit).
	case events.StatShells:
		a.pushAmmoEvidence(e, &snap.shells, &snap.shellsSet, "shells")
	case events.StatNails:
		a.pushAmmoEvidence(e, &snap.nails, &snap.nailsSet, "nails")
	case events.StatRockets:
		a.pushAmmoEvidence(e, &snap.rockets, &snap.rocketsSet, "rockets")
	case events.StatCells:
		a.pushAmmoEvidence(e, &snap.cells, &snap.cellsSet, "cells")
	case events.StatItems:
		if !snap.itemsSet {
			snap.items, snap.itemsSet = e.Value, true
			return
		}
		prev := snap.items
		snap.items = e.Value
		newlySet := e.Value & ^prev
		// Armor — mutually exclusive bits. Whichever was newly set
		// identifies the kind.
		if newlySet&events.ITArmor1 != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"ga"})
		}
		if newlySet&events.ITArmor2 != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"ya"})
		}
		if newlySet&events.ITArmor3 != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"ra"})
		}
		// Weapons.
		if newlySet&events.ITSuperShotgun != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"ssg"})
		}
		if newlySet&events.ITNailgun != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"ng"})
		}
		if newlySet&events.ITSuperNailgun != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"sng"})
		}
		if newlySet&events.ITGrenadeLauncher != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"gl"})
		}
		if newlySet&events.ITRocketLauncher != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"rl"})
		}
		if newlySet&events.ITLightning != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"lg"})
		}
		// Powerups.
		if newlySet&events.ITQuad != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"quad"})
		}
		if newlySet&events.ITInvulnerability != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"pent"})
		}
		if newlySet&events.ITInvisibility != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"ring"})
		}
		if newlySet&events.ITSuit != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"suit"})
		}
		// Megahealth — IT_SUPERHEALTH transition is the canonical
		// pickup signal (the +100 health is correlated but not
		// uniquely identifying).
		if newlySet&events.ITSuperHealth != 0 {
			a.pushStatEvidence(e.PlayerNum, e.Time, []string{"mh"})
		}
	}
}

// pushAmmoEvidence emits "any positive delta" evidence for an ammo
// stat. Box magnitudes vary (loadout cap rounding, backpacks pre-empting
// the box) so we don't gate on a specific size — the kind=K filter at
// attribution time handles disambiguation.
func (a *ItemAnalyzer) pushAmmoEvidence(e *events.StatUpdateEvent, field *int, set *bool, kind string) {
	if !*set {
		*field, *set = e.Value, true
		return
	}
	delta := e.Value - *field
	*field = e.Value
	if delta > 0 {
		a.pushStatEvidence(e.PlayerNum, e.Time, []string{kind})
	}
}

// pushStatEvidence appends a stat-delta evidence row to a slot's
// pending buffer.
func (a *ItemAnalyzer) pushStatEvidence(slot int, time float64, kinds []string) {
	a.pendingStatEvidence[slot] = append(a.pendingStatEvidence[slot], statEvidence{
		time:  time,
		kinds: kinds,
	})
}

// pruneBuffers drops entries older than maxBufferAge from the pending
// buffers so they don't grow unbounded across long matches.
func (a *ItemAnalyzer) pruneBuffers(t float64) {
	cutoff := t - maxBufferAge
	for slot, entries := range a.pendingPrints {
		kept := entries[:0]
		for _, e := range entries {
			if e.time >= cutoff {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(a.pendingPrints, slot)
		} else {
			a.pendingPrints[slot] = kept
		}
	}
	for slot, entries := range a.pendingStatEvidence {
		kept := entries[:0]
		for _, ev := range entries {
			if ev.time >= cutoff && !ev.consumed {
				kept = append(kept, ev)
			}
		}
		if len(kept) == 0 {
			delete(a.pendingStatEvidence, slot)
		} else {
			a.pendingStatEvidence[slot] = kept
		}
	}
	for entNum, h := range a.pendingHints {
		if h.time < cutoff {
			delete(a.pendingHints, entNum)
		}
	}
}

// handleSpawn resets the slot's stat snapshot and pending evidence so
// the post-spawn loadout (which arrives as a burst of stat updates)
// doesn't masquerade as pickup deltas. The first stat update for each
// field re-seeds the baseline silently.
func (a *ItemAnalyzer) handleSpawn(e *events.SpawnEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	delete(a.playerStats, e.PlayerNum)
	delete(a.pendingStatEvidence, e.PlayerNum)
}

// handleDeath is the backup path for the "holder died" case. DeathEvent
// is derived from the same StatHealth transition that would already
// trigger stampHeldMHs via handleStatUpdate, but subscribing to both
// is cheap insurance against event-ordering quirks. Also clears any
// stat snapshot / pending evidence so the upcoming respawn loadout
// doesn't feed the classifier.
func (a *ItemAnalyzer) handleDeath(e *events.DeathEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	a.playerHealth[e.PlayerNum] = 0
	a.stampHeldMHs(e.PlayerNum, e.Time)
	delete(a.playerStats, e.PlayerNum)
	delete(a.pendingStatEvidence, e.PlayerNum)
}

// stampHeldMHs closes out every MH phase currently owned by the given
// slot by stamping RespawnAt = max(pickup + 5, crossing) + 20.
// Idempotent — calling it twice for the same slot has no effect the
// second time because heldMHs[slot] is cleared.
func (a *ItemAnalyzer) stampHeldMHs(slot int, crossing float64) {
	ents := a.heldMHs[slot]
	if len(ents) == 0 {
		return
	}
	for _, ent := range ents {
		it := a.items[ent]
		if it == nil || len(it.phases) == 0 {
			continue
		}
		last := &it.phases[len(it.phases)-1]
		if last.TakenAt == 0 || last.RespawnAt != 0 {
			continue
		}
		pickup := a.mhPickup[ent]
		rotEnd := crossing
		if pickup+5 > rotEnd {
			rotEnd = pickup + 5
		}
		last.RespawnAt = rotEnd + 20
		delete(a.mhPickup, ent)
	}
	delete(a.heldMHs, slot)
}

// AttributionCounts returns the per-source attribution tally
// (hint / print / stat / distance / none). Used by the diagnostic
// harness to monitor signal coverage across the corpus. The map is
// safe to read after Finalize.
func (a *ItemAnalyzer) AttributionCounts() map[string]int {
	out := make(map[string]int, len(a.attrCounts))
	for k, v := range a.attrCounts {
		out[k] = v
	}
	return out
}

// Finalize builds the ItemsResult. Item names are kind-scoped
// ("ra", "mh_1", "mh_2", ...) and ordered deterministically by world
// position. Loc labels are attached best-effort from the .loc corpus
// — absent loc file yields empty Loc strings; the item list itself
// is always populated when the demo has any item events.
//
// Picker display names are resolved here via co.SlotName so the
// demoinfo-overridden display name lands instead of the eager userinfo
// name (mirrors WeaponPickupsAnalyzer's pattern). Team is read from
// ctx.Players[slot] — the userinfo team is what every other analyser
// reports.
func (a *ItemAnalyzer) Finalize(result *Result) error {
	if len(a.items) == 0 {
		return nil
	}

	// Best-effort loc lookup — does NOT affect whether items appear.
	if a.locFinder == nil && a.mapName != "" {
		if f, err := loc.LoadForMap(a.mapName); err == nil {
			a.locFinder = f
		}
	}

	type entry struct {
		entNum int
		it     *itemEntity
	}
	byKind := map[string][]entry{}
	for ent, it := range a.items {
		byKind[it.kind] = append(byKind[it.kind], entry{entNum: ent, it: it})
	}

	out := make([]ItemTimeline, 0, len(a.items))
	for kind, list := range byKind {
		sort.Slice(list, func(i, j int) bool {
			a, b := list[i].it, list[j].it
			if a.origin[0] != b.origin[0] {
				return a.origin[0] < b.origin[0]
			}
			if a.origin[1] != b.origin[1] {
				return a.origin[1] < b.origin[1]
			}
			if a.origin[2] != b.origin[2] {
				return a.origin[2] < b.origin[2]
			}
			// Identical origins → break ties by entNum so the
			// `_1`/`_2` suffixing stays stable across runs.
			return list[i].entNum < list[j].entNum
		})
		for i, e := range list {
			name := kind
			if len(list) > 1 {
				name = fmt.Sprintf("%s_%d", kind, i+1)
			}
			locName := ""
			if a.locFinder != nil {
				locName = a.locFinder.FindNearest(e.it.origin[0], e.it.origin[1], e.it.origin[2])
			}
			a.resolveAttributions(e.it)
			out = append(out, ItemTimeline{
				Name:   name,
				Kind:   kind,
				EntNum: e.entNum,
				X:      e.it.origin[0],
				Y:      e.it.origin[1],
				Z:      e.it.origin[2],
				Loc:    locName,
				Phases: e.it.phases,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})

	result.Items = &ItemsResult{Items: out}
	return nil
}

// resolveAttributions writes TakenBy / Team into each closed phase
// from the slot recorded at OnEvent time. Display name is read from
// CoreOutputs.SlotName when available so demoinfo-resolved names land
// in the output; falls back to ctx.Players[slot].Name (userinfo) when
// the core outputs aren't wired (unit tests that don't seed co).
func (a *ItemAnalyzer) resolveAttributions(it *itemEntity) {
	for i := range it.phases {
		if i >= len(it.pickups) {
			break
		}
		pa := it.pickups[i]
		if pa.slot < 0 {
			continue
		}
		if it.phases[i].TakenAt == 0 {
			continue
		}
		name := a.co.SlotName(pa.slot)
		if name == "" {
			if pa.slot < len(a.ctx.Players) && a.ctx.Players[pa.slot] != nil {
				name = a.ctx.Players[pa.slot].Name
			}
		}
		team := ""
		if pa.slot < len(a.ctx.Players) && a.ctx.Players[pa.slot] != nil {
			team = a.ctx.Players[pa.slot].Team
		}
		it.phases[i].TakenBy = name
		it.phases[i].Team = team
	}
}

// --- helpers ---

func absDelta(a, b float64) float64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

func containsKind(kinds []string, k string) bool {
	return slices.Contains(kinds, k)
}

// sortedKeys returns the integer keys of map m in ascending order.
// Generic over any value type so the same helper handles
// pendingPrints / pendingStatEvidence iteration with deterministic
// candidate enumeration.
func sortedKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
