// Package analyzer provides pluggable analysis modules for MVD demos.
package analyzer

import (
	"github.com/mvd-analyzer/internal/mvd"
	"github.com/mvd-analyzer/internal/parser"
)

// Context provides shared state for analyzers
type Context struct {
	ServerData  *mvd.ServerData
	Players     [mvd.MaxClients]*mvd.PlayerInfo
	FragsBySlot map[int]int      // Final frag count per slot
	DemoInfo    *DemoInfoResult  // Parsed demoinfo (set during finalization)
	FragEntries []FragEntry      // Frag entries from frag analyzer (set during finalization)
}

// SlotDemoInfo holds the resolved demoinfo player for a slot.
type SlotDemoInfo struct {
	Name string // Display name from demoinfo
	Team string // Team from demoinfo
}

// ResolveSlotDemoInfo bridges slot↔demoinfo using login join (for authenticated
// players) then name join (for unauthenticated players). Returns a map from
// slot number to the matched demoinfo player's display name and team.
func (ctx *Context) ResolveSlotDemoInfo() map[int]SlotDemoInfo {
	result := make(map[int]SlotDemoInfo)
	if ctx.DemoInfo == nil {
		return result
	}

	demoByLogin := make(map[string]*DemoInfoPlayer)
	demoByName := make(map[string]*DemoInfoPlayer)
	nameCount := make(map[string]int)

	for i := range ctx.DemoInfo.Players {
		p := &ctx.DemoInfo.Players[i]
		if p.Name == "" {
			continue
		}
		if p.Login != "" {
			demoByLogin[p.Login] = p
		}
		norm := normalizePlayerName(p.Name)
		nameCount[norm]++
		if nameCount[norm] == 1 {
			demoByName[norm] = p
		} else {
			delete(demoByName, norm)
		}
	}

	for slot, live := range ctx.Players {
		if live == nil {
			continue
		}
		if live.Auth != "" {
			if dp, ok := demoByLogin[live.Auth]; ok {
				result[slot] = SlotDemoInfo{Name: dp.Name, Team: dp.Team}
				continue
			}
		}
		if dp, ok := demoByName[normalizePlayerName(live.Name)]; ok {
			result[slot] = SlotDemoInfo{Name: dp.Name, Team: dp.Team}
		}
	}

	return result
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
	FilePath         string                   `json:"filePath"`
	Duration         float64                  `json:"duration"`
	Match            *MatchResult             `json:"match,omitempty"`
	Frags            *FragResult              `json:"frags,omitempty"`
	Messages         *MessagesResult          `json:"messages,omitempty"`
	DemoInfo         *DemoInfoResult          `json:"demoInfo,omitempty"`
	TimelineAnalysis *TimelineAnalysisResult  `json:"timelineAnalysis,omitempty"`
	Metadata         *MetadataResult          `json:"metadata,omitempty"`
	LocGraph         *LocGraphResult          `json:"locGraph,omitempty"`
	Errors           []string                 `json:"errors,omitempty"`
}

