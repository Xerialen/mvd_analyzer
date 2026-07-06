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
//     is delayed until rot completes; a matching
//     `//ktx timer` directive fires when rot finishes.
//   - line 541:  Armor (GA / YA / RA) — RespawnSec = 20.
//   - line 1048: Weapons (RL / LG / GL / SSG / SNG / NG) — RespawnSec
//     = weapon_time (typically 30, mode-dependent).
//   - lines 2074, 2083: Powerups (Quad / Pent / Ring) — RespawnSec
//     varies by mode (60 / 180 / 240 / 300).
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
	// ktxDirectivePrefix is the common opener for every KTX
	// STUFFCMD_DEMOONLY pickup/drop hint directive (//ktx took, //ktx bp,
	// //ktx drop, ...). tryEmitKtxHints checks it once before fanning out.
	ktxDirectivePrefix = "//ktx "
	ktxTookPrefix      = "//ktx took "
	ktxBpPrefix        = "//ktx bp "
)

// parseKtxHintInts matches a `//ktx <verb> ...` directive: it trims
// trailing whitespace, checks for prefix, splits the remainder on
// whitespace, and parses the first n fields as ints. Returns the parsed
// ints and true on success; false if the prefix doesn't match, there
// are fewer than n fields, or any field is not an integer. Malformed
// input is a soft failure (the StuffTextEvent for the same command is
// emitted regardless), so callers just skip emitting their typed hint.
func parseKtxHintInts(cmd, prefix string, n int) ([]int, bool) {
	s := strings.TrimRight(cmd, "\n\r ")
	if !strings.HasPrefix(s, prefix) {
		return nil, false
	}
	parts := strings.Fields(s[len(prefix):])
	if len(parts) < n {
		return nil, false
	}
	out := make([]int, n)
	for i := 0; i < n; i++ {
		v, err := strconv.Atoi(parts[i])
		if err != nil {
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

// tryEmitKtxHints fans a stufftext payload out to the KTX pickup/drop
// hint matchers, but only when it carries a `//ktx ` directive — the
// common case (weapon-stat tickers, download hints, fullserverinfo)
// short-circuits after one prefix check.
func (p *Parser) tryEmitKtxHints(cmd string, time float64) error {
	if !strings.HasPrefix(cmd, ktxDirectivePrefix) {
		return nil
	}
	if err := p.tryEmitBackpackDropHint(cmd, time); err != nil {
		return err
	}
	if err := p.tryEmitItemPickupHint(cmd, time); err != nil {
		return err
	}
	return p.tryEmitBackpackPickupHint(cmd, time)
}

// tryEmitItemPickupHint scans a stuffcmd payload for `//ktx took`
// and emits a typed ItemPickupHintEvent on success. Returns nil
// silently on malformed input — the StuffTextEvent for the same
// command has already been emitted by the caller, so dropping a
// hint event is a soft failure.
func (p *Parser) tryEmitItemPickupHint(cmd string, time float64) error {
	v, ok := parseKtxHintInts(cmd, ktxTookPrefix, 3)
	if !ok {
		return nil
	}
	return p.emit(&ItemPickupHintEvent{
		ItemEnt:    v[0],
		RespawnSec: v[1],
		PlayerEnt:  v[2],
		Time:       time,
	})
}

// tryEmitBackpackPickupHint scans a stuffcmd payload for `//ktx bp`
// and emits a typed BackpackPickupHintEvent on success. Silently
// drops malformed input for the same reason as above.
func (p *Parser) tryEmitBackpackPickupHint(cmd string, time float64) error {
	v, ok := parseKtxHintInts(cmd, ktxBpPrefix, 2)
	if !ok {
		return nil
	}
	return p.emit(&BackpackPickupHintEvent{
		BackpackEnt: v[0],
		PlayerEnt:   v[1],
		Time:        time,
	})
}
