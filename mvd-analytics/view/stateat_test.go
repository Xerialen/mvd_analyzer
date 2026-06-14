package view

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func TestStateAtCarryForward(t *testing.T) {
	r := makeStream(t, result.PlayerStream{
		Name: "p1",
		Health: []result.ChangeI16{
			{T: 0, V: 100},
			{T: 5000, V: 50},
		},
		RL: []result.Interval{{Start: 1000, End: 3000}},
	})
	v, err := StateAt(r, StateAtOptions{
		Time:   2.5,
		Fields: []string{FieldHealth, FieldRL},
	})
	if err != nil {
		t.Fatalf("StateAt: %v", err)
	}
	st := v.Players["p1"]
	if st.Health == nil || *st.Health != 100 {
		t.Fatalf("Health at 2.5 = %v, want 100", deref(st.Health))
	}
	if st.RL == nil || *st.RL != true {
		t.Fatalf("RL at 2.5 = %v, want true", st.RL)
	}
}

// StateAt snaps view / hgt / lq to the nearest position sample, and
// omits hgt/lq when the track lacks those columns (no BSP).
func TestStateAtViewHeightLiquid(t *testing.T) {
	r := makeStream(t, result.PlayerStream{
		Name: "p1",
		Position: &result.PositionTrack{
			T:   []int32{0, 1000, 2000},
			X:   []float32{0, 100, 200},
			Y:   []float32{0, 0, 0},
			Z:   []float32{0, 0, 0},
			H:   []float32{5, 40, result.NoFloor},
			Lq:  []int8{0, 5, 7},
			VP:  []int16{10, 20, 30},
			VYa: []int16{-10, -20, -30},
			VX:  []float32{100, 200, 300},
			VY:  []float32{-100, -200, -300},
			VZ:  []float32{1, 2, 3},
		},
	})
	v, err := StateAt(r, StateAtOptions{
		Time:   1.1, // nearest sample is index 1 (t=1000)
		Fields: []string{FieldView, FieldHeight, FieldLiquid, FieldVelocity},
	})
	if err != nil {
		t.Fatalf("StateAt: %v", err)
	}
	st := v.Players["p1"]
	if st.View == nil || st.View.VP != 20 || st.View.VYa != -20 {
		t.Errorf("View at 1.1 = %+v, want {20,-20}", st.View)
	}
	if st.Hgt == nil || *st.Hgt != 40 {
		t.Errorf("Hgt at 1.1 = %v, want 40", deref32(st.Hgt))
	}
	if st.Lq == nil || *st.Lq != 5 {
		t.Errorf("Lq at 1.1 = %v, want 5", st.Lq)
	}
	if st.Vel == nil || st.Vel.VX != 200 || st.Vel.VY != -200 || st.Vel.VZ != 2 {
		t.Errorf("Vel at 1.1 = %+v, want {200,-200,2}", st.Vel)
	}

	// Bare x/y/z track: view present (always recorded), but hgt/lq absent.
	r2 := makeStream(t, result.PlayerStream{
		Name: "p1",
		Position: &result.PositionTrack{
			T:   []int32{0, 1000},
			X:   []float32{0, 100},
			Y:   []float32{0, 0},
			Z:   []float32{0, 0},
			VP:  []int16{1, 2},
			VYa: []int16{3, 4},
		},
	})
	v2, _ := StateAt(r2, StateAtOptions{Time: 0, Fields: []string{FieldView, FieldHeight, FieldLiquid, FieldVelocity}})
	st2 := v2.Players["p1"]
	if st2.View == nil {
		t.Errorf("view should be present on bare track")
	}
	if st2.Hgt != nil || st2.Lq != nil || st2.Vel != nil {
		t.Errorf("hgt/lq/vel must be absent without their columns: hgt=%v lq=%v vel=%v", st2.Hgt, st2.Lq, st2.Vel)
	}
}

func deref32(p *float32) float32 {
	if p == nil {
		return -1
	}
	return *p
}

func TestStateAtBeforeFirstSample(t *testing.T) {
	r := makeStream(t, result.PlayerStream{
		Name:   "p1",
		Health: []result.ChangeI16{{T: 5000, V: 100}},
	})
	v, err := StateAt(r, StateAtOptions{
		Time:   2.0,
		Fields: []string{FieldHealth},
	})
	if err != nil {
		t.Fatalf("StateAt: %v", err)
	}
	st := v.Players["p1"]
	if st.Health != nil {
		t.Fatalf("Health pointer not nil before first sample: got %d", *st.Health)
	}
}

func TestStateAtIntervalBoundary(t *testing.T) {
	r := makeStream(t, result.PlayerStream{
		Name: "p1",
		Quad: []result.Interval{{Start: 1000, End: 2000}},
	})
	// At end boundary (half-open): Time=2.0 should NOT be in interval.
	v, _ := StateAt(r, StateAtOptions{Time: 2.0, Fields: []string{FieldQuad}})
	st := v.Players["p1"]
	if st.Quad == nil || *st.Quad != false {
		t.Fatalf("Quad at end boundary = %v, want false", st.Quad)
	}
	// At start boundary (closed): should be true.
	v, _ = StateAt(r, StateAtOptions{Time: 1.0, Fields: []string{FieldQuad}})
	st = v.Players["p1"]
	if st.Quad == nil || *st.Quad != true {
		t.Fatalf("Quad at start boundary = %v, want true", st.Quad)
	}
}

func TestStateAtSpawnDeathRejected(t *testing.T) {
	r := makeStream(t, result.PlayerStream{Name: "p1"})
	_, err := StateAt(r, StateAtOptions{Time: 1, Fields: []string{FieldSpawns}})
	if err == nil {
		t.Fatalf("expected error for FieldSpawns in StateAt")
	}
}

func TestStateAtLocResolvesName(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{
			Players: []result.PlayerStream{{
				Name: "p1",
				Loc:  []result.ChangeI16{{T: 0, V: 1}, {T: 5000, V: 2}},
			}},
			Global: result.GlobalStream{MatchStart: 0, MatchEnd: 10000},
		},
		TimelineAnalysis: &result.TimelineAnalysisResult{LocTable: []string{"", "rl", "ya"}},
	}
	v, err := StateAt(r, StateAtOptions{Time: 2.5, Fields: []string{FieldLoc}})
	if err != nil {
		t.Fatalf("StateAt: %v", err)
	}
	if got := v.Players["p1"].Loc; got == nil || *got != "rl" {
		t.Fatalf("Loc at 2.5 = %v, want rl", got)
	}
	v, _ = StateAt(r, StateAtOptions{Time: 6, Fields: []string{FieldLoc}})
	if got := v.Players["p1"].Loc; got == nil || *got != "ya" {
		t.Fatalf("Loc at 6 = %v, want ya", got)
	}
	// Index mode → raw LocTable index in Li, Loc nil.
	vi, _ := StateAt(r, StateAtOptions{Time: 2.5, Fields: []string{FieldLoc}, LocIndex: true})
	st := vi.Players["p1"]
	if st.Li == nil || *st.Li != 1 {
		t.Fatalf("Li at 2.5 = %v, want 1", st.Li)
	}
	if st.Loc != nil {
		t.Fatalf("Loc should be nil in index mode, got %v", *st.Loc)
	}
}

func deref(p *int16) int16 {
	if p == nil {
		return -1
	}
	return *p
}
