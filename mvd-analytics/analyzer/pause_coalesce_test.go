package analyzer

import "testing"

// TestCoalescePauses folds per-idle-frame paused_duration samples into one
// segment per pause: contiguous samples (game clock frozen, so near-identical
// times) sum into one DurationMs anchored at the plateau (latest) game time; a
// real-gameplay gap larger than pauseCoalesceGapSec starts a new segment.
// Mirrors duel-with-pauses.mvd: two ~6.6s pauses, each a leading transition
// frame at a slightly earlier time plus a run at the frozen plateau.
func TestCoalescePauses(t *testing.T) {
	samples := []pauseSample{
		// pause 1: transition frame at 28.455, then plateau at 28.465
		{Time: 28.455, DurationMs: 104},
		{Time: 28.465, DurationMs: 100},
		{Time: 28.465, DurationMs: 105},
		// pause 2 (after ~10s of play): transition at 38.186, plateau at 38.199
		{Time: 38.186, DurationMs: 107},
		{Time: 38.199, DurationMs: 100},
		{Time: 38.199, DurationMs: 102},
	}
	got := coalescePauses(samples)
	if len(got) != 2 {
		t.Fatalf("got %d pauses, want 2: %+v", len(got), got)
	}
	if got[0].AtMs != 28465 || got[0].DurationMs != 309 {
		t.Errorf("pause 1 = %+v, want {AtMs:28465 DurationMs:309}", got[0])
	}
	if got[1].AtMs != 38199 || got[1].DurationMs != 309 {
		t.Errorf("pause 2 = %+v, want {AtMs:38199 DurationMs:309}", got[1])
	}
}

func TestCoalescePausesEmpty(t *testing.T) {
	if got := coalescePauses(nil); got != nil {
		t.Errorf("coalescePauses(nil) = %+v, want nil", got)
	}
}
