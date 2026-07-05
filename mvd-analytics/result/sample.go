package result

import "math"

// TrackSample is an interpolated point-in-time sample of a PositionTrack.
//
// X/Y/Z are the world position. VP/VYa are the view angles in raw angle16
// units (decode: deg = v*360/65536; pitch > 180° = up), interpolated on the
// shortest arc so the 360°/0° seam never jumps. VX/VY/VZ are velocity in
// Quake units/sec. HasView / HasVel report whether the optional view-angle /
// velocity columns were present on the track (and thus interpolated); when
// false the corresponding fields are zero.
type TrackSample struct {
	X, Y, Z    float64
	VP, VYa    float64
	VX, VY, VZ float64
	HasView    bool
	HasVel     bool
}

// SampleAt linearly interpolates the track at match-relative time tMs.
//
// Unlike view.StateAt (which snaps to the nearest sample), this interpolates:
// position and velocity are lerped between the two bracketing samples; view
// angles are interpolated on the shortest arc in angle16 space. Queries
// outside the track clamp to the first/last sample (no extrapolation). ok is
// false only for a nil or empty track. Shared so any position-derived
// analytic (aim, and later LOS / the bucket reducers) can sample at an
// arbitrary instant instead of the nearest tick.
func (pt *PositionTrack) SampleAt(tMs int32) (TrackSample, bool) {
	if pt == nil || len(pt.T) == 0 {
		return TrackSample{}, false
	}
	n := len(pt.T)
	hasView := len(pt.VP) == n && len(pt.VYa) == n
	hasVel := len(pt.VX) == n && len(pt.VY) == n && len(pt.VZ) == n
	at := func(i int) TrackSample {
		s := TrackSample{X: float64(pt.X[i]), Y: float64(pt.Y[i]), Z: float64(pt.Z[i]), HasView: hasView, HasVel: hasVel}
		if hasView {
			// angle16 is stored as int16 but read as the unsigned 0..65535
			// turn fraction.
			s.VP = float64(uint16(pt.VP[i]))
			s.VYa = float64(uint16(pt.VYa[i]))
		}
		if hasVel {
			s.VX, s.VY, s.VZ = float64(pt.VX[i]), float64(pt.VY[i]), float64(pt.VZ[i])
		}
		return s
	}
	if tMs <= pt.T[0] {
		return at(0), true
	}
	if tMs >= pt.T[n-1] {
		return at(n - 1), true
	}
	// Largest i0 with T[i0] <= tMs; i1 = i0+1 brackets tMs.
	lo, hi, i0 := 0, n-1, 0
	for lo <= hi {
		mid := (lo + hi) >> 1
		if pt.T[mid] <= tMs {
			i0 = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	i1 := i0 + 1
	span := pt.T[i1] - pt.T[i0]
	if span <= 0 {
		return at(i0), true
	}
	f := float64(tMs-pt.T[i0]) / float64(span)
	a, b := at(i0), at(i1)
	out := TrackSample{
		X:       a.X + (b.X-a.X)*f,
		Y:       a.Y + (b.Y-a.Y)*f,
		Z:       a.Z + (b.Z-a.Z)*f,
		HasView: hasView,
		HasVel:  hasVel,
	}
	if hasView {
		out.VP = lerpAngle16(a.VP, b.VP, f)
		out.VYa = lerpAngle16(a.VYa, b.VYa, f)
	}
	if hasVel {
		out.VX = a.VX + (b.VX-a.VX)*f
		out.VY = a.VY + (b.VY-a.VY)*f
		out.VZ = a.VZ + (b.VZ-a.VZ)*f
	}
	return out, true
}

// lerpAngle16 interpolates between two angle16 values (each in [0,65536))
// along the shortest arc, returning a value in [0,65536). Interpolating the
// raw fraction would jump the long way around the 357°→2° seam.
func lerpAngle16(a, b, f float64) float64 {
	d := math.Mod(b-a, 65536)
	switch {
	case d < -32768:
		d += 65536
	case d > 32768:
		d -= 65536
	}
	r := math.Mod(a+d*f, 65536)
	if r < 0 {
		r += 65536
	}
	return r
}
