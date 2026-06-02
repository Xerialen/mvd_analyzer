package result

// FragResult contains frag analysis results.
type FragResult struct {
	TotalFrags int                     `json:"totalFrags"`
	Frags      []FragEntry             `json:"frags"`
	ByWeapon   map[string]int          `json:"byWeapon"`
	ByPlayer   map[string]*PlayerFrags `json:"byPlayer"`
}

// FragEntry represents a single frag event. Time is match-relative
// milliseconds (schema v8).
type FragEntry struct {
	Time       int32  `json:"time"`
	Killer     string `json:"killer"`
	Victim     string `json:"victim"`
	Weapon     string `json:"weapon"`
	IsSuicide  bool   `json:"isSuicide,omitempty"`
	IsTeamKill bool   `json:"isTeamKill,omitempty"`
}

// PlayerFrags holds per-player frag statistics.
type PlayerFrags struct {
	Kills     int            `json:"kills"`
	Deaths    int            `json:"deaths"`
	TeamKills int            `json:"teamkills,omitempty"` // Teammates this player killed (KTX "tk"). Killer-named obituaries only; see frag analyzer.
	ByWeapon  map[string]int `json:"byWeapon"`
}
