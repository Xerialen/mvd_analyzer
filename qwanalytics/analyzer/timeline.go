package analyzer

import (
	"strings"

	"github.com/mvd-analyzer/qwanalytics/loc"
	"github.com/mvd-analyzer/qwdemo/events"
)

// TimelineAnalyzer tracks time-bucketed player state for the timeline view.
//
// The analyzer is split across several files in this package:
//
//   - timeline.go            (this file) state, types, OnEvent, sampling
//   - timeline_finalize.go   Finalize orchestration
//   - timeline_buckets.go    high-res export + window aggregation for graphs
//   - timeline_powerups.go   powerup pickup/loss event detection
//   - timeline_streaks.go    spawn-to-death frag streak detection
//   - timeline_regions.go    map region control auto-detection + custom defs
type TimelineAnalyzer struct {
	ctx                 *Context
	bucketDuration      float64 // High-res sampling interval (default 50ms)
	playerState         map[int]*timelinePlayerState
	playerNames         map[int]string // Slot -> player name (from UserInfoEvent)
	playerUserIDs       map[int]int    // Slot -> UserID (for Hub viewer track param)
	buckets             []*timelineBucketData
	rawFrags       []fragEvent  // Raw frag events (player num, time)
	rawDeaths      []deathEvent // Raw death events (player num, time)
	rawSpawns      []deathEvent // Raw spawn events (reusing deathEvent type)
	lastSampleTime      float64
	matchStartTime      float64
	matchStarted        bool
	matchEndTime        float64
	matchEnded          bool
	locFinder           *loc.Finder // Location finder for map (nil if no .loc file)
}

// fragEvent tracks a frag before team assignment
type fragEvent struct {
	Time      float64
	PlayerNum int
	Delta     int // +N for kills, -N for suicides/teamkills
}

// deathEvent tracks a player death (detected via health transition)
type deathEvent struct {
	Time      float64
	PlayerNum int
}

// timelinePlayerState tracks current state for a single player as the
// parser walks the demo. items is the raw item bitfield from svc_updatestat;
// it's decoded into weapons/powerups/armor type at sampling time.
type timelinePlayerState struct {
	items      int // raw item bitfield (weapons, powerups, armor type)
	vitals     vitals
	prevHealth int // previous sample's health, for death/spawn edge detection
	ammo       ammoCounts
	pos        playerPosition
	frags      int
}

// timelineBucketData holds raw aggregated data during analysis
type timelineBucketData struct {
	startTime  float64
	endTime    float64
	playerData map[int]*playerBucketRawData // Keyed by slot
}

// playerBucketRawData holds per-player data for a single high-res bucket.
// Sub-structs (weapons, powerups, vitals, ammo, pos) are shared with
// playerWindowAggregate so the aggregation loop can copy whole groups at a
// time.
type playerBucketRawData struct {
	name     string
	team     string
	weapons  weaponLoadout
	powerups powerupLoadout
	vitals   vitals
	ammo     ammoCounts
	pos      playerPosition
	location string // named location from .loc file
	dead     bool   // true for death-frame entries (health just went to 0)
	spawn    bool   // true for spawn-frame entries (health just went from 0 to >0)
}

// NewTimelineAnalyzer creates a new timeline analyzer
func NewTimelineAnalyzer() *TimelineAnalyzer {
	return &TimelineAnalyzer{
		bucketDuration:      DefaultHighResBucketDuration, // 50ms for high-res data
		playerState:         make(map[int]*timelinePlayerState),
		playerNames:         make(map[int]string),
		playerUserIDs:       make(map[int]int),
		buckets:             make([]*timelineBucketData, 0, timelineBucketPrealloc),
	}
}

// SetBucketDuration allows configuring the high-res sampling interval.
// Must be called before Init(). Common values: 0.01 (10ms), 0.05 (50ms), 0.1 (100ms)
func (a *TimelineAnalyzer) SetBucketDuration(duration float64) {
	a.bucketDuration = duration
}

func (a *TimelineAnalyzer) Name() string { return "timelineAnalysis" }

func (a *TimelineAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

// SetLocFinder sets the location finder for map position lookups
func (a *TimelineAnalyzer) SetLocFinder(finder *loc.Finder) {
	a.locFinder = finder
}

func (a *TimelineAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.StatUpdateEvent:
		return a.handleStatUpdate(e)
	case *events.PrintEvent:
		// Detect match start/end from print messages
		a.detectMatchStart(e)
		a.detectMatchEnd(e)
	case *events.IntermissionEvent:
		// svc_intermission is the most reliable end-of-match signal: KTX
		// fires it on timelimit/fraglimit hit even when there's no matching
		// bprint string. Mark the match as ended so further stat/position
		// updates stop being sampled.
		if a.matchStarted && !a.matchEnded {
			a.matchEndTime = e.Time
			a.matchEnded = true
		}
	case *events.FragUpdateEvent:
		// Track frag events from frag updates (more reliable than stat updates)
		a.handleFragUpdate(e)
	case *events.UserInfoEvent:
		// Track player names and UserIDs for team resolution and Hub viewer links
		if e.Player != nil && e.Player.Name != "" {
			a.playerNames[e.Player.Slot] = e.Player.Name
			// Only update UserID if we don't have one yet, or if the new one is valid
			// Some servers resend userinfo with UserID=0 or corrupted values
			// Keep the first valid UserID we see for each slot
			newUserID := e.Player.UserID
			existingUserID := a.playerUserIDs[e.Player.Slot]
			if existingUserID == 0 && newUserID > 0 {
				// No existing ID, use whatever we got (first valid value)
				a.playerUserIDs[e.Player.Slot] = newUserID
			}
			// Otherwise keep existing UserID - first valid value wins
		}
	case *events.PlayerPositionEvent:
		// Track player positions
		a.handlePositionUpdate(e)
	}
	return nil
}

