package mapclip

// PosedHull is a collision hull positioned at an entity origin — one
// mover (lift, door, train) at a moment in time. The pose changes per
// sample, so callers build these transiently from their mover-origin
// timelines rather than holding long-lived scene state.
type PosedHull struct {
	H      *Hull
	Origin [3]float32
}

// sceneAABBSlack widens the posed-AABB fast reject a touch beyond the
// exact player-box inflation so a column exactly on the inflated
// boundary is traced rather than rejected.
const sceneAABBSlack = 1.0

// columnMayHit reports whether a downward trace in column (cx, cy)
// starting at z could possibly hit this posed hull: the column must be
// horizontally within the model bounds plus the player-box inflation,
// and the start must not be below the inflated solid's bottom (solid
// extends playerBoxTop below raw geometry in hull 1; a start under
// that can only miss, and a start inside it reports no floor anyway).
func (m PosedHull) columnMayHit(cx, cy, z float32) bool {
	h := m.H
	if h == nil || len(h.nodes) == 0 {
		return false
	}
	const pad = playerBoxHalf + sceneAABBSlack
	if cx < h.mins[0]+m.Origin[0]-pad || cx > h.maxs[0]+m.Origin[0]+pad {
		return false
	}
	if cy < h.mins[1]+m.Origin[1]-pad || cy > h.maxs[1]+m.Origin[1]+pad {
		return false
	}
	return z >= h.mins[2]+m.Origin[2]-playerBoxTop
}

// HeightAboveFloorBoxScene is HeightAboveFloorBox over the union of the
// worldspawn hull and posed mover hulls: for every footprint column the
// highest floor across all hulls wins, then the player-feet offset is
// subtracted. This is what stands a player on the dm2 RA lift instead
// of the shaft floor far beneath it — the lift's posed hull contributes
// the higher floor and takes the max.
//
// movers may be nil/empty, making the result identical to
// world.HeightAboveFloorBox. A column that starts inside one hull
// (player overlapping a mover mid-travel) skips that hull only; the
// other hulls still contribute. The bool is false only when no column
// finds a floor in any hull.
func HeightAboveFloorBoxScene(world *Hull, movers []PosedHull, x, y, z float32) (float32, bool) {
	if len(movers) == 0 {
		if world == nil {
			return 0, false
		}
		return world.HeightAboveFloorBox(x, y, z)
	}
	best := float32(0)
	any := false
	for _, dx := range footprintOffsets {
		for _, dy := range footprintOffsets {
			cx, cy := x+dx, y+dy
			if world != nil {
				if floorZ, ok := world.FloorBelow(cx, cy, z); ok && (!any || floorZ > best) {
					best, any = floorZ, true
				}
			}
			for _, m := range movers {
				if !m.columnMayHit(cx, cy, z) {
					continue
				}
				if floorZ, ok := m.H.FloorBelowAt(cx, cy, z, m.Origin); ok && (!any || floorZ > best) {
					best, any = floorZ, true
				}
			}
		}
	}
	if !any {
		return 0, false
	}
	return (z - playerFeetOffset) - best, true
}
