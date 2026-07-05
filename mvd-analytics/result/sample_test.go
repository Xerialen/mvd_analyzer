package result

import (
	"math"
	"testing"
)

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestPositionTrackSampleAt(t *testing.T) {
	pt := &PositionTrack{
		T:   []int32{0, 100, 200},
		X:   []float32{0, 10, 30},
		Y:   []float32{0, 0, 0},
		Z:   []float32{0, 0, 0},
		VP:  []int16{0, 0, 0},
		VYa: []int16{0, 1000, 2000},
		VX:  []float32{0, 100, 100},
		VY:  []float32{0, 0, 0},
		VZ:  []float32{0, 0, 0},
	}

	// Empty/nil track → not ok.
	if _, ok := (&PositionTrack{}).SampleAt(50); ok {
		t.Fatal("empty track should return ok=false")
	}

	// Exact sample time returns that sample.
	s, ok := pt.SampleAt(100)
	if !ok || !approx(s.X, 10, 1e-9) || !approx(s.VYa, 1000, 1e-9) {
		t.Fatalf("exact-sample mismatch: %+v ok=%v", s, ok)
	}

	// Midpoint interpolates position and view yaw.
	s, _ = pt.SampleAt(150)
	if !approx(s.X, 20, 1e-6) { // halfway between 10 and 30
		t.Fatalf("midpoint X = %v, want 20", s.X)
	}
	if !approx(s.VYa, 1500, 1e-6) { // halfway between 1000 and 2000
		t.Fatalf("midpoint VYa = %v, want 1500", s.VYa)
	}
	if !s.HasView || !s.HasVel {
		t.Fatalf("expected HasView/HasVel, got %+v", s)
	}

	// Clamp before first and after last (no extrapolation).
	if s, _ = pt.SampleAt(-50); !approx(s.X, 0, 1e-9) {
		t.Fatalf("pre-clamp X = %v, want 0", s.X)
	}
	if s, _ = pt.SampleAt(9999); !approx(s.X, 30, 1e-9) {
		t.Fatalf("post-clamp X = %v, want 30", s.X)
	}
}

func TestPositionTrackSampleAtAngleSeam(t *testing.T) {
	// 65000 (~357°) → 500 (~2.7°): shortest arc crosses the 0/360 seam, so
	// the midpoint must land near 65518 (~359.8°), NOT ~32750 (~180°).
	a16 := func(u uint16) int16 { return int16(u) } // runtime cast (const would overflow)
	pt := &PositionTrack{
		T:   []int32{0, 100},
		X:   []float32{0, 0},
		Y:   []float32{0, 0},
		Z:   []float32{0, 0},
		VP:  []int16{0, 0},
		VYa: []int16{a16(65000), a16(500)},
	}
	s, _ := pt.SampleAt(50)
	if !approx(s.VYa, 65518, 1.0) {
		t.Fatalf("seam midpoint VYa = %v, want ~65518", s.VYa)
	}
}

func TestLerpAngle16(t *testing.T) {
	if got := lerpAngle16(0, 16384, 0.5); !approx(got, 8192, 1e-9) {
		t.Fatalf("lerpAngle16(0,16384,.5) = %v, want 8192", got)
	}
	// 100 → 65436: shortest arc is a -200 step down through the seam, so the
	// midpoint is exactly 0 (= 65536), not the long-way 32768.
	if got := lerpAngle16(100, 65436, 0.5); !approx(got, 0, 1e-6) {
		t.Fatalf("lerpAngle16(100,65436,.5) = %v, want 0", got)
	}
}