func (a *TimelineAnalyzer) handlePositionUpdate(e *events.PlayerPositionEvent) {
	// Always track position, even during warmup (for continuity)
	state := a.getOrCreatePlayerState(e.PlayerNum)
	state.pos = playerPosition{x: e.Origin[0], y: e.Origin[1], z: e.Origin[2]}

	// Sample at bucket boundaries — position updates arrive at ~73 Hz,
	// much more frequently than stat updates (~3 Hz). Without this,
	// ~93% of 50ms buckets would miss position data entirely.
	if a.matchStarted && !a.matchEnded {
		currentBucket := int(e.Time / a.bucketDuration)
		lastBucket := int(a.lastSampleTime / a.bucketDuration)

		if currentBucket > lastBucket {
			for b := lastBucket + 1; b <= currentBucket; b++ {
				bucketTime := float64(b) * a.bucketDuration
				a.sampleCurrentState(bucketTime)
			}
			a.lastSampleTime = e.Time
		}
	}
}

func (a *TimelineAnalyzer) detectMatchStart(e *events.PrintEvent) {
	if a.matchStarted {
		return
	}

	// KTX servers print "The match has begun!" or similar
	// Also detect "Fight!" or countdown end
	msg := e.Message
	if strings.Contains(msg, "match has begun") || strings.Contains(msg, "Fight!") ||
		strings.Contains(msg, "begins in 1") || strings.Contains(msg, "Go!") {
		a.matchStartTime = e.Time
		a.matchStarted = true
	}
}

// detectMatchEnd watches for the KTX print messages that signal the match has
// ended (timelimit/fraglimit, or explicit "the match is over"). Once flagged,
// further stat and position updates are ignored so the post-match intermission
// camera doesn't keep producing buckets — and so KTX's `health = 1000 + dmg`
// damage-indicator sentinels (combat.c:1001) don't get frozen into them.
// Patterns mirror MatchAnalyzer (analyzer/match.go) for consistency.
func (a *TimelineAnalyzer) detectMatchEnd(e *events.PrintEvent) {
	if a.matchEnded || !a.matchStarted {
		return
	}
	msg := e.Message
	if strings.Contains(msg, "the match is over") ||
		strings.Contains(msg, "match ended") ||
		strings.Contains(msg, "game over") ||
		strings.Contains(msg, "match complete") ||
		strings.Contains(msg, "timelimit hit") ||
		strings.Contains(msg, "fraglimit hit") {
		a.matchEndTime = e.Time
		a.matchEnded = true
	}
}

func (a *TimelineAnalyzer) handleFragUpdate(e *events.FragUpdateEvent) {
	state := a.getOrCreatePlayerState(e.PlayerNum)

	// Track frag changes (both increases and decreases)
	// Frags increase on kills, decrease on suicides/teamkills
	if a.matchStarted && e.Frags != state.frags {
		delta := e.Frags - state.frags
		// Sanity check: filter unreasonable deltas caused by parsing artifacts
		// (e.g., misaligned reads producing garbage frag values).
		// No player can gain or lose >5 frags in a single server frame.
		// When a corrupt value arrives, do NOT update state.frags — keep the
		// last known good value. The next valid update will naturally produce
		// the correct cumulative delta (e.g., corrupt reads 9→272, correction
		// reads 272→10, but by keeping state at 9 the correction gives delta +1).
		if delta >= -5 && delta <= 5 {
			a.rawFrags = append(a.rawFrags, fragEvent{
				Time:      e.Time,
				PlayerNum: e.PlayerNum,
				Delta:     delta,
			})
			state.frags = e.Frags
		}
		// else: corrupt value, don't update state.frags
	}
}