// MetadataResult bundles every demo metadata source we can extract from
// non-payload protocol commands: the bulk `fullserverinfo` cvar dump that
// arrives as a stufftext at connection time, any mid-game serverinfo
// updates, and the parsed match-settings table that KTX renders into the
// countdown centerprint. Together these cover almost all server / match
// config a tournament viewer would want to display.
type MetadataResult struct {
	// ServerInfo is the union of `\key\value\…` pairs from the initial
	// fullserverinfo stufftext plus every per-key svc_serverinfo update
	// that arrived later in the demo. Last-write-wins for keys that get
	// overwritten (e.g. `status` cycles through Countdown → "3 min left"
	// → "2 min left" → ... → Standby).
	ServerInfo map[string]string `json:"serverInfo,omitempty"`

	// MatchSettings is the parsed view of KTX's countdown centerprint —
	// the most reliable source of match-level cvars (mode, deathmatch,
	// teamplay, timelimit, fraglimit, spawn algorithm, antilag,
	// overtime, weapon disables, mode flags). Only populated when KTX
	// rendered a countdown (every standard duel/team match does).
	MatchSettings *MatchSettings `json:"matchSettings,omitempty"`

	// CountdownText is the raw, color-stripped multi-line text of the
	// last countdown centerprint we observed before the match started.
	// Kept verbatim so downstream consumers can display unknown KTX
	// rows we haven't promoted into MatchSettings yet (race scoring
	// systems, hoonymode strings, custom mod stats, etc).
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

// MessagesResult contains match messages (frags and chat) for timeline display
type MessagesResult struct {
	Events []MatchEvent `json:"events"`
}

// MatchEvent represents a frag or chat message in the match
type MatchEvent struct {
	Time    float64 `json:"time"`
	Type    string  `json:"type"`    // "frag", "chat", "teamsay"
	Player  string  `json:"player"`  // Who sent/killed
	Team    string  `json:"team"`    // Player's team
	Message string  `json:"message"` // Chat text or frag description
	Victim  string  `json:"victim,omitempty"` // For frags
	Weapon  string  `json:"weapon,omitempty"` // For frags
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
	Handicap    int                         `json:"handicap,omitempty"`
	Bot         *DemoInfoBot                `json:"bot,omitempty"`
	Stats       *DemoInfoStats              `json:"stats,omitempty"`
	Dmg         *DemoInfoDmg                `json:"dmg,omitempty"`
	Spree       *DemoInfoSpree              `json:"spree,omitempty"`
	Control     float64                     `json:"control,omitempty"`
	Speed       *DemoInfoSpeed              `json:"speed,omitempty"`
	XferRL      int                         `json:"xferRL,omitempty"`
	XferLG      int                         `json:"xferLG,omitempty"`
	Weapons     map[string]*DemoInfoWeapon  `json:"weapons,omitempty"`
	Items       map[string]*DemoInfoItem    `json:"items,omitempty"`
}

// DemoInfoBot is the per-player bot block KTX writes when the player slot
// is held by a frogbot. Only present when KTX was built with BOT_SUPPORT
// and the player is a bot. Useful to flag automated demos and to filter
// bot-vs-human matches.
type DemoInfoBot struct {
	Skill      int  `json:"skill"`
	Customised bool `json:"customised"`
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

// TimelineAnalysisResult contains time-bucketed data for timeline visualization
type TimelineAnalysisResult struct {
	HighResDuration float64             `json:"highResDuration,omitempty"` // Seconds per high-res bucket (0.05s)
	MatchStartTime  float64             `json:"matchStartTime"`            // When match actually started (after warmup)
	DemoOffset      float64             `json:"demoOffset,omitempty"`      // Seconds from demo start to match start (for Hub viewer links)
	HighResBuckets  []HighResBucket     `json:"highResBuckets,omitempty"`  // High-res buckets with per-player and per-team data
	FragEvents      []TimelineFragEvent `json:"fragEvents,omitempty"`      // Frag events for score timeline
	PowerupEvents   []PowerupEvent      `json:"powerupEvents,omitempty"`   // Powerup pickups for Key Moments
	FragStreaks      []FragStreakEvent    `json:"fragStreaks,omitempty"`      // Top longest frag streaks for Key Moments
	LocationData    []MapLocation       `json:"locationData,omitempty"`    // Location points from .loc file for map view
	LocTable        []string            `json:"locTable,omitempty"`        // Interned loc names; index 0 is "" sentinel. HighResPlayerData.Li indexes into this.
	PlayerUserIDs   map[string]int      `json:"playerUserIDs,omitempty"`   // Player name -> UserID for Hub viewer links
	RegionControl   *RegionControlResult `json:"regionControl,omitempty"`  // Region control stats
}

// ControlRegion represents a named area on the map for control tracking
type ControlRegion struct {
	Name      string        `json:"name"`
	Points    []MapLocation `json:"points"`
	CentroidX float32       `json:"centroidX"`
	CentroidY float32       `json:"centroidY"`
}

// RegionControlResult contains auto-detected region definitions
type RegionControlResult struct {
	Regions []ControlRegion `json:"regions"`
}

// HighResBucket - compact bucket for high-resolution timeline data.
// Uses short JSON keys to reduce payload size. Each bucket contains
// per-player state snapshots and pre-computed team aggregations.
type HighResBucket struct {
	T  float64                       `json:"t"`            // Start time
	P  map[string]*HighResPlayerData `json:"p,omitempty"`  // Player data by name
	TD map[string]*HighResTeamData   `json:"td,omitempty"` // Pre-computed team aggregations by team name
}

// HighResTeamData holds pre-computed team-level aggregations for a single
// high-res bucket. Compact JSON keys match the player data convention.
type HighResTeamData struct {
	RL   int            `json:"rl,omitempty"`   // Players with RL only (no LG)
	LG   int            `json:"lg,omitempty"`   // Players with LG only (no RL)
	RLLG int            `json:"rllg,omitempty"` // Players with both RL and LG
	W    int            `json:"w,omitempty"`    // Total players with RL or LG
	Q    int            `json:"q,omitempty"`    // Players with quad
	Pe   int            `json:"pe,omitempty"`   // Players with pent
	R    int            `json:"r,omitempty"`    // Players with ring
	Pw   int            `json:"pw,omitempty"`   // Total with any powerup
	TH   int            `json:"th,omitempty"`   // Total health (sum across team)
	TA   int            `json:"ta,omitempty"`   // Total armor (sum across team)
	ABT  map[string]int `json:"abt,omitempty"`  // Armor by type: "ra"/"ya"/"ga" -> player count
}

// HighResPlayerData - full player state snapshot (compact keys)
type HighResPlayerData struct {
	X       float32 `json:"x"`
	Y       float32 `json:"y"`
	H       int     `json:"h"`             // Health
	A       int     `json:"a"`             // Armor
	AT      string  `json:"at,omitempty"`  // Armor type: "ga"/"ya"/"ra"
	RL      bool    `json:"rl,omitempty"`  // Has rocket launcher
	LG      bool    `json:"lg,omitempty"`  // Has lightning gun
	SSG     bool    `json:"ssg,omitempty"` // Has super shotgun
	SNG     bool    `json:"sng,omitempty"` // Has super nailgun
	Q       bool    `json:"q,omitempty"`   // Has quad
	Pent    bool    `json:"pe,omitempty"`  // Has pent
	R       bool    `json:"r,omitempty"`   // Has ring
	Rockets int     `json:"rk,omitempty"`  // Rocket ammo
	Cells   int     `json:"cl,omitempty"`  // Cell ammo
	D       bool    `json:"d,omitempty"`   // Death frame marker
	Sp      bool    `json:"sp,omitempty"`  // Spawn frame marker
	Li      int     `json:"li,omitempty"`  // Loc-name index into TimelineAnalysisResult.LocTable (0 = no loc)
}

// MapLocation represents a named point in a map for visualization
type MapLocation struct {
	X    float32 `json:"x"`
	Y    float32 `json:"y"`
	Z    float32 `json:"z"`
	Name string  `json:"name"`
}

// TimelineFragEvent represents a single frag with time, player and team info
type TimelineFragEvent struct {
	Time   float64 `json:"time"`
	Player string  `json:"player"` // Player name who got the frag
	Team   string  `json:"team"`
	Delta  int     `json:"delta"` // Frag count change (+1 for kill, -1 for suicide/teamkill)
}

// PowerupEvent represents a powerup pickup event for Key Moments
type PowerupEvent struct {
	Time         float64 `json:"time"`         // Demo time when picked up
	EndTime      float64 `json:"endTime"`      // Demo time when lost/expired
	PlayerName   string  `json:"playerName"`   // Player name
	PlayerSlot   int     `json:"playerSlot"`   // Player slot in demo
	PlayerUserID int     `json:"playerUserID"` // Player UserID for Hub viewer track param
	Team         string  `json:"team"`         // Player's team
	PowerupType  string  `json:"powerupType"`  // "quad", "pent", or "ring"
	Duration     float64 `json:"duration"`     // Seconds held
	Frags        int     `json:"frags"`        // Kills during powerup run
}

// FragStreakEvent represents a frag streak (spawn-to-death run) for Key Moments
type FragStreakEvent struct {
	Time         float64 `json:"time"`         // Demo time when player spawned
	EndTime      float64 `json:"endTime"`      // Demo time when player died (or match ended)
	PlayerName   string  `json:"playerName"`   // Player name
	PlayerUserID int     `json:"playerUserID"` // Player UserID for Hub viewer track param
	Team         string  `json:"team"`         // Player's team
	Frags        int     `json:"frags"`        // Number of kills during run
	Duration     float64 `json:"duration"`     // Seconds alive
	Ewep         string  `json:"ewep"`         // Effective weapon (most kills with)
}

