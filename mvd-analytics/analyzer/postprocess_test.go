package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// shiftAndFilterPosition must trim every sample-aligned column by the
// same amount when dropping pre-match samples — a column left at its
// old length ships values attributed to the wrong sample, and the
// consumers that guard on len(col) == len(T) (BuildLocGraph,
// view.RegionControl, airgibsPost) silently skip the player.
func TestShiftAndFilterPosition_TrimsAllColumns(t *testing.T) {
	pt := &result.PositionTrack{
		T:  []int32{100, 200, 300, 400},
		X:  []int32{1, 2, 3, 4},
		Y:  []int32{10, 20, 30, 40},
		Z:  []int32{11, 22, 33, 44},
		Li: []int16{5, 6, 7, 8},
		H:  []int32{50, 60, result.NoFloor, 80},
		Lq: []int8{0, 5, 7, 0},
	}
	shiftAndFilterPosition(pt, 300)

	wantT := []int32{0, 100}
	if len(pt.T) != 2 || pt.T[0] != wantT[0] || pt.T[1] != wantT[1] {
		t.Fatalf("T = %v, want %v", pt.T, wantT)
	}
	for name, got := range map[string]int{
		"X": len(pt.X), "Y": len(pt.Y), "Z": len(pt.Z),
		"Li": len(pt.Li), "H": len(pt.H), "Lq": len(pt.Lq),
	} {
		if got != len(pt.T) {
			t.Errorf("len(%s) = %d, want %d (aligned with T)", name, got, len(pt.T))
		}
	}
	if pt.X[0] != 3 || pt.Li[0] != 7 || pt.H[0] != result.NoFloor || pt.H[1] != 80 || pt.Lq[0] != 7 {
		t.Errorf("columns misaligned after trim: X=%v Li=%v H=%v Lq=%v", pt.X, pt.Li, pt.H, pt.Lq)
	}
}

// Optional columns absent (nil) must stay absent rather than being
// sliced into existence or panicking.
func TestShiftAndFilterPosition_AbsentOptionalColumns(t *testing.T) {
	pt := &result.PositionTrack{
		T: []int32{100, 200},
		X: []int32{1, 2},
		Y: []int32{1, 2},
		Z: []int32{1, 2},
	}
	shiftAndFilterPosition(pt, 200)
	if len(pt.T) != 1 || pt.T[0] != 0 {
		t.Fatalf("T = %v, want [0]", pt.T)
	}
	if pt.Li != nil || pt.H != nil || pt.Lq != nil {
		t.Errorf("optional columns materialized: Li=%v H=%v Lq=%v", pt.Li, pt.H, pt.Lq)
	}
}
