package parser

import (
	"encoding/binary"
	"testing"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// The A2 unification replaced the standalone skipSpawnBaseline /
// skipEntityDelta byte-skippers with decode-and-discard through the same
// readers the parse paths use (readBaselineBody, readEntityDelta). These
// tests pin the one property the old skip paths guaranteed: the shared
// reader consumes exactly the wire byte count for a command, leaving the
// cursor aligned for the next command in the payload. A one-byte drift
// here is the "silent misalignment" failure the parser guards against.

// skipConsumed feeds a payload through p.skipCommand for one command and
// returns how many bytes it consumed. A trailing sentinel byte proves the
// reader stopped exactly at the command boundary (not that it ran to EOF).
func skipConsumed(t *testing.T, p *Parser, cmd byte, body []byte) int {
	t.Helper()
	payload := append(append([]byte{}, body...), 0xAB) // sentinel
	r := mvd.NewBufferReader(payload)
	if err := p.skipCommand(r, cmd); err != nil {
		t.Fatalf("skipCommand(cmd %d): %v", cmd, err)
	}
	consumed := r.Offset()
	if rem := r.Remaining(); rem != 1 {
		t.Fatalf("cmd %d: %d bytes left after skip, want 1 (the sentinel) — consumed %d of %d",
			cmd, rem, consumed, len(body))
	}
	if b, _ := r.ReadByte(); b != 0xAB {
		t.Fatalf("cmd %d: byte after skip = 0x%02x, want the 0xAB sentinel (misaligned)", cmd, b)
	}
	return consumed
}

func TestSkipSpawnStatic_ByteCount(t *testing.T) {
	// svc_spawnstatic is a bare baseline body (no entity-number prefix):
	// model(1)+frame(1)+colormap(1)+skin(1) + 3×(coord + angle byte).
	// Short coords → 4 + 3×3 = 13 bytes; float coords → 4 + 3×5 = 19.
	shortBody := []byte{5, 0, 0, 3} // model, frame, colormap, skin
	for i := 0; i < 3; i++ {
		shortBody = appendCoord(shortBody, float32(16*(i+1)))
		shortBody = append(shortBody, byte(i)) // angle
	}
	p := NewParser(nil)
	p.floatCoords = false
	if got := skipConsumed(t, p, mvd.SvcSpawnStatic, shortBody); got != 13 {
		t.Errorf("short-coord svc_spawnstatic consumed %d bytes, want 13", got)
	}

	floatBody := []byte{5, 0, 0, 3}
	for i := 0; i < 3; i++ {
		floatBody = binary.LittleEndian.AppendUint32(floatBody, 0x40000000) // 4-byte coord
		floatBody = append(floatBody, byte(i))                              // angle
	}
	pf := NewParser(nil)
	pf.floatCoords = true
	if got := skipConsumed(t, pf, mvd.SvcSpawnStatic, floatBody); got != 19 {
		t.Errorf("float-coord svc_spawnstatic consumed %d bytes, want 19", got)
	}
}

func TestSkipFTESpawnStatic2_ByteCount(t *testing.T) {
	// svc_fte_spawnstatic2: 2-byte flag word + entity delta fields.

	// Case 1: only the three origins set (short coords), no U_MOREBITS.
	// word(2) + 3 short coords(6) = 8 bytes.
	word := uint16(3) | uOrigin1 | uOrigin2 | uOrigin3
	body := binary.LittleEndian.AppendUint16(nil, word)
	body = appendCoord(body, 16)
	body = appendCoord(body, -32)
	body = appendCoord(body, 64)
	p := NewParser(nil)
	if got := skipConsumed(t, p, mvd.SvcFTESpawnStatic2, body); got != 8 {
		t.Errorf("origins-only fte_spawnstatic2 consumed %d bytes, want 8", got)
	}

	// Case 2: U_MOREBITS pulls in a low-order byte selecting U_MODEL and
	// U_SKIN. word(2) + lowbyte(1) + model(1) + skin(1) = 5 bytes.
	word2 := uint16(3) | uMoreBits
	body2 := binary.LittleEndian.AppendUint16(nil, word2)
	body2 = append(body2, byte(uModel|uSkin)) // low-order flags
	body2 = append(body2, 7)                  // modelindex
	body2 = append(body2, 2)                  // skin
	p2 := NewParser(nil)
	if got := skipConsumed(t, p2, mvd.SvcFTESpawnStatic2, body2); got != 5 {
		t.Errorf("morebits fte_spawnstatic2 consumed %d bytes, want 5", got)
	}

	// Case 3: negotiated FTE extensions — U_FTE_EVENMORE pulls in the
	// evenmorebits byte, U_FTE_YETMORE its high byte, then the trans and
	// colourmod payloads (both gated on the negotiated extension).
	// word(2) + lowbyte(1) + evenmore(1) + yetmore(1) + trans(1) + colourmod(3) = 9.
	word3 := uint16(3) | uMoreBits
	body3 := binary.LittleEndian.AppendUint16(nil, word3)
	body3 = append(body3, byte(uFTEEvenMore))          // low-order flags
	body3 = append(body3, byte(uFTETrans|uFTEYetMore)) // evenmorebits low
	body3 = append(body3, byte(uFTEColourMod>>8))      // evenmorebits high
	body3 = append(body3, 128)                         // trans
	body3 = append(body3, 255, 128, 64)                // colourmod RGB
	p3 := NewParser(nil)
	p3.fteExtensions = mvd.FTEPextTrans | mvd.FTEPextColourMod
	if got := skipConsumed(t, p3, mvd.SvcFTESpawnStatic2, body3); got != 9 {
		t.Errorf("fte evenmore fte_spawnstatic2 consumed %d bytes, want 9", got)
	}

	// Case 4: NO FTE extension negotiated — bit 7 of the low-order byte
	// must NOT trigger an evenmorebits read (mvdsv only emits the byte
	// when an extension was negotiated, sv_ents.c:216-235; ezquake gates
	// the read the same way, cl_ents.c:505). The old parse path read it
	// unconditionally — the F3 divergence this pins.
	// word(2) + lowbyte(1) + model(1) + coord(2) = 6.
	word4 := uint16(3) | uMoreBits | uOrigin1
	body4 := binary.LittleEndian.AppendUint16(nil, word4)
	body4 = append(body4, byte(uModel|uFTEEvenMore)) // low-order flags
	body4 = append(body4, 7)                         // modelindex
	body4 = appendCoord(body4, 16)
	p4 := NewParser(nil)
	p4.fteExtensions = 0
	if got := skipConsumed(t, p4, mvd.SvcFTESpawnStatic2, body4); got != 6 {
		t.Errorf("non-FTE fte_spawnstatic2 consumed %d bytes, want 6", got)
	}
}

// The shared readBaselineBody must decode a svc_spawnbaseline body to the
// same field values whether it is reached via the entity-number-prefixed
// parse path or the bare svc_spawnstatic decode-and-discard path — the two
// used to be separate implementations (parseSpawnBaseline vs skipSpawnStatic).
func TestBaselineBody_DecodeParity(t *testing.T) {
	origin := [3]float32{16, -32, 64}
	body := []byte{9, 1, 0, 2} // model=9, frame=1, colormap=0, skin=2
	for i := 0; i < 3; i++ {
		body = appendCoord(body, origin[i])
		body = append(body, byte(i))
	}

	state, err := readBaselineBody(mvd.NewBufferReader(body), false)
	if err != nil {
		t.Fatalf("readBaselineBody: %v", err)
	}
	if state.ModelIndex != 9 || state.Frame != 1 || state.Colormap != 0 || state.SkinNum != 2 {
		t.Errorf("decoded fields = %+v, want model 9 frame 1 colormap 0 skin 2", state)
	}
	if state.Origin != origin || !state.Present {
		t.Errorf("decoded origin/present = %v/%v, want %v/true", state.Origin, state.Present, origin)
	}

	// Same body, reached through svc_spawnbaseline (2-byte entnum prefix),
	// must register a baseline with the identical origin.
	p := NewParser(nil)
	prefixed := binary.LittleEndian.AppendUint16(nil, 42) // entnum
	prefixed = append(prefixed, body...)
	if err := p.parseSpawnBaseline(mvd.NewBufferReader(prefixed), 0, false); err != nil {
		t.Fatalf("parseSpawnBaseline: %v", err)
	}
	got := p.baselines[42]
	if got == nil || got.Origin != origin || got.ModelIndex != 9 || got.SkinNum != 2 {
		t.Errorf("registered baseline = %+v, want the same body decode", got)
	}
}
