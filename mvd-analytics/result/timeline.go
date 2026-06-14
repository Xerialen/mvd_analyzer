package result

// TimelineAnalysisResult contains the event-shaped derived results
// (frag events, powerup events, streaks) plus the loc / region
// metadata needed to interpret per-player data in result.Streams.
//
// HighResBuckets and HighResDuration were deleted at schema v7;
// bucketed data is now produced on demand by qwanalytics/view.Buckets.
// Timing and the wall-clock anchor moved to Streams.Global at schema v23
// (MatchStartTime, DemoOffset, DemoStartUnixMs, DemoStartAccuracyMs, Pauses).
// What remains here is the event-shaped derived data plus map/loc metadata.
type TimelineAnalysisResult struct {
	FragEvents    []TimelineFragEvent  `json:"fragEvents,omitempty"`    // Frag events for score timeline
	DeathEvents   []TimelineDeathEvent `json:"deathEvents,omitempty"`   // Per-player deaths for the frags/deaths drill-down
	KillEvents    []TimelineKillEvent  `json:"killEvents,omitempty"`    // Per-player enemy kills for the frags/deaths drill-down
	PowerupEvents []PowerupEvent       `json:"powerupEvents,omitempty"` // Powerup pickups for Key Moments
	FragStreaks   []FragStreakEvent    `json:"fragStreaks,omitempty"`   // Top longest frag streaks for Key Moments
	Airgibs       []AirgibEvent        `json:"airgibs,omitempty"`       // Top airborne rocket hits (airgibs) for Key Moments
	LocationData  []MapLocation        `json:"locationData,omitempty"`  // Location points from .loc file for map view
	LocTable      []string             `json:"locTable,omitempty"`      // Interned loc names; index 0 is "" sentinel.
	PlayerUserIDs map[string]int       `json:"playerUserIDs,omitempty"` // Player name -> UserID for Hub viewer links
	RegionControl *RegionControlResult `json:"regionControl,omitempty"` // Region control stats
}

