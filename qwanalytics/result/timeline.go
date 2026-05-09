package result

// TimelineAnalysisResult contains time-bucketed data for timeline visualization.
type TimelineAnalysisResult struct {
	HighResDuration float64              `json:"highResDuration,omitempty"` // Seconds per high-res bucket (0.05s)
	MatchStartTime  float64              `json:"matchStartTime"`            // When match actually started (after warmup)
	DemoOffset      float64              `json:"demoOffset,omitempty"`      // Seconds from demo start to match start (for Hub viewer links)
	HighResBuckets  []HighResBucket      `json:"highResBuckets,omitempty"`  // High-res buckets with per-player and per-team data
	FragEvents      []TimelineFragEvent  `json:"fragEvents,omitempty"`      // Frag events for score timeline
	PowerupEvents   []PowerupEvent       `json:"powerupEvents,omitempty"`   // Powerup pickups for Key Moments
	FragStreaks     []FragStreakEvent    `json:"fragStreaks,omitempty"`     // Top longest frag streaks for Key Moments
	LocationData    []MapLocation        `json:"locationData,omitempty"`    // Location points from .loc file for map view
	LocTable        []string             `json:"locTable,omitempty"`        // Interned loc names; index 0 is "" sentinel.
	PlayerUserIDs   map[string]int       `json:"playerUserIDs,omitempty"`   // Player name -> UserID for Hub viewer links
	RegionControl   *RegionControlResult `json:"regionControl,omitempty"`   // Region control stats
}

// ControlRegion represents a named area on the map for control tracking.
//
// Regions are loc-name lists, not polygons: Locs is the authoritative
// logical membership (a player is "in" the region iff their resolved
// loc name is in this list). Points and Centroid are rendering anchors
// derived from the matching MapLocation entries — useful for drawing
// the region overlay on the map but not used by the control classifier.
type ControlRegion struct {
	Name      string        `json:"name"`
	Locs      []string      `json:"locs"`
	Points    []MapLocation `json:"points"`
	CentroidX float32       `json:"centroidX"`
	CentroidY float32       `json:"centroidY"`
}

// RegionControlResult contains region definitions plus per-bucket and
// match-aggregate control state computed by analyzer.ComputeRegionControl.
//
// BucketStates is a compact representation: one ASCII char per
// HighResBucket per region. Codes mirror classifyRegionState in
// qw-web/static/app.js:
//
//	'_'  empty (no living players)
//	'A'  teamAControl     (only A present armed; or both present, only A armed)
//	'a'  teamAWeakControl (only A present, none armed)
//	'C'  contested        (both present, both armed)
//	'c'  weakContested    (both present, neither armed)
//	'B'/'b' mirror of A/a
//
// "Armed" = carrying RL or LG. TeamA / TeamB name which match.teams[]
// entry the encoding mapped to "A" and "B".
type RegionControlResult struct {
	Regions      []ControlRegion        `json:"regions"`
	TeamA        string                 `json:"teamA,omitempty"`
	TeamB        string                 `json:"teamB,omitempty"`
	BucketStates map[string]string      `json:"bucketStates,omitempty"`
	Stats        map[string]RegionStats `json:"stats,omitempty"`
}

// RegionStats is the match-aggregate share of each control state for a
// single region, expressed as a percentage (0..100, one decimal place).
// The seven values sum to 100 within rounding.
type RegionStats struct {
	TeamAControl     float64 `json:"teamAControl"`
	TeamAWeakControl float64 `json:"teamAWeakControl"`
	Contested        float64 `json:"contested"`
	WeakContested    float64 `json:"weakContested"`
	Empty            float64 `json:"empty"`
	TeamBWeakControl float64 `json:"teamBWeakControl"`
	TeamBControl     float64 `json:"teamBControl"`
}

// HighResBucket is a compact bucket for high-resolution timeline data.
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
	GL   int            `json:"gl,omitempty"`   // Players carrying GL (independent of RL/LG)
	Q    int            `json:"q,omitempty"`    // Players with quad
	Pe   int            `json:"pe,omitempty"`   // Players with pent
	R    int            `json:"r,omitempty"`    // Players with ring
	Pw   int            `json:"pw,omitempty"`   // Total with any powerup
	TH   int            `json:"th,omitempty"`   // Total health (sum across team)
	TA   int            `json:"ta,omitempty"`   // Total armor (sum across team)
	ABT  map[string]int `json:"abt,omitempty"`  // Armor by type: "ra"/"ya"/"ga" -> player count
}

// HighResPlayerData is a full player state snapshot (compact keys).
type HighResPlayerData struct {
	X       float32 `json:"x"`
	Y       float32 `json:"y"`
	Z       float32 `json:"z"`             // World z from svc_playerinfo origin[2]
	H       int     `json:"h"`             // Health
	A       int     `json:"a"`             // Armor
	AT      string  `json:"at,omitempty"`  // Armor type: "ga"/"ya"/"ra"
	RL      bool    `json:"rl,omitempty"`  // Has rocket launcher
	LG      bool    `json:"lg,omitempty"`  // Has lightning gun
	GL      bool    `json:"gl,omitempty"`  // Has grenade launcher
	SSG     bool    `json:"ssg,omitempty"` // Has super shotgun
	SNG     bool    `json:"sng,omitempty"` // Has super nailgun
	Q       bool    `json:"q,omitempty"`   // Has quad
	Pent    bool    `json:"pe,omitempty"`  // Has pent
	R       bool    `json:"r,omitempty"`   // Has ring
	Shells  int     `json:"sh,omitempty"`  // Shotgun shells
	Nails   int     `json:"nl,omitempty"`  // Nailgun nails
	Rockets int     `json:"rk,omitempty"`  // Rocket ammo
	Cells   int     `json:"cl,omitempty"`  // Cell ammo
	D       bool    `json:"d,omitempty"`   // Death frame marker
	Sp      bool    `json:"sp,omitempty"`  // Spawn frame marker
	Li      int     `json:"li,omitempty"`  // Loc-name index into TimelineAnalysisResult.LocTable (0 = no loc)
}

// MapLocation represents a named point in a map for visualization.
type MapLocation struct {
	X    float32 `json:"x"`
	Y    float32 `json:"y"`
	Z    float32 `json:"z"`
	Name string  `json:"name"`
}

// TimelineFragEvent represents a single frag with time, player and team info.
type TimelineFragEvent struct {
	Time   float64 `json:"time"`
	Player string  `json:"player"` // Player name who got the frag
	Team   string  `json:"team"`
	Delta  int     `json:"delta"` // Frag count change (+1 for kill, -1 for suicide/teamkill)
}

// PowerupEvent represents a powerup pickup event for Key Moments.
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

// FragStreakEvent represents a frag streak (spawn-to-death run) for Key Moments.
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
