// Package mapclip is the static per-map collision-hull corpus: the
// player clip hull (hull 1) of a Quake map's worldspawn, distilled to
// just what a straight-down "what floor is under this point" trace
// needs. Generated offline from BSP CLIPNODES by cmd/mapgen and loaded
// at analyze time by map name, mirroring the loc / mapents corpora.
//
// Why a clip hull and not the rendering floor faces: the engine stands
// a player on the *collision* hull, which is the rendering geometry
// inflated by the 32×32×56 player box. Tracing that hull reproduces
// exactly what the server does in PM_CategorizePosition
// (mvdsv/src/pmove.c) — width-aware, multi-level, slope-correct — so a
// player on a narrow ledge, on the chandelier-height geometry in
// schloss, or mid-rocket-jump over a pit gets the right floor with no
// edge artifacts. See mvdsv/src/cmodel.c RecursiveHullTrace for the
// algorithm this file ports.
//
// Scope: a Hull is one BSP model's clip hull. The worldspawn hull
// (model 0) does not contain brush-model entities — the dm2 RA/quad
// lift, func_door, func_train are separate submodels pruned out when
// it is built — so a worldspawn-only trace under a player riding one
// reads the static floor beneath the platform. Those entities' own
// hulls are built per submodel (BuildModel) and posed at their
// demo-streamed origins by the scene query (scene.go,
// HeightAboveFloorBoxScene), mirroring the client's physent setup in
// CL_SetSolidEntities (ezquake cl_ents.c).
//
// The hulls are built straight from the map's provisioned BSP at
// analyze time (see loader.go / mapbsp), so there is no separate corpus
// to ship or keep in sync — a map update is just a new .bsp.
package mapclip

// BSP clip-hull leaf contents codes (negative child values in a clip
// tree). Mirrors mvdsv/src/bspfile.h — only the two we branch on are
// named; any other negative value is treated as non-solid open space.
const (
	contentsEmpty = -1
	contentsSolid = -2
)

const (
	// playerFeetOffset is -mins.z of the standard Quake player hull
	// ({-16,-16,-24}..{16,16,32}, mvdsv/src/cmodel.c:673). A straight-
	// down hull-1 trace stops the player *origin* one DIST_EPSILON above
	// the floor plane; the floor surface itself is playerFeetOffset below
	// that origin. FloorBelow subtracts it so the returned value is the
	// real floor Z, and a grounded player satisfies origin.z - floorZ ≈
	// playerFeetOffset.
	playerFeetOffset = 24.0

	// footprintMargin is how far from the origin HeightAboveFloorBox
	// samples floor columns. The traced hull is already the world inflated
	// by the ±16 player box (cmodel.c:673), so the point trace at the
	// origin alone is the true 32-wide box; this margin only adds a small
	// safety band on top. At 8 the effective reach is as if the box were
	// 48 wide (16 true + 8 margin per side) — enough to keep a player
	// skimming a ledge / well rim a few units past the box edge attached
	// to the rim, without over-reaching to unrelated geometry.
	footprintMargin = 8.0

	// distEpsilon matches DIST_EPSILON in mvdsv/src/cmodel.c:212 — the
	// 1/32-unit nudge that keeps the impact point just off the plane.
	distEpsilon = 0.03125

	// traceMargin extends the downward trace a little past the world's
	// lowest point so geometry sitting exactly on the bounding box still
	// registers as a hit rather than the ray ending in mid-air.
	traceMargin = 64.0

	// startSolidNudge is how far FloorBelow lifts an embedded trace
	// start for one retry. Stream positions are stored as truncated
	// int32 world units (streamBuilder.posX/Y/Z), so a grounded origin
	// over a fractional-Z hull surface — a slope, a ramp lip — can
	// reconstruct up to one unit inside the inflated hull and read
	// start-solid where the live player stood in open space. One unit
	// of lift recovers exactly that class; a start more than a unit
	// deep is genuinely embedded and still reports no floor.
	startSolidNudge = 1.0

	// playerBoxHalf / playerBoxTop are the remaining dimensions of the
	// standard player hull ({-16,-16,-24}..{16,16,32}): the half-width
	// the clip hull is inflated by sideways, and +maxs.z, the depth
	// solid extends downward past raw geometry. Used by the scene
	// query's posed-AABB fast reject (scene.go).
	playerBoxHalf = 16.0
	playerBoxTop  = 32.0
)

// plane is a renumbered collision plane. typ < 3 marks an axis-aligned
// plane (X/Y/Z) for the fast distance path, matching mvdsv's
// plane->type < 3 check.
type plane struct {
	n    [3]float32
	dist float32
	typ  int32
}

// clipNode is one renumbered hull node. plane indexes Hull.planes; a
// child >= 0 is another node index, a child < 0 is a contents code.
type clipNode struct {
	plane    int32
	children [2]int32
}

