package result

// ItemsResult is the per-match pickup-item timeline — which items are
// on the map, who picked each one when, and when each becomes
// available again. Driven by the KTX-only `//ktx took|timer|drop`
// demo-only stuffcmds (ktx/src/items.c). Absent on non-KTX demos.
type ItemsResult struct {
	Items []ItemTimeline `json:"items"`
}

// ItemTimeline covers one item entity across the match. Multiple
// items of the same kind (two MHs on schloss, two RAs on ztndm3)
// get deterministic suffixed names (`mh_1`, `mh_2`) in ctrl-sorted
// order so diffs stay stable.
type ItemTimeline struct {
	Name   string      `json:"name"`
	Kind   string      `json:"kind"`
	X      float32     `json:"x"`
	Y      float32     `json:"y"`
	Z      float32     `json:"z"`
	Loc    string      `json:"loc,omitempty"` // nearest named location from the map's .loc file
	Phases []ItemPhase `json:"phases"`
}

// ItemPhase is one observable row of "item is up, then someone takes
// it, then it'll come back at T". For MH the RespawnAt is unknown
// until the matching `//ktx timer` event arrives — in that window we
// leave RespawnAt at 0 and consumers should treat the item as "still
// held". TakenBy / Team are set to the picker's display name and
// team; empty for the initial "available from match start" phase.
type ItemPhase struct {
	AvailableFrom float64 `json:"availableFrom"`
	TakenAt       float64 `json:"takenAt,omitempty"`
	TakenBy       string  `json:"takenBy,omitempty"`
	Team          string  `json:"team,omitempty"`
	RespawnAt     float64 `json:"respawnAt,omitempty"`
}
