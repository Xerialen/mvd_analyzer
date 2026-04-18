package result

// MatchResult contains match summary information.
type MatchResult struct {
	Map       string       `json:"map"`
	GameDir   string       `json:"gameDir"`
	Duration  float64      `json:"duration"`
	StartTime float64      `json:"startTime,omitempty"`
	EndTime   float64      `json:"endTime,omitempty"`
	Players   []PlayerStat `json:"players"`
	Teams     []TeamStat   `json:"teams,omitempty"`
}

// PlayerStat represents a player's final statistics.
type PlayerStat struct {
	Name   string `json:"name"`
	Team   string `json:"team"`
	Kills  int    `json:"kills"`
	Deaths int    `json:"deaths"`
	Frags  int    `json:"frags"`
}

// TeamStat represents a team's statistics.
type TeamStat struct {
	Name  string `json:"name"`
	Frags int    `json:"frags"`
}
