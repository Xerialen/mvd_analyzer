package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-reader/events"
)

func TestBuildMoverStreams(t *testing.T) {
	a := &TimelineAnalyzer{}
	// Two movers fed out of entity-number order; output must be sorted.
	a.handleMoverSpawn(&events.MoverSpawnEvent{EntNum: 5, SubModel: 2, TimeMs: 100, Origin: [3]float32{10, 20, 30}})
	a.handleMoverState(&events.MoverStateEvent{EntNum: 5, TimeMs: 200, Origin: [3]float32{10, 20, 64}, Visible: true})
	a.handleMoverSpawn(&events.MoverSpawnEvent{EntNum: 3, SubModel: 1, TimeMs: 50, Origin: [3]float32{0, 0, 0}})

	ms := a.buildMoverStreams()
	if len(ms) != 2 {
		t.Fatalf("got %d mover streams, want 2", len(ms))
	}
	if ms[0].EntNum != 3 || ms[1].EntNum != 5 {
		t.Errorf("entnum order = [%d %d], want [3 5] (sorted)", ms[0].EntNum, ms[1].EntNum)
	}

	m5 := ms[1]
	if m5.SubModel != 2 {
		t.Errorf("ent5 SubModel = %d, want 2", m5.SubModel)
	}
	if len(m5.T) != 2 || m5.T[0] != 100 || m5.T[1] != 200 {
		t.Errorf("ent5 T = %v, want [100 200]", m5.T)
	}
	if m5.Z[0] != 30 || m5.Z[1] != 64 {
		t.Errorf("ent5 Z = %v, want [30 64]", m5.Z)
	}
	if !m5.Vis[0] || !m5.Vis[1] {
		t.Errorf("ent5 Vis = %v, want [true true]", m5.Vis)
	}
}

func TestShiftAndClampMoverStream(t *testing.T) {
	// States at 0/5000/15000, match starts at 10000: the latest pre-match
	// state (5000) clamps to t=0 carrying its pose, the earlier 0 state is
	// dropped, and 15000 shifts to 5000.
	m := &result.MoverStream{
		EntNum: 1, SubModel: 1,
		T:   []int32{0, 5000, 15000},
		X:   []float32{1, 2, 3},
		Y:   []float32{1, 2, 3},
		Z:   []float32{10, 20, 30},
		Vis: []bool{true, true, false},
	}
	shiftAndClampMoverStream(m, 10000)

	if len(m.T) != 2 || m.T[0] != 0 || m.T[1] != 5000 {
		t.Fatalf("T = %v, want [0 5000]", m.T)
	}
	if m.Z[0] != 20 || m.Z[1] != 30 {
		t.Errorf("Z = %v, want [20 30] (pose from 5000 then 15000)", m.Z)
	}
	if m.Vis[0] != true || m.Vis[1] != false {
		t.Errorf("Vis = %v, want [true false]", m.Vis)
	}
}

func TestShiftAndClampMoverStream_ParkedBaseline(t *testing.T) {
	// A parked mover's only state is its demo-open baseline (pre-match):
	// it must survive, clamped to t=0.
	m := &result.MoverStream{
		T: []int32{0}, X: []float32{1}, Y: []float32{2}, Z: []float32{3}, Vis: []bool{true},
	}
	shiftAndClampMoverStream(m, 10000)
	if len(m.T) != 1 || m.T[0] != 0 || m.Z[0] != 3 {
		t.Errorf("parked: T=%v Z=%v, want [0]/[3] (baseline kept at t=0)", m.T, m.Z)
	}
}

func TestShiftAndClampMoverStream_FirstSeenMidMatch(t *testing.T) {
	// A mover whose first wire state is after match start keeps everything,
	// shifted normally (no clamp).
	m := &result.MoverStream{
		T: []int32{12000, 14000}, X: []float32{1, 1}, Y: []float32{2, 2}, Z: []float32{3, 4}, Vis: []bool{true, true},
	}
	shiftAndClampMoverStream(m, 10000)
	if len(m.T) != 2 || m.T[0] != 2000 || m.T[1] != 4000 {
		t.Errorf("mid-match: T=%v, want [2000 4000]", m.T)
	}
}
