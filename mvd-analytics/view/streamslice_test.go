package view

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func TestStreamSliceCarryForward(t *testing.T) {
	r := makeStream(t, result.PlayerStream{
		Name: "p1",
		Health: []result.ChangeI16{
			{T: 0, V: 100},
			{T: 5000, V: 50},
		},
	})
	v, err := StreamSlice(r, StreamSliceOptions{
		StartTime: 2,
		EndTime:   4,
		Fields:    []string{FieldHealth},
	}) // StartTime/EndTime are seconds (public API)
	if err != nil {
		t.Fatalf("StreamSlice: %v", err)
	}
	if len(v.Players) != 1 {
		t.Fatalf("len players = %d, want 1", len(v.Players))
	}
	h := v.Players[0].Health
	// Window has no native entry; carry-forward synthesises one at
	// StartTime (2000 ms in schema v8).
	if len(h) != 1 || h[0].T != 2000 || h[0].V != 100 {
		t.Fatalf("expected 1 entry at t=2000ms v=100, got %+v", h)
	}
}

func TestStreamSliceIntervalClamping(t *testing.T) {
	r := makeStream(t, result.PlayerStream{
		Name: "p1",
		RL:   []result.Interval{{Start: 1000, End: 6000}},
	})
	v, err := StreamSlice(r, StreamSliceOptions{
		StartTime: 2,
		EndTime:   4,
		Fields:    []string{FieldRL},
	}) // StartTime/EndTime are seconds (public API)
	if err != nil {
		t.Fatalf("StreamSlice: %v", err)
	}
	rl := v.Players[0].RL
	if len(rl) != 1 {
		t.Fatalf("len rl = %d, want 1", len(rl))
	}
	// Clamped to [2000, 4000) ms (schema v8: Interval is int32 ms).
	if rl[0].Start != 2000 || rl[0].End != 4000 {
		t.Fatalf("clamped interval = %+v, want [2000,4000)", rl[0])
	}
}

func TestStreamSlicePosition(t *testing.T) {
	r := makeStream(t, result.PlayerStream{
		Name: "p1",
		Position: &result.PositionTrack{
			// Schema v8: T is int32 ms. Samples at 0, 1s, 2s, 3s, 4s.
			T: []int32{0, 1000, 2000, 3000, 4000},
			X: []int32{0, 100, 200, 300, 400},
			Y: []int32{0, 0, 0, 0, 0},
			Z: []int32{0, 0, 0, 0, 0},
		},
	})
	v, err := StreamSlice(r, StreamSliceOptions{
		StartTime: 1.5,
		EndTime:   3.5,
		Fields:    []string{FieldPosition},
	})
	if err != nil {
		t.Fatalf("StreamSlice: %v", err)
	}
	pos := v.Players[0].Position
	if pos == nil {
		t.Fatalf("Position nil")
	}
	// Should include samples at t=2 and t=3 (i.e. 2000 ms, 3000 ms).
	if len(pos.T) != 2 {
		t.Fatalf("len pos = %d, want 2", len(pos.T))
	}
	if pos.X[0] != 200 || pos.X[1] != 300 {
		t.Fatalf("positions = %v, want [200, 300]", pos.X)
	}
	if pos.T[0] != 2000 || pos.T[1] != 3000 {
		t.Fatalf("pos.T = %v, want [2000, 3000]", pos.T)
	}
}

