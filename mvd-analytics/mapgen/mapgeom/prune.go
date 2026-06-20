package mapgeom

import (
	"math"

	"github.com/mvd-analyzer/mvd-analytics/mapgen/bsp"
)

// usageCell is the dedup/index quantization (world units) for FloorUsage.
// Floor-contact samples are recorded at the engine's ~13ms native rate, so
// a single match yields hundreds of thousands of near-identical points;
// quantizing to an 8-unit grid collapses them to one representative per
// cell while staying well under the prune match tolerances.
const usageCell float32 = 8.0

// FloorUsage is the set of map floor-contact points collected from demos,
// used by BuildPruned to drop floor faces no player ever stood on (or flew
// over). Points are deduplicated onto an 8-unit grid and indexed by their
// quantized XY cell for fast polygon queries. The zero value is unusable;
// construct with NewFloorUsage.
type FloorUsage struct {
	cell  float32
	seen  map[[3]int32]bool        // dedup key (qx,qy,qz)
	byXY  map[[2]int32][]usagePoint // spatial index keyed by (qx,qy)
	demos int
}

type usagePoint struct{ x, y, z float32 }

// NewFloorUsage returns an empty usage accumulator.
func NewFloorUsage() *FloorUsage {
	return &FloorUsage{
		cell: usageCell,
		seen: make(map[[3]int32]bool),
		byXY: make(map[[2]int32][]usagePoint),
	}
}

// AddDemo records that one more demo contributed to this usage set (for
// PruneInfo.Demos provenance only).
func (u *FloorUsage) AddDemo() { u.demos++ }

// AddPoint records a world-space floor-contact point (the supporting
// surface directly beneath a player at one sample). Duplicates that
// quantize to the same 8-unit cell are dropped.
func (u *FloorUsage) AddPoint(x, y, z float32) {
	key := [3]int32{u.q(x), u.q(y), u.q(z)}
	if u.seen[key] {
		return
	}
	u.seen[key] = true
	xy := [2]int32{key[0], key[1]}
	u.byXY[xy] = append(u.byXY[xy], usagePoint{x, y, z})
}

// Points is the number of distinct (deduplicated) floor-contact points.
func (u *FloorUsage) Points() int { return len(u.seen) }

// Demos is the number of demos that contributed (see AddDemo).
func (u *FloorUsage) Demos() int { return u.demos }

func (u *FloorUsage) q(v float32) int32 {
	return int32(math.Floor(float64(v / u.cell)))
}

// matches reports whether any recorded floor-contact point lands on the
// floor face described by ring (its XY polygon) and plane (the face's BSP
// plane, used to evaluate the face Z at a queried XY). A point matches when
// it is within xyTol of the polygon in XY AND the face's plane Z at the
// nearest polygon point is within zTol of the point's Z. plane must be the
// original (non-side-negated) BSP plane so its equation holds.
func (u *FloorUsage) matches(ring []bsp.Vec3, plane bsp.Plane, xyTol, zTol float32) bool {
	if len(ring) < 3 || len(u.byXY) == 0 {
		return false
	}
	// Polygon XY bounding box, expanded by the XY tolerance.
	minX, minY := ring[0].X, ring[0].Y
	maxX, maxY := ring[0].X, ring[0].Y
	for _, v := range ring[1:] {
		minX = minF(minX, v.X)
		minY = minF(minY, v.Y)
		maxX = maxF(maxX, v.X)
		maxY = maxF(maxY, v.Y)
	}
	qx0, qx1 := u.q(minX-xyTol), u.q(maxX+xyTol)
	qy0, qy1 := u.q(minY-xyTol), u.q(maxY+xyTol)
	for qx := qx0; qx <= qx1; qx++ {
		for qy := qy0; qy <= qy1; qy++ {
			for _, pt := range u.byXY[[2]int32{qx, qy}] {
				d, cx, cy := distPointToPolygonXY(pt.x, pt.y, ring)
				if d > xyTol {
					continue
				}
				z, ok := planeZAt(plane, cx, cy)
				if !ok {
					continue
				}
				if absF(z-pt.z) <= zTol {
					return true
				}
			}
		}
	}
	return false
}

// planeZAt evaluates a BSP plane's Z at (x,y): nx*x+ny*y+nz*z = dist. ok is
// false when the plane is too near vertical to have a single Z (never the
// case for floor faces, whose normal Z >= FloorNormalZ).
func planeZAt(p bsp.Plane, x, y float32) (float32, bool) {
	if absF(p.Normal.Z) < 1e-4 {
		return 0, false
	}
	return (p.Dist - p.Normal.X*x - p.Normal.Y*y) / p.Normal.Z, true
}

// distPointToPolygonXY returns the 2D distance from (px,py) to the polygon
// ring (XY of each vertex) and the nearest point on/in it. Inside the
// polygon the distance is 0 and the nearest point is (px,py); outside it is
// the closest point on the nearest edge.
func distPointToPolygonXY(px, py float32, ring []bsp.Vec3) (dist, nx, ny float32) {
	if pointInPolygonXY(px, py, ring) {
		return 0, px, py
	}
	best := float32(math.MaxFloat32)
	bx, by := px, py
	for i := 0; i < len(ring); i++ {
		a := ring[i]
		b := ring[(i+1)%len(ring)]
		cx, cy := closestOnSegment(px, py, a.X, a.Y, b.X, b.Y)
		dx, dy := px-cx, py-cy
		if d := dx*dx + dy*dy; d < best {
			best = d
			bx, by = cx, cy
		}
	}
	return float32(math.Sqrt(float64(best))), bx, by
}

// closestOnSegment returns the point on segment (ax,ay)-(bx,by) closest to
// (px,py).
func closestOnSegment(px, py, ax, ay, bx, by float32) (float32, float32) {
	dx, dy := bx-ax, by-ay
	len2 := dx*dx + dy*dy
	if len2 == 0 {
		return ax, ay
	}
	t := ((px-ax)*dx + (py-ay)*dy) / len2
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return ax + t*dx, ay + t*dy
}

// pointInPolygonXY is a standard even-odd ray cast in the XY plane.
func pointInPolygonXY(px, py float32, ring []bsp.Vec3) bool {
	in := false
	n := len(ring)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := ring[i].X, ring[i].Y
		xj, yj := ring[j].X, ring[j].Y
		if (yi > py) != (yj > py) {
			xc := (xj-xi)*(py-yi)/(yj-yi) + xi
			if px < xc {
				in = !in
			}
		}
	}
	return in
}

func minF(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func maxF(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func absF(a float32) float32 {
	if a < 0 {
		return -a
	}
	return a
}
