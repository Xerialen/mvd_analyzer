package result

// FragResult contains frag analysis results.
type FragResult struct {
	TotalFrags int                     `json:"totalFrags"`
	Frags      []FragEntry             `json:"frags"`
	ByWeapon   map[string]int          `json:"byWeapon"`
	ByPlayer   map[string]*PlayerFrags `json:"byPlayer"`
}

// FragEntry represents a single frag event.
type FragEntry struct {
	Time       float64 `json:"time"`
	Killer     string  `json:"killer"`
	Victim     string  `json:"victim"`
	Weapon     string  `json:"weapon"`
	IsSuicide  bool    `json:"isSuicide,omitempty"`
	IsTeamKill bool    `json:"isTeamKill,omitempty"`
}

// PlayerFrags holds per-player frag statistics.
type PlayerFrags struct {
	Kills    int            `json:"kills"`
	Deaths   int            `json:"deaths"`
	ByWeapon map[string]int `json:"byWeapon"`
}