// Clean break (schema v31): pos carries t/x/y/z and the per-sample loc
// label li, but NOT height/liquid — those project into the hgt / lq
// sibling tracks, and view direction into the view track. Each track
// stays absent when the source doesn't carry its column.
func TestStreamSliceColumnProjection(t *testing.T) {
	r := makeStream(t, result.PlayerStream{
		Name: "p1",
		Position: &result.PositionTrack{
			T:   []int32{0, 1000, 2000, 3000, 4000},
			X:   []int32{0, 100, 200, 300, 400},
			Y:   []int32{0, 0, 0, 0, 0},
			Z:   []int32{0, 0, 0, 0, 0},
			Li:  []int16{1, 2, 3, 4, 5},
			H:   []int32{0, 10, result.NoFloor, 30, 40},
			Lq:  []int8{0, 0, 5, 6, 7},
			VP:  []int16{0, 100, 200, 300, 400},
			VYa: []int16{0, -100, -200, -300, -400},
			VX:  []int32{0, 11, 22, 33, 44},
			VY:  []int32{0, -11, -22, -33, -44},
			VZ:  []int32{0, 1, 2, 3, 4},
		},
	})
	v, err := StreamSlice(r, StreamSliceOptions{
		StartTime: 1.5,
		EndTime:   3.5,
		Fields:    []string{FieldPosition, FieldHeight, FieldLiquid, FieldView, FieldVelocity},
	})
	if err != nil {
		t.Fatalf("StreamSlice: %v", err)
	}
	sl := v.Players[0]

	// pos: t/x/y/z + li only. H/Lq must NOT ride along pos anymore.
	pos := sl.Position
	if pos == nil {
		t.Fatalf("Position nil")
	}
	if len(pos.Li) != len(pos.T) {
		t.Fatalf("pos li not aligned: t=%d li=%d", len(pos.T), len(pos.Li))
	}
	if pos.Li[0] != 3 || pos.Li[1] != 4 {
		t.Errorf("pos.Li = %v, want [3, 4]", pos.Li)
	}
	if pos.H != nil || pos.Lq != nil || pos.VP != nil || pos.VYa != nil {
		t.Errorf("pos must be strict x/y/z(+li); got H=%v Lq=%v VP=%v VYa=%v", pos.H, pos.Lq, pos.VP, pos.VYa)
	}

	// hgt: t + h.
	if hgt := sl.Height; hgt == nil || len(hgt.H) != len(hgt.T) || hgt.H[0] != result.NoFloor || hgt.H[1] != 30 {
		t.Errorf("hgt track = %+v, want H=[NoFloor, 30]", hgt)
	}
	// lq: t + lq.
	if lq := sl.Liquid; lq == nil || len(lq.Lq) != len(lq.T) || lq.Lq[0] != 5 || lq.Lq[1] != 6 {
		t.Errorf("lq track = %+v, want Lq=[5, 6]", lq)
	}
	// view: t + vp/vya.
	vw := sl.View
	if vw == nil || len(vw.VP) != len(vw.T) || len(vw.VYa) != len(vw.T) {
		t.Fatalf("view track not aligned: %+v", vw)
	}
	if vw.VP[0] != 200 || vw.VP[1] != 300 || vw.VYa[0] != -200 || vw.VYa[1] != -300 {
		t.Errorf("view = vp%v vya%v, want vp[200,300] vya[-200,-300]", vw.VP, vw.VYa)
	}
	// vel: t + vx/vy/vz.
	vel := sl.Velocity
	if vel == nil || len(vel.VX) != len(vel.T) || len(vel.VY) != len(vel.T) || len(vel.VZ) != len(vel.T) {
		t.Fatalf("velocity track not aligned: %+v", vel)
	}
	if vel.VX[0] != 22 || vel.VX[1] != 33 || vel.VZ[0] != 2 {
		t.Errorf("vel = vx%v vz%v, want vx[22,33] vz[2,3]", vel.VX, vel.VZ)
	}

	// A bare x/y/z track: pos carries no li, and hgt/lq/view stay absent.
	r2 := makeStream(t, result.PlayerStream{
		Name: "p1",
		Position: &result.PositionTrack{
			T: []int32{0, 1000},
			X: []int32{0, 100},
			Y: []int32{0, 0},
			Z: []int32{0, 0},
		},
	})
	v2, err := StreamSlice(r2, StreamSliceOptions{
		StartTime: 0,
		EndTime:   2,
		Fields:    []string{FieldPosition, FieldHeight, FieldLiquid, FieldView, FieldVelocity},
	})
	if err != nil {
		t.Fatalf("StreamSlice: %v", err)
	}
	sl2 := v2.Players[0]
	if sl2.Position == nil || sl2.Position.Li != nil {
		t.Errorf("li materialized on bare track: %+v", sl2.Position)
	}
	if sl2.Height != nil || sl2.Liquid != nil || sl2.View != nil || sl2.Velocity != nil {
		t.Errorf("hgt/lq/view/vel materialized on bare track: hgt=%v lq=%v view=%v vel=%v",
			sl2.Height, sl2.Liquid, sl2.View, sl2.Velocity)
	}
}

func TestStreamSliceLocResolvesNames(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{
			Players: []result.PlayerStream{{
				Name: "p1",
				Loc:  []result.ChangeI16{{T: 0, V: 1}, {T: 3000, V: 2}, {T: 7000, V: 1}},
			}},
			Global: result.GlobalStream{MatchStart: 0, MatchEnd: 10000},
		},
		TimelineAnalysis: &result.TimelineAnalysisResult{LocTable: []string{"", "rl", "ya"}},
	}
	v, err := StreamSlice(r, StreamSliceOptions{StartTime: 0, EndTime: 10, Fields: []string{FieldLoc}})
	if err != nil {
		t.Fatalf("StreamSlice: %v", err)
	}
	loc := v.Players[0].Loc
	wantV := []string{"rl", "ya", "rl"}
	wantT := []int32{0, 3000, 7000}
	if len(loc) != len(wantV) {
		t.Fatalf("got %d loc entries, want %d: %+v", len(loc), len(wantV), loc)
	}
	for i := range wantV {
		if loc[i].V != wantV[i] || loc[i].T != wantT[i] {
			t.Fatalf("loc[%d] = {T:%d V:%q}, want {T:%d V:%q}", i, loc[i].T, loc[i].V, wantT[i], wantV[i])
		}
	}
	// Index mode → raw int16 index stream under Li, Loc empty.
	vi, _ := StreamSlice(r, StreamSliceOptions{StartTime: 0, EndTime: 10, Fields: []string{FieldLoc}, LocIndex: true})
	li := vi.Players[0].Li
	wantI := []int16{1, 2, 1}
	if len(li) != len(wantI) {
		t.Fatalf("got %d li entries, want %d: %+v", len(li), len(wantI), li)
	}
	for i := range wantI {
		if li[i].V != wantI[i] {
			t.Fatalf("li[%d].V = %d, want %d", i, li[i].V, wantI[i])
		}
	}
	if vi.Players[0].Loc != nil {
		t.Fatalf("Loc should be nil in index mode, got %+v", vi.Players[0].Loc)
	}
}