// AirgibEvent is one enemy rocket hit landed on an airborne victim — an
// "airgib" — surfaced in Key Moments (schema v25). Height is the victim's
// feet above the floor at the moment of the hit (PositionTrack.H), so the
// list is meaningful only on maps with a provisioned BSP (where H is
// populated). The analyzer emits every qualifying hit sorted by Height
// descending (uncapped since schema v30 — the airgibMinHeightUnits
// qualification threshold already bounds the list); the web view
// re-sorts client-side.
//
// "Airgib" here is a DIRECT enemy rocket hit (the rocket model striking
// the player — splash/radius hits are excluded) whose victim was at least
// airgibMinHeightUnits above the floor; self / teammate / environmental
// hits are excluded too. Lethal records whether the hit killed: a matching
// rocket frag (same attacker→victim) within a short window of the hit. On
// the rare back-to-back double-rocket exchange this window can attribute
// lethality to the airborne hit when a later rocket landed the kill; it is
// a highlight heuristic, not an exact killing-blow flag.
type AirgibEvent struct {
	Time           int32   `json:"time"`                     // hit time, match-relative ms
	Attacker       string  `json:"attacker"`                 // resolved name of the rocketeer
	AttackerTeam   string  `json:"attackerTeam,omitempty"`   //
	AttackerUserID int     `json:"attackerUserID,omitempty"` // for Hub viewer links (shooter perspective)
	Victim         string  `json:"victim"`                   // resolved name of the airborne victim
	VictimTeam     string  `json:"victimTeam,omitempty"`     //
	VictimUserID   int     `json:"victimUserID,omitempty"`   //
	Height         float32 `json:"height"`                   // victim feet above floor at the hit (units)
	// HeightAboveAttacker is the victim's origin minus the shooter's at
	// the hit (units; negative when the victim was below the shooter) —
	// the vertical gap the rocket climbed, often what makes an airgib
	// look spectacular independent of the floor height (schema v29).
	// Origin-to-origin, so the equal hull offsets cancel. 0 (omitted)
	// when the shooter had no position sample near the hit; a genuine
	// dead-level hit also reads 0.
	HeightAboveAttacker float32 `json:"heightAboveAttacker,omitempty"`
	Loc                 string  `json:"loc,omitempty"`    // victim's loc at the hit
	Damage              int     `json:"damage"`           // raw rocket damage (unbound, incl. overkill)
	Lethal              bool    `json:"lethal,omitempty"` // the hit killed the victim (matching rocket frag)
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
// match-aggregate control state. At schema v7 the per-bucket
// BucketStates field is no longer baked into the default result —
// callers that want it ask for it via view.RegionControl(opts) or the
// WASM bridge's recomputeRegionControl, which derive it at the
// requested resolution from result.Streams.
//
// BucketStates may still be populated by query-time results (the
// JSON shape is unchanged when the field is present): one ASCII char
// per bucket per region. Codes mirror classifyRegionState in
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
//
// ByPlayer attributes presence to individual players: who actually held
// the region. Each entry counts the number of buckets that player was
// observed in the region, split by whether they were armed (carrying
// RL or LG) at the time. Consumers answer "who kept red?" by sorting
// the region's ByPlayer entries by Armed+Unarmed descending; "who was
// the armed presence?" by sorting on Armed alone.
type RegionStats struct {
	TeamAControl     float64                      `json:"teamAControl"`
	TeamAWeakControl float64                      `json:"teamAWeakControl"`
	Contested        float64                      `json:"contested"`
	WeakContested    float64                      `json:"weakContested"`
	Empty            float64                      `json:"empty"`
	TeamBWeakControl float64                      `json:"teamBWeakControl"`
	TeamBControl     float64                      `json:"teamBControl"`
	ByPlayer         map[string]RegionPlayerStats `json:"byPlayer,omitempty"`
}

// RegionPlayerStats is one player's presence in one region, summed
// across all buckets in the (sub-)match window. Multiplying Armed or
// Unarmed by the bucket WindowMs yields presence in milliseconds.
type RegionPlayerStats struct {
	Team    string `json:"team"`
	Armed   int    `json:"armed"`   // bucket count present while carrying RL or LG
	Unarmed int    `json:"unarmed"` // bucket count present without RL/LG
}

// MapLocation represents a named point in a map for visualization.
type MapLocation struct {
	X    float32 `json:"x"`
	Y    float32 `json:"y"`
	Z    float32 `json:"z"`
	Name string  `json:"name"`
}

// TimelineFragEvent represents a single frag with time, player and team info.
// Time is integer milliseconds (schema v8).
type TimelineFragEvent struct {
	Time   int32  `json:"time"`
	Player string `json:"player"` // Player name who got the frag
	Team   string `json:"team"`
	Delta  int    `json:"delta"` // Frag count change (+1 for kill, -1 for suicide/teamkill)
}

// TimelinePause represents one game pause as a flat segment in the
// game-time→wall-clock mapping. AtMs is the match-relative game time (ms)
// at which the game clock froze (negative if the pause occurred during the
// countdown/warmup). DurationMs is the real wall-clock time the pause
// consumed, recovered by summing the mvdhidden 0x000A (paused_duration)
// samples mvdsv embeds once per idle frame while paused. See the
// TimelineAnalysisResult.DemoStartUnixMs formula for how consumers fold
// these into a wall-clock mapping.
type TimelinePause struct {
	AtMs       int32 `json:"atMs"`       // match-relative game time the pause sits at (clock frozen here)
	DurationMs int32 `json:"durationMs"` // real wall-clock ms the pause lasted
}

// TimelineDeathEvent represents a single death pinned to the player who
// died, for the per-player frags/deaths drill-down. Sourced from the
// authoritative protocol DeathEvent (every death counts once — enemy
// kill, suicide, world, or being teamkilled), matching KTX's
// player->deaths (ktx/src/client.c) and thus its efficiency definition.
// Time is integer milliseconds (schema v8).
type TimelineDeathEvent struct {
	Time   int32  `json:"time"`
	Player string `json:"player"` // Player name who died
	Team   string `json:"team"`
}

// TimelineKillEvent represents a single enemy kill pinned to the player
// who got it (the killer), for the per-player frags/deaths drill-down.
// Sourced from the same canonical frag log (FragEntries) that
// frags.byPlayer[name].kills is counted from, filtered to real enemy
// kills (suicides and teamkills excluded). A player's cumulative
// killEvents therefore reconciles exactly with byPlayer.kills and with
// the kills-based efficiency = kills/(kills+deaths). Time is integer
// milliseconds (schema v8).
type TimelineKillEvent struct {
	Time   int32  `json:"time"`
	Player string `json:"player"` // Player name who got the kill (killer)
	Team   string `json:"team"`
}

// PowerupEvent represents a powerup pickup event for Key Moments.
// Time/EndTime/Duration are integer milliseconds (schema v8).
type PowerupEvent struct {
	Time         int32  `json:"time"`         // Demo time when picked up (ms)
	EndTime      int32  `json:"endTime"`      // Demo time when lost/expired (ms)
	PlayerName   string `json:"playerName"`   // Player name
	PlayerSlot   int    `json:"playerSlot"`   // Player slot in demo
	PlayerUserID int    `json:"playerUserID"` // Player UserID for Hub viewer track param
	Team         string `json:"team"`         // Player's team
	PowerupType  string `json:"powerupType"`  // "quad", "pent", or "ring"
	Duration     int32  `json:"duration"`     // Milliseconds held
	Frags        int    `json:"frags"`        // Kills during powerup run
}

// FragStreakEvent represents a frag streak (spawn-to-death run) for Key Moments.
// Time/EndTime/Duration are integer milliseconds (schema v8).
type FragStreakEvent struct {
	Time         int32  `json:"time"`         // Demo time when player spawned (ms)
	EndTime      int32  `json:"endTime"`      // Demo time when player died (or match ended) (ms)
	PlayerName   string `json:"playerName"`   // Player name
	PlayerUserID int    `json:"playerUserID"` // Player UserID for Hub viewer track param
	Team         string `json:"team"`         // Player's team
	Frags        int    `json:"frags"`        // Number of kills during run
	Duration     int32  `json:"duration"`     // Milliseconds alive
	Ewep         string `json:"ewep"`         // Effective weapon (most kills with)
}
