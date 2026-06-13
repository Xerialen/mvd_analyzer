package parser

import (
	"encoding/binary"
	"testing"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// buildPlayerInfoPayload encodes the body of a svc_playerinfo message
// (everything after the command byte) for a given player slot and flag
// word. Origin/Angle bits are stripped so only the flag word and the
// frame byte are present in the encoded payload — enough for the
// DF_DEAD path to fire without dragging in coord encoding.
func buildPlayerInfoPayload(playerNum byte, flags uint16) []byte {
	out := []byte{playerNum}
	out = binary.LittleEndian.AppendUint16(out, flags)
	out = append(out, 0) // frame byte
	return out
}

func buildPlayerInfoPayloadFull(playerNum byte, flags uint16, origin [3]int16, angles [3]int16) []byte {
	out := buildPlayerInfoPayload(playerNum, flags)
	for i := 0; i < 3; i++ {
		if flags&(mvd.DFOrigin<<i) != 0 {
			out = binary.LittleEndian.AppendUint16(out, uint16(origin[i]))
		}
	}
	for i := 0; i < 3; i++ {
		if flags&(mvd.DFAngles<<i) != 0 {
			out = binary.LittleEndian.AppendUint16(out, uint16(angles[i]))
		}
	}
	return out
}

func TestParsePlayerInfo_CarriesForwardDeltaCompressedAngles(t *testing.T) {
	p := NewParser(nil)
	var positions []*PlayerPositionEvent
	p.OnEvent(func(e Event) error {
		if pos, ok := e.(*PlayerPositionEvent); ok {
			positions = append(positions, pos)
		}
		return nil
	})

	baseFlags := uint16(mvd.DFOrigin | mvd.DFOriginY | mvd.DFOriginZ | mvd.DFAngles | mvd.DFAnglesY)
	r := mvd.NewBufferReader(buildPlayerInfoPayloadFull(1, baseFlags, [3]int16{8, 16, 24}, [3]int16{1000, -2000, 0}))
	if err := p.parsePlayerInfo(r, 1.0, 1000, false); err != nil {
		t.Fatalf("parsePlayerInfo initial: %v", err)
	}

	r = mvd.NewBufferReader(buildPlayerInfoPayloadFull(1, 0, [3]int16{}, [3]int16{}))
	if err := p.parsePlayerInfo(r, 1.016, 1016, false); err != nil {
		t.Fatalf("parsePlayerInfo omitted angles: %v", err)
	}

	r = mvd.NewBufferReader(buildPlayerInfoPayloadFull(1, mvd.DFAnglesY, [3]int16{}, [3]int16{0, -1900, 0}))
	if err := p.parsePlayerInfo(r, 1.029, 1029, false); err != nil {
		t.Fatalf("parsePlayerInfo partial angle update: %v", err)
	}

	if len(positions) != 3 {
		t.Fatalf("position events = %d, want 3", len(positions))
	}
	if got := positions[1].Angles; got != [3]int16{1000, -2000, 0} {
		t.Errorf("omitted angle flags produced %v, want carried-forward [1000 -2000 0]", got)
	}
	if got := positions[2].Angles; got != [3]int16{1000, -1900, 0} {
		t.Errorf("partial angle update produced %v, want pitch carry + yaw update [1000 -1900 0]", got)
	}
}

// First svc_playerinfo for a slot, alive, must synthesise a SpawnEvent
// so analytics has a starting boundary for the player.
func TestParsePlayerInfo_FirstSeenAliveFiresSpawn(t *testing.T) {
	p := NewParser(nil)
	var spawns, deaths int
	p.OnEvent(func(e Event) error {
		switch e.(type) {
		case *SpawnEvent:
			spawns++
		case *DeathEvent:
			deaths++
		}
		return nil
	})

	r := mvd.NewBufferReader(buildPlayerInfoPayload(3, 0)) // no DF_DEAD
	if err := p.parsePlayerInfo(r, 1.0, 1000, false); err != nil {
		t.Fatalf("parsePlayerInfo: %v", err)
	}
	if spawns != 1 || deaths != 0 {
		t.Errorf("spawns=%d deaths=%d, want 1 spawn 0 deaths", spawns, deaths)
	}
}

// First svc_playerinfo for a slot already marked dead must not emit a
// DeathEvent — we have no prior alive state to transition from. State
// is still recorded so the next alive frame fires a SpawnEvent.
func TestParsePlayerInfo_FirstSeenDeadDoesNotFireDeath(t *testing.T) {
	p := NewParser(nil)
	var spawns, deaths int
	p.OnEvent(func(e Event) error {
		switch e.(type) {
		case *SpawnEvent:
			spawns++
		case *DeathEvent:
			deaths++
		}
		return nil
	})

	r := mvd.NewBufferReader(buildPlayerInfoPayload(3, mvd.DFDead))
	if err := p.parsePlayerInfo(r, 1.0, 1000, false); err != nil {
		t.Fatalf("parsePlayerInfo (dead): %v", err)
	}
	if spawns != 0 || deaths != 0 {
		t.Errorf("first-seen-dead: spawns=%d deaths=%d, want 0/0", spawns, deaths)
	}

	// Subsequent alive sample must produce a SpawnEvent against the
	// pre-seeded dead state.
	r = mvd.NewBufferReader(buildPlayerInfoPayload(3, 0))
	if err := p.parsePlayerInfo(r, 2.0, 2000, false); err != nil {
		t.Fatalf("parsePlayerInfo (alive): %v", err)
	}
	if spawns != 1 || deaths != 0 {
		t.Errorf("dead→alive: spawns=%d deaths=%d, want 1/0", spawns, deaths)
	}
}

// Alive→dead and dead→alive transitions in svc_playerinfo must produce
// one DeathEvent / one SpawnEvent respectively.
func TestParsePlayerInfo_TransitionsFireDeathAndSpawn(t *testing.T) {
	p := NewParser(nil)
	var events []Event
	p.OnEvent(func(e Event) error {
		events = append(events, e)
		return nil
	})

	// First sample alive → SpawnEvent.
	for _, frame := range []struct {
		flags uint16
		ms    int32
	}{
		{0, 1000},                      // alive
		{0, 1100},                      // alive (no transition)
		{mvd.DFDead, 1200},             // alive → dead
		{mvd.DFDead | mvd.DFGIB, 1250}, // still dead (no second event)
		{0, 1300},                      // dead → alive
	} {
		r := mvd.NewBufferReader(buildPlayerInfoPayload(2, frame.flags))
		if err := p.parsePlayerInfo(r, float64(frame.ms)/1000.0, frame.ms, false); err != nil {
			t.Fatalf("parsePlayerInfo ms=%d: %v", frame.ms, err)
		}
	}

	var deaths, spawns int
	for _, e := range events {
		switch e.(type) {
		case *DeathEvent:
			deaths++
		case *SpawnEvent:
			spawns++
		}
	}
	if deaths != 1 || spawns != 2 {
		t.Errorf("deaths=%d spawns=%d, want 1 death (alive→dead), 2 spawns (first-seen + dead→alive)", deaths, spawns)
	}
}

// When both signals (DF_DEAD via svc_playerinfo + StatHealth crossing
// 0) fire for the same transition, the dedup helper ensures consumers
// see exactly one DeathEvent followed by exactly one SpawnEvent —
// regardless of which source emitted first.
func TestDeathSpawnDedup_AcrossStatAndPlayerInfoSignals(t *testing.T) {
	p := NewParser(nil)
	var deaths, spawns int
	p.OnEvent(func(e Event) error {
		switch e.(type) {
		case *DeathEvent:
			deaths++
		case *SpawnEvent:
			spawns++
		}
		return nil
	})

	// First svc_playerinfo, alive — fires SpawnEvent.
	r := mvd.NewBufferReader(buildPlayerInfoPayload(0, 0))
	if err := p.parsePlayerInfo(r, 1.0, 1000, false); err != nil {
		t.Fatalf("first playerinfo: %v", err)
	}
	// Stat reaches us next; healthOld=0 (default) and healthNew=100
	// would normally fire a SpawnEvent — but dedup against the prior
	// playerinfo SpawnEvent means it's a no-op.
	if err := p.updateStat(0, mvd.StatHealth, 100, 1.1, 1100); err != nil {
		t.Fatalf("stat 100: %v", err)
	}
	if spawns != 1 || deaths != 0 {
		t.Errorf("after spawn signals: spawns=%d deaths=%d, want 1/0", spawns, deaths)
	}

	// Player dies: DF_DEAD set in playerinfo first, then health
	// crosses 0 in a stat update — second signal must be a no-op.
	r = mvd.NewBufferReader(buildPlayerInfoPayload(0, mvd.DFDead))
	if err := p.parsePlayerInfo(r, 2.0, 2000, false); err != nil {
		t.Fatalf("death playerinfo: %v", err)
	}
	if err := p.updateStat(0, mvd.StatHealth, -10, 2.05, 2050); err != nil {
		t.Fatalf("stat -10: %v", err)
	}
	if deaths != 1 || spawns != 1 {
		t.Errorf("after death signals: deaths=%d spawns=%d, want 1/1", deaths, spawns)
	}

	// Respawn: stat fires first this time (0 → 100), then DF_DEAD
	// clears in playerinfo — dedup again.
	if err := p.updateStat(0, mvd.StatHealth, 100, 3.0, 3000); err != nil {
		t.Fatalf("respawn stat: %v", err)
	}
	r = mvd.NewBufferReader(buildPlayerInfoPayload(0, 0))
	if err := p.parsePlayerInfo(r, 3.01, 3010, false); err != nil {
		t.Fatalf("respawn playerinfo: %v", err)
	}
	if deaths != 1 || spawns != 2 {
		t.Errorf("after respawn signals: deaths=%d spawns=%d, want 1/2", deaths, spawns)
	}
}
