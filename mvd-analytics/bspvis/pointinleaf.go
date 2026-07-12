package bspvis

// Q1 leaf contents constants (mvdsv/src/bspfile.h:144-149). Anything <
// CONTENTS_EMPTY is "occupied" in some sense; only CONTENTS_SOLID
// actually blocks visibility / movement.
const (
	ContentsEmpty = -1
	ContentsSolid = -2
	ContentsWater = -3
	ContentsSlime = -4
	ContentsLava  = -5
	ContentsSky   = -6
)

// LeafCount returns the number of leaves in the visibility BSP, including
// the universal CONTENTS_SOLID sink at index 0.
func (b *BSP) LeafCount() int {
	return len(b.Leaves)
}

// LeafContents returns the contents value of the given leaf, or
// CONTENTS_SOLID for indices outside the valid range — the same fallback
// the engine uses when an invalid leaf is queried.
func (b *BSP) LeafContents(leafIdx int) int32 {
	if leafIdx < 0 || leafIdx >= len(b.Leaves) {
		return ContentsSolid
	}
	return b.Leaves[leafIdx].Contents
}

// PointInLeaf returns the index of the leaf containing the given point,
// descending the worldspawn visibility BSP from Models[0].HeadNodes[0].
//
// Algorithm follows ezquake-source/src/r_model.c:Mod_PointInLeaf
// (lines 72-94) and mvdsv/src/cmodel.c:CM_PointInLeaf (lines 397-418):
// at each node compute the signed plane distance, recurse the front
// child (>0) or back child (<=0) — ties go to the back child. Stop when
// a child is negative; the leaf index is -1 - child.
//
// If the model has no head node (degenerate map), returns 0 (the
// CONTENTS_SOLID sink at leaf 0).
func (b *BSP) PointInLeaf(p [3]float32) int {
	if len(b.Models) == 0 || len(b.Nodes) == 0 {
		return 0
	}
	cur := b.Models[0].HeadNodes[0]
	for cur >= 0 {
		if int(cur) >= len(b.Nodes) {
			return 0
		}
		n := &b.Nodes[cur]
		pl := &b.Planes[n.PlaneID]
		d := pl.Normal.X*p[0] + pl.Normal.Y*p[1] + pl.Normal.Z*p[2] - pl.Dist
		if d > 0 {
			cur = n.Children[0]
		} else {
			cur = n.Children[1]
		}
	}
	leafIdx := int(-1 - cur)
	if leafIdx < 0 || leafIdx >= len(b.Leaves) {
		return 0
	}
	return leafIdx
}

// BoxLeafs appends the index of every leaf the axis-aligned box [mins,maxs]
// overlaps to dst (which is truncated first) and returns it, descending the
// worldspawn visibility BSP. This is the engine broadphase (Mod_BoxLeafnums /
// SV_FindTouchedLeafs): one descent that prunes whole subtrees the box is on
// one side of. A small box (a player hull) touches only 1–3 leaves. Leaves are
// disjoint, so no leaf is reported twice. Sign convention matches PointInLeaf
// (front = d>0).
func (b *BSP) BoxLeafs(mins, maxs [3]float32, dst []int) []int {
	dst = dst[:0]
	if len(b.Models) == 0 || len(b.Nodes) == 0 {
		return dst
	}
	return b.boxLeafs(b.Models[0].HeadNodes[0], mins, maxs, dst)
}

func (b *BSP) boxLeafs(nodeIdx int32, mins, maxs [3]float32, dst []int) []int {
	if nodeIdx < 0 {
		if leaf := int(-1 - nodeIdx); leaf >= 0 && leaf < len(b.Leaves) {
			dst = append(dst, leaf)
		}
		return dst
	}
	if int(nodeIdx) >= len(b.Nodes) {
		return dst
	}
	n := &b.Nodes[nodeIdx]
	pl := &b.Planes[n.PlaneID]
	dmin, dmax := boxPlaneDist(pl, mins, maxs)
	if dmin > 0 { // box entirely on the front side
		return b.boxLeafs(n.Children[0], mins, maxs, dst)
	}
	if dmax <= 0 { // box entirely on the back side
		return b.boxLeafs(n.Children[1], mins, maxs, dst)
	}
	dst = b.boxLeafs(n.Children[0], mins, maxs, dst)
	return b.boxLeafs(n.Children[1], mins, maxs, dst)
}

// boxPlaneDist returns the signed distances of the box's nearest and farthest
// extents from the plane (dmin <= dmax). Axis-aligned planes (Type 0–2) take a
// fast path; the general case projects the supporting box corners onto the
// normal.
func boxPlaneDist(pl *Plane, mins, maxs [3]float32) (dmin, dmax float32) {
	if pl.Type >= 0 && pl.Type < 3 {
		return mins[pl.Type] - pl.Dist, maxs[pl.Type] - pl.Dist
	}
	nrm := [3]float32{pl.Normal.X, pl.Normal.Y, pl.Normal.Z}
	for i, ni := range nrm {
		if ni >= 0 {
			dmin += ni * mins[i]
			dmax += ni * maxs[i]
		} else {
			dmin += ni * maxs[i]
			dmax += ni * mins[i]
		}
	}
	return dmin - pl.Dist, dmax - pl.Dist
}
