package analyzer

import "sort"

// detectPowerupEvents derives PowerupEvent records from each player's
// streamBuilder interval lists (Quad / Pent / Ring). Each closed
// interval becomes one PowerupEvent. Replaces v6's per-bucket scan;
// the streamBuilder already records open / close transitions exactly
// at the events that flipped them, so this is just a translation.
//
// matchEndMs is the single effective match end computed once in Finalize
// and shared with buildStreamsResult's stream finalize, so a still-open
// powerup run closes at the same instant as the weapon intervals (F13).
func (a *TimelineAnalyzer) detectPowerupEvents(matchEndMs int32) []PowerupEvent {
	if len(a.playerState) == 0 {
		return nil
	}

	events := []PowerupEvent{}
	for slot, state := range a.playerState {
		if state == nil {
			continue
		}
		// Close any still-open intervals at the shared match end so finalize
		// doesn't drop ongoing powerup runs.
		state.streams.quad.closeAtMatchEnd(matchEndMs)
		state.streams.pent.closeAtMatchEnd(matchEndMs)
		state.streams.ring.closeAtMatchEnd(matchEndMs)

		appendRuns := func(runs []intervalRecord, kind string) {
			for _, r := range runs {
				events = append(events, a.createPowerupEvent(slot, kind, r.start, r.end))
			}
		}
		appendRuns(state.streams.quad.closed, "quad")
		appendRuns(state.streams.pent.closed, "pent")
		appendRuns(state.streams.ring.closed, "ring")
	}

	sort.Slice(events, func(i, j int) bool {
		return events[i].Time < events[j].Time
	})
	return events
}

// createPowerupEvent creates a PowerupEvent with resolved player info.
// startTime/endTime are int32 ms (schema v8).
func (a *TimelineAnalyzer) createPowerupEvent(slot int, powerupType string, startTime, endTime int32) PowerupEvent {
	event := PowerupEvent{
		Time:        startTime,
		EndTime:     endTime,
		PlayerSlot:  slot,
		PowerupType: powerupType,
		Duration:    endTime - startTime,
	}

	if userID, ok := a.playerUserIDs[slot]; ok {
		event.PlayerUserID = userID
	}

	// Resolve the identity that held the slot when the powerup run began
	// (startTime), so a quad/pent/ring run picked up before a reconnect
	// is credited to the right player.
	event.PlayerName, event.Team = a.resolveAt(slot, startTime)
	if event.PlayerUserID == 0 {
		if player := a.ctx.Players[slot]; player != nil && player.UserID != 0 {
			event.PlayerUserID = player.UserID
		}
	}

	return event
}
