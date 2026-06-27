package bspvis

// RayHitsSolid reports whether the segment a->c crosses any
// CONTENTS_SOLID leaf in the visibility BSP. Both endpoints are world-
// unit coordinates (same scale as the loc-attribution pipeline).
//
// This is a boolean specialisation of the standard Quake
// SV_RecursiveHullCheck (mvdsv/src/cmodel.c:RecursiveHullTrace, lines
// 222-339): we descend the visibility tree, split the segment at plane
// crossings, and report "hit" the moment any traversed leaf has
// CONTENTS_SOLID. The full engine version tracks the impact point,
// surface normal, and a DIST_EPSILON nudge; for line-of-sight we don't
// need any of that.
//
// Liquid leaves (water/slime/lava) do not block — only CONTENTS_SOLID.
// A segment that starts inside solid returns true (start-solid is a
// "hit" by definition here).
func (b *BSP) RayHitsSolid(a, c [3]float32) bool {
	if len(b.Models) == 0 || len(b.Nodes) == 0 {
		return true
	}
	return b.segHitsSolid(b.Models[0].HeadNodes[0], a, c)
}

// segHitsSolid is the recursive trace. nodeIdx >= 0 selects an interior
// node; nodeIdx < 0 selects leaf -1 - nodeIdx.
//
// Sign convention here differs from PointInLeaf: hull trace uses
// `>= 0 -> front` (not `> 0`), matching cmodel.c:RecursiveHullTrace and
// SV_RecursiveHullCheck in WinQuake/world.c. The `=` matters when an
// endpoint lies exactly on a splitting plane — the engine biases such
// rays to the front child.
func (b *BSP) segHitsSolid(nodeIdx int32, p1, p2 [3]float32) bool {
	if nodeIdx < 0 {
		leafIdx := int(-1 - nodeIdx)
		if leafIdx < 0 || leafIdx >= len(b.Leaves) {
			return true
		}
		return b.Leaves[leafIdx].Contents == ContentsSolid
	}
	if int(nodeIdx) >= len(b.Nodes) {
		return true
	}
	n := &b.Nodes[nodeIdx]
	if int(n.PlaneID) >= len(b.Planes) {
		return true
	}
	pl := &b.Planes[n.PlaneID]
	t1 := pl.Normal.X*p1[0] + pl.Normal.Y*p1[1] + pl.Normal.Z*p1[2] - pl.Dist
	t2 := pl.Normal.X*p2[0] + pl.Normal.Y*p2[1] + pl.Normal.Z*p2[2] - pl.Dist

	if t1 >= 0 && t2 >= 0 {
		return b.segHitsSolid(n.Children[0], p1, p2)
	}
	if t1 < 0 && t2 < 0 {
		return b.segHitsSolid(n.Children[1], p1, p2)
	}

	denom := t1 - t2
	frac := float32(0.5)
	if denom != 0 {
		frac = t1 / denom
	}
	if frac < 0 {
		frac = 0
	} else if frac > 1 {
		frac = 1
	}
	mid := [3]float32{
		p1[0] + frac*(p2[0]-p1[0]),
		p1[1] + frac*(p2[1]-p1[1]),
		p1[2] + frac*(p2[2]-p1[2]),
	}

	near, far := int32(0), int32(1)
	if t1 < 0 {
		near, far = 1, 0
	}
	if b.segHitsSolid(n.Children[near], p1, mid) {
		return true
	}
	return b.segHitsSolid(n.Children[far], mid, p2)
}

// RayHitsSolidModel reports whether the segment a->c (world coords) crosses
// a CONTENTS_SOLID leaf of brush submodel modelIdx posed at world origin —
// the test for "is a sightline blocked by this door / lift / plat / train at
// its current pose". It is RayHitsSolid for an inline brush model instead of
// worldspawn (Models[0]).
//
// modelIdx is the BSP models-lump index (the "*N" the entity carries as its
// model; MoverStream.SubModel holds exactly this N). The trace runs in the
// model's local frame — endpoints minus origin — because an inline model's
// planes are compiled around a zero origin and the entity origin is the
// translation it has been moved by (mirrors mapclip.Hull.FloorBelowAt and the
// engine's SV_ClipMoveToEntity, which transforms the trace into the model's
// frame). origin is the mover's current world origin (zero for a brush at
// rest).
//
// HeadNodes[0] (the visibility / point hull) is the correct geometry: a
// sightline is a zero-width ray, so the point hull is what the engine's
// traceline consults against bmodels — not the player-box-inflated clip hull.
// A cheap posed-AABB reject returns false early when the segment can't reach
// the model. Out-of-range modelIdx never blocks. Liquid leaves don't block,
// only CONTENTS_SOLID; a segment that starts inside the posed solid returns
// true (same start-solid convention as RayHitsSolid).
func (b *BSP) RayHitsSolidModel(modelIdx int32, origin, a, c [3]float32) bool {
	if modelIdx < 0 || int(modelIdx) >= len(b.Models) {
		return false
	}
	m := &b.Models[modelIdx]
	mins := [3]float32{m.Mins.X + origin[0], m.Mins.Y + origin[1], m.Mins.Z + origin[2]}
	maxs := [3]float32{m.Maxs.X + origin[0], m.Maxs.Y + origin[1], m.Maxs.Z + origin[2]}
	if !segmentHitsAABB(a, c, mins, maxs) {
		return false
	}
	la := [3]float32{a[0] - origin[0], a[1] - origin[1], a[2] - origin[2]}
	lc := [3]float32{c[0] - origin[0], c[1] - origin[1], c[2] - origin[2]}
	return b.segHitsSolid(m.HeadNodes[0], la, lc)
}

// segmentHitsAABB reports whether segment p0->p1 intersects the axis-aligned
// box [min,max] (slab method; endpoints inside count as a hit). Used only as
// a broadphase reject before the exact hull trace, so the box is padded a
// touch outward to keep a grazing ray from being false-rejected.
func segmentHitsAABB(p0, p1, min, max [3]float32) bool {
	const pad = 1.0
	tmin, tmax := float32(0), float32(1)
	for i := 0; i < 3; i++ {
		lo, hi := min[i]-pad, max[i]+pad
		d := p1[i] - p0[i]
		if d > -1e-6 && d < 1e-6 {
			// Segment runs parallel to this slab; reject when it lies outside.
			if p0[i] < lo || p0[i] > hi {
				return false
			}
			continue
		}
		inv := 1 / d
		t1, t2 := (lo-p0[i])*inv, (hi-p0[i])*inv
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		if t1 > tmin {
			tmin = t1
		}
		if t2 < tmax {
			tmax = t2
		}
		if tmin > tmax {
			return false
		}
	}
	return true
}
