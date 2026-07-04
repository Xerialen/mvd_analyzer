package parser

import (
	"encoding/binary"
	"testing"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// lightningPayload builds a svc_temp_entity body (after the command byte):
// [byte teType][short ent][coord×3 start][coord×3 end].
func lightningPayload(teType byte, ent int, start, end [3]float32) []byte {
	b := []byte{teType}
	b = binary.LittleEndian.AppendUint16(b, uint16(ent))
	for _, v := range start {
		b = appendCoord(b, v)
	}
	for _, v := range end {
		b = appendCoord(b, v)
	}
	return b
}

func TestParseTempEntity_LightningBeam(t *testing.T) {
	p := NewParser(nil)
	var got *BeamEvent
	p.OnEvent(func(e Event) error {
		if b, ok := e.(*BeamEvent); ok {
			got = b
		}
		return nil
	})

	start := [3]float32{10, 20, 30}
	end := [3]float32{100, -40, 16}
	// TE_LIGHTNING2 (6), entity 4 (player slot 3).
	payload := lightningPayload(6, 4, start, end)
	if teType, err := p.parseTempEntity(mvd.NewBufferReader(payload), 2.0, 2000, false); err != nil {
		t.Fatalf("parseTempEntity: type %d err %v", teType, err)
	}
	if got == nil {
		t.Fatal("no BeamEvent emitted")
	}
	if got.Ent != 4 || got.Type != 6 || got.Start != start || got.End != end {
		t.Errorf("beam = %+v, want ent 4 type 6 start %v end %v", got, start, end)
	}
	if got.Time != 2.0 || got.TimeMs != 2000 {
		t.Errorf("beam time = %v/%d, want 2.0/2000", got.Time, got.TimeMs)
	}
}

// A point-effect temp entity (TE_EXPLOSION = 3, three coords) is consumed
// without emitting a beam, and the cursor lands exactly at the end.
func TestParseTempEntity_PointEffectNoBeam(t *testing.T) {
	p := NewParser(nil)
	fired := false
	p.OnEvent(func(e Event) error {
		if _, ok := e.(*BeamEvent); ok {
			fired = true
		}
		return nil
	})
	payload := []byte{3}
	for _, v := range [3]float32{1, 2, 3} {
		payload = appendCoord(payload, v)
	}
	r := mvd.NewBufferReader(payload)
	if teType, err := p.parseTempEntity(r, 1.0, 1000, false); err != nil || teType != 3 {
		t.Fatalf("parseTempEntity: type %d err %v", teType, err)
	}
	if fired {
		t.Errorf("explosion fired a BeamEvent")
	}
	if r.Remaining() != 0 {
		t.Errorf("remaining = %d, want 0 (exact consume)", r.Remaining())
	}
}
