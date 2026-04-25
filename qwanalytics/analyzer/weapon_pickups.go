package analyzer

import (
	"math"
	"sort"

	"github.com/mvd-analyzer/qwdemo/events"
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

	// backpackEnt → drop info, populated from BackpackDropHintEvent.
	// Used to attribute weapon and dropper on a BackpackPickupHintEvent.
	packInfo map[int]packDrop

	pickups []wpPickupRecord
	deaths  []wpDeathRecord

	timing MatchTimingDetector
}

// UseCoreOutputs is part of the CoreConsumer contract — WeaponPickups
// consumes co.FragEntries during its Finalize to attribute kills to
// each weapon pickup window.
func (a *WeaponPickupsAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

// playerName returns the best display name for a slot. Prefers the
// CoreOutputs slot table when populated; falls back to the live
// userinfo entry in ctx.Players. The fallback path keeps unit tests
// that wire up only ctx.Players (without seeding co.Slots) working.
func (a *WeaponPickupsAnalyzer) playerName(slot int) string {
	if name := a.core.SlotName(slot); name != "" {
		return name
	}
	if slot >= 0 && slot < len(a.ctx.Players) {
		if p := a.ctx.Players[slot]; p != nil {
			return p.Name
		}
	}
	return ""
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
	source      string // "world" | "backpack"
	hadBefore   bool
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

func NewWeaponPickupsAnalyzer() *WeaponPickupsAnalyzer {
	return &WeaponPickupsAnalyzer{
		playerItems: make(map[int]int),
		itemKind:    make(map[int]string),
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
	case *events.StatUpdateEvent:
		if e.StatIndex == events.StatItems {
			a.playerItems[e.PlayerNum] = e.Value
		}
	case *events.ItemSpawnEvent:
		if _, ok := weaponBit[e.Kind]; ok {
			a.itemKind[e.EntNum] = e.Kind
		}
	case *events.BackpackDropHintEvent:
		a.handleDropHint(e)
	case *events.ItemPickupHintEvent:
		a.handleItemPickup(e)
	case *events.BackpackPickupHintEvent:
		a.handlePackPickup(e)
	case *events.DeathEvent:
		if a.timing.Started && !a.timing.Ended {
			a.deaths = append(a.deaths, wpDeathRecord{time: e.Time, slot: e.PlayerNum})
		}
	}
	return nil
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
func (a *WeaponPickupsAnalyzer) Finalize() (interface{}, error) {
	if len(a.pickups) == 0 {
		return nil, nil
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
		k := pwKey{a.playerName(p.pickerSlot), p.weapon}
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
		for _, w := range windows {
			if w.start < f.Time && f.Time <= w.end {
				best = w.pickupIdx
			} else if w.start >= f.Time {
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

		entry := WeaponPickup{
			Time:          p.time,
			Player:        a.playerName(p.pickerSlot),
			Team:          picker.Team,
			Weapon:        p.weapon,
			Source:        p.source,
			HadBefore:     p.hadBefore,
			Kills:         kills[i],
			NextDeathTime: nextDeath,
		}
		if p.source == "backpack" {
			entry.BackpackEnt = p.backpackEnt
			entry.DropTime = p.dropTime
			if dropper := a.ctx.Players[p.dropperSlot]; dropper != nil {
				entry.Dropper = a.playerName(p.dropperSlot)
				entry.DropperTeam = dropper.Team
			}
		}
		out = append(out, entry)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
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
