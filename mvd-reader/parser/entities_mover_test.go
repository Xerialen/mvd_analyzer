package parser

import (
	"encoding/binary"
	"testing"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// moverTestParser returns a parser with a model list where index 2 is
// the inline submodel "*1" and index 3 a non-mover model, plus
// collectors for the mover events.
func moverTestParser() (*Parser, *[]*MoverSpawnEvent, *[]*MoverStateEvent) {
	p := NewParser(nil)
	p.modelList = []string{"", "maps/dm2.bsp", "*1", "progs/player.mdl"}
	var spawns []*MoverSpawnEvent
	var states []*MoverStateEvent
	p.OnEvent(func(e Event) error {
		switch ev := e.(type) {
		case *MoverSpawnEvent:
			spawns = append(spawns, ev)
		case *MoverStateEvent:
			states = append(states, ev)
		}
		return nil
	})
	return p, &spawns, &states
}

func moverBaseline(modelIdx int, origin [3]float32) *EntityState {
	return &EntityState{ModelIndex: modelIdx, Origin: origin, Present: true}
}

// appendCoord appends a short wire coord (13.3 fixed point).
func appendCoord(out []byte, v float32) []byte {
	return binary.LittleEndian.AppendUint16(out, uint16(int16(v*8)))
}

// fullPacketWithOrigin encodes a svc_packetentities body (after the
// command byte) holding one entity whose three origin coords are set,
// terminated by the 0x0000 word.
func fullPacketWithOrigin(ent int, origin [3]float32) []byte {
	word := uint16(ent) | uOrigin1 | uOrigin2 | uOrigin3
	out := binary.LittleEndian.AppendUint16(nil, word)
	for i := 0; i < 3; i++ {
		out = appendCoord(out, origin[i])
	}
	return binary.LittleEndian.AppendUint16(out, 0)
}

// emptyFullPacket encodes a full packet containing no entities.
func emptyFullPacket() []byte {
	return binary.LittleEndian.AppendUint16(nil, 0)
}

// A baseline whose model resolves to "*N" fires MoverSpawnEvent once;
// a resent identical baseline must not fire again.
func TestMoverSpawn_FromBaseline(t *testing.T) {
	p, spawns, states := moverTestParser()

	origin := [3]float32{16, -32, 64}
	if err := p.registerBaseline(42, moverBaseline(2, origin), 0.5); err != nil {
		t.Fatalf("registerBaseline: %v", err)
	}
	if len(*spawns) != 1 {
		t.Fatalf("spawns = %d, want 1", len(*spawns))
	}
	s := (*spawns)[0]
	if s.EntNum != 42 || s.Model != "*1" || s.SubModel != 1 || s.Origin != origin {
		t.Errorf("spawn = %+v, want ent 42 model *1 sub 1 origin %v", s, origin)
	}
	if s.TimeMs != 500 {
		t.Errorf("spawn TimeMs = %d, want 500", s.TimeMs)
	}

	// Identical resend: no second spawn, no state change.
	if err := p.registerBaseline(42, moverBaseline(2, origin), 0.6); err != nil {
		t.Fatalf("registerBaseline resend: %v", err)
	}
	if len(*spawns) != 1 || len(*states) != 0 {
		t.Errorf("after resend: spawns=%d states=%d, want 1/0", len(*spawns), len(*states))
	}

	// Non-mover models fire nothing.
	if err := p.registerBaseline(43, moverBaseline(3, origin), 0.5); err != nil {
		t.Fatalf("registerBaseline player model: %v", err)
	}
	if len(*spawns) != 1 {
		t.Errorf("player-model baseline fired a mover spawn")
	}
}

// An origin delta in a packet frame fires MoverStateEvent carrying the
// new origin and the packet's wire ms; an unchanged frame is silent.
func TestMoverState_OriginChange(t *testing.T) {
	p, _, states := moverTestParser()
	base := [3]float32{16, -32, 0}
	if err := p.registerBaseline(42, moverBaseline(2, base), 0); err != nil {
		t.Fatalf("registerBaseline: %v", err)
	}

	// The lift rises 8 units.
	moved := [3]float32{16, -32, 8}
	p.lastEntityPacketTime, p.lastEntityPacketTimeMs = 1.234, 1234
	r := mvd.NewBufferReader(fullPacketWithOrigin(42, moved))
	if err := p.parsePacketEntities(r, false, false, 0); err != nil {
		t.Fatalf("parsePacketEntities: %v", err)
	}
	if len(*states) != 1 {
		t.Fatalf("states = %d, want 1", len(*states))
	}
	st := (*states)[0]
	if st.EntNum != 42 || !st.Visible || st.Origin != moved || st.TimeMs != 1234 {
		t.Errorf("state = %+v, want ent 42 visible origin %v at 1234ms", st, moved)
	}

	// Same pose next frame: no event.
	p.lastEntityPacketTime, p.lastEntityPacketTimeMs = 1.3, 1300
	r = mvd.NewBufferReader(fullPacketWithOrigin(42, moved))
	if err := p.parsePacketEntities(r, false, false, 0); err != nil {
		t.Fatalf("parsePacketEntities repeat: %v", err)
	}
	if len(*states) != 1 {
		t.Errorf("unchanged frame emitted a state event")
	}
}

// Dropping out of the frame set flips Visible false (carrying the last
// visible origin); reappearing flips it back true.
func TestMoverState_VisibilityFlip(t *testing.T) {
	p, _, states := moverTestParser()
	base := [3]float32{16, -32, 0}
	if err := p.registerBaseline(42, moverBaseline(2, base), 0); err != nil {
		t.Fatalf("registerBaseline: %v", err)
	}

	// Full packet without the entity → invisible.
	p.lastEntityPacketTime, p.lastEntityPacketTimeMs = 2.0, 2000
	if err := p.parsePacketEntities(mvd.NewBufferReader(emptyFullPacket()), false, false, 0); err != nil {
		t.Fatalf("parsePacketEntities empty: %v", err)
	}
	if len(*states) != 1 || (*states)[0].Visible || (*states)[0].Origin != base {
		t.Fatalf("states = %+v, want one invisible event at the last origin", *states)
	}

	// Reappears (delta from baseline state) → visible again.
	p.lastEntityPacketTime, p.lastEntityPacketTimeMs = 2.5, 2500
	if err := p.parsePacketEntities(mvd.NewBufferReader(fullPacketWithOrigin(42, base)), false, false, 0); err != nil {
		t.Fatalf("parsePacketEntities reappear: %v", err)
	}
	if len(*states) != 2 || !(*states)[1].Visible {
		t.Fatalf("states = %+v, want a second, visible event", *states)
	}
}

// A baseline that lands before the model list can't be classified yet;
// the first packet diff after the list arrives must emit the spawn,
// with the baseline origin.
func TestMoverSpawn_BaselineBeforeModelList(t *testing.T) {
	p, spawns, _ := moverTestParser()
	p.modelList = nil // model list not received yet

	base := [3]float32{100, 200, 24}
	if err := p.registerBaseline(42, moverBaseline(2, base), 0); err != nil {
		t.Fatalf("registerBaseline: %v", err)
	}
	if len(*spawns) != 0 {
		t.Fatalf("spawn fired without a model list")
	}

	p.modelList = []string{"", "maps/dm2.bsp", "*1", "progs/player.mdl"}
	p.lastEntityPacketTime, p.lastEntityPacketTimeMs = 3.0, 3000
	if err := p.parsePacketEntities(mvd.NewBufferReader(fullPacketWithOrigin(42, base)), false, false, 0); err != nil {
		t.Fatalf("parsePacketEntities: %v", err)
	}
	if len(*spawns) != 1 {
		t.Fatalf("spawns = %d, want 1 (late classification)", len(*spawns))
	}
	s := (*spawns)[0]
	if s.Model != "*1" || s.SubModel != 1 || s.Origin != base || s.TimeMs != 3000 {
		t.Errorf("late spawn = %+v, want *1/1 at baseline origin %v, 3000ms", s, base)
	}
}

// classifyMover accepts only "*N" with N >= 1.
func TestClassifyMover(t *testing.T) {
	cases := []struct {
		path string
		sub  int
		ok   bool
	}{
		{"*1", 1, true},
		{"*27", 27, true},
		{"*0", 0, false},   // worldspawn is never an entity model
		{"*", 0, false},
		{"*x", 0, false},
		{"maps/dm2.bsp", 0, false},
		{"progs/armor.mdl", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		sub, ok := classifyMover(c.path)
		if sub != c.sub || ok != c.ok {
			t.Errorf("classifyMover(%q) = (%d,%v), want (%d,%v)", c.path, sub, ok, c.sub, c.ok)
		}
	}
}