// Hull is one BSP model's player clip hull (the worldspawn, or an
// inline brush submodel a mover entity poses), ready for downward
// traces. root is the entry node (>= 0) or a contents code (< 0, for the
// degenerate "whole model is one leaf" case). minZ is the model
// bounding-box floor, the depth a trace descends to. mins/maxs are the
// raw model geometry bounds (un-inflated), kept for the scene query's
// fast reject.
type Hull struct {
	planes     []plane
	nodes      []clipNode
	root       int32
	minZ       float32
	mins, maxs [3]float32
}

// HeightAboveFloor returns how far the player's feet are above the floor
// directly beneath the origin (x, y, z): ~0 when grounded, positive when
// jumping or airborne. The bool is false when there is no floor to
// measure from — over a void / bottomless pit, on a moving brush model
// excluded from the worldspawn hull, or an embedded origin — leaving the
// height undefined (callers use a sentinel). The player-feet offset is
// applied here so the value reads 0 on the ground without the caller
// needing to know the hull dimensions.
func (h *Hull) HeightAboveFloor(x, y, z float32) (float32, bool) {
	floorZ, ok := h.FloorBelow(x, y, z)
	if !ok {
		return 0, false
	}
	return (z - playerFeetOffset) - floorZ, true
}

// HeightAboveFloorBox is the footprint-aware companion to
// HeightAboveFloor: instead of the single origin column it measures the
// player's feet above the *highest* floor found under the player's
// footprint, sampling a 3×3 grid of columns at ±footprintMargin in X/Y.
// The bool is false only when no column finds a floor (the whole
// footprint is over a void / moving brush model).
//
// Why the box and not just the origin: a player skimming the rim of a
// well or the lip of a ledge has their origin momentarily over the pit —
// a single straight-down trace there plunges to the distant floor far
// below and reads a huge height — while the box itself still overhangs
// the near rim. Standing on something the box overlaps is what the eye
// (and the server's ground check) sees, so the near floor is the honest
// answer. Taking the highest of the sampled columns picks that near rim.
//
// Note on reach: the traced hull is already the world inflated by the
// ±16 player box, so the centre column alone is the true 32-wide box;
// the ±footprintMargin ring only adds a small safety band (effective
// reach ~48 wide at margin 8) so a rim skim a few units past the box
// edge still attaches to the rim. Columns only ever see surfaces at or
// below z (the trace runs downward from z), so the result is never
// pulled up by geometry above the player.
func (h *Hull) HeightAboveFloorBox(x, y, z float32) (float32, bool) {
	best := float32(0)
	any := false
	for _, dx := range footprintOffsets {
		for _, dy := range footprintOffsets {
			floorZ, ok := h.FloorBelow(x+dx, y+dy, z)
			if !ok {
				continue
			}
			if !any || floorZ > best {
				best = floorZ
				any = true
			}
		}
	}
	if !any {
		return 0, false
	}
	return (z - playerFeetOffset) - best, true
}

// footprintOffsets are the X/Y sample offsets HeightAboveFloorBox walks —
// the centre and a ring at ±footprintMargin (a 3×3 grid).
var footprintOffsets = [3]float32{-footprintMargin, 0, footprintMargin}

// FloorBelow returns the Z of the floor surface directly beneath the
// point (x, y, z) — the highest solid hull surface at or below it — and
// true when one exists. It returns (0, false) when there is no floor in
// range (the point is over a void / bottomless pit) or the point starts
// inside solid (an embedded / clipping origin we can't attribute).
//
// A start that reads solid is retried once from startSolidNudge higher:
// stream positions are truncated to int32, so a grounded origin over a
// fractional-Z surface (a slope) can reconstruct just inside the hull —
// the retry stands it back up instead of dropping the column. A nudged
// start that is itself solid (a genuinely embedded origin, or a head
// touching the ceiling hull) keeps reporting no floor. The retry can
// place the found surface a fraction of a unit above the original z, so
// grounded heights read ≈0 with up to a unit of slack on either side.
//
// The value is the floor surface, so a player standing on it has
// z - FloorBelow ≈ playerFeetOffset (24); a player jumping or airborne
// over it reads larger. Coordinates are world units (Z up).
func (h *Hull) FloorBelow(x, y, z float32) (float32, bool) {
	floorZ, ok, startsolid := h.floorTrace(x, y, z)
	if !ok && startsolid {
		floorZ, ok, _ = h.floorTrace(x, y, z+startSolidNudge)
	}
	return floorZ, ok
}

