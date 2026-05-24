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
// all observed players. Total / ByPlayer / ByTeam count all observed
// time; Armed, Unarmed, Quad and Pent carry the same breakdown restricted
// to the samples where the player held RL or LG (Armed), held neither
// (Unarmed, the complement of Armed), or had an active Quad / Pent
// powerup, so consumers can re-weight the graph / heatmap by combat
// posture without re-deriving from streams.
type LocNode struct {
	Name     string             `json:"name"`
	X        float32            `json:"x"`
	Y        float32            `json:"y"`
	Z        float32            `json:"z"`
	Total    float64            `json:"total"`
	ByPlayer map[string]float64 `json:"byPlayer"`
	ByTeam   map[string]float64 `json:"byTeam,omitempty"`
	Armed    *LocWeights        `json:"armed,omitempty"`
	Unarmed  *LocWeights        `json:"unarmed,omitempty"`
	Quad     *LocWeights        `json:"quad,omitempty"`
	Pent     *LocWeights        `json:"pent,omitempty"`
}

// LocWeights is a time-spent breakdown (seconds) for one conditioned
// metric on a LocNode — same shape as the node's own Total / ByPlayer /
// ByTeam, omitted entirely when no observed sample met the condition.
type LocWeights struct {
	Total    float64            `json:"total"`
	ByPlayer map[string]float64 `json:"byPlayer"`
	ByTeam   map[string]float64 `json:"byTeam,omitempty"`
}

// LocEdge is a directed transition from one loc to another, with
// transition counts grouped by player and team. Armed, Unarmed, Quad and
// Pent mirror the node-level conditioning: the subset of transitions made
// while the player held RL or LG (Armed), held neither (Unarmed), or had
// an active quad / pent, so the frontend can draw a self-contained
// movement graph per combat posture.
type LocEdge struct {
	From     string          `json:"from"`
	To       string          `json:"to"`
	Kind     string          `json:"kind"`
	Total    int             `json:"total"`
	ByPlayer map[string]int  `json:"byPlayer"`
	ByTeam   map[string]int  `json:"byTeam,omitempty"`
	Armed    *LocEdgeWeights `json:"armed,omitempty"`
	Unarmed  *LocEdgeWeights `json:"unarmed,omitempty"`
	Quad     *LocEdgeWeights `json:"quad,omitempty"`
	Pent     *LocEdgeWeights `json:"pent,omitempty"`
}

// LocEdgeWeights is a transition-count breakdown for one conditioned
// metric on a LocEdge — same shape as the edge's own Total / ByPlayer /
// ByTeam, omitted when no transition met the condition.
type LocEdgeWeights struct {
	Total    int            `json:"total"`
	ByPlayer map[string]int `json:"byPlayer"`
	ByTeam   map[string]int `json:"byTeam,omitempty"`
}
