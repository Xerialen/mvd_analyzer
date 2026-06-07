package parser

import (
	"encoding/binary"
	"testing"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// TestParseHiddenPausedDuration_BareForm pins the workaround for the malformed
// paused-duration block mvdsv actually emits: SV_MVDWritePausedTimeToStreams
// (sv_demo.c) hand-writes the 0x000A block WITHOUT the leading 4-byte
// mvdhidden_block_header_t.length field every other hidden block carries, so the
// dem_multiple payload is a bare [type_id:u16][duration:byte] (3 bytes). The
// normal length-prefixed block loop reads the type_id as a truncated length and
// bails; parseHiddenMessage must detect this exact shape and decode it.
func TestParseHiddenPausedDuration_BareForm(t *testing.T) {
	p := NewParser(nil)
	var got *PausedDurationEvent
	p.OnEvent(func(e Event) error {
		if pd, ok := e.(*PausedDurationEvent); ok {
			got = pd
		}
		return nil
	})

	payload := make([]byte, 3)
	binary.LittleEndian.PutUint16(payload[0:], mvd.MVDHiddenPausedDuration)
	payload[2] = 104 // real ms for this idle frame, as seen in duel-with-pauses.mvd

	msg := &mvd.DemoMessage{Payload: payload}
	msg.Time = 28.465
	if err := p.parseHiddenMessage(msg); err != nil {
		t.Fatalf("parseHiddenMessage: %v", err)
	}
	if got == nil {
		t.Fatal("no PausedDurationEvent emitted for bare 0x000A block")
	}
	if got.DurationMs != 104 || got.Time != 28.465 {
		t.Errorf("got %+v, want {DurationMs:104 Time:28.465}", got)
	}
}

// TestParseHiddenPausedDuration_StandardForm guards the form QW-Group/mvdsv
// PR #210 emits once merged: the 0x000A block written with the standard length
// header (dem_multiple payload [u32 length=1][u16 0x000A][byte]), which flows
// through the normal block loop's MVDHiddenPausedDuration case.
func TestParseHiddenPausedDuration_StandardForm(t *testing.T) {
	p := NewParser(nil)
	var got *PausedDurationEvent
	p.OnEvent(func(e Event) error {
		if pd, ok := e.(*PausedDurationEvent); ok {
			got = pd
		}
		return nil
	})

	// Standard framing inside the dem_multiple payload:
	// [length:u32 = bytes-after-typeid = 1][type_id:u16][duration:byte]
	payload := make([]byte, 7)
	binary.LittleEndian.PutUint32(payload[0:], 1)
	binary.LittleEndian.PutUint16(payload[4:], mvd.MVDHiddenPausedDuration)
	payload[6] = 97

	msg := &mvd.DemoMessage{Payload: payload}
	msg.Time = 12.0
	if err := p.parseHiddenMessage(msg); err != nil {
		t.Fatalf("parseHiddenMessage: %v", err)
	}
	if got == nil {
		t.Fatal("no PausedDurationEvent emitted for standard-framed 0x000A block")
	}
	if got.DurationMs != 97 || got.Time != 12.0 {
		t.Errorf("got %+v, want {DurationMs:97 Time:12}", got)
	}
}
