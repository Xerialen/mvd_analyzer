package view_test

// Real-demo correctness check for the windowed aim recompute. Lives in the
// external test package because it drives the analyzer pipeline (which imports
// view) to build a realistic result.Result. Skips offline when the corpus
// cache is absent.

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/view"
)

// A windowed aim recompute must count exactly the player's shots that fall in
// the window — hand-derivable from res.Shots.Shots — and its crosshair samples
// must all lie inside the window. This pins the "recompute over the windowed
// shot slice" contract against a real demo.
func TestAimWindowMatchesShotCounts(t *testing.T) {
	res := loadDemo(t, corpus1on1)
	if res.Aim == nil || res.Shots == nil || len(res.Aim.Players) == 0 {
		t.Skip("demo has no aim/shots")
	}

	// Pick a mid-match window [t0, t1] in ms, then in seconds for the view.
	// Use the first and last shot times to bracket a real interior slice.
	var lo, hi int32 = 1<<31 - 1, 0
	for _, s := range res.Shots.Shots {
		if s.Time < lo {
			lo = s.Time
		}
		if s.Time > hi {
			hi = s.Time
		}
	}
	if hi <= lo {
		t.Skip("degenerate shot time range")
	}
	// A window covering the middle 50% of the match.
	span := hi - lo
	t0 := lo + span/4
	t1 := hi - span/4
	fromSec := float64(t0) / 1000.0
	toSec := float64(t1) / 1000.0

	got, err := view.Aim(res, view.AimOptions{From: fromSec, To: toSec})
	if err != nil {
		t.Fatalf("windowed Aim: %v", err)
	}

	// secToMs rounds to nearest ms; reproduce the exact window bounds the view
	// used so the hand-derived shot counts line up with the recompute.
	startMs := int32(fromSec*1000 + 0.5)
	endMs := int32(toSec*1000 + 0.5)

	// For every player in the windowed result, the per-weapon Shots must equal
	// the count of that player's shots of that weapon inside [startMs,endMs].
	for _, pa := range got.Players {
		want := map[string]int{}
		for _, s := range res.Shots.Shots {
			if s.Player != pa.Player || s.Time < startMs || s.Time > endMs {
				continue
			}
			want[s.Weapon]++
		}
		for _, wa := range pa.Weapons {
			if wa.Shots != want[wa.Weapon] {
				t.Errorf("player %s weapon %s: windowed Shots=%d, want %d (hand-counted in-window fires)",
					pa.Player, wa.Weapon, wa.Shots, want[wa.Weapon])
			}
		}
		// Every crosshair sample lies inside the window.
		if pa.Crosshair != nil {
			for i, tt := range pa.Crosshair.T {
				if tt < startMs || tt > endMs {
					t.Errorf("player %s crosshair sample %d at t=%d is outside [%d,%d]",
						pa.Player, i, tt, startMs, endMs)
				}
			}
		}
	}
}

// The unfiltered view.Aim (no window) must return exactly the stored aim.
func TestAimUnfilteredEqualsStoredRealDemo(t *testing.T) {
	res := loadDemo(t, corpus1on1)
	if res.Aim == nil {
		t.Skip("demo has no aim")
	}
	got, err := view.Aim(res, view.AimOptions{})
	if err != nil {
		t.Fatalf("Aim: %v", err)
	}
	if got != res.Aim {
		t.Fatalf("unfiltered Aim did not return the stored pointer")
	}
}