// FloorBelowAt is FloorBelow with the hull posed at entity origin org:
// the trace runs in model-local space (point − org) and the returned
// floor Z is translated back into world space (+org[2]), mirroring how
// the engine traces inline brush models at the entity's current origin
// (ezquake cl_ents.c CL_SetSolidEntities → posed physent hulls). An
// inline submodel's geometry is compiled in world coordinates; org is
// the translation the entity has applied to it (zero for a door at
// rest).
func (h *Hull) FloorBelowAt(x, y, z float32, org [3]float32) (float32, bool) {
	floorZ, ok := h.FloorBelow(x-org[0], y-org[1], z-org[2])
	if !ok {
		return 0, false
	}
	return floorZ + org[2], true
}

// floorTrace runs one straight-down hull trace from (x, y, z). The
// third result distinguishes "no floor because the start was inside
// solid" (retryable by FloorBelow) from "nothing solid below".
func (h *Hull) floorTrace(x, y, z float32) (floorZ float32, ok, startsolid bool) {
	if h == nil || len(h.nodes) == 0 {
		return 0, false, false
	}
	bottom := h.minZ - traceMargin
	if bottom >= z {
		return 0, false, false
	}
	start := [3]float32{x, y, z}
	end := [3]float32{x, y, bottom}

	ht := hullTrace{h: h, fraction: 1, endpos: end}
	res := ht.recurse(h.root, 0, 1, start, end)
	if res == trSolid || ht.startsolid {
		return 0, false, true // origin embedded in solid
	}
	if ht.fraction >= 1 {
		return 0, false, false // nothing solid below within range
	}
	return ht.endpos[2] - playerFeetOffset, true, false
}

// trace return codes, mirroring RecursiveHullTrace's TR_* enum.
const (
	trEmpty = iota
	trSolid
	trBlocked
)

// hullTrace carries the in-progress trace state, mirroring the
// hulltrace_local_t + trace_t fields RecursiveHullTrace touches. Only
// the fields a vertical floor trace needs are kept: we never read the
// impact plane normal, so it isn't computed.
type hullTrace struct {
	h          *Hull
	fraction   float32
	endpos     [3]float32
	startsolid bool
	inopen     bool
	inwater    bool
	leafcount  int
}

// recurse is a direct port of mvdsv RecursiveHullTrace (cmodel.c:223).
// The C `goto start` tail-iteration becomes the for loop; the genuine
// plane-straddling split still recurses. Returns trEmpty / trSolid /
// trBlocked and, on trBlocked, leaves the impact point in fraction /
// endpos.
func (ht *hullTrace) recurse(num int32, p1f, p2f float32, p1, p2 [3]float32) int {
	for {
		if num < 0 {
			ht.leafcount++
			if num == contentsSolid {
				if ht.leafcount == 1 {
					ht.startsolid = true
				}
				return trSolid
			}
			if num == contentsEmpty {
				ht.inopen = true
			} else {
				ht.inwater = true
			}
			return trEmpty
		}

		node := ht.h.nodes[num]
		pl := ht.h.planes[node.plane]
		var t1, t2 float32
		if pl.typ < 3 {
			t1 = p1[pl.typ] - pl.dist
			t2 = p2[pl.typ] - pl.dist
		} else {
			t1 = pl.n[0]*p1[0] + pl.n[1]*p1[1] + pl.n[2]*p1[2] - pl.dist
			t2 = pl.n[0]*p2[0] + pl.n[1]*p2[1] + pl.n[2]*p2[2] - pl.dist
		}

		if t1 >= 0 && t2 >= 0 {
			num = node.children[0]
			continue
		}
		if t1 < 0 && t2 < 0 {
			num = node.children[1]
			continue
		}

		// The segment straddles this plane: split at the crossing and
		// walk the near side, then the far side.
		frac := clampFrac(t1 / (t1 - t2))
		midf := p1f + (p2f-p1f)*frac
		var mid [3]float32
		for i := 0; i < 3; i++ {
			mid[i] = p1[i] + frac*(p2[i]-p1[i])
		}

		nearside := int32(0)
		if t1 < t2 {
			nearside = 1
		}

		check := ht.recurse(node.children[nearside], p1f, midf, p1, mid)
		if check == trBlocked {
			return check
		}
		if check == trSolid && (ht.inopen || ht.inwater) {
			return check
		}
		oldcheck := check

		check = ht.recurse(node.children[1-nearside], midf, p2f, mid, p2)
		if check == trEmpty || check == trBlocked {
			return check
		}
		if oldcheck != trEmpty {
			return check // still in solid
		}

		// Near side empty, far side solid: this plane is the impact.
		// Place the endpoint DIST_EPSILON on the near side.
		if t1 < t2 {
			frac = clampFrac((t1 + distEpsilon) / (t1 - t2))
		} else {
			frac = clampFrac((t1 - distEpsilon) / (t1 - t2))
		}
		midf = p1f + (p2f-p1f)*frac
		for i := 0; i < 3; i++ {
			mid[i] = p1[i] + frac*(p2[i]-p1[i])
		}
		ht.fraction = midf
		ht.endpos = mid
		return trBlocked
	}
}

func clampFrac(f float32) float32 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
