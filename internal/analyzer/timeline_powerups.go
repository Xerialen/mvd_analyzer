package analyzer

import "sort"

// powerupKinds is the table of powerups the timeline analyzer tracks. Adding
// a new powerup here is enough to surface it in PowerupEvent output — no
// switch statements to update.
var powerupKinds = []struct {
	name string
	has  func(*playerBucketRawData) bool
}{
	{"quad", func(p *playerBucketRawData) bool { return p.powerups.quad }},
	{"pent", func(p *playerBucketRawData) bool { return p.powerups.pent }},
	{"ring", func(p *playerBucketRawData) bool { return p.powerups.ring }},
}

// detectPowerupEvents scans buckets for powerup pickup/loss transitions
func (a *TimelineAnalyzer) detectPowerupEvents(nameToTeam map[string]string, slotToTeam map[int]string, slotToPlayer map[int]string) []PowerupEvent {
	if len(a.buckets) == 0 {
		return nil
	}

	events := []PowerupEvent{}

	// Track active powerups per player slot per type
	// Map: slot -> powerupType -> startTime (0 if not active)
	activeRuns := make(map[int]map[string]float64)

	for _, bucket := range a.buckets {
		for slot, pData := range bucket.playerData {
			if activeRuns[slot] == nil {
				activeRuns[slot] = make(map[string]float64)
			}

			for _, pk := range powerupKinds {
				hasIt := pk.has(pData)
				startTime := activeRuns[slot][pk.name]

				if hasIt && startTime == 0 {
					// Powerup just picked up
					activeRuns[slot][pk.name] = bucket.startTime
				} else if !hasIt && startTime > 0 {
					// Powerup just lost
					event := a.createPowerupEvent(slot, pk.name, startTime, bucket.startTime, nameToTeam, slotToTeam, slotToPlayer)
					events = append(events, event)
					activeRuns[slot][pk.name] = 0
				}
			}
		}
	}

	// Handle powerups still active at end of demo
	lastBucket := a.buckets[len(a.buckets)-1]
	for slot, runs := range activeRuns {
		for pType, startTime := range runs {
			if startTime > 0 {
				event := a.createPowerupEvent(slot, pType, startTime, lastBucket.endTime, nameToTeam, slotToTeam, slotToPlayer)
				events = append(events, event)
			}
		}
	}

	// Sort by time
	sort.Slice(events, func(i, j int) bool {
		return events[i].Time < events[j].Time
	})

	return events
}

// createPowerupEvent creates a PowerupEvent with resolved player info
func (a *TimelineAnalyzer) createPowerupEvent(slot int, powerupType string, startTime, endTime float64, nameToTeam map[string]string, slotToTeam map[int]string, slotToPlayer map[int]string) PowerupEvent {
	event := PowerupEvent{
		Time:        startTime,
		EndTime:     endTime,
		PlayerSlot:  slot,
		PowerupType: powerupType,
		Duration:    endTime - startTime,
	}

	// Set UserID from our tracking map (used for Hub viewer track param)
	if userID, ok := a.playerUserIDs[slot]; ok {
		event.PlayerUserID = userID
	}

	// Prefer the demoinfo-resolved name (via slotToPlayer) so the emitted
	// name matches what the frontend joins against. Only fall back to the
	// userinfo name when the strict (team, frags) match failed for this slot.
	if name := slotToPlayer[slot]; name != "" {
		event.PlayerName = name
	}
	if t := slotToTeam[slot]; t != "" {
		event.Team = t
	}
	if player := a.ctx.Players[slot]; player != nil {
		if event.PlayerName == "" {
			event.PlayerName = player.Name
		}
		if event.Team == "" {
			event.Team = player.Team
		}
		if event.PlayerUserID == 0 && player.UserID != 0 {
			event.PlayerUserID = player.UserID
		}
	}
	if event.PlayerName == "" {
		if name, ok := a.playerNames[slot]; ok {
			event.PlayerName = name
		}
	}
	if event.Team == "" && event.PlayerName != "" {
		event.Team = nameToTeam[event.PlayerName]
	}

	return event
}
