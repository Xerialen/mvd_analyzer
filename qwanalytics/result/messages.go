package result

// MessagesResult contains match messages (frags and chat) for timeline display.
type MessagesResult struct {
	Events []MatchEvent `json:"events"`
}

// MatchEvent represents a frag or chat message in the match.
type MatchEvent struct {
	Time    float64 `json:"time"`
	Type    string  `json:"type"`             // "frag", "chat", "teamsay"
	Player  string  `json:"player"`           // Who sent/killed
	Team    string  `json:"team"`             // Player's team
	Message string  `json:"message"`          // Chat text or frag description
	Victim  string  `json:"victim,omitempty"` // For frags
	Weapon  string  `json:"weapon,omitempty"` // For frags
}
