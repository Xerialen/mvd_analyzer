package result

// DemoInfoResult contains parsed KTX embedded JSON stats (authoritative).
type DemoInfoResult struct {
	Version   int              `json:"version,omitempty"`
	Date      string           `json:"date,omitempty"`
	Map       string           `json:"map,omitempty"`
	Hostname  string           `json:"hostname,omitempty"`
	IP        string           `json:"ip,omitempty"`
	Port      int              `json:"port,omitempty"`
	Mode      string           `json:"mode,omitempty"`
	Timelimit int              `json:"timelimit,omitempty"`
	Fraglimit int              `json:"fraglimit,omitempty"`
	Duration  int              `json:"duration,omitempty"`
	Demo      string           `json:"demo,omitempty"`
	Teams     []string         `json:"teams,omitempty"`
	Players   []DemoInfoPlayer `json:"players,omitempty"`
	RawJSON   string           `json:"rawJson,omitempty"` // For debugging failed parses
}

// DemoInfoPlayer contains player stats from KTX JSON.
type DemoInfoPlayer struct {
	Name        string                     `json:"name"`
	Team        string                     `json:"team"`
	TopColor    int                        `json:"topColor,omitempty"`
	BottomColor int                        `json:"bottomColor,omitempty"`
	Ping        int                        `json:"ping,omitempty"`
	Login       string                     `json:"login,omitempty"`
	Handicap    int                        `json:"handicap,omitempty"`
	Bot         *DemoInfoBot               `json:"bot,omitempty"`
	Stats       *DemoInfoStats             `json:"stats,omitempty"`
	Dmg         *DemoInfoDmg               `json:"dmg,omitempty"`
	Spree       *DemoInfoSpree             `json:"spree,omitempty"`
	Control     float64                    `json:"control,omitempty"`
	Speed       *DemoInfoSpeed             `json:"speed,omitempty"`
	XferRL      int                        `json:"xferRL,omitempty"`
	XferLG      int                        `json:"xferLG,omitempty"`
	Weapons     map[string]*DemoInfoWeapon `json:"weapons,omitempty"`
	Items       map[string]*DemoInfoItem   `json:"items,omitempty"`
}

// DemoInfoBot is the per-player bot block KTX writes when the player slot
// is held by a frogbot. Only present when KTX was built with BOT_SUPPORT
// and the player is a bot.
type DemoInfoBot struct {
	Skill      int  `json:"skill"`
	Customised bool `json:"customised"`
}

// DemoInfoStats contains frag/death stats from KTX JSON.
type DemoInfoStats struct {
	Frags      int `json:"frags"`
	Deaths     int `json:"deaths"`
	TK         int `json:"tk,omitempty"`
	SpawnFrags int `json:"spawn-frags,omitempty"`
	Kills      int `json:"kills,omitempty"`
	Suicides   int `json:"suicides,omitempty"`
}

// DemoInfoDmg contains damage stats from KTX JSON.
type DemoInfoDmg struct {
	Taken        int `json:"taken"`
	Given        int `json:"given"`
	Team         int `json:"team,omitempty"`
	Self         int `json:"self,omitempty"`
	TeamWeapons  int `json:"team-weapons,omitempty"`
	EnemyWeapons int `json:"enemy-weapons,omitempty"`
	TakenToDie   int `json:"taken-to-die,omitempty"`
}

// DemoInfoSpree contains spree stats from KTX JSON.
type DemoInfoSpree struct {
	Max  int `json:"max,omitempty"`
	Quad int `json:"quad,omitempty"`
}

// DemoInfoSpeed contains speed stats from KTX JSON.
type DemoInfoSpeed struct {
	Max float64 `json:"max,omitempty"`
	Avg float64 `json:"avg,omitempty"`
}

// DemoInfoWeapon contains weapon stats from KTX JSON.
type DemoInfoWeapon struct {
	Acc     *DemoInfoAcc     `json:"acc,omitempty"`
	Kills   *DemoInfoKills   `json:"kills,omitempty"`
	Deaths  int              `json:"deaths,omitempty"`
	Pickups *DemoInfoPickups `json:"pickups,omitempty"`
	Damage  *DemoInfoDamage  `json:"damage,omitempty"`
}

// DemoInfoAcc contains accuracy stats from KTX JSON (authoritative).
type DemoInfoAcc struct {
	Attacks int `json:"attacks"` // Pellet count for SG/SSG
	Hits    int `json:"hits"`
	Real    int `json:"real,omitempty"`    // Real hits (not splash)
	Virtual int `json:"virtual,omitempty"` // Virtual hits (splash)
}

// DemoInfoKills contains kill breakdown from KTX JSON.
type DemoInfoKills struct {
	Total int `json:"total,omitempty"`
	Team  int `json:"team,omitempty"`
	Enemy int `json:"enemy,omitempty"`
	Self  int `json:"self,omitempty"`
}

// DemoInfoPickups contains pickup stats from KTX JSON.
type DemoInfoPickups struct {
	Dropped         int `json:"dropped,omitempty"`
	Taken           int `json:"taken,omitempty"`
	TotalTaken      int `json:"total-taken,omitempty"`
	SpawnTaken      int `json:"spawn-taken,omitempty"`
	SpawnTotalTaken int `json:"spawn-total-taken,omitempty"`
}

// DemoInfoDamage contains damage breakdown from KTX JSON.
type DemoInfoDamage struct {
	Enemy int `json:"enemy,omitempty"`
	Team  int `json:"team,omitempty"`
}

// DemoInfoItem contains item stats from KTX JSON.
type DemoInfoItem struct {
	Took int `json:"took,omitempty"`
	Time int `json:"time,omitempty"`
}
