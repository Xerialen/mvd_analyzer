package parser

import (
	"encoding/binary"
	"testing"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// projPacket encodes a full svc_packetentities body holding one entity with
// an explicit model index and origin (model flag rides the low-byte
// extension, so uMoreBits is set in the word).
func projPacket(ent, modelIdx int, origin [3]float32) []byte {
	word := uint16(ent) | uMoreBits | uOrigin1 | uOrigin2 | uOrigin3
	out := binary.LittleEndian.AppendUint16(nil, word)
	out = append(out, byte(uModel)) // low-byte flags: model present
	out = append(out, byte(modelIdx))
	for i := 0; i < 3; i++ {
		out = appendCoord(out, origin[i])
	}
	return binary.LittleEndian.AppendUint16(out, 0)
}

// A rocket entity appearing in a packet fires ProjectileSpawnEvent; its
// removal (absent from the next full packet) fires ProjectileDespawnEvent
// carrying the last observed origin. The per-ent classification is cleared
// on despawn so a recycled entnum re-classifies.
func TestProjectile_SpawnThenDespawn(t *testing.T) {
	p := NewParser(nil)
	p.modelList = []string{"", "maps/dm2.bsp", "progs/missile.mdl", "progs/player.mdl"}
	var spawns []*ProjectileSpawnEvent
	var despawns []*ProjectileDespawnEvent
	p.OnEvent(func(e Event) error {
		switch ev := e.(type) {
		case *ProjectileSpawnEvent:
			spawns = append(spawns, ev)
		case *ProjectileDespawnEvent:
			despawns = append(despawns, ev)
		}
		return nil
	})

	origin := [3]float32{100, 200, 50}
	p.lastEntityPacketTime, p.lastEntityPacketTimeMs = 1.0, 1000
	if err := p.parsePacketEntities(mvd.NewBufferReader(projPacket(50, 2, origin)), false, false, 0); err != nil {
		t.Fatalf("spawn packet: %v", err)
	}
	p.lastEntityPacketTime, p.lastEntityPacketTimeMs = 1.5, 1500
	if err := p.parsePacketEntities(mvd.NewBufferReader(emptyFullPacket()), false, false, 0); err != nil {
		t.Fatalf("despawn packet: %v", err)
	}

	if len(spawns) != 1 {
		t.Fatalf("spawns = %d, want 1", len(spawns))
	}
	if s := spawns[0]; s.EntNum != 50 || s.Kind != "rl" || s.Origin != origin || s.TimeMs != 1000 {
		t.Errorf("spawn = %+v, want ent 50 kind rl origin %v t 1000", s, origin)
	}
	if len(despawns) != 1 {
		t.Fatalf("despawns = %d, want 1", len(despawns))
	}
	if d := despawns[0]; d.EntNum != 50 || d.Kind != "rl" || d.Origin != origin || d.TimeMs != 1500 {
		t.Errorf("despawn = %+v, want ent 50 kind rl origin %v t 1500", d, origin)
	}
	if _, stillTracked := p.spawnedProjectiles[50]; stillTracked {
		t.Errorf("ent 50 still tracked after despawn — entnum reuse would misclassify")
	}
}

// A non-projectile model (player) in a packet fires nothing.
func TestProjectile_NonProjectileIgnored(t *testing.T) {
	p := NewParser(nil)
	p.modelList = []string{"", "maps/dm2.bsp", "progs/missile.mdl", "progs/player.mdl"}
	fired := false
	p.OnEvent(func(e Event) error {
		switch e.(type) {
		case *ProjectileSpawnEvent, *ProjectileDespawnEvent:
			fired = true
		}
		return nil
	})
	p.lastEntityPacketTime, p.lastEntityPacketTimeMs = 1.0, 1000
	if err := p.parsePacketEntities(mvd.NewBufferReader(projPacket(60, 3, [3]float32{1, 2, 3})), false, false, 0); err != nil {
		t.Fatalf("packet: %v", err)
	}
	if fired {
		t.Errorf("player model fired a projectile event")
	}
}
