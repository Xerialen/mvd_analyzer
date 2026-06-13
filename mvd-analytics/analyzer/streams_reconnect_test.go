package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// newStreamState returns a timelinePlayerState whose builder carries a
// few spawns + position samples in [from, to) ms, simulating one slot
// occupancy's worth of play.
func newStreamState(from, to int32) *timelinePlayerState {
	s := &timelinePlayerState{}
	for t := from; t < to; t += 100 {
		s.streams.recordPosition(t, float32(t), 0, 0, 0, 0)
	}
	s.streams.recordSpawn(from)
	s.streams.recordDeath(to - 50)
	s.streams.recordHealth(from, 100)
	return s
}

func findStream(s *result.Streams, name string) bool {
	if s == nil {
		return false
	}
	for _, p := range s.Players {
		if p.Name == name {
			return true
		}
	}
	return false
}

// TestStreams_ReconnectMergesIntoOneStream: a player on slot 7 for the
// first half then slot 2 for the second half (both sessions resolving to
// one identity) must emit a single merged PlayerStream spanning both
// halves — not two phantom half-streams.
func TestStreams_ReconnectMergesIntoOneStream(t *testing.T) {
	a := NewTimelineAnalyzer()
	a.timing.Started = true
	a.playerState[7] = newStreamState(0, 600_000)
	a.playerState[2] = newStreamState(600_000, 1_200_000)
	a.UseCoreOutputs(&CoreOutputs{Sessions: map[int][]ResolvedSession{
		7: {{StartMs: minInt32, EndMs: maxInt32, Name: "rusti", Team: "jah", IdentityKey: "id:0"}},
		2: {{StartMs: minInt32, EndMs: maxInt32, Name: "rusti", Team: "jah", IdentityKey: "id:0"}},
	}})

	streams := a.buildStreamsResult(nil, nil, 0, 1200)
	if streams == nil {
		t.Fatal("nil streams")
	}
	if n := len(streams.Players); n != 1 {
		var names []string
		for _, p := range streams.Players {
			names = append(names, p.Name)
		}
		t.Fatalf("want 1 merged player, got %d: %v", n, names)
	}
	p := streams.Players[0]
	if p.Name != "rusti" {
		t.Errorf("name = %q, want rusti", p.Name)
	}
	// Both halves' spawns survived the merge.
	if len(p.Spawns) != 2 {
		t.Errorf("spawns = %d, want 2 (one per half)", len(p.Spawns))
	}
	// Positions span both halves.
	if p.Position == nil || len(p.Position.T) == 0 {
		t.Fatal("no position track on merged stream")
	}
	first, last := p.Position.T[0], p.Position.T[len(p.Position.T)-1]
	if first != 0 || last < 1_100_000 {
		t.Errorf("merged position span = [%d,%d], want it to cover both halves", first, last)
	}
}

// TestStreams_SharedSlotSplitsByHandover: slot 7 is used by two genuine
// players over time (both with play). They must be carved into two
// separate streams at the handover, each carrying only its window.
func TestStreams_SharedSlotSplitsByHandover(t *testing.T) {
	a := NewTimelineAnalyzer()
	a.timing.Started = true
	// One slot, two occupancies with real play.
	st := newStreamState(0, 300_000)
	for t := int32(400_000); t < 700_000; t += 100 {
		st.streams.recordPosition(t, float32(t), 0, 0, 0, 0)
	}
	st.streams.recordSpawn(400_000)
	a.playerState[7] = st
	a.UseCoreOutputs(&CoreOutputs{Sessions: map[int][]ResolvedSession{
		7: {
			{StartMs: minInt32, EndMs: 350_000, Name: "alpha", Team: "red", IdentityKey: "id:0"},
			{StartMs: 350_000, EndMs: maxInt32, Name: "bravo", Team: "blue", IdentityKey: "id:1"},
		},
	}})

	streams := a.buildStreamsResult(nil, nil, 0, 700)
	if streams == nil || len(streams.Players) != 2 {
		t.Fatalf("want 2 split players, got %v", streams)
	}
	if !findStream(streams, "alpha") || !findStream(streams, "bravo") {
		t.Fatalf("expected both alpha and bravo, got %+v", streams.Players)
	}
	for _, p := range streams.Players {
		if p.Position == nil {
			continue
		}
		for _, tt := range p.Position.T {
			if p.Name == "alpha" && tt >= 350_000 {
				t.Errorf("alpha got a sample at %d, past the handover", tt)
			}
			if p.Name == "bravo" && tt < 350_000 {
				t.Errorf("bravo got a sample at %d, before the handover", tt)
			}
		}
	}
}

// TestStreams_PhantomSessionDropped: a session with no recorded play
// (a vacated slot taken by a non-player) must not emit a stream.
func TestStreams_PhantomSessionDropped(t *testing.T) {
	a := NewTimelineAnalyzer()
	a.timing.Started = true
	a.playerState[7] = newStreamState(0, 600_000) // real player on slot 7
	a.playerState[9] = &timelinePlayerState{}      // phantom: no play recorded
	a.UseCoreOutputs(&CoreOutputs{Sessions: map[int][]ResolvedSession{
		7: {{StartMs: minInt32, EndMs: maxInt32, Name: "rusti", Team: "jah", IdentityKey: "id:0"}},
		9: {{StartMs: minInt32, EndMs: maxInt32, Name: "Luk", Team: "", IdentityKey: "id:1"}},
	}})

	streams := a.buildStreamsResult(nil, nil, 0, 600)
	if streams == nil || len(streams.Players) != 1 {
		t.Fatalf("want 1 player (phantom dropped), got %v", streams)
	}
	if findStream(streams, "Luk") {
		t.Error("phantom Luk should have been dropped")
	}
}
