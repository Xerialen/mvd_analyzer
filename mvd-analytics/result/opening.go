package result

// OpeningResult captures the opening of the match in one small block:
// where each player stood when the countdown ended, and who won the
// first take of each contested map item. It is a projection of data
// the items / weapon-pickups / streams sections already carry — kept
// as its own artifact so "how did the opening go" is one cheap fetch
// instead of three large ones.
type OpeningResult struct {
	Players    []OpeningPlayer `json:"players"`
	FirstTakes []OpeningTake   `json:"firstTakes"`
}

// OpeningPlayer is one player's match-start spawn. Loc is the resolved
// location name of the spawn (empty when the map has no .loc corpus).
type OpeningPlayer struct {
	Name string `json:"name"`
	Team string `json:"team,omitempty"`
	Loc  string `json:"loc,omitempty"`
}

// OpeningTake is the first in-match take of one tracked item spawner.
// Tracked kinds are the contested majors: armors, mega, powerups, and
// the RL/LG weapon spawners. Item/EntNum/Loc identify the spawner
// (ItemTimeline naming: ya_1 vs ya_2). A spawner nobody took has no
// entry.
type OpeningTake struct {
	Item    string `json:"item"`
	Kind    string `json:"kind"`
	EntNum  int    `json:"entNum"`
	Loc     string `json:"loc,omitempty"`
	Time    int32  `json:"time"` // match-relative milliseconds (schema v8)
	TakenBy string `json:"takenBy"`
	Team    string `json:"team,omitempty"`
}
