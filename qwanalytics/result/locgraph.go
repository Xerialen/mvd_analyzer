package result

// LocGraphResult is the aggregate movement graph: loc nodes weighted by
// time-spent, directed edges weighted by transition count. Per-player
// and per-team breakdowns are carried on every node and edge so the
// frontend can filter without re-aggregating.
type LocGraphResult struct {
	Locs  []LocNode `json:"locs"`
	Edges []LocEdge `json:"edges"`
}

// LocNode is a single location on the map with aggregate time spent by
// all observed players.
type LocNode struct {
	Name     string             `json:"name"`
	X        float32            `json:"x"`
	Y        float32            `json:"y"`
	Z        float32            `json:"z"`
	Total    float64            `json:"total"`
	ByPlayer map[string]float64 `json:"byPlayer"`
	ByTeam   map[string]float64 `json:"byTeam,omitempty"`
}

// LocEdge is a directed transition from one loc to another, with
// transition counts grouped by player and team.
type LocEdge struct {
	From     string         `json:"from"`
	To       string         `json:"to"`
	Kind     string         `json:"kind"`
	Total    int            `json:"total"`
	ByPlayer map[string]int `json:"byPlayer"`
	ByTeam   map[string]int `json:"byTeam,omitempty"`
}
