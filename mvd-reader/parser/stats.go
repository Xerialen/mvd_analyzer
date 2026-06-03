package parser

import (
	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// StatUpdateEvent is emitted when a player stat is updated
type StatUpdateEvent struct {
	PlayerNum int
	StatIndex int
	Value     int
	Time      float64
}

func (e *StatUpdateEvent) EventType() EventType { return EventStatUpdate }
func (e *StatUpdateEvent) EventTime() float64   { return e.Time }

// FragUpdateEvent is emitted when a player's frag count changes
type FragUpdateEvent struct {
	PlayerNum int
	Frags     int
	Time      float64
}

func (e *FragUpdateEvent) EventType() EventType { return EventFragUpdate }
func (e *FragUpdateEvent) EventTime() float64   { return e.Time }

// DamageEvent is emitted when damage is dealt (from hidden messages)
type DamageEvent struct {
	Attacker  int  // Attacker player number (entity - 1); -1 for world / non-player inflictor (lava, fall, trigger, ...)
	Victim    int  // Victim player number (entity - 1)
	Damage    int  // Amount of damage dealt
	DeathType int  // Weapon/death type (DtRL, DtSG, etc.)
	IsSplash  bool // True if splash damage
	Time      float64
}

func (e *DamageEvent) EventType() EventType { return EventDamage }
func (e *DamageEvent) EventTime() float64   { return e.Time }

// DemoInfoEvent is emitted when embedded JSON stats are found
type DemoInfoEvent struct {
	BlockNum int    // Block number for multi-block JSON
	Content  []byte // JSON content (may be partial)
	Time     float64
}

func (e *DemoInfoEvent) EventType() EventType { return EventDemoInfo }
func (e *DemoInfoEvent) EventTime() float64   { return e.Time }

// DeathEvent is emitted when a player transitions from alive to dead.
// Two protocol-level signals feed this:
//   - StatHealth crossing >0 → ≤0 (this file). Reliable for the player
//     whose dem_stats block we're currently consuming; structurally
//     blind to deaths whose stat update lands in a block addressed to
//     a different player.
//   - The DF_DEAD bit in svc_playerinfo (position.go). Broadcast in
//     every frame for every player, so it catches the deaths the
//     stat-based detector misses.
//
// The two sources are deduplicated in maybeEmitDeath / maybeEmitSpawn,
// so consumers see exactly one event per state transition regardless of
// which signal fired first. Obituary parsing for killer / weapon
// attribution remains a separate concern in analytics.
//
// TimeMs is the canonical wire-native time in integer milliseconds. Use it
// for boundary comparisons (analyzer persistence layer); Time is the
// derived float64 seconds view.
type DeathEvent struct {
	PlayerNum int
	Time      float64
	TimeMs    int32
}

func (e *DeathEvent) EventType() EventType { return EventDeath }
func (e *DeathEvent) EventTime() float64   { return e.Time }

// SpawnEvent is emitted when a player transitions from dead to alive —
// either a respawn after death, or a first-spawn when a player joins
// active play (spectator / pre-connect → alive). Consumers treat both
// cases identically.
//
// Sources mirror DeathEvent: StatHealth crossing ≤0 → >0, and the
// DF_DEAD bit clearing in svc_playerinfo. Deduplicated via the
// maybeEmit* helpers.
//
// TimeMs is the canonical wire-native time in integer milliseconds.
type SpawnEvent struct {
	PlayerNum int
	Time      float64
	TimeMs    int32
}

func (e *SpawnEvent) EventType() EventType { return EventSpawn }
func (e *SpawnEvent) EventTime() float64   { return e.Time }

// parseUpdateStat parses svc_updatestat message (byte value)
func (p *Parser) parseUpdateStat(r *mvd.BufferReader, time float64, timeMs int32, playerNum int) error {
	statIndex, err := r.ReadByte()
	if err != nil {
		return err
	}

	value, err := r.ReadByte()
	if err != nil {
		return err
	}

	return p.updateStat(playerNum, int(statIndex), int(value), time, timeMs)
}

// parseUpdateStatLong parses svc_updatestatlong message (long value)
func (p *Parser) parseUpdateStatLong(r *mvd.BufferReader, time float64, timeMs int32, playerNum int) error {
	statIndex, err := r.ReadByte()
	if err != nil {
		return err
	}

	value, err := r.ReadInt32()
	if err != nil {
		return err
	}

	return p.updateStat(playerNum, int(statIndex), int(value), time, timeMs)
}

// parseUpdateFrags parses svc_updatefrags message
func (p *Parser) parseUpdateFrags(r *mvd.BufferReader, time float64) error {
	playerNum, err := r.ReadByte()
	if err != nil {
		return err
	}

	frags, err := r.ReadInt16()
	if err != nil {
		return err
	}

	// Bounds check
	if playerNum >= mvd.MaxClients {
		return nil // Ignore invalid player numbers
	}

	if p.players[playerNum] != nil {
		p.players[playerNum].Frags = int(frags)
	}

	return p.emit(&FragUpdateEvent{
		PlayerNum: int(playerNum),
		Frags:     int(frags),
		Time:      time,
	})
}

// updateStat updates player stats and emits event
func (p *Parser) updateStat(playerNum, statIndex, value int, time float64, timeMs int32) error {
	// Health-transition detection for DeathEvent / SpawnEvent — captured
	// from the pre-mutation value so the transition check below is driven
	// by the actual 100→-20 style edge, not the post-mutation state.
	healthOld, healthNew := 0, 0
	isHealthUpdate := false

	if playerNum >= 0 && playerNum < mvd.MaxClients {
		stats := p.playerStats[playerNum]

		switch statIndex {
		case mvd.StatHealth:
			healthOld = stats.Health
			stats.Health = value
			healthNew = value
			isHealthUpdate = true
		case mvd.StatArmor:
			stats.Armor = value
		case mvd.StatShells:
			stats.Shells = value
		case mvd.StatNails:
			stats.Nails = value
		case mvd.StatRockets:
			stats.Rockets = value
		case mvd.StatCells:
			stats.Cells = value
		case mvd.StatActiveWeapon:
			stats.ActiveWeapon = value
		case mvd.StatItems:
			stats.Items = value
		case mvd.StatFrags:
			if p.players[playerNum] != nil {
				p.players[playerNum].Frags = value
			}
		}
	}

	if err := p.emit(&StatUpdateEvent{
		PlayerNum: playerNum,
		StatIndex: statIndex,
		Value:     value,
		Time:      time,
	}); err != nil {
		return err
	}

	// DeathEvent / SpawnEvent are emitted AFTER the StatUpdateEvent so
	// analyzer state that snapshots from vitals at sample time sees the
	// post-damage health. The parser owns this signal so downstream
	// analytics never need to compare health across sampling boundaries.
	// Routed through maybeEmit* so the DF_DEAD detector in position.go
	// can fire for the same transition without producing a duplicate.
	if isHealthUpdate {
		if healthOld > 0 && healthNew <= 0 {
			return p.maybeEmitDeath(playerNum, time, timeMs)
		}
		if healthOld <= 0 && healthNew > 0 {
			return p.maybeEmitSpawn(playerNum, time, timeMs)
		}
	}
	return nil
}

// maybeEmitDeath emits a DeathEvent for the given player only if their
// last-known dead/alive state is "alive" or unknown. Deduplicates across
// the two transition sources (StatHealth edges, DF_DEAD bit in
// svc_playerinfo) so consumers see one event per real transition.
func (p *Parser) maybeEmitDeath(playerNum int, time float64, timeMs int32) error {
	if playerNum < 0 || playerNum >= mvd.MaxClients {
		return nil
	}
	if p.playerDeadKnown[playerNum] && p.playerDead[playerNum] {
		return nil
	}
	p.playerDeadKnown[playerNum] = true
	p.playerDead[playerNum] = true
	return p.emit(&DeathEvent{PlayerNum: playerNum, Time: time, TimeMs: timeMs})
}

// maybeEmitSpawn mirrors maybeEmitDeath for the alive transition.
func (p *Parser) maybeEmitSpawn(playerNum int, time float64, timeMs int32) error {
	if playerNum < 0 || playerNum >= mvd.MaxClients {
		return nil
	}
	if p.playerDeadKnown[playerNum] && !p.playerDead[playerNum] {
		return nil
	}
	p.playerDeadKnown[playerNum] = true
	p.playerDead[playerNum] = false
	return p.emit(&SpawnEvent{PlayerNum: playerNum, Time: time, TimeMs: timeMs})
}

// forceEmitDeath emits a DeathEvent unconditionally and updates the
// per-player dead-state cursor — bypassing the
// "skip-if-already-dead" check that maybeEmitDeath enforces for the
// STAT_HEALTH and DF_DEAD sources. The obituary path needs this
// because KTX can broadcast an obit whose corresponding entity-state
// transition never reaches the wire:
//
//   - Tight respawn cycles where the player dies and respawns and dies
//     again entirely between two MVD sample frames — DF_DEAD never
//     appears clear between the two deaths but each kill still emits
//     an obit.
//   - The pent-deflection corner case (KTX dtTELE2): when a "mortal"
//     tries to telefrag a Satan-pent player, KTX prints "Satan's
//     power deflects X's telefrag" and decrements X's frag count
//     (ktx/src/client.c:5141-5149). KTX's authoritative deathcount
//     scoreboard counts this as a death, but DF_DEAD may not flip
//     because the player was already in a dead state from a prior
//     real death the wire still represents as one continuous "dead"
//     interval.
//
// In both cases the stat-based detector and the DF_DEAD detector
// (correctly) see no transition, and only the obit knows a death
// happened. Bypass dedup so the death is recorded. The naturally-
// following SpawnEvent (next svc_playerinfo with DF_DEAD clear)
// arrives via the normal maybeEmitSpawn path; if no respawn ever
// becomes observable on the wire (the deflection case), no
// SpawnEvent fires and the death sits unpaired — that's a faithful
// reflection of what KTX's own scoreboard reports.
func (p *Parser) forceEmitDeath(playerNum int, time float64, timeMs int32) error {
	if playerNum < 0 || playerNum >= mvd.MaxClients {
		return nil
	}
	p.playerDeadKnown[playerNum] = true
	p.playerDead[playerNum] = true
	return p.emit(&DeathEvent{PlayerNum: playerNum, Time: time, TimeMs: timeMs})
}
