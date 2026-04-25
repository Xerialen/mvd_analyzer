package analyzer

import (
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
	core                *CoreOutputs
	bucketDuration      float64 // High-res sampling interval (default 50ms)
	playerState         map[int]*timelinePlayerState
	playerNames         map[int]string // Slot -> player name (from UserInfoEvent)
	playerUserIDs       map[int]int    // Slot -> UserID (for Hub viewer track param)
	buckets             []*timelineBucketData
	rawFrags       []fragEvent  // Raw frag events (player num, time)
	rawDeaths      []deathEvent // Raw death events (player num, time)
	rawSpawns      []deathEvent // Raw spawn events (reusing deathEvent type)
	lastSampleTime      float64
	timing              MatchTimingDetector
	locFinder           *loc.Finder // Location finder for map (nil if no .loc file)
	blipThresholdMs     int         // Per-player loc smoothing threshold, 0 disables
}

// UseCoreOutputs is part of the CoreConsumer contract — Timeline
// consumes co.DemoInfo (map name + player team table) and
// co.FragEntries (for streak detection and powerup-frag counts) during
// its Finalize.
func (a *TimelineAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

// coreFragEntries is a nil-safe accessor for co.FragEntries; returns
// an empty slice when CoreOutputs hasn't been wired up (e.g. unit tests
// that only exercise OnEvent without going through the registry).
func (a *TimelineAnalyzer) coreFragEntries() []FragEntry {
	if a.core == nil {
		return nil
	}
	return a.core.FragEntries
}

// SetBlipThresholdMs configures the minimum residence a player must log
// in a loc for it to count as stable. Any shorter residence (wall bleed,
// nearest-point flicker at boundaries) is reassigned to an adjacent
// stable loc during finalization before downstream consumers read Li.
// Must be called before Init(). Zero disables the filter.
func (a *TimelineAnalyzer) SetBlipThresholdMs(ms int) {
	a.blipThresholdMs = ms
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
	items  int // raw item bitfield (weapons, powerups, armor type)
	vitals vitals
	// isDead flips on DeathEvent and flips back on SpawnEvent. Drives
	// sampleCurrentState's "skip this player" behaviour between death
	// and respawn; the D/Sp per-bucket flags are set authoritatively by
	// the event handlers, not inferred from prevHealth/health sampling.
	isDead bool
	// firstMatchBucketSeen remains false until the player has been
	// sampled or event-flagged at least once during the match. The
	// first visible bucket for each player is marked spawn=true —
	// downstream consumers (loc-graph cursor, blip filter boundary)
	// treat match entry the same as a respawn, so a synthetic marker
	// for players who were already alive at match start (and therefore
	// have no StatHealth transition to drive a real SpawnEvent) keeps
	// those consumers honest.
	firstMatchBucketSeen bool
	ammo                 ammoCounts
	pos                  playerPosition
	frags                int
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
	case *events.DeathEvent:
		a.handleDeath(e)
	case *events.SpawnEvent:
		a.handleSpawn(e)
	case *events.PrintEvent:
		a.timing.OnPrint(e)
	case *events.IntermissionEvent:
		// svc_intermission is the most reliable end-of-match signal: KTX
		// fires it on timelimit/fraglimit hit even when there's no matching
		// bprint string.
		a.timing.OnIntermission(e.Time)
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
	if a.timing.Started && !a.timing.Ended {
		currentBucket := int(e.Time / a.bucketDuration)
		lastBucket := int(a.lastSampleTime / a.bucketDuration)

		if currentBucket > lastBucket {
			for b := lastBucket + 1; b <= currentBucket; b++ {
				a.sampleCurrentStateAtIndex(b)
			}
			a.lastSampleTime = e.Time
		}
	}
}

func (a *TimelineAnalyzer) handleFragUpdate(e *events.FragUpdateEvent) {
	state := a.getOrCreatePlayerState(e.PlayerNum)

	// Track frag changes (both increases and decreases)
	// Frags increase on kills, decrease on suicides/teamkills
	if a.timing.Started && e.Frags != state.frags {
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
	if !a.timing.Started || a.timing.Ended {
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
			a.sampleCurrentStateAtIndex(b)
		}
		a.lastSampleTime = e.Time
	}

	return nil
}

// sampleCurrentStateAtIndex is the index-addressed twin of sampleCurrentState
// used by the event-driven fill loops. Callers synthesize bucket times as
// `float64(b) * bucketDuration`, but that product is not guaranteed to
// round-trip back to `b` through `int(t / bucketDuration)` — float64 cannot
// represent 0.05 exactly, so for many indices (e.g. 324: 324*0.05 =
// 16.199999999…) the recomputed index comes back as `b-1`, and the wrong
// bucket gets populated. That caused one in every ~15 high-res buckets to
// end up empty and get dropped by the exporter, producing the visible
// timeline gaps. Passing the known-correct bucket index directly avoids
// the round-trip entirely.
func (a *TimelineAnalyzer) sampleCurrentStateAtIndex(bucketIndex int) {
	bucket := a.getOrCreateBucketByIndex(bucketIndex)
	a.populateBucket(bucket)
}

func (a *TimelineAnalyzer) sampleCurrentState(time float64) {
	bucket := a.getOrCreateBucket(time)
	a.populateBucket(bucket)
}

func (a *TimelineAnalyzer) populateBucket(bucket *timelineBucketData) {
	// Sample stats per player. Death / spawn flags (pd.dead / pd.spawn)
	// are set by handleDeath / handleSpawn from parser-emitted events,
	// not by this sampler, so we preserve those flags when a bucket is
	// revisited.
	for slot := 0; slot < events.MaxClients; slot++ {
		player := a.ctx.Players[slot]
		if player == nil || player.Spectator {
			continue
		}

		state := a.playerState[slot]
		if state == nil {
			continue
		}

		// Skip dead players entirely — handleDeath already placed the
		// death-bucket pData with dead=true; subsequent buckets until
		// SpawnEvent should not carry a playerData entry. Also skip
		// when vitals.health is still non-positive: during warmup
		// handleStatUpdate is guarded and vitals stays at the zero
		// value, so the first match-time sample-fills that run before
		// the first StatHealth update should not emit empty pData for
		// the player — the loc-graph and blip filter treat those as
		// "present but invalid" and that's a regression vs. the prior
		// prevHealth-based sampler.
		if state.isDead || state.vitals.health <= 0 {
			continue
		}

		pData := bucket.playerData[slot]
		if pData == nil {
			pData = &playerBucketRawData{}
			bucket.playerData[slot] = pData
		}
		a.snapshotPlayerData(pData, player, state)
		if !state.firstMatchBucketSeen {
			pData.spawn = true
			state.firstMatchBucketSeen = true
		}
	}
}

// snapshotPlayerData populates the "current state" fields of pData from
// the live player state. Deliberately does NOT touch pData.dead /
// pData.spawn — those are owned by handleDeath / handleSpawn so that a
// SpawnEvent that arrives after an earlier sample-fill can't have its
// flag erased by a subsequent re-sample of the same bucket.
func (a *TimelineAnalyzer) snapshotPlayerData(pData *playerBucketRawData, player *events.PlayerInfo, state *timelinePlayerState) {
	pData.name = player.Name
	pData.team = player.Team
	pData.vitals = state.vitals
	pData.ammo = state.ammo
	pData.pos = state.pos
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

	switch {
	case state.items&events.ITArmor3 != 0:
		pData.vitals.armorType = "ra"
	case state.items&events.ITArmor2 != 0:
		pData.vitals.armorType = "ya"
	case state.items&events.ITArmor1 != 0:
		pData.vitals.armorType = "ga"
	default:
		pData.vitals.armorType = ""
	}
}

// handleDeath records the authoritative death transition from the parser
// and marks the death bucket. Same guard as handleStatUpdate: only
// match-time events are recorded so warmup cycles don't pollute state.
func (a *TimelineAnalyzer) handleDeath(e *events.DeathEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	state := a.getOrCreatePlayerState(e.PlayerNum)
	a.rawDeaths = append(a.rawDeaths, deathEvent{Time: e.Time, PlayerNum: e.PlayerNum})

	player := a.ctx.Players[e.PlayerNum]
	if player == nil || player.Spectator {
		state.isDead = true
		return
	}

	bucket := a.getOrCreateBucket(e.Time)
	pData := bucket.playerData[e.PlayerNum]
	if pData == nil {
		pData = &playerBucketRawData{}
		a.snapshotPlayerData(pData, player, state)
		bucket.playerData[e.PlayerNum] = pData
	}
	pData.dead = true
	state.isDead = true
	state.firstMatchBucketSeen = true
}

// handleSpawn is the mirror of handleDeath for the respawn transition —
// or the first-spawn when a player moves from spectator / pre-connect to
// active play. Consumers treat both cases identically.
func (a *TimelineAnalyzer) handleSpawn(e *events.SpawnEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	state := a.getOrCreatePlayerState(e.PlayerNum)
	a.rawSpawns = append(a.rawSpawns, deathEvent{Time: e.Time, PlayerNum: e.PlayerNum})
	state.isDead = false

	player := a.ctx.Players[e.PlayerNum]
	if player == nil || player.Spectator {
		return
	}

	bucket := a.getOrCreateBucket(e.Time)
	pData := bucket.playerData[e.PlayerNum]
	if pData == nil {
		pData = &playerBucketRawData{}
		a.snapshotPlayerData(pData, player, state)
		bucket.playerData[e.PlayerNum] = pData
	}
	pData.spawn = true
	state.firstMatchBucketSeen = true
}

func (a *TimelineAnalyzer) getOrCreateBucket(time float64) *timelineBucketData {
	return a.getOrCreateBucketByIndex(int(time / a.bucketDuration))
}

func (a *TimelineAnalyzer) getOrCreateBucketByIndex(bucketIndex int) *timelineBucketData {
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
	s := &timelinePlayerState{}
	a.playerState[playerNum] = s
	return s
}
