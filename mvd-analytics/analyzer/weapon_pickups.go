package analyzer

import (
	"math"
	"sort"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// WeaponPickupsAnalyzer records every slot-weapon acquisition and
// attaches an effectiveness metric — frags the picker scored with the
// weapon before their next death.
//
// Signal coverage:
//   - World-spawner pickups come from ItemPickupHintEvent (the KTX
//     //ktx took directive). ItemSpawnEvent gives us the entNum→Kind
//     map we need to classify the pickup. Only the six slot weapons
//     (rl, lg, gl, ssg, sng, ng) are recorded; armor / health /
//     powerup hints are ignored.
//   - Backpack pickups come from BackpackPickupHintEvent (the KTX
//     //ktx bp directive), paired with BackpackDropHintEvent for the
//     weapon attribution and the dropper's identity. Only RL and LG
//     packs get these hints, so SSG/NG/SNG/GL-only packs do not
//     appear here.
//   - In weapon-stay modes (deathmatch 2/3/5, coop — KTX weapon_touch's
//     `leave` flag, ktx/src/items.c:835) touched weapons keep their
//     model and the //ktx took hint is never emitted, so the two
//     signals above are blind for world weapon grabs. There the
//     analyzer synthesizes pickups from STAT_ITEMS weapon-bit 0→1
//     transitions instead, classifying source by proximity to a
//     weapon spawn entity ("world") or, failing that, "unknown"
//     (typically a non-RL/LG backpack grant, which has no hint of its
//     own). Synthesized entries carry Inferred=true.
//
// hadBefore is computed from the STAT_ITEMS bitfield maintained per
// slot: a pickup where the bit is already set is a redundant grab
// (most commonly a teammate-denial pickup). Kills counting ignores
// the distinction — if the picker frags with the weapon between
// pickup and next death, the entry gets the credit whether the weapon
// was fresh or already in inventory.
//
// Kills attribution uses ctx.FragEntries (populated by FragAnalyzer
// during Finalize), so this analyzer MUST be registered after
// FragAnalyzer in the default registry.
type WeaponPickupsAnalyzer struct {
	ctx  *Context
	core *CoreOutputs

	// Per-slot current STAT_ITEMS bitfield. Indexed by slot, not
	// edict. Maintained in real time; a lookup at pickup-event time
	// gives the pre-pickup state because the server sends the
	// STAT_ITEMS update on the next packet after the //ktx hint.
	playerItems map[int]int

	// entNum → item Kind string, populated from ItemSpawnEvent. Used
	// to classify ItemPickupHintEvents (world pickups).
	itemKind map[int]string

	// entNum → world origin for weapon spawn entities, populated
	// alongside itemKind. Used by the weapon-stay synthesis to decide
	// whether a STAT_ITEMS flip happened on a weapon pad.
	itemOrigin map[int][3]float32

	// backpackEnt → drop info, populated from BackpackDropHintEvent.
	// Used to attribute weapon and dropper on a BackpackPickupHintEvent.
	packInfo map[int]packDrop

	pickups []wpPickupRecord
	deaths  []wpDeathRecord

	// Weapon-stay synthesis state. flips keeps its own STAT_ITEMS
	// baseline separate from playerItems (which must track plainly for
	// hadBefore) — see weaponFlipTracker for the boundary rules.
	flips weaponFlipTracker
	pos   posTracker

	weaponStay weaponStayDetector
	timing     MatchTimingDetector
}

// UseCoreOutputs is part of the CoreConsumer contract — WeaponPickups
// consumes co.FragEntries during its Finalize to attribute kills to
// each weapon pickup window.
func (a *WeaponPickupsAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

// identityAt returns the resolved name+team that held a slot at time
// tMs (integer ms). Prefers the reconnect-aware CoreOutputs session
// table so a player's pre-reconnect pickups/drops aren't relabelled with
// whoever later took their slot; falls back to the live userinfo entry
// in ctx.Players (which keeps unit tests that only wire up ctx.Players
// working).
func (a *WeaponPickupsAnalyzer) identityAt(slot int, tMs int32) SlotInfo {
	id := a.core.SlotIdentityAt(slot, tMs)
	if id.Name == "" || id.Team == "" {
		if slot >= 0 && slot < len(a.ctx.Players) {
			if p := a.ctx.Players[slot]; p != nil {
				if id.Name == "" {
					id.Name = p.Name
				}
				if id.Team == "" {
					id.Team = p.Team
				}
			}
		}
	}
	return id
}

type packDrop struct {
	weapon      string // "rl" or "lg"
	dropperSlot int
	dropTime    float64
}

type wpPickupRecord struct {
	time        float64
	pickerSlot  int
	weapon      string
	source      string // "world" | "backpack" | "unknown"
	hadBefore   bool
	inferred    bool    // synthesized from a STAT_ITEMS flip (weapon-stay), no KTX hint
	backpackEnt int     // 0 for world pickups
	dropperSlot int     // -1 for world pickups
	dropTime    float64 // 0 for world pickups
}

type wpDeathRecord struct {
	time float64
	slot int
}

// Bit masks from qwdemo/mvd/types.go reproduced here for local
// readability; the events package re-exports the same constants.
const (
	wpItShotgun         = 1 << 0 // SG — starting weapon, not tracked
	wpItSuperShotgun    = 1 << 1 // SSG
	wpItNailgun         = 1 << 2 // NG
	wpItSuperNailgun    = 1 << 3 // SNG
	wpItGrenadeLauncher = 1 << 4 // GL
	wpItRocketLauncher  = 1 << 5 // RL
	wpItLightning       = 1 << 6 // LG
)

// weaponBit maps a pickup Weapon code to the STAT_ITEMS bit that
// indicates the player already holds that weapon.
var weaponBit = map[string]int{
	"ssg": wpItSuperShotgun,
	"ng":  wpItNailgun,
	"sng": wpItSuperNailgun,
	"gl":  wpItGrenadeLauncher,
	"rl":  wpItRocketLauncher,
	"lg":  wpItLightning,
}

// weaponKindsOrdered fixes the iteration order over weaponBit so a
// single STAT_ITEMS update granting several weapons at once emits
// records deterministically.
var weaponKindsOrdered = []string{"ssg", "ng", "sng", "gl", "rl", "lg"}

func NewWeaponPickupsAnalyzer() *WeaponPickupsAnalyzer {
	return &WeaponPickupsAnalyzer{
		playerItems: make(map[int]int),
		itemKind:    make(map[int]string),
		itemOrigin:  make(map[int][3]float32),
		packInfo:    make(map[int]packDrop),
	}
}

func (a *WeaponPickupsAnalyzer) Name() string { return "weaponPickups" }

func (a *WeaponPickupsAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

func (a *WeaponPickupsAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.PrintEvent:
		a.timing.OnPrint(e)
	case *events.IntermissionEvent:
		a.timing.OnIntermission(e.Time)
	case *events.StuffTextEvent:
		a.weaponStay.OnStuffText(e)
	case *events.ServerInfoEvent:
		a.weaponStay.OnServerInfo(e)
	case *events.StatUpdateEvent:
		if e.StatIndex == events.StatItems {
			// Synthesis reads the pre-update synthItems baseline, so it
			// must run before playerItems/synthItems absorb the value.
			a.maybeSynthesizeFromItemsFlip(e)
			a.playerItems[e.PlayerNum] = e.Value
		}
	case *events.PlayerPositionEvent:
		if a.weaponStay.WeaponStay() {
			a.pos.Record(e.PlayerNum, e.Origin, e.Time)
		}
	case *events.ItemSpawnEvent:
		if _, ok := weaponBit[e.Kind]; ok {
			a.itemKind[e.EntNum] = e.Kind
			a.itemOrigin[e.EntNum] = e.Origin
		}
	case *events.BackpackDropHintEvent:
		a.handleDropHint(e)
	case *events.ItemPickupHintEvent:
		a.handleItemPickup(e)
	case *events.BackpackPickupHintEvent:
		a.handlePackPickup(e)
	case *events.SpawnEvent:
		a.flips.OnSpawn(e.PlayerNum, e.Time)
	case *events.DeathEvent:
		a.flips.OnDeath(e.PlayerNum, e.Time)
		if a.timing.Started && !a.timing.Ended {
			a.deaths = append(a.deaths, wpDeathRecord{time: e.Time, slot: e.PlayerNum})
		}
	}
	return nil
}

// maybeSynthesizeFromItemsFlip recovers world weapon pickups in
// weapon-stay modes, where KTX never emits //ktx took for weapons and
// the weapon entity never leaves the wire (ktx/src/items.c:1046-1052).
// Every grant in those modes is a STAT_ITEMS weapon-bit 0→1 transition
// (re-touch while holding the bit is blocked, items.c:844), so the flip
// is a complete record of who gained which weapon when. Baseline and
// boundary rules (warmup, death, spawn loadout) live in
// weaponFlipTracker; only the recording is match-gated here.
func (a *WeaponPickupsAnalyzer) maybeSynthesizeFromItemsFlip(e *events.StatUpdateEvent) {
	if !a.weaponStay.WeaponStay() || a.timing.Ended {
		return
	}
	slot := e.PlayerNum
	if slot < 0 || slot >= len(a.ctx.Players) || a.ctx.Players[slot] == nil {
		return
	}
	kinds := a.flips.Observe(slot, e.Value, e.Time)
	if !a.timing.Started {
		return
	}
	for _, kind := range kinds {
		// A hint-driven record (//ktx bp, or //ktx took if weapon-stay
		// was somehow mis-detected) precedes the stat flip on the wire —
		// if one already explains this grant, don't double-record it.
		if a.recentRecordExplains(slot, kind, e.Time) {
			continue
		}
		a.pickups = append(a.pickups, wpPickupRecord{
			time:        e.Time,
			pickerSlot:  slot,
			weapon:      kind,
			source:      a.classifyFlipSource(slot, kind, e.Time),
			hadBefore:   false, // by construction: the bit was 0
			inferred:    true,
			dropperSlot: -1,
		})
	}
}

// classifyFlipSource decides where a synthesized grant came from:
// "world" when the picker passed within touch range of a same-kind
// weapon spawn during the stat-lag window, else "unknown" rather than
// a forced guess. A hintless (non-RL/LG) pack grabbed while standing
// on a matching pad classifies as "world" — genuinely wire-ambiguous:
// KTX emits no hint for those packs and the backpack entity-state
// stream flutters too hard to serve as a discriminator (hundreds of
// spurious Taken transitions per resting pack; see the visibility-
// flutter note in mvd-reader/MVD_FORMAT.md). KTX `taken` parity is
// preserved either way; only the world-vs-pack split can drift.
func (a *WeaponPickupsAnalyzer) classifyFlipSource(slot int, kind string, t float64) string {
	if _, ok := a.nearestWeaponEntityDistSq(slot, kind, t); ok {
		return "world"
	}
	return "unknown"
}

// recentRecordExplains reports whether a pickup record for (slot, kind)
// already exists within statForwardWindow of t. Records are appended in
// event order, so scanning back from the tail terminates at the window
// edge.
func (a *WeaponPickupsAnalyzer) recentRecordExplains(slot int, kind string, t float64) bool {
	for i := len(a.pickups) - 1; i >= 0; i-- {
		rec := &a.pickups[i]
		if t-rec.time > statForwardWindow {
			return false
		}
		if rec.pickerSlot == slot && rec.weapon == kind {
			return true
		}
	}
	return false
}

// nearestWeaponEntityDistSq returns the smallest squared distance from
// the slot's position history to any spawn entity of the given weapon
// kind during the stat lag window [t-statForwardWindow, t], gated by
// touchGateSq. The window (not the flip instant) matters:
// per-player stat updates can lag the touch by a few hundred ms,
// during which the picker keeps moving.
func (a *WeaponPickupsAnalyzer) nearestWeaponEntityDistSq(slot int, kind string, t float64) (float32, bool) {
	best := float32(0)
	found := false
	for ent, k := range a.itemKind {
		if k != kind {
			continue
		}
		origin, ok := a.itemOrigin[ent]
		if !ok {
			continue
		}
		if d, ok := a.pos.MinDistSqIn(slot, t-statForwardWindow, t, origin); ok && d <= touchGateSq && (!found || d < best) {
			best = d
			found = true
		}
	}
	return best, found
}

func (a *WeaponPickupsAnalyzer) handleDropHint(e *events.BackpackDropHintEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	weapon := weaponFromItemFlags(e.ItemFlags)
	if weapon == "" {
		return
	}
	slot := e.PlayerEnt - 1
	a.packInfo[e.BackpackEnt] = packDrop{
		weapon:      weapon,
		dropperSlot: slot,
		dropTime:    e.Time,
	}
}

func (a *WeaponPickupsAnalyzer) handleItemPickup(e *events.ItemPickupHintEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	kind, ok := a.itemKind[e.ItemEnt]
	if !ok {
		return // not a weapon (armor / health / powerup / unknown)
	}
	slot := e.PlayerEnt - 1
	if slot < 0 || slot >= len(a.ctx.Players) || a.ctx.Players[slot] == nil {
		return
	}
	// If the weapon-stay synthesis already recorded this grant from a
	// STAT_ITEMS flip that beat the hint onto the wire, upgrade that
	// record in place — the hint is the authoritative source. hadBefore
	// stays false: the flip proved the bit was fresh (the post-flip
	// playerItems would misread it as a redundant grab).
	if rec := a.inferredRecordFor(slot, kind, e.Time); rec != nil {
		rec.source = "world"
		rec.inferred = false
		return
	}
	bit := weaponBit[kind]
	hadBefore := a.playerItems[slot]&bit != 0
	a.pickups = append(a.pickups, wpPickupRecord{
		time:        e.Time,
		pickerSlot:  slot,
		weapon:      kind,
		source:      "world",
		hadBefore:   hadBefore,
		dropperSlot: -1,
	})
}

// inferredRecordFor returns the most recent synthesized record for
// (slot, kind) within statForwardWindow of t, or nil. Used by the hint
// handlers to upgrade a flip-derived record in place when its hint
// arrives late instead of appending a duplicate.
func (a *WeaponPickupsAnalyzer) inferredRecordFor(slot int, kind string, t float64) *wpPickupRecord {
	for i := len(a.pickups) - 1; i >= 0; i-- {
		rec := &a.pickups[i]
		if t-rec.time > statForwardWindow {
			return nil
		}
		if rec.inferred && rec.pickerSlot == slot && rec.weapon == kind {
			return rec
		}
	}
	return nil
}

func (a *WeaponPickupsAnalyzer) handlePackPickup(e *events.BackpackPickupHintEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	drop, ok := a.packInfo[e.BackpackEnt]
	if !ok {
		return // pack's drop hint wasn't seen (ent recycle, warmup, etc.)
	}
	slot := e.PlayerEnt - 1
	if slot < 0 || slot >= len(a.ctx.Players) || a.ctx.Players[slot] == nil {
		return
	}
	// Late-hint insurance, mirroring handleItemPickup: if the flip's
	// synthesized record landed first, rewrite it as the backpack
	// pickup this hint proves it was.
	if rec := a.inferredRecordFor(slot, drop.weapon, e.Time); rec != nil {
		rec.source = "backpack"
		rec.inferred = false
		rec.backpackEnt = e.BackpackEnt
		rec.dropperSlot = drop.dropperSlot
		rec.dropTime = drop.dropTime
		delete(a.packInfo, e.BackpackEnt)
		return
	}
	bit := weaponBit[drop.weapon]
	hadBefore := a.playerItems[slot]&bit != 0
	a.pickups = append(a.pickups, wpPickupRecord{
		time:        e.Time,
		pickerSlot:  slot,
		weapon:      drop.weapon,
		source:      "backpack",
		hadBefore:   hadBefore,
		backpackEnt: e.BackpackEnt,
		dropperSlot: drop.dropperSlot,
		dropTime:    drop.dropTime,
	})
	// The backpack is now consumed — clear the entry so a stale
	// entNum can't attribute a later pickup to the same drop.
	delete(a.packInfo, e.BackpackEnt)
}

// Finalize pairs every recorded pickup with its picker's next death
// and attributes kills from ctx.FragEntries. Attribution rules:
//
//  1. Only pickups that actually granted the weapon (HadBefore=false)
//     are eligible for kill credit. In QW weapons are cleared on
//     respawn, so within a single life each (player, weapon) pair has
//     at most one "granting" pickup; redundant grabs (hadBefore=true)
//     never gave the player anything new and must not claim kills
//     that would have happened anyway. They still appear in the
//     result with Kills=0 so the denial semantics stay visible.
//  2. Each frag is credited to the most-recent eligible pickup whose
//     window [pickupTime, nextDeath] contains the frag. This prevents
//     a granting pickup in an earlier life from absorbing kills made
//     after the player died and acquired the weapon again.
//
// FragEntries are name-keyed, so we resolve the picker's display name
// from ctx.Players[slot].Name (patched by the registry to the
// DemoInfo name post-Finalize of DemoInfoAnalyzer).
func (a *WeaponPickupsAnalyzer) Finalize(result *Result) error {
	if len(a.pickups) == 0 {
		return nil
	}

	// Partition deaths by slot, time-ordered, for next-death lookup.
	// Deaths are recorded in arrival order which is already monotonic
	// in time since OnEvent is serial.
	deathsBySlot := make(map[int][]float64)
	for _, d := range a.deaths {
		deathsBySlot[d.slot] = append(deathsBySlot[d.slot], d.time)
	}

	// Build pickup windows keyed by (killerName, weapon). Each window
	// is [pickup.time, nextDeath] (or +Inf if the player never dies
	// again this match). Windows per key are already time-ordered
	// because pickups were appended in event order.
	type pwKey struct{ killer, weapon string }
	type pickupWindow struct {
		pickupIdx int
		start     float64
		end       float64
	}
	windowsByPW := make(map[pwKey][]pickupWindow)
	for i, p := range a.pickups {
		if p.hadBefore {
			continue // redundant grab — not eligible for kill credit (rule 1)
		}
		if a.ctx.Players[p.pickerSlot] == nil {
			continue
		}
		end := findNextAfter(deathsBySlot[p.pickerSlot], p.time)
		if end == 0 {
			end = math.Inf(1)
		}
		k := pwKey{a.identityAt(p.pickerSlot, msTime(p.time)).Name, p.weapon}
		windowsByPW[k] = append(windowsByPW[k], pickupWindow{i, p.time, end})
	}

	// Attribute each valid frag to the latest covering window.
	kills := make([]int, len(a.pickups))
	var fragEntries []FragEntry
	if a.core != nil {
		fragEntries = a.core.FragEntries
	}
	for _, f := range fragEntries {
		if f.IsSuicide || f.IsTeamKill {
			continue
		}
		windows := windowsByPW[pwKey{f.Killer, f.Weapon}]
		best := -1
		// FragEntry.Time is int32 ms (schema v8); pickup window
		// start/end are still float64 seconds — convert per-frag.
		fTimeSec := float64(f.Time) * 0.001
		for _, w := range windows {
			if w.start < fTimeSec && fTimeSec <= w.end {
				best = w.pickupIdx
			} else if w.start >= fTimeSec {
				break // windows are time-ordered; further starts are all in the future
			}
		}
		if best >= 0 {
			kills[best]++
		}
	}

	out := make([]WeaponPickup, 0, len(a.pickups))
	for i, p := range a.pickups {
		picker := a.ctx.Players[p.pickerSlot]
		if picker == nil {
			continue
		}
		nextDeath := findNextAfter(deathsBySlot[p.pickerSlot], p.time)

		pickerID := a.identityAt(p.pickerSlot, msTime(p.time))
		entry := WeaponPickup{
			Time:          msTime(p.time),
			Player:        pickerID.Name,
			Team:          pickerID.Team,
			Weapon:        p.weapon,
			Source:        p.source,
			HadBefore:     p.hadBefore,
			Inferred:      p.inferred,
			Kills:         kills[i],
			NextDeathTime: msTime(nextDeath),
		}
		if p.source == "backpack" {
			entry.BackpackEnt = p.backpackEnt
			entry.DropTime = msTime(p.dropTime)
			if dropper := a.ctx.Players[p.dropperSlot]; dropper != nil {
				dropperID := a.identityAt(p.dropperSlot, msTime(p.dropTime))
				entry.Dropper = dropperID.Name
				entry.DropperTeam = dropperID.Team
			}
		}
		out = append(out, entry)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	result.WeaponPickups = out
	return nil
}

// findNextAfter returns the smallest value in the (already-sorted)
// slice strictly greater than t. Returns 0 if none — callers treat 0
// as "no death before match end" and count kills up to the end of
// the frag list.
func findNextAfter(sorted []float64, t float64) float64 {
	for _, v := range sorted {
		if v > t {
			return v
		}
	}
	return 0
}
