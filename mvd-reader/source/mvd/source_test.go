package mvd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
	wire "github.com/mvd-analyzer/mvd-reader/mvd"
	"github.com/mvd-analyzer/mvd-reader/parser"
)

// framePayload wraps a raw svc_* payload in the MVD dem_all message
// framing the decoder expects: [timeDelta:1][typeByte:1][size:u32][payload].
func framePayload(timeDelta byte, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteByte(timeDelta)
	b.WriteByte(wire.DemAll) // messageType=DemAll, playerNum=0
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(payload)))
	b.Write(size[:])
	b.Write(payload)
	return b.Bytes()
}

// TestSourceEndOfDemoDrainsTailThenEOF verifies the events.Source EOF
// contract (reader F2 / A3): a demo whose final message carries a real
// event immediately followed by the standard svc_disconnect "EndOfDemo"
// termination must yield the tail event first and then io.EOF — the
// disconnect end must map to io.EOF, and the tail event queued by that
// same ParseOne must not be dropped.
func TestSourceEndOfDemoDrainsTailThenEOF(t *testing.T) {
	// Payload: svc_updatefrags(player 0, frags 5) then svc_disconnect "EndOfDemo".
	var payload bytes.Buffer
	payload.WriteByte(wire.SvcUpdateFrags)
	payload.WriteByte(0) // player 0
	var frags [2]byte
	binary.LittleEndian.PutUint16(frags[:], 5)
	payload.Write(frags[:])
	payload.WriteByte(wire.SvcDisconnect)
	payload.WriteString("EndOfDemo")
	payload.WriteByte(0) // null terminator

	stream := framePayload(10, payload.Bytes())

	src, err := NewFromReader(bytes.NewReader(stream))
	if err != nil {
		t.Fatalf("NewFromReader: %v", err)
	}
	defer src.Close()

	ev, err := src.Next()
	if err != nil {
		t.Fatalf("first Next returned error, want tail event: %v", err)
	}
	fu, ok := ev.(*events.FragUpdateEvent)
	if !ok {
		t.Fatalf("first event = %T, want *FragUpdateEvent (tail before EndOfDemo)", ev)
	}
	if fu.PlayerNum != 0 || fu.Frags != 5 {
		t.Fatalf("FragUpdateEvent = %+v, want {PlayerNum:0 Frags:5}", fu)
	}

	if _, err := src.Next(); err != io.EOF {
		t.Fatalf("second Next err = %v, want io.EOF after EndOfDemo", err)
	}
	// The contract stays at io.EOF once the stream is exhausted.
	if _, err := src.Next(); err != io.EOF {
		t.Fatalf("third Next err = %v, want io.EOF", err)
	}
}

// TestSourceNonEOFErrorDrainsTailFirst verifies the pendErr half of the
// Source contract: when the same ParseOne call both queues events and
// returns a non-EOF error, Next drains the queued events first, then
// surfaces the error exactly once, and reports io.EOF on every call after
// that. A second handler that rejects StuffTextEvent forces the error
// mid-message, after the Source's own queue-appending handler already ran —
// the exact ordering the drain exists for.
func TestSourceNonEOFErrorDrainsTailFirst(t *testing.T) {
	// Payload: svc_updatefrags (queued fine) then svc_stufftext (queued by
	// the Source handler, then rejected by the erroring handler below).
	var payload bytes.Buffer
	payload.WriteByte(wire.SvcUpdateFrags)
	payload.WriteByte(3) // player 3
	var frags [2]byte
	binary.LittleEndian.PutUint16(frags[:], 7)
	payload.Write(frags[:])
	payload.WriteByte(wire.SvcStuffText)
	payload.WriteString("hello")
	payload.WriteByte(0) // null terminator

	stream := framePayload(10, payload.Bytes())

	src, err := NewFromReader(bytes.NewReader(stream))
	if err != nil {
		t.Fatalf("NewFromReader: %v", err)
	}
	defer src.Close()

	errBoom := errors.New("boom")
	src.parser.OnEvent(func(e parser.Event) error {
		if _, ok := e.(*parser.StuffTextEvent); ok {
			return errBoom
		}
		return nil
	})

	ev, err := src.Next()
	if err != nil {
		t.Fatalf("first Next returned error, want queued FragUpdateEvent: %v", err)
	}
	if fu, ok := ev.(*events.FragUpdateEvent); !ok || fu.PlayerNum != 3 || fu.Frags != 7 {
		t.Fatalf("first event = %#v, want FragUpdateEvent{PlayerNum:3 Frags:7}", ev)
	}

	ev, err = src.Next()
	if err != nil {
		t.Fatalf("second Next returned error, want queued StuffTextEvent before the error: %v", err)
	}
	if st, ok := ev.(*events.StuffTextEvent); !ok || st.Command != "hello" {
		t.Fatalf("second event = %#v, want StuffTextEvent{Command:\"hello\"}", ev)
	}

	if _, err := src.Next(); !errors.Is(err, errBoom) {
		t.Fatalf("third Next err = %v, want the handler error after the queue drained", err)
	}
	// The error is surfaced exactly once; afterwards the stream reports a
	// plain end.
	if _, err := src.Next(); err != io.EOF {
		t.Fatalf("fourth Next err = %v, want io.EOF after the error was surfaced", err)
	}
}
