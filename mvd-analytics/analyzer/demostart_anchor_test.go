package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// TestPlausibleDemoStartUnixMs guards the range check that rejects the
// non-timestamp 0x000B payloads seen in the corpus (61, 11701) while
// accepting real demo-open wall clocks.
func TestPlausibleDemoStartUnixMs(t *testing.T) {
	cases := []struct {
		v    int64
		want bool
	}{
		{0, false},
		{61, false},          // corpus game 211805
		{11701, false},       // corpus game 212545
		{946684800000, true}, // 2000-01-01, lower bound
		{1780260653484, true},
		{1777115225 * 1000, true}, // epoch-derived (game 211805's `epoch`)
		{4102444800000, false},    // 2100-01-01, exclusive upper bound
	}
	for _, c := range cases {
		if got := plausibleDemoStartUnixMs(c.v); got != c.want {
			t.Errorf("plausibleDemoStartUnixMs(%d) = %v, want %v", c.v, got, c.want)
		}
	}
}

// TestClockDemoStartAnchor exercises the wall-clock anchor the clock derives
// and publishes on co.Clock: the millisecond-accurate mvdhidden 0x000B block
// (accuracy 1) wins, else the whole-second serverinfo `epoch` cvar
// (accuracy 1000), else nothing. The timeline writes these onto
// Streams.Global; here we assert the clock's derivation directly. This folds
// in the fallback the deleted deriveDemoStartAnchor post-processor owned.
func TestClockDemoStartAnchor(t *testing.T) {
	const (
		epochSecs   = 1780260653 // 2026-05-31T20:50:53Z, whole seconds
		hiddenMs    = 1780260653484
		fullEpoch   = `fullserverinfo "\epoch\1780260653\maxfps\77"`
		fullNoEpoch = `fullserverinfo "\maxfps\77\timelimit\10"`
		fullGarbage = `fullserverinfo "\epoch\not-a-number\maxfps\77"`
	)

	tests := []struct {
		name         string
		events       []events.Event
		wantUnixMs   int64
		wantAccuracy int32
	}{
		{
			name:         "0x000B present — epoch must not overwrite",
			events:       []events.Event{&events.StuffTextEvent{Command: fullEpoch}, &events.DemoStartTimestampEvent{UnixMs: hiddenMs}},
			wantUnixMs:   hiddenMs,
			wantAccuracy: 1,
		},
		{
			name:         "epoch fallback when 0x000B absent",
			events:       []events.Event{&events.StuffTextEvent{Command: fullEpoch}},
			wantUnixMs:   epochSecs * 1000,
			wantAccuracy: 1000,
		},
		{
			name:         "implausible 0x000B falls back to epoch",
			events:       []events.Event{&events.StuffTextEvent{Command: fullEpoch}, &events.DemoStartTimestampEvent{UnixMs: 61}},
			wantUnixMs:   epochSecs * 1000,
			wantAccuracy: 1000,
		},
		{
			name:         "no epoch and no 0x000B — anchor stays absent",
			events:       []events.Event{&events.StuffTextEvent{Command: fullNoEpoch}},
			wantUnixMs:   0,
			wantAccuracy: 0,
		},
		{
			name:         "garbage epoch is ignored",
			events:       []events.Event{&events.StuffTextEvent{Command: fullGarbage}},
			wantUnixMs:   0,
			wantAccuracy: 0,
		},
		{
			name:         "mid-game serverinfo epoch update wins (last write)",
			events:       []events.Event{&events.ServerInfoEvent{Key: "epoch", Value: "1780260653"}},
			wantUnixMs:   epochSecs * 1000,
			wantAccuracy: 1000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := NewClockAnalyzer()
			for _, e := range tc.events {
				if err := a.OnEvent(e); err != nil {
					t.Fatalf("OnEvent: %v", err)
				}
			}
			co := &CoreOutputs{}
			a.PopulateCore(co)
			if co.Clock == nil {
				t.Fatal("clock not published")
			}
			if co.Clock.DemoStartUnixMs != tc.wantUnixMs {
				t.Errorf("DemoStartUnixMs = %d, want %d", co.Clock.DemoStartUnixMs, tc.wantUnixMs)
			}
			if co.Clock.DemoStartAccuracyMs != tc.wantAccuracy {
				t.Errorf("DemoStartAccuracyMs = %d, want %d", co.Clock.DemoStartAccuracyMs, tc.wantAccuracy)
			}
		})
	}
}
