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
	Errors           []string                 `json:"errors,omitempty"`
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
	BucketDuration  float64             `json:"bucketDuration"`            // Seconds per graph bucket (1.0s)
	HighResDuration float64             `json:"highResDuration,omitempty"` // Seconds per high-res bucket (0.05s)
	MatchStartTime  float64             `json:"matchStartTime"`            // When match actually started (after warmup)
	DemoOffset      float64             `json:"demoOffset,omitempty"`      // Seconds from demo start to match start (for Hub viewer links)
	Buckets         []TimelineBucket    `json:"buckets"`                   // 1s aggregated buckets for graphs
	HighResBuckets  []HighResBucket     `json:"highResBuckets,omitempty"`  // High-res buckets for map visualization
	FragEvents      []TimelineFragEvent `json:"fragEvents,omitempty"`      // Frag events for score timeline
	PowerupEvents   []PowerupEvent      `json:"powerupEvents,omitempty"`   // Powerup pickups for Key Moments
	FragStreaks      []FragStreakEvent    `json:"fragStreaks,omitempty"`      // Top longest frag streaks for Key Moments
	LocationData    []MapLocation       `json:"locationData,omitempty"`    // Location points from .loc file for map view
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

// HighResBucket - compact bucket for high-resolution map data
// Uses short JSON keys to reduce payload size
type HighResBucket struct {
	T float64                       `json:"t"`           // Start time
	P map[string]*HighResPlayerData `json:"p,omitempty"` // Player data by name
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

// TimelineBucket represents aggregated data for a time slice
type TimelineBucket struct {
	StartTime  float64                      `json:"startTime"`
	EndTime    float64                      `json:"endTime"`
	PlayerData map[string]*PlayerBucketData `json:"playerData"` // Keyed by player name (primary)
	TeamData   map[string]*TeamBucketData   `json:"teamData"`   // Keyed by team name (aggregated from players)
}

// PlayerBucketData holds per-player stats for a time bucket
type PlayerBucketData struct {
	Team string `json:"team"`

	// Weapons (boolean flags in source, but stored as count for consistency)
	HasRL bool `json:"hasRL,omitempty"`
	HasLG bool `json:"hasLG,omitempty"`

	// Powerups
	HasQuad bool `json:"hasQuad,omitempty"`
	HasPent bool `json:"hasPent,omitempty"`
	HasRing bool `json:"hasRing,omitempty"`

	// Health/Armor
	Health    int    `json:"health"`
	Armor     int    `json:"armor"`
	ArmorType string `json:"armorType,omitempty"` // "ga"/"ya"/"ra"

	// Ammo
	Shells  int `json:"shells,omitempty"`
	Nails   int `json:"nails,omitempty"`
	Rockets int `json:"rockets,omitempty"`
	Cells   int `json:"cells,omitempty"`

	// Position (from svc_playerinfo)
	X        float32 `json:"x,omitempty"`        // World X coordinate
	Y        float32 `json:"y,omitempty"`        // World Y coordinate
	Z        float32 `json:"z,omitempty"`        // World Z coordinate
	Location string  `json:"location,omitempty"` // Named location from .loc file
}

// TeamBucketData holds per-team aggregated stats for a time bucket
type TeamBucketData struct {
	// Weapon control (granular)
	PlayersWithRL      int `json:"playersWithRL"`      // RL only (no LG)
	PlayersWithLG      int `json:"playersWithLG"`      // LG only (no RL)
	PlayersWithRLLG    int `json:"playersWithRLLG"`    // Both RL and LG
	PlayersWithWeapons int `json:"playersWithWeapons"` // Total with RL or LG

	// Powerups (granular)
	PlayersWithQuad    int `json:"playersWithQuad"`
	PlayersWithPent    int `json:"playersWithPent"`
	PlayersWithRing    int `json:"playersWithRing"`
	PlayersWithPowerups int `json:"playersWithPowerups"` // Total with any powerup

	// Health/Armor
	AvgHealth   float64 `json:"avgHealth"`
	AvgArmor    float64 `json:"avgArmor"`
	TotalHealth int     `json:"totalHealth,omitempty"` // Sum of all players' health
	TotalArmor  int     `json:"totalArmor,omitempty"`  // Sum of all players' armor

	// Detailed tracking
	ArmorByType  map[string]int `json:"armorByType,omitempty"` // "ga"/"ya"/"ra" -> count
	TotalShells  int            `json:"totalShells,omitempty"`
	TotalNails   int            `json:"totalNails,omitempty"`
	TotalRockets int            `json:"totalRockets,omitempty"`
	TotalCells   int            `json:"totalCells,omitempty"`
}