func (a *TimelineAnalyzer) handleStatUpdate(e *events.StatUpdateEvent) error {
	// Ignore all state during countdown/warmup - players have all weapons,
	// infinite ammo, etc. which is meaningless. Match starts fresh with
	// 100 health and base shotgun. After match end, ignore stat updates too:
	// the intermission camera otherwise freezes the last seen value (often a
	// KTX damage-indicator sentinel like health=1000+dmg) into every bucket.
	if !a.matchStarted || a.matchEnded {
		return nil
	}

	state := a.getOrCreatePlayerState(e.PlayerNum)

	switch e.StatIndex {
	case events.StatHealth:
		// KTX uses health = 1000 + damage as a damage-indicator sentinel
		// (ktx/src/combat.c:1001). Real player health is capped at 250.
		// Drop sentinel values so they don't get sampled into buckets.
		if e.Value <= 250 {
			state.vitals.health = e.Value
		}
	case events.StatArmor:
		// Same shape: KTX overwrites armorvalue in pre-match speed-meter
		// and in damage feedback paths with values > 200. Real armor caps
		// at 200 (RA). Reject anything larger.
		if e.Value <= 200 {
			state.vitals.armor = e.Value
		}
	case events.StatItems:
		state.items = e.Value
	case events.StatShells:
		state.ammo.shells = e.Value
	case events.StatNails:
		state.ammo.nails = e.Value
	case events.StatRockets:
		state.ammo.rockets = e.Value
	case events.StatCells:
		state.ammo.cells = e.Value
	}

	// Sample at bucket boundaries - fill ALL buckets since last sample
	currentBucket := int(e.Time / a.bucketDuration)
	lastBucket := int(a.lastSampleTime / a.bucketDuration)

	if currentBucket > lastBucket {
		// Fill all buckets from lastBucket+1 to currentBucket
		for b := lastBucket + 1; b <= currentBucket; b++ {
			bucketTime := float64(b) * a.bucketDuration
			a.sampleCurrentState(bucketTime)
		}
		a.lastSampleTime = e.Time
	}

	return nil
}

func (a *TimelineAnalyzer) sampleCurrentState(time float64) {
	bucket := a.getOrCreateBucket(time)

	// Sample stats per player
	for slot := 0; slot < events.MaxClients; slot++ {
		player := a.ctx.Players[slot]
		if player == nil || player.Spectator {
			continue
		}

		state := a.playerState[slot]
		if state == nil {
			continue
		}

		// Detect death/spawn transitions (prevHealth starts at -1 = uninitialized)
		isDead := state.vitals.health <= 0
		isDeathFrame := isDead && state.prevHealth > 0   // just died
		isSpawnFrame := !isDead && state.prevHealth <= 0 // spawned (first appearance or after death)

		state.prevHealth = state.vitals.health

		// Record death/spawn events for frag streak calculation
		if isDeathFrame {
			a.rawDeaths = append(a.rawDeaths, deathEvent{
				Time:      time,
				PlayerNum: slot,
			})
		}
		if isSpawnFrame {
			a.rawSpawns = append(a.rawSpawns, deathEvent{
				Time:      time,
				PlayerNum: slot,
			})
		}

		// Skip players who are dead (unless this is the death frame)
		if isDead && !isDeathFrame {
			continue
		}

		// Create player data for this bucket
		pData := &playerBucketRawData{
			name:   player.Name,
			team:   player.Team,
			vitals: state.vitals,
			ammo:   state.ammo,
			pos:    state.pos,
			dead:   isDeathFrame,
			spawn:  isSpawnFrame,
		}

		pData.weapons = weaponLoadout{
			rl:  state.items&events.ITRocketLauncher != 0,
			lg:  state.items&events.ITLightning != 0,
			ssg: state.items&events.ITSuperShotgun != 0,
			sng: state.items&events.ITSuperNailgun != 0,
		}
		pData.powerups = powerupLoadout{
			quad: state.items&events.ITQuad != 0,
			pent: state.items&events.ITInvulnerability != 0,
			ring: state.items&events.ITInvisibility != 0,
		}

		// Decode armor type from the item bitfield (RA > YA > GA).
		switch {
		case state.items&events.ITArmor3 != 0:
			pData.vitals.armorType = "ra"
		case state.items&events.ITArmor2 != 0:
			pData.vitals.armorType = "ya"
		case state.items&events.ITArmor1 != 0:
			pData.vitals.armorType = "ga"
		}

		bucket.playerData[slot] = pData
	}
}

func (a *TimelineAnalyzer) getOrCreateBucket(time float64) *timelineBucketData {
	bucketIndex := int(time / a.bucketDuration)

	// Extend buckets array if needed
	for len(a.buckets) <= bucketIndex {
		newBucket := &timelineBucketData{
			startTime:  float64(len(a.buckets)) * a.bucketDuration,
			endTime:    float64(len(a.buckets)+1) * a.bucketDuration,
			playerData: make(map[int]*playerBucketRawData),
		}
		a.buckets = append(a.buckets, newBucket)
	}

	return a.buckets[bucketIndex]
}

func (a *TimelineAnalyzer) getOrCreatePlayerState(playerNum int) *timelinePlayerState {
	if s, ok := a.playerState[playerNum]; ok {
		return s
	}
	s := &timelinePlayerState{prevHealth: -1} // -1 = uninitialized
	a.playerState[playerNum] = s
	return s
}
