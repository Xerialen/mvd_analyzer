package parser

import (
	"strconv"
	"strings"
)

// BackpackDropHintEvent is the typed representation of KTX's
// `//ktx drop <ent> <item_flags> <player_ent>` STUFFCMD_DEMOONLY
// directive (ktx/src/items.c:2740-2741). KTX only emits this for RL
// and LG drops — not for backpacks containing other weapons or just
// ammo — so absence of this event for a given backpack entity means
// the contents are unknown, not zero.
//
// The hint is always paired with a backpack entity spawn at the same
// origin; downstream consumers correlate by BackpackEnt.
type BackpackDropHintEvent struct {
	BackpackEnt int // server edict number of the spawned backpack
	ItemFlags   int // 32 = IT_ROCKET_LAUNCHER, 64 = IT_LIGHTNING
	PlayerEnt   int // dropper's edict (player_slot + 1)
	Time        float64
}

func (e *BackpackDropHintEvent) EventType() EventType { return EventBackpackDropHint }
func (e *BackpackDropHintEvent) EventTime() float64   { return e.Time }

const ktxDropPrefix = "//ktx drop "

// tryEmitBackpackDropHint scans a stuffcmd payload for `//ktx drop`
// and emits a typed BackpackDropHintEvent on success. Returns nil
// silently on malformed input — the StuffTextEvent for the same
// command has already been emitted by the caller, so dropping a
// hint event is a soft failure.
func (p *Parser) tryEmitBackpackDropHint(cmd string, time float64) error {
	s := strings.TrimRight(cmd, "\n\r ")
	if !strings.HasPrefix(s, ktxDropPrefix) {
		return nil
	}
	rest := s[len(ktxDropPrefix):]
	parts := strings.Fields(rest)
	if len(parts) < 3 {
		return nil
	}
	ent, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil
	}
	flags, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil
	}
	playerEnt, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil
	}
	return p.emit(&BackpackDropHintEvent{
		BackpackEnt: ent,
		ItemFlags:   flags,
		PlayerEnt:   playerEnt,
		Time:        time,
	})
}
