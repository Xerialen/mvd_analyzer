package analyzer

import (
	"strings"

	"github.com/mvd-analyzer/internal/loc"
	"github.com/mvd-analyzer/internal/mvd"
	"github.com/mvd-analyzer/internal/parser"
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
	graphBucketDuration float64 // Graph aggregation interval (always 1s)
	playerState         map[int]*timelinePlayerState
	playerNames         map[int]string // Slot -> player name (from UserInfoEvent)
	playerUserIDs       map[int]int    // Slot -> UserID (for Hub viewer track param)
	buckets             []*timelineBucketData
	fragEventsRaw       []fragEventRaw  // Raw frag events (player num, time)
	deathEventsRaw      []deathEventRaw // Raw death events (player num, time)
	spawnEventsRaw      []deathEventRaw // Raw spawn events (reusing deathEventRaw type)
	lastSampleTime      float64
	matchStartTime      float64
	matchStarted        bool
	locFinder           *loc.Finder // Location finder for map (nil if no .loc file)
}

// fragEventRaw tracks a frag before team assignment
type fragEventRaw struct {
	Time      float64
	PlayerNum int
	Delta     int // +N for kills, -N for suicides/teamkills
}

// deathEventRaw tracks a player death (detected via health transition)
type deathEventRaw struct {
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
		bucketDuration:      DefaultHighResBucketDuration, // 50ms for high-res map data
		graphBucketDuration: DefaultGraphBucketDuration,   // 1s for graphs
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

func (a *TimelineAnalyzer) OnEvent(event parser.Event) error {
	switch e := event.(type) {
	case *parser.StatUpdateEvent:
		return a.handleStatUpdate(e)
	case *parser.PrintEvent:
		// Detect match start from countdown message
		a.detectMatchStart(e)
	case *parser.FragUpdateEvent:
		// Track frag events from frag updates (more reliable than stat updates)
		a.handleFragUpdate(e)
	case *parser.UserInfoEvent:
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
	case *parser.PlayerPositionEvent:
		// Track player positions
		a.handlePositionUpdate(e)
	}
	return nil
}

func (a *TimelineAnalyzer) handlePositionUpdate(e *parser.PlayerPositionEvent) {
	// Always track position, even during warmup (for continuity)
	state := a.getOrCreatePlayerState(e.PlayerNum)
	state.pos = playerPosition{x: e.Origin[0], y: e.Origin[1], z: e.Origin[2]}

	// Sample at bucket boundaries — position updates arrive at ~73 Hz,
	// much more frequently than stat updates (~3 Hz). Without this,
	// ~93% of 50ms buckets would miss position data entirely.
	if a.matchStarted {
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

func (a *TimelineAnalyzer) detectMatchStart(e *parser.PrintEvent) {
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

func (a *TimelineAnalyzer) handleFragUpdate(e *parser.FragUpdateEvent) {
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
			a.fragEventsRaw = append(a.fragEventsRaw, fragEventRaw{
				Time:      e.Time,
				PlayerNum: e.PlayerNum,
				Delta:     delta,
			})
			state.frags = e.Frags
		}
		// else: corrupt value, don't update state.frags
	}
}

func (a *TimelineAnalyzer) handleStatUpdate(e *parser.StatUpdateEvent) error {
	// Ignore all state during countdown/warmup - players have all weapons,
	// infinite ammo, etc. which is meaningless. Match starts fresh with
	// 100 health and base shotgun.
	if !a.matchStarted {
		return nil
	}

	state := a.getOrCreatePlayerState(e.PlayerNum)

	switch e.StatIndex {
	case mvd.StatHealth:
		state.vitals.health = e.Value
	case mvd.StatArmor:
		state.vitals.armor = e.Value
	case mvd.StatItems:
		state.items = e.Value
	case mvd.StatShells:
		state.ammo.shells = e.Value
	case mvd.StatNails:
		state.ammo.nails = e.Value
	case mvd.StatRockets:
		state.ammo.rockets = e.Value
	case mvd.StatCells:
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
	for slot := 0; slot < mvd.MaxClients; slot++ {
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
			a.deathEventsRaw = append(a.deathEventsRaw, deathEventRaw{
				Time:      time,
				PlayerNum: slot,
			})
		}
		if isSpawnFrame {
			a.spawnEventsRaw = append(a.spawnEventsRaw, deathEventRaw{
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
			rl:  state.items&mvd.ITRocketLauncher != 0,
			lg:  state.items&mvd.ITLightning != 0,
			ssg: state.items&mvd.ITSuperShotgun != 0,
			sng: state.items&mvd.ITSuperNailgun != 0,
		}
		pData.powerups = powerupLoadout{
			quad: state.items&mvd.ITQuad != 0,
			pent: state.items&mvd.ITInvulnerability != 0,
			ring: state.items&mvd.ITInvisibility != 0,
		}

		// Decode armor type from the item bitfield (RA > YA > GA).
		switch {
		case state.items&mvd.ITArmor3 != 0:
			pData.vitals.armorType = "ra"
		case state.items&mvd.ITArmor2 != 0:
			pData.vitals.armorType = "ya"
		case state.items&mvd.ITArmor1 != 0:
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
