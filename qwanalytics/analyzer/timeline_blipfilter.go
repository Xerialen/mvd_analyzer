package analyzer

// Loc-bleed smoothing. After the initial nearest-loc pass writes
// pData.location for every bucket, many maps produce short-lived
// "blips" — a player walking along the cathedral ramp in schloss
// briefly picks up a Quad.high label because a point of that cluster
// sits physically on the far side of a wall, and the nearest-point
// finder has no concept of walls. The result is a spurious edge the
// loc graph insists is real.
//
// The filter collapses any residence shorter than blipThresholdMs
// into an adjacent stable residence. The rewrite happens on
// pData.location, so every downstream consumer — the loc graph, the
// region-control timeline, the map-tab loc labels — sees the same
// smoothed track. There is no per-analyzer hysteresis anywhere else.
//
// Rules (per player, run split by death/spawn and by any bucket with
// no resolved loc so nothing crosses those gaps):
//
//   - A run with no segments ≥ threshold is left untouched (can't
//     anchor blips to a stable).
//   - Leading blips before the first stable segment are relabeled to
//     that first stable's loc.
//   - Trailing blips after the last stable are relabeled to that last
//     stable's loc.
//   - A blip run between two stables A and D is split at the bucket
//     level: the first ceil(N/2) blip buckets take A's loc, the
//     remaining floor(N/2) take D's loc. A gets the tie when N is
//     odd. If A and D are the same loc, all blips collapse to it
//     (the classic "wall bleed and return" case becomes a single
//     uninterrupted residence).

// applyBlipFilter rewrites pData.location in every bucket to collapse
// residences shorter than thresholdMs. Noop when the threshold is 0
// or there is nothing to process.
func (a *TimelineAnalyzer) applyBlipFilter(thresholdMs int) {
	if thresholdMs <= 0 || len(a.buckets) == 0 {
		return
	}
	threshold := float64(thresholdMs) / 1000.0
	bucketDur := a.bucketDuration
	if bucketDur <= 0 {
		bucketDur = DefaultHighResBucketDuration
	}

	// Collect every slot that appears anywhere in the timeline.
	slots := make(map[int]struct{})
	for _, b := range a.buckets {
		for slot := range b.playerData {
			slots[slot] = struct{}{}
		}
	}

	for slot := range slots {
		a.filterPlayerBlips(slot, threshold, bucketDur)
	}
}

// filterPlayerBlips walks a single player's bucket sequence, splitting
// it into runs at death/spawn/no-loc boundaries, and applies the
// blip-collapse rules to each run.
func (a *TimelineAnalyzer) filterPlayerBlips(slot int, threshold, bucketDur float64) {
	var run []*playerBucketRawData
	flush := func() {
		if len(run) > 0 {
			filterBlipsInRun(run, threshold, bucketDur)
			run = run[:0]
		}
	}

	for _, b := range a.buckets {
		pd, ok := b.playerData[slot]
		if !ok {
			continue
		}
		// dead/spawn markers and buckets where nearest-loc couldn't
		// resolve (typically 0,0,0 positions before the first real
		// sample) must not be bridged by the filter.
		if pd.dead || pd.spawn || pd.location == "" {
			flush()
			continue
		}
		run = append(run, pd)
	}
	flush()
}

// filterBlipsInRun applies the rewrite to one contiguous run of
// bucket data (same-slot, no death/spawn/missing-loc breaks inside).
func filterBlipsInRun(run []*playerBucketRawData, threshold, bucketDur float64) {
	if len(run) == 0 {
		return
	}

	// Group consecutive same-loc buckets into segments (half-open
	// [start, end) indexes into run).
	type segment struct {
		loc        string
		start, end int
	}
	var segs []segment
	for i := 0; i < len(run); {
		loc := run[i].location
		j := i + 1
		for j < len(run) && run[j].location == loc {
			j++
		}
		segs = append(segs, segment{loc: loc, start: i, end: j})
		i = j
	}

	// A segment is stable if its residence meets the threshold.
	stable := make([]bool, len(segs))
	firstStable, lastStable := -1, -1
	for i, s := range segs {
		dur := float64(s.end-s.start) * bucketDur
		if dur >= threshold {
			stable[i] = true
			if firstStable < 0 {
				firstStable = i
			}
			lastStable = i
		}
	}
	if firstStable < 0 {
		// Entire run is blips. No anchor — leave everything as-is.
		return
	}

	// Leading blips take the first stable's loc.
	for i := 0; i < firstStable; i++ {
		relabelRun(run, segs[i].start, segs[i].end, segs[firstStable].loc)
	}

	// For every pair of adjacent stables, split the blip run between
	// them (or collapse to A if they share a loc).
	prev := firstStable
	for next := firstStable + 1; next <= lastStable; next++ {
		if !stable[next] {
			continue
		}
		if prev+1 < next {
			aLoc := segs[prev].loc
			dLoc := segs[next].loc
			firstBlipSeg := prev + 1
			if aLoc == dLoc {
				// "A ... blips ... A" collapses to uninterrupted A.
				for i := firstBlipSeg; i < next; i++ {
					relabelRun(run, segs[i].start, segs[i].end, aLoc)
				}
			} else {
				// Total blip bucket count, split ceil(N/2) | floor(N/2).
				total := 0
				for i := firstBlipSeg; i < next; i++ {
					total += segs[i].end - segs[i].start
				}
				aCount := (total + 1) / 2
				assigned := 0
				for i := firstBlipSeg; i < next; i++ {
					for k := segs[i].start; k < segs[i].end; k++ {
						if assigned < aCount {
							run[k].location = aLoc
						} else {
							run[k].location = dLoc
						}
						assigned++
					}
				}
			}
		}
		prev = next
	}

	// Trailing blips take the last stable's loc.
	for i := lastStable + 1; i < len(segs); i++ {
		relabelRun(run, segs[i].start, segs[i].end, segs[lastStable].loc)
	}
}

// relabelRun overwrites pData.location for every bucket in the
// half-open interval [lo, hi) of the run slice.
func relabelRun(run []*playerBucketRawData, lo, hi int, loc string) {
	for i := lo; i < hi; i++ {
		run[i].location = loc
	}
}
