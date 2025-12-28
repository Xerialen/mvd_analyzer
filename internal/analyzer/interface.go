// Package analyzer provides pluggable analysis modules for MVD demos.
package analyzer

import (
	"github.com/mvd-analyzer/internal/mvd"
	"github.com/mvd-analyzer/internal/parser"
)

// Context provides shared state for analyzers
type Context struct {
	ServerData *mvd.ServerData
	Players    [mvd.MaxClients]*mvd.PlayerInfo
}

// Analyzer is the interface for analysis modules
type Analyzer interface {
	// Name returns the unique identifier for this analyzer
	Name() string

	// Init is called before parsing begins
	Init(ctx *Context) error

	// OnEvent receives parsed events during parsing
	OnEvent(event parser.Event) error

	// Finalize is called after parsing completes and returns results
	Finalize() (interface{}, error)
}

// Result holds results from all analyzers
type Result struct {
	FilePath    string             `json:"filePath"`
	Duration    float64            `json:"duration"`
	Match       *MatchResult       `json:"match,omitempty"`
	Frags       *FragResult        `json:"frags,omitempty"`
	Stats       *StatsResult       `json:"stats,omitempty"`
	WeaponStats *WeaponStatsResult `json:"weaponStats,omitempty"`
	DemoInfo    *DemoInfoResult    `json:"demoInfo,omitempty"`
	Errors      []string           `json:"errors,omitempty"`
}

// MatchResult contains match summary information
type MatchResult struct {
	Map       string       `json:"map"`
	GameDir   string       `json:"gameDir"`
	Duration  float64      `json:"duration"`
	StartTime float64      `json:"startTime,omitempty"`
	EndTime   float64      `json:"endTime,omitempty"`
	Players   []PlayerStat `json:"players"`
	Teams     []TeamStat   `json:"teams,omitempty"`
}

// PlayerStat represents a player's final statistics
type PlayerStat struct {
	Name   string `json:"name"`
	Team   string `json:"team"`
	Kills  int    `json:"kills"`
	Deaths int    `json:"deaths"`
	Frags  int    `json:"frags"`
}

// TeamStat represents a team's statistics
type TeamStat struct {
	Name  string `json:"name"`
	Frags int    `json:"frags"`
}

// FragResult contains frag analysis results
type FragResult struct {
	TotalFrags int         `json:"totalFrags"`
	Frags      []FragEntry `json:"frags"`
	ByWeapon   map[string]int `json:"byWeapon"`
	ByPlayer   map[string]*PlayerFrags `json:"byPlayer"`
}

// FragEntry represents a single frag event
type FragEntry struct {
	Time       float64 `json:"time"`
	Killer     string  `json:"killer"`
	Victim     string  `json:"victim"`
	Weapon     string  `json:"weapon"`
	IsSuicide  bool    `json:"isSuicide,omitempty"`
	IsTeamKill bool    `json:"isTeamKill,omitempty"`
}

// PlayerFrags holds per-player frag statistics
type PlayerFrags struct {
	Kills  int            `json:"kills"`
	Deaths int            `json:"deaths"`
	ByWeapon map[string]int `json:"byWeapon"`
}

// StatsResult contains stats tracking results
type StatsResult struct {
	PlayerStats map[string]*PlayerStatsEntry `json:"playerStats"`
}

// PlayerStatsEntry holds final stats for a player
type PlayerStatsEntry struct {
	MaxHealth int `json:"maxHealth"`
	MaxArmor  int `json:"maxArmor"`
}

// WeaponStatsResult contains weapon usage statistics
type WeaponStatsResult struct {
	PlayerStats map[string]*PlayerWeaponStatsEntry `json:"playerStats"`
}

// PlayerWeaponStatsEntry holds weapon stats for a player
type PlayerWeaponStatsEntry struct {
	Weapons     map[string]*WeaponStatEntry `json:"weapons"`
	Environment *EnvironmentalDamage        `json:"environment,omitempty"`
}

// EnvironmentalDamage tracks damage received from environmental sources
type EnvironmentalDamage struct {
	Lava    int `json:"lava,omitempty"`    // Damage from lava
	Slime   int `json:"slime,omitempty"`   // Damage from slime
	Drown   int `json:"drown,omitempty"`   // Damage from drowning
	Fall    int `json:"fall,omitempty"`    // Fall damage
	Squish  int `json:"squish,omitempty"`  // Crush damage (world-attributed)
	Trigger int `json:"trigger,omitempty"` // trigger_hurt damage
}

// WeaponStatEntry holds statistics for a single weapon
type WeaponStatEntry struct {
	Shots      int     `json:"shots"`
	Hits       int     `json:"hits"`
	Damage     int     `json:"damage"`
	Overkill   int     `json:"overkill,omitempty"`
	TeamDamage int     `json:"teamDamage,omitempty"`
	SelfDamage int     `json:"selfDamage,omitempty"`
	Accuracy   float64 `json:"accuracy"`
}

