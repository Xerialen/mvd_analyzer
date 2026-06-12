package analyzer

import (
	"sort"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// moverTrack is one inline brush-model entity's wire-state timeline at
// demo-relative ms — the same pre-normalize clock as
// streamBuilder.posT, so the floor-height pass can look a pose up by a
// position sample's own timestamp. Hold-last lookup is exact, not an
// approximation: MVD delta compression only re-sends an origin when it
// changes, so the step function through the recorded samples IS the
// entity's motion (a travelling lift re-sends every frame; a parked
// one is silent).
type moverTrack struct {
	subModel int
	t        []int32
	org      [][3]float32
	vis      []bool
}

// atCursor returns the mover's pose at tMs: the last recorded state at
// or before tMs, clamped to the first state for queries before the
// spawn (the baseline pose held from demo open). cur is a caller-held
// cursor that only moves forward, so scanning a time-ascending sample
// stream costs O(samples + len(track)) instead of a search per sample;
// start each ascending scan at -1.
func (m *moverTrack) atCursor(tMs int32, cur *int) (org [3]float32, visible bool) {
	if len(m.t) == 0 {
		return [3]float32{}, false
	}
	for *cur+1 < len(m.t) && m.t[*cur+1] <= tMs {
		*cur++
	}
	i := *cur
	if i < 0 {
		i = 0
	}
	return m.org[i], m.vis[i]
}

func (m *moverTrack) append(tMs int32, org [3]float32, visible bool) {
	m.t = append(m.t, tMs)
	m.org = append(m.org, org)
	m.vis = append(m.vis, visible)
}

func (a *TimelineAnalyzer) handleMoverSpawn(e *events.MoverSpawnEvent) {
	if a.movers == nil {
		a.movers = make(map[int]*moverTrack)
	}
	mt := a.movers[e.EntNum]
	if mt == nil {
		mt = &moverTrack{subModel: e.SubModel}
		a.movers[e.EntNum] = mt
	}
	mt.append(e.TimeMs, e.Origin, true)
}

func (a *TimelineAnalyzer) handleMoverState(e *events.MoverStateEvent) {
	mt := a.movers[e.EntNum]
	if mt == nil {
		// A state change for an entity whose spawn we never saw (clipped
		// demo). Without the submodel identity there is no hull to pose,
		// so there's nothing useful to record.
		return
	}
	mt.append(e.TimeMs, e.Origin, e.Visible)
}

// moverSubModels returns the distinct submodel indices the demo's
// mover entities reference, for the hull loader.
func (a *TimelineAnalyzer) moverSubModels() []int {
	if len(a.movers) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(a.movers))
	subs := make([]int, 0, len(a.movers))
	for _, mt := range a.movers {
		if !seen[mt.subModel] {
			seen[mt.subModel] = true
			subs = append(subs, mt.subModel)
		}
	}
	sort.Ints(subs)
	return subs
}
