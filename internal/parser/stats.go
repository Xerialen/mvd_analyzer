package parser

import (
	"github.com/mvd-analyzer/internal/mvd"
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
	Attacker  int     // Attacker player number (entity - 1)
	Victim    int     // Victim player number (entity - 1)
	Damage    int     // Amount of damage dealt
	DeathType int     // Weapon/death type (DtRL, DtSG, etc.)
	IsSplash  bool    // True if splash damage
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

// parseUpdateStat parses svc_updatestat message (byte value)
func (p *Parser) parseUpdateStat(r *mvd.BufferReader, time float64, playerNum int) error {
	statIndex, err := r.ReadByte()
	if err != nil {
		return err
	}

	value, err := r.ReadByte()
	if err != nil {
		return err
	}

	return p.updateStat(playerNum, int(statIndex), int(value), time)
}

// parseUpdateStatLong parses svc_updatestatlong message (long value)
func (p *Parser) parseUpdateStatLong(r *mvd.BufferReader, time float64, playerNum int) error {
	statIndex, err := r.ReadByte()
	if err != nil {
		return err
	}

	value, err := r.ReadInt32()
	if err != nil {
		return err
	}

	return p.updateStat(playerNum, int(statIndex), int(value), time)
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
func (p *Parser) updateStat(playerNum, statIndex, value int, time float64) error {
	if playerNum >= 0 && playerNum < mvd.MaxClients {
		stats := p.playerStats[playerNum]

		switch statIndex {
		case mvd.StatHealth:
			stats.Health = value
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

	return p.emit(&StatUpdateEvent{
		PlayerNum: playerNum,
		StatIndex: statIndex,
		Value:     value,
		Time:      time,
	})
}
