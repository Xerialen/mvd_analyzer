package parser

import (
	"strconv"
	"strings"
)

// ItemPickupHintEvent is the typed representation of KTX's
// `//ktx took <ent> <respawn_sec> <player_ent>` STUFFCMD_DEMOONLY
// directive. KTX emits it on every competitive pickup (ktx/src/items.c):
//
//   - line 355:  Megahealth — RespawnSec is 0 because the 20 s timer
//                is delayed until rot completes; a matching
//                `//ktx timer` directive fires when rot finishes.
//   - line 541:  Armor (GA / YA / RA) — RespawnSec = 20.
//   - line 1048: Weapons (RL / LG / GL / SSG / SNG / NG) — RespawnSec
//                = weapon_time (typically 30, mode-dependent).
//   - lines 2074, 2083: Powerups (Quad / Pent / Ring) — RespawnSec
//                varies by mode (60 / 180 / 240 / 300).
//
// Small healths (15 / 25) do NOT emit this hint — they're not
// respawning items in the KTX scheme. Backpacks use a separate
// `//ktx bp` hint (see BackpackPickupHintEvent).
//
// This is the authoritative pickup-attribution signal for KTX demos.
// Unlike the entity-state stream (where the picking player can only
// be inferred via nearest-origin heuristics) ItemEnt + PlayerEnt
// pin the touch to concrete edicts.
type ItemPickupHintEvent struct {
	ItemEnt    int // server edict of the picked-up item
	RespawnSec int // nominal respawn timer in seconds; 0 for MH until rot
	PlayerEnt  int // picking player's edict (slot + 1; edict 0 is world)
	Time       float64
}

func (e *ItemPickupHintEvent) EventType() EventType { return EventItemPickupHint }
func (e *ItemPickupHintEvent) EventTime() float64   { return e.Time }

// BackpackPickupHintEvent is the typed representation of KTX's
// `//ktx bp <backpack_ent> <player_ent>` STUFFCMD_DEMOONLY directive
// (ktx/src/items.c:2471). It fires only when the picked backpack
// contains IT_ROCKET_LAUNCHER or IT_LIGHTNING — the same domain as
// BackpackDropHintEvent (the drop side) — so the pair is symmetric.
//
// For backpack pickup attribution this is the only reliable signal:
// backpack edicts exhibit entity-state visibility flutter on the
// wire that makes contest detection unreliable. See
// PICKUP-SIGNALS-INVESTIGATION.md at the repo root for the
// protocol-level analysis.
type BackpackPickupHintEvent struct {
	BackpackEnt int // server edict of the picked-up backpack
	PlayerEnt   int // picking player's edict (slot + 1)
	Time        float64
}

func (e *BackpackPickupHintEvent) EventType() EventType { return EventBackpackPickupHint }
func (e *BackpackPickupHintEvent) EventTime() float64   { return e.Time }

const (
	ktxTookPrefix = "//ktx took "
	ktxBpPrefix   = "//ktx bp "
)

// tryEmitItemPickupHint scans a stuffcmd payload for `//ktx took`
// and emits a typed ItemPickupHintEvent on success. Returns nil
// silently on malformed input — the StuffTextEvent for the same
// command has already been emitted by the caller, so dropping a
// hint event is a soft failure.
func (p *Parser) tryEmitItemPickupHint(cmd string, time float64) error {
	s := strings.TrimRight(cmd, "\n\r ")
	if !strings.HasPrefix(s, ktxTookPrefix) {
		return nil
	}
	parts := strings.Fields(s[len(ktxTookPrefix):])
	if len(parts) < 3 {
		return nil
	}
	ent, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil
	}
	respawn, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil
	}
	playerEnt, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil
	}
	return p.emit(&ItemPickupHintEvent{
		ItemEnt:    ent,
		RespawnSec: respawn,
		PlayerEnt:  playerEnt,
		Time:       time,
	})
}

// tryEmitBackpackPickupHint scans a stuffcmd payload for `//ktx bp`
// and emits a typed BackpackPickupHintEvent on success. Silently
// drops malformed input for the same reason as above.
func (p *Parser) tryEmitBackpackPickupHint(cmd string, time float64) error {
	s := strings.TrimRight(cmd, "\n\r ")
	if !strings.HasPrefix(s, ktxBpPrefix) {
		return nil
	}
	parts := strings.Fields(s[len(ktxBpPrefix):])
	if len(parts) < 2 {
		return nil
	}
	bpEnt, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil
	}
	playerEnt, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil
	}
	return p.emit(&BackpackPickupHintEvent{
		BackpackEnt: bpEnt,
		PlayerEnt:   playerEnt,
		Time:        time,
	})
}
