package parser

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// TestParseNails_Decode decodes a svc_nails2 (indexed) snapshot into a
// NailsFrameEvent when nail decoding is enabled, preserving ids and the
// bit-packed origin.
func TestParseNails_Decode(t *testing.T) {
	p := NewParser(nil)
	p.SetDecodeNails(true)
	var got *NailsFrameEvent
	p.OnEvent(func(e Event) error {
		if n, ok := e.(*NailsFrameEvent); ok {
			got = n
		}
		return nil
	})

	b0 := [6]byte{10, 20, 30, 40, 50, 60}
	b1 := [6]byte{1, 2, 3, 4, 5, 6}
	payload := []byte{2, 7}
	payload = append(payload, b0[:]...)
	payload = append(payload, 8)
	payload = append(payload, b1[:]...)

	if err := p.parseNails(mvd.NewBufferReader(payload), true, 1.0, 1000); err != nil {
		t.Fatalf("parseNails: %v", err)
	}
	if got == nil || len(got.Nails) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got.Nails[0].ID != 7 || got.Nails[1].ID != 8 {
		t.Errorf("ids = %d/%d, want 7/8", got.Nails[0].ID, got.Nails[1].ID)
	}
	wantX := float32(((int(b0[0]) + ((int(b0[1]) & 15) << 8)) << 1) - 4096)
	if got.Nails[0].Origin[0] != wantX {
		t.Errorf("origin[0] = %v, want %v", got.Nails[0].Origin[0], wantX)
	}
	if got.TimeMs != 1000 {
		t.Errorf("TimeMs = %d, want 1000", got.TimeMs)
	}
}

// TestParseNails_GatedOff consumes the payload without emitting when nail
// decoding is off (the default), keeping the byte cursor aligned.
func TestParseNails_GatedOff(t *testing.T) {
	p := NewParser(nil) // decodeNails defaults false
	fired := false
	p.OnEvent(func(e Event) error {
		if _, ok := e.(*NailsFrameEvent); ok {
			fired = true
		}
		return nil
	})
	payload := []byte{1, 7, 1, 2, 3, 4, 5, 6} // svc_nails2: count 1, id 7, 6 bytes
	r := mvd.NewBufferReader(payload)
	if err := p.parseNails(r, true, 1, 1000); err != nil {
		t.Fatalf("parseNails: %v", err)
	}
	if fired {
		t.Error("NailsFrameEvent emitted despite nail decode disabled")
	}
	if r.Remaining() != 0 {
		t.Errorf("remaining = %d, want 0 (payload consumed)", r.Remaining())
	}
}

// TestClassifyNail recognises the spike models that nailhack servers send as
// packet entities.
func TestClassifyNail(t *testing.T) {
	for _, m := range []string{"progs/spike.mdl", "progs/s_spike.mdl"} {
		if classifyNail(m) != "nail" {
			t.Errorf("classifyNail(%q) != nail", m)
		}
	}
	if classifyNail("progs/missile.mdl") != "" {
		t.Errorf("missile misclassified as nail")
	}
}
