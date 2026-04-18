package result

// MetadataResult bundles every demo metadata source we can extract from
// non-payload protocol commands: the bulk `fullserverinfo` cvar dump that
// arrives as a stufftext at connection time, any mid-game serverinfo
// updates, and the parsed match-settings table that KTX renders into the
// countdown centerprint.
type MetadataResult struct {
	// ServerInfo is the union of `\key\value\…` pairs from the initial
	// fullserverinfo stufftext plus every per-key svc_serverinfo update
	// that arrived later in the demo. Last-write-wins for keys that get
	// overwritten (e.g. `status` cycles through Countdown → "3 min left"
	// → "2 min left" → ... → Standby).
	ServerInfo map[string]string `json:"serverInfo,omitempty"`

	// MatchSettings is the parsed view of KTX's countdown centerprint —
	// the most reliable source of match-level cvars.
	MatchSettings *MatchSettings `json:"matchSettings,omitempty"`

	// CountdownText is the raw, color-stripped multi-line text of the
	// last countdown centerprint we observed before the match started.
	CountdownText string `json:"countdownText,omitempty"`
}

// MatchSettings is the structured view of the KTX countdown table.
// All fields are optional — only those that appeared in the centerprint
// for this particular demo are populated.
//
// Source: ktx/src/match.c PrintCountdown() — search for `strlcat(text, ...)`
// to see the format strings.
type MatchSettings struct {
	Mode       string `json:"mode,omitempty"`       // "Duel" / "Team" / "FFA" / "LGC" / "CA" / "CTF" / etc.
	Deathmatch int    `json:"deathmatch,omitempty"` // 0..5
	Teamplay   int    `json:"teamplay,omitempty"`   // QW teamplay setting
	Timelimit  int    `json:"timelimit,omitempty"`  // minutes
	Fraglimit  int    `json:"fraglimit,omitempty"`
	Spawnmodel string `json:"spawnmodel,omitempty"` // "QW" / "KTS" / "KT" / "KTX" / "KT2" — see respawn_model_name_short
	SpawnK     *int   `json:"spawnK,omitempty"`     // numeric k_spw value (0..4) decoded from Spawnmodel
	Antilag    int    `json:"antilag,omitempty"`    // 0/1/2
	Overtime   string `json:"overtime,omitempty"`   // "5" minutes, or "sd" for sudden death
	Powerups   string `json:"powerups,omitempty"`   // "on" / "off" / "QPRS"
	Dmgfrags   bool   `json:"dmgfrags,omitempty"`
	NoItems    bool   `json:"noItems,omitempty"`
	Midair     bool   `json:"midair,omitempty"`
	Instagib   bool   `json:"instagib,omitempty"`
	Yawnmode   bool   `json:"yawnmode,omitempty"`
	Airstep    bool   `json:"airstep,omitempty"`
	VWep       bool   `json:"vwep,omitempty"`
	Noweapon   string `json:"noweapon,omitempty"` // disabled weapons, e.g. "gl" or "gl axe"
	Matchtag   string `json:"matchtag,omitempty"` // tournament/event tag, e.g. "qwsldraft"
	SOCDv2     string `json:"socdv2,omitempty"`   // "stats" / "warn" / "block"
}
