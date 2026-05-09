package result

// MessagesResult contains match messages (frags and chat) for timeline display.
type MessagesResult struct {
	Events []MatchEvent `json:"events"`
}

// MatchEvent represents a frag or chat message in the match.
//
// Message is the Q-normalised text as emitted upstream — it preserves
// ezQuake chat markup (color codes &cRGB, sound triggers, macro
// delimiters) so consumers can render coloured chat. MessageClean is
// the same text with that markup stripped, suitable for plain-text
// surfaces (search, exports, AI agents). For frag events the analyzer
// constructs Message itself and Clean is identical, so MessageClean is
// elided via omitempty when it equals Message — consumers should treat
// a missing MessageClean as "use Message".
type MatchEvent struct {
	Time         float64 `json:"time"`
	Type         string  `json:"type"`                   // "frag", "chat", "teamsay"
	Player       string  `json:"player"`                 // Who sent/killed
	Team         string  `json:"team"`                   // Player's team
	Message      string  `json:"message"`                // Chat text or frag description (markup-preserving)
	MessageClean string  `json:"messageClean,omitempty"` // Same text with ezQuake markup stripped
	Victim       string  `json:"victim,omitempty"`       // For frags
	Weapon       string  `json:"weapon,omitempty"`       // For frags
}