// DemoInfoResult contains parsed KTX embedded JSON stats (authoritative)
type DemoInfoResult struct {
	Version   int               `json:"version,omitempty"`
	Date      string            `json:"date,omitempty"`
	Map       string            `json:"map,omitempty"`
	Hostname  string            `json:"hostname,omitempty"`
	IP        string            `json:"ip,omitempty"`
	Port      int               `json:"port,omitempty"`
	Mode      string            `json:"mode,omitempty"`
	Timelimit int               `json:"timelimit,omitempty"`
	Fraglimit int               `json:"fraglimit,omitempty"`
	Duration  int               `json:"duration,omitempty"`
	Demo      string            `json:"demo,omitempty"`
	Teams     []string          `json:"teams,omitempty"`
	Players   []DemoInfoPlayer  `json:"players,omitempty"`
	RawJSON   string            `json:"rawJson,omitempty"` // For debugging failed parses
}

// DemoInfoPlayer contains player stats from KTX JSON
type DemoInfoPlayer struct {
	Name        string                      `json:"name"`
	Team        string                      `json:"team"`
	TopColor    int                         `json:"topColor,omitempty"`
	BottomColor int                         `json:"bottomColor,omitempty"`
	Ping        int                         `json:"ping,omitempty"`
	Login       string                      `json:"login,omitempty"`
	Stats       *DemoInfoStats              `json:"stats,omitempty"`
	Dmg         *DemoInfoDmg                `json:"dmg,omitempty"`
	Spree       *DemoInfoSpree              `json:"spree,omitempty"`
	Control     float64                     `json:"control,omitempty"`
	Speed       *DemoInfoSpeed              `json:"speed,omitempty"`
	Weapons     map[string]*DemoInfoWeapon  `json:"weapons,omitempty"`
	Items       map[string]*DemoInfoItem    `json:"items,omitempty"`
}

// DemoInfoStats contains frag/death stats from KTX JSON
type DemoInfoStats struct {
	Frags      int `json:"frags"`
	Deaths     int `json:"deaths"`
	TK         int `json:"tk,omitempty"`
	SpawnFrags int `json:"spawn-frags,omitempty"`
	Kills      int `json:"kills,omitempty"`
	Suicides   int `json:"suicides,omitempty"`
}

// DemoInfoDmg contains damage stats from KTX JSON
type DemoInfoDmg struct {
	Taken        int `json:"taken"`
	Given        int `json:"given"`
	Team         int `json:"team,omitempty"`
	Self         int `json:"self,omitempty"`
	TeamWeapons  int `json:"team-weapons,omitempty"`
	EnemyWeapons int `json:"enemy-weapons,omitempty"`
	TakenToDie   int `json:"taken-to-die,omitempty"`
}

// DemoInfoSpree contains spree stats from KTX JSON
type DemoInfoSpree struct {
	Max  int `json:"max,omitempty"`
	Quad int `json:"quad,omitempty"`
}

// DemoInfoSpeed contains speed stats from KTX JSON
type DemoInfoSpeed struct {
	Max float64 `json:"max,omitempty"`
	Avg float64 `json:"avg,omitempty"`
}

// DemoInfoWeapon contains weapon stats from KTX JSON
type DemoInfoWeapon struct {
	Acc     *DemoInfoAcc     `json:"acc,omitempty"`
	Kills   *DemoInfoKills   `json:"kills,omitempty"`
	Deaths  int              `json:"deaths,omitempty"`
	Pickups *DemoInfoPickups `json:"pickups,omitempty"`
	Damage  *DemoInfoDamage  `json:"damage,omitempty"`
}

// DemoInfoAcc contains accuracy stats from KTX JSON (authoritative)
type DemoInfoAcc struct {
	Attacks int `json:"attacks"` // Pellet count for SG/SSG
	Hits    int `json:"hits"`
	Real    int `json:"real,omitempty"`    // Real hits (not splash)
	Virtual int `json:"virtual,omitempty"` // Virtual hits (splash)
}

// DemoInfoKills contains kill breakdown from KTX JSON
type DemoInfoKills struct {
	Total    int `json:"total,omitempty"`
	Team     int `json:"team,omitempty"`
	Enemy    int `json:"enemy,omitempty"`
	Self     int `json:"self,omitempty"`
}

// DemoInfoPickups contains pickup stats from KTX JSON
type DemoInfoPickups struct {
	Dropped        int `json:"dropped,omitempty"`
	Taken          int `json:"taken,omitempty"`
	TotalTaken     int `json:"total-taken,omitempty"`
	SpawnTaken     int `json:"spawn-taken,omitempty"`
	SpawnTotalTaken int `json:"spawn-total-taken,omitempty"`
}

// DemoInfoDamage contains damage breakdown from KTX JSON
type DemoInfoDamage struct {
	Enemy int `json:"enemy,omitempty"`
	Team  int `json:"team,omitempty"`
}

// DemoInfoItem contains item stats from KTX JSON
type DemoInfoItem struct {
	Took int `json:"took,omitempty"`
	Time int `json:"time,omitempty"`
}
