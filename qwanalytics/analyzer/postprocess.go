package analyzer

// Default post-processors for the registry. Each one is registered by
// NewDefaultRegistry; callers building a registry from scratch can
// pick which ones they want via RegisterPostProcessor.

// normalizeMatchRelativeTimes shifts every time-stamped field in
// Result so that t=0 is the moment the match started. The original
// match-start offset is preserved in TimelineAnalysis.DemoOffset so
// the frontend can map back to demo-time when needed (e.g. building
// hub viewer URLs).
//
// Warmup buckets (those with negative t after the shift) are dropped
// from HighResBuckets — they would otherwise produce garbage
// pre-match samples that the timeline view has no use for.
func normalizeMatchRelativeTimes(result *Result, _ *CoreOutputs) {
	matchStart := 0.0
	if result.TimelineAnalysis != nil {
		matchStart = result.TimelineAnalysis.MatchStartTime
	}
	if matchStart <= 0 {
		return
	}

	if ta := result.TimelineAnalysis; ta != nil {
		for i := range ta.HighResBuckets {
			ta.HighResBuckets[i].T -= matchStart
		}
		for i := range ta.FragEvents {
			ta.FragEvents[i].Time -= matchStart
		}
		for i := range ta.PowerupEvents {
			ta.PowerupEvents[i].Time -= matchStart
			ta.PowerupEvents[i].EndTime -= matchStart
		}
		for i := range ta.FragStreaks {
			ta.FragStreaks[i].Time -= matchStart
			ta.FragStreaks[i].EndTime -= matchStart
		}
		ta.DemoOffset = matchStart
		ta.MatchStartTime = 0

		// Filter out warmup buckets (negative times after normalization).
		dropped := 0
		filteredHR := ta.HighResBuckets[:0]
		for _, b := range ta.HighResBuckets {
			if b.T >= 0 {
				filteredHR = append(filteredHR, b)
			} else {
				dropped++
			}
		}
		ta.HighResBuckets = filteredHR

		// RegionControl.BucketStates was computed before this filter
		// ran (in TimelineAnalyzer.Finalize), so the strings still
		// include the dropped warmup samples. Slice off the same
		// prefix to keep bucketStates[regionName][i] aligned with
		// HighResBuckets[i], then recompute aggregate Stats from the
		// truncated strings so percentages reflect match-only state.
		if dropped > 0 && ta.RegionControl != nil && len(ta.RegionControl.BucketStates) > 0 {
			for name, s := range ta.RegionControl.BucketStates {
				if dropped < len(s) {
					ta.RegionControl.BucketStates[name] = s[dropped:]
				} else {
					ta.RegionControl.BucketStates[name] = ""
				}
			}
			ta.RegionControl.Stats = recomputeRegionStatsFromStrings(ta.RegionControl.BucketStates)
		}
	}

	if result.Messages != nil {
		for i := range result.Messages.Events {
			result.Messages.Events[i].Time -= matchStart
		}
	}

	if result.Frags != nil {
		for i := range result.Frags.Frags {
			result.Frags.Frags[i].Time -= matchStart
		}
	}

	if result.Match != nil {
		result.Match.StartTime -= matchStart
		result.Match.EndTime -= matchStart
	}

	if result.Items != nil {
		for i := range result.Items.Items {
			ph := result.Items.Items[i].Phases
			for j := range ph {
				// AvailableFrom=0 is the synthetic "match start" marker
				// for initial phases; leave it alone. All real
				// timestamps get shifted.
				if ph[j].AvailableFrom > 0 {
					ph[j].AvailableFrom -= matchStart
				}
				if ph[j].TakenAt > 0 {
					ph[j].TakenAt -= matchStart
				}
				if ph[j].RespawnAt > 0 {
					ph[j].RespawnAt -= matchStart
				}
			}
		}
	}

	for i := range result.Backpacks {
		result.Backpacks[i].Time -= matchStart
	}

	for i := range result.WeaponPickups {
		result.WeaponPickups[i].Time -= matchStart
		if result.WeaponPickups[i].NextDeathTime > 0 {
			result.WeaponPickups[i].NextDeathTime -= matchStart
		}
		if result.WeaponPickups[i].DropTime > 0 {
			result.WeaponPickups[i].DropTime -= matchStart
		}
	}
}

// duelTeamNormalize is the post-processor wrapper around
// normalizeDuelTeams (defined in duel_normalize.go).
func duelTeamNormalize(result *Result, _ *CoreOutputs) {
	normalizeDuelTeams(result)
}

// locGraphPost runs BuildLocGraph on the assembled Result. Has to run
// after the time and duel normalisations so the loc nodes/edges use
// the same time base and team labels as the rest of the result.
func locGraphPost(result *Result, _ *CoreOutputs) {
	result.LocGraph = BuildLocGraph(result)
}
