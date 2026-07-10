package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// Diagnostic capture is an explicit opt-in for non-match recordings. The
// normal timeline must continue to discard warmup positions, while the
// diagnostic path preserves the same entity-state samples on a demo-relative
// clock and closes its half-open stream window just after the final sample.
func TestTimelineDiagnosticPositionCaptureIsOptIn(t *testing.T) {
	position := &events.PlayerPositionEvent{
		PlayerNum: 1,
		Origin:    [3]float32{10, 20, 30},
		Time:      2.021,
		TimeMs:    2021,
	}
	eventSequence := []events.Event{
		position,
		&events.PrintEvent{Message: "The match has begun!", Time: 10},
		&events.PrintEvent{Message: "The match is over", Time: 20},
		&events.PlayerPositionEvent{
			PlayerNum: 1,
			Origin:    [3]float32{40, 50, 60},
			Time:      21,
			TimeMs:    21000,
		},
	}

	defaultAnalyzer := NewTimelineAnalyzer()
	defaultContext := &Context{}
	defaultContext.Players[1] = &events.PlayerInfo{Slot: 1, Name: "cand-1", Team: "red"}
	if err := defaultAnalyzer.Init(defaultContext); err != nil {
		t.Fatal(err)
	}
	for _, event := range eventSequence {
		if err := defaultAnalyzer.OnEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	defaultResult := &Result{}
	if err := defaultAnalyzer.Finalize(defaultResult); err != nil {
		t.Fatal(err)
	}
	if defaultResult.Streams != nil {
		t.Fatalf("ordinary analysis retained warmup positions: %+v", defaultResult.Streams)
	}

	diagnosticAnalyzer := NewTimelineAnalyzer()
	diagnosticAnalyzer.EnableDiagnosticPositionCapture()
	diagnosticContext := &Context{}
	diagnosticContext.Players[1] = &events.PlayerInfo{Slot: 1, Name: "cand-1", Team: "red"}
	if err := diagnosticAnalyzer.Init(diagnosticContext); err != nil {
		t.Fatal(err)
	}
	for _, event := range eventSequence {
		if err := diagnosticAnalyzer.OnEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	diagnosticResult := &Result{}
	if err := diagnosticAnalyzer.Finalize(diagnosticResult); err != nil {
		t.Fatal(err)
	}
	// Exercise the same post-processing that the default registry runs. A
	// detected match start at 10s must not rebase or filter the explicit
	// demo-relative diagnostic stream.
	normalizeMatchRelativeTimes(diagnosticResult, nil)
	if diagnosticResult.Streams == nil || len(diagnosticResult.Streams.Players) != 1 {
		t.Fatalf("diagnostic stream missing: %+v", diagnosticResult.Streams)
	}
	got := diagnosticResult.Streams.Players[0]
	if got.Name != "cand-1" {
		t.Fatalf("identity = %q, want cand-1", got.Name)
	}
	if got.Position == nil || len(got.Position.T) != 2 || got.Position.T[0] != 2021 || got.Position.T[1] != 21000 {
		t.Fatalf("position track = %+v, want pre-start/post-end samples at demo t=2021/21000ms", got.Position)
	}
	if got.Position.X[0] != 10 || got.Position.Y[0] != 20 || got.Position.Z[0] != 30 {
		t.Fatalf("position = %v/%v/%v, want 10/20/30", got.Position.X, got.Position.Y, got.Position.Z)
	}
	if diagnosticResult.Streams.Global.MatchStart != 0 || diagnosticResult.Streams.Global.MatchEnd != 21001 {
		t.Fatalf("diagnostic window = [%d,%d), want [0,21001)",
			diagnosticResult.Streams.Global.MatchStart,
			diagnosticResult.Streams.Global.MatchEnd,
		)
	}
}
