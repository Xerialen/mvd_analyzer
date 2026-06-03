package parser

import (
	"encoding/binary"
	"testing"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// dmgRecord builds the 8-byte mvdhidden_dmgdone payload:
// <short flags|deathtype> <short attackerEnt> <short victimEnt> <short damage>
// (little-endian, entity numbers 1-indexed).
func dmgRecord(flagsAndType, attackerEnt, victimEnt uint16, damage int16) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint16(b[0:], flagsAndType)
	binary.LittleEndian.PutUint16(b[2:], attackerEnt)
	binary.LittleEndian.PutUint16(b[4:], victimEnt)
	binary.LittleEndian.PutUint16(b[6:], uint16(damage))
	return b
}

func TestParseHiddenDamage_PlayerToPlayer(t *testing.T) {
	p := NewParser(nil)
	var got *DamageEvent
	p.OnEvent(func(e Event) error {
		if d, ok := e.(*DamageEvent); ok {
			got = d
		}
		return nil
	})

	// attacker ent 4 (slot 3), victim ent 1 (slot 0), 89 RL damage, splash
	const splash = 1 << 15
	payload := dmgRecord(uint16(splash|mvd.DtRL), 4, 1, 89)
	if err := p.parseHiddenDamage(mvd.NewBufferReader(payload), 12.5, len(payload)); err != nil {
		t.Fatalf("parseHiddenDamage: %v", err)
	}
	if got == nil {
		t.Fatal("no DamageEvent emitted")
	}
	if got.Attacker != 3 || got.Victim != 0 || got.Damage != 89 ||
		got.DeathType != mvd.DtRL || !got.IsSplash || got.Time != 12.5 {
		t.Errorf("got %+v", got)
	}
}

func TestParseHiddenDamage_WorldInflictorEmitsSentinel(t *testing.T) {
	// World/environmental damage: KTX sends the record with a non-player
	// attacker (worldspawn = edict 0, or a non-client entity). The victim
	// is still a player, so we must surface the damage-taken with
	// Attacker == -1 rather than dropping it.
	cases := []struct {
		name        string
		attackerEnt uint16
		deathType   uint16
	}{
		{"worldspawn", 0, mvd.DtFall},            // edict 0 -> slot -1
		{"nonClientEnt", 600, mvd.DtTriggerHurt}, // entity well past MaxClients
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewParser(nil)
			var got *DamageEvent
			p.OnEvent(func(e Event) error {
				if d, ok := e.(*DamageEvent); ok {
					got = d
				}
				return nil
			})
			payload := dmgRecord(tc.deathType, tc.attackerEnt, 2 /*victim slot 1*/, 25)
			if err := p.parseHiddenDamage(mvd.NewBufferReader(payload), 3.0, len(payload)); err != nil {
				t.Fatalf("parseHiddenDamage: %v", err)
			}
			if got == nil {
				t.Fatal("world damage dropped; expected a DamageEvent")
			}
			if got.Attacker != -1 {
				t.Errorf("Attacker = %d, want -1 (world sentinel)", got.Attacker)
			}
			if got.Victim != 1 || got.Damage != 25 || got.DeathType != int(tc.deathType) {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestParseHiddenDamage_ZeroDamageDropped(t *testing.T) {
	p := NewParser(nil)
	var emitted int
	p.OnEvent(func(e Event) error {
		if _, ok := e.(*DamageEvent); ok {
			emitted++
		}
		return nil
	})
	payload := dmgRecord(mvd.DtRL, 1, 2, 0)
	if err := p.parseHiddenDamage(mvd.NewBufferReader(payload), 1.0, len(payload)); err != nil {
		t.Fatalf("parseHiddenDamage: %v", err)
	}
	if emitted != 0 {
		t.Errorf("emitted %d events for zero damage, want 0", emitted)
	}
}
