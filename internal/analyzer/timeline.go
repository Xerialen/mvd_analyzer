package analyzer

import (
	"sort"
	"strings"

	"github.com/mvd-analyzer/internal/loc"
	"github.com/mvd-analyzer/internal/mvd"
	"github.com/mvd-analyzer/internal/parser"
)

// Default bucket durations
const (
	DefaultHighResBucketDuration = 0.05 // 50ms for high-res map visualization
	DefaultGraphBucketDuration   = 1.0  // 1 second for graph aggregation
)

// TimelineAnalyzer tracks time-bucketed player state for timeline visualization
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

// timelinePlayerState tracks current state for a single player
type timelinePlayerState struct {
	items      int // Current items (weapons, powerups, armor type)
	health     int
	prevHealth int // Previous sample's health, for detecting death/spawn transitions
	armor      int
	shells     int
	nails      int
	rockets    int
	cells      int
	frags      int // Current frag count
	x, y, z    float32 // Last known position
}

// timelineBucketData holds raw aggregated data during analysis
type timelineBucketData struct {
	startTime  float64
	endTime    float64
	playerData map[int]*playerBucketRawData // Keyed by slot
}

// playerBucketRawData holds per-player data for a bucket
type playerBucketRawData struct {
	name      string
	team      string
	hasRL     bool
	hasLG     bool
	hasSSG    bool
	hasSNG    bool
	hasQuad   bool
	hasPent   bool
	hasRing   bool
	health    int
	armor     int
	armorType string // "ga"/"ya"/"ra"
	shells    int
	nails     int
	rockets   int
	cells     int
	x, y, z   float32 // Position
	location  string  // Named location from .loc file
	dead      bool    // True for death-frame entries (health just went to 0)
	spawn     bool    // True for spawn-frame entries (health just went from 0 to >0)
}

// NewTimelineAnalyzer creates a new timeline analyzer
func NewTimelineAnalyzer() *TimelineAnalyzer {
	return &TimelineAnalyzer{
		bucketDuration:      DefaultHighResBucketDuration, // 50ms for high-res map data
		graphBucketDuration: DefaultGraphBucketDuration,   // 1s for graphs
		playerState:         make(map[int]*timelinePlayerState),
		playerNames:         make(map[int]string),
		playerUserIDs:       make(map[int]int),
		buckets:             make([]*timelineBucketData, 0, 24000), // Pre-alloc for 20min @ 50ms
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
	state.x = e.Origin[0]
	state.y = e.Origin[1]
	state.z = e.Origin[2]

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
		state.health = e.Value
	case mvd.StatArmor:
		state.armor = e.Value
	case mvd.StatItems:
		state.items = e.Value
	case mvd.StatShells:
		state.shells = e.Value
	case mvd.StatNails:
		state.nails = e.Value
	case mvd.StatRockets:
		state.rockets = e.Value
	case mvd.StatCells:
		state.cells = e.Value
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
		isDead := state.health <= 0
		isDeathFrame := isDead && state.prevHealth > 0                           // just died
		isSpawnFrame := !isDead && (state.prevHealth <= 0) // spawned (first appearance or after death)

		state.prevHealth = state.health

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
			name:    player.Name,
			team:    player.Team,
			health:  state.health,
			armor:   state.armor,
			shells:  state.shells,
			nails:   state.nails,
			rockets: state.rockets,
			cells:   state.cells,
			dead:    isDeathFrame,
			spawn:   isSpawnFrame,
		}

		// Track weapons
		pData.hasRL = (state.items & mvd.ITRocketLauncher) != 0
		pData.hasLG = (state.items & mvd.ITLightning) != 0
		pData.hasSSG = (state.items & mvd.ITSuperShotgun) != 0
		pData.hasSNG = (state.items & mvd.ITSuperNailgun) != 0

		// Track powerups
		pData.hasQuad = (state.items & mvd.ITQuad) != 0
		pData.hasPent = (state.items & mvd.ITInvulnerability) != 0
		pData.hasRing = (state.items & mvd.ITInvisibility) != 0

		// Track armor type
		if state.items&mvd.ITArmor3 != 0 {
			pData.armorType = "ra"
		} else if state.items&mvd.ITArmor2 != 0 {
			pData.armorType = "ya"
		} else if state.items&mvd.ITArmor1 != 0 {
			pData.armorType = "ga"
		}

		// Track position (location name resolved in Finalize)
		pData.x = state.x
		pData.y = state.y
		pData.z = state.z

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

func (a *TimelineAnalyzer) Finalize() (interface{}, error) {
	// Do a final sample at the end
	if len(a.buckets) > 0 {
		lastBucket := a.buckets[len(a.buckets)-1]
		if lastBucket.endTime > a.lastSampleTime {
			a.sampleCurrentState(lastBucket.endTime)
		}
	}

	// Try to load loc file from DemoInfo.Map if not already loaded
	if a.locFinder == nil && a.ctx.DemoInfo != nil && a.ctx.DemoInfo.Map != "" {
		if finder, err := loc.LoadForMap(a.ctx.DemoInfo.Map); err == nil {
			a.locFinder = finder
		}
	}

	// Resolve location names now that we have the loc finder
	if a.locFinder != nil {
		for _, bucket := range a.buckets {
			for _, pData := range bucket.playerData {
				if pData.x != 0 || pData.y != 0 || pData.z != 0 {
					pData.location = a.locFinder.FindNearest(pData.x, pData.y, pData.z)
				}
			}
		}
	}

	// Build a name->team lookup from DemoInfo (authoritative source)
	// Use both exact name and normalized name for matching
	nameToTeam := make(map[string]string)
	normNameToTeam := make(map[string]string) // Normalized names (lowercase, alphanumeric only)

	if a.ctx.DemoInfo != nil {
		for _, p := range a.ctx.DemoInfo.Players {
			if p.Name == "" || p.Team == "" {
				continue
			}
			nameToTeam[p.Name] = p.Team
			normNameToTeam[normalizePlayerName(p.Name)] = p.Team
		}
	}

	// Bridge slot↔demoinfo via login join / name join.
	resolved := a.ctx.ResolveSlotDemoInfo()
	slotToTeam := make(map[int]string)
	slotToPlayer := make(map[int]string)
	for slot, di := range resolved {
		if di.Team != "" {
			slotToTeam[slot] = di.Team
			slotToPlayer[slot] = di.Name
		}
	}

	// Convert raw frag events to final events with player and team info
	fragEvents := make([]TimelineFragEvent, 0, len(a.fragEventsRaw))
	for _, raw := range a.fragEventsRaw {
		// Prefer the demoinfo-resolved name (via slotToPlayer) so the
		// emitted player name matches what the timeline buckets and the
		// frontend's demoinfo-keyed Team Status panel expect. Fall back to
		// the userinfo name only when neither the login join nor the name
		// join matched this slot to a demoinfo entry.
		playerName := slotToPlayer[raw.PlayerNum]
		team := slotToTeam[raw.PlayerNum]

		if playerName == "" {
			if player := a.ctx.Players[raw.PlayerNum]; player != nil {
				playerName = player.Name
				if team == "" {
					team = player.Team
				}
			}
		}
		if playerName == "" {
			if name, ok := a.playerNames[raw.PlayerNum]; ok {
				playerName = name
			}
		}

		// If we still have a name but no team, look it up in DemoInfo by name.
		if playerName != "" && team == "" {
			team = nameToTeam[playerName]
			if team == "" {
				team = normNameToTeam[normalizePlayerName(playerName)]
			}
		}

		if team != "" {
			fragEvents = append(fragEvents, TimelineFragEvent{
				Time:   raw.Time,
				Player: playerName,
				Team:   team,
				Delta:  raw.Delta,
			})
		}
	}

	// Detect powerup pickup events for Key Moments
	powerupEvents := a.detectPowerupEvents(nameToTeam, slotToTeam, slotToPlayer)

	// Count frags during each powerup run
	for i := range powerupEvents {
		pe := &powerupEvents[i]
		for _, fe := range a.ctx.FragEntries {
			if fe.Killer != pe.PlayerName || fe.IsSuicide || fe.IsTeamKill {
				continue
			}
			if fe.Time >= pe.Time && fe.Time <= pe.EndTime {
				pe.Frags++
			}
		}
	}

	// Export location data for map visualization
	var locationData []MapLocation
	if a.locFinder != nil {
		locs := a.locFinder.Locations()
		locationData = make([]MapLocation, len(locs))
		for i, l := range locs {
			locationData[i] = MapLocation{
				X:    l.X,
				Y:    l.Y,
				Z:    l.Z,
				Name: l.Name,
			}
		}
	}

	// Build slot->name mapping for exports.
	//
	// Prefer the DemoInfo-derived name (resolved above via login join or
	// name join) over the live userinfo name. The two can differ when
	// the userinfo "name" field is an auth/login string but the player's
	// actual displayed netname is a different (often colored) string —
	// the frontend joins timeline data against DemoInfo player names, so
	// we must export the same name DemoInfo did or the per-player health/
	// armor stack disappears for that player.
	slotToName := make(map[int]string)
	for slot := 0; slot < mvd.MaxClients; slot++ {
		if name := slotToPlayer[slot]; name != "" {
			slotToName[slot] = name
		} else if player := a.ctx.Players[slot]; player != nil && player.Name != "" {
			slotToName[slot] = player.Name
		} else if name := a.playerNames[slot]; name != "" {
			slotToName[slot] = name
		}
	}

	// Export high-res buckets (50ms) for map visualization
	highResBuckets := a.exportHighResBuckets(slotToName)

	// Aggregate to 1s buckets for graphs
	graphBuckets := a.aggregateToGraphBuckets(slotToName, slotToTeam)

	// Build name -> UserID mapping for Hub viewer links
	playerUserIDsByName := make(map[string]int)
	for slot, userID := range a.playerUserIDs {
		if userID > 0 {
			name := slotToName[slot]
			if name != "" {
				playerUserIDsByName[name] = userID
			}
		}
	}

	// Detect top 5 longest frag streaks for Key Moments
	fragStreaks := a.detectFragStreaks(10, nameToTeam, playerUserIDsByName)

	// Auto-detect control regions from loc data (stats computed client-side)
	var regionControl *RegionControlResult
	if a.locFinder != nil {
		regions := a.buildControlRegions()
		if len(regions) > 0 {
			regionControl = &RegionControlResult{Regions: regions}
		}
	}

	result := &TimelineAnalysisResult{
		BucketDuration:  a.graphBucketDuration, // 1.0 for graphs
		HighResDuration: a.bucketDuration,      // 0.05 for map
		MatchStartTime:  a.matchStartTime,
		Buckets:         graphBuckets,    // 1s aggregated for graphs
		HighResBuckets:  highResBuckets,  // 50ms for map visualization
		FragEvents:      fragEvents,
		PowerupEvents:   powerupEvents,
		FragStreaks:      fragStreaks,
		LocationData:    locationData,
		PlayerUserIDs:   playerUserIDsByName,
		RegionControl:   regionControl,
	}

	return result, nil
}

// exportHighResBuckets converts internal buckets to compact export format for map
func (a *TimelineAnalyzer) exportHighResBuckets(slotToName map[int]string) []HighResBucket {
	result := make([]HighResBucket, 0, len(a.buckets))

	for _, b := range a.buckets {
		if len(b.playerData) == 0 {
			continue // Skip empty buckets
		}

		hb := HighResBucket{
			T: b.startTime,
			P: make(map[string]*HighResPlayerData),
		}

		for slot, pd := range b.playerData {
			name := slotToName[slot]
			if name == "" {
				name = pd.name // Fallback to stored name
			}
			if name == "" {
				continue
			}

			hb.P[name] = &HighResPlayerData{
				X:       pd.x,
				Y:       pd.y,
				H:       pd.health,
				A:       pd.armor,
				AT:      pd.armorType,
				RL:      pd.hasRL,
				LG:      pd.hasLG,
				SSG:     pd.hasSSG,
				SNG:     pd.hasSNG,
				Q:       pd.hasQuad,
				Pent:    pd.hasPent,
				R:       pd.hasRing,
				Rockets: pd.rockets,
				Cells:   pd.cells,
				D:       pd.dead,
				Sp:      pd.spawn,
			}
		}

		if len(hb.P) > 0 {
			result = append(result, hb)
		}
	}

	return result
}

// aggregateToGraphBuckets groups high-res buckets into 1s buckets for graphs
func (a *TimelineAnalyzer) aggregateToGraphBuckets(slotToName map[int]string, slotToTeam map[int]string) []TimelineBucket {
	if len(a.buckets) == 0 {
		return nil
	}

	// Calculate how many high-res buckets per graph bucket
	bucketsPerSecond := int(a.graphBucketDuration / a.bucketDuration)
	if bucketsPerSecond < 1 {
		bucketsPerSecond = 1
	}

	// Calculate number of graph buckets needed
	numGraphBuckets := (len(a.buckets) + bucketsPerSecond - 1) / bucketsPerSecond
	graphBuckets := make([]TimelineBucket, 0, numGraphBuckets)

	for i := 0; i < len(a.buckets); i += bucketsPerSecond {
		end := i + bucketsPerSecond
		if end > len(a.buckets) {
			end = len(a.buckets)
		}

		// Aggregate this window
		graphBucket := a.aggregateWindow(a.buckets[i:end], slotToName, slotToTeam)
		graphBuckets = append(graphBuckets, graphBucket)
	}

	return graphBuckets
}

// aggregateWindow aggregates a slice of high-res buckets into a single graph bucket
func (a *TimelineAnalyzer) aggregateWindow(buckets []*timelineBucketData, slotToName map[int]string, slotToTeam map[int]string) TimelineBucket {
	if len(buckets) == 0 {
		return TimelineBucket{
			PlayerData: make(map[string]*PlayerBucketData),
			TeamData:   make(map[string]*TeamBucketData),
		}
	}

	result := TimelineBucket{
		StartTime:  buckets[0].startTime,
		EndTime:    buckets[len(buckets)-1].endTime,
		PlayerData: make(map[string]*PlayerBucketData),
		TeamData:   make(map[string]*TeamBucketData),
	}

	// Track per-player aggregates across the window
	// For weapons/powerups: take MAX (peak control)
	// For health/armor: take LAST value (current state)
	playerAggregates := make(map[string]*playerWindowAggregate)

	for _, b := range buckets {
		for slot, pRaw := range b.playerData {
			name := slotToName[slot]
			if name == "" {
				name = pRaw.name
			}
			if name == "" {
				continue
			}

			team := pRaw.team
			if team == "" {
				team = slotToTeam[slot]
			}

			if playerAggregates[name] == nil {
				playerAggregates[name] = &playerWindowAggregate{team: team}
			}
			agg := playerAggregates[name]

			// Weapons/powerups: OR (if they had it at any point in window)
			agg.hasRL = agg.hasRL || pRaw.hasRL
			agg.hasLG = agg.hasLG || pRaw.hasLG
			agg.hasQuad = agg.hasQuad || pRaw.hasQuad
			agg.hasPent = agg.hasPent || pRaw.hasPent
			agg.hasRing = agg.hasRing || pRaw.hasRing

			// Health/armor/ammo: take last value (overwrite)
			agg.health = pRaw.health
			agg.armor = pRaw.armor
			agg.armorType = pRaw.armorType
			agg.shells = pRaw.shells
			agg.nails = pRaw.nails
			agg.rockets = pRaw.rockets
			agg.cells = pRaw.cells

			// Position: take last value
			agg.x = pRaw.x
			agg.y = pRaw.y
			agg.z = pRaw.z
			agg.location = pRaw.location
		}
	}

	// Build PlayerData from aggregates
	teamAggregates := make(map[string]*teamAggregator)

	for name, agg := range playerAggregates {
		result.PlayerData[name] = &PlayerBucketData{
			Team:      agg.team,
			HasRL:     agg.hasRL,
			HasLG:     agg.hasLG,
			HasQuad:   agg.hasQuad,
			HasPent:   agg.hasPent,
			HasRing:   agg.hasRing,
			Health:    agg.health,
			Armor:     agg.armor,
			ArmorType: agg.armorType,
			Shells:    agg.shells,
			Nails:     agg.nails,
			Rockets:   agg.rockets,
			Cells:     agg.cells,
			X:         agg.x,
			Y:         agg.y,
			Z:         agg.z,
			Location:  agg.location,
		}

		// Aggregate for team stats
		team := agg.team
		if team != "" {
			if teamAggregates[team] == nil {
				teamAggregates[team] = &teamAggregator{
					armorByType: make(map[string]int),
				}
			}
			ta := teamAggregates[team]

			// Weapons
			if agg.hasRL && agg.hasLG {
				ta.playersWithRLLG++
				ta.playersWithWeapons++
			} else if agg.hasRL {
				ta.playersWithRL++
				ta.playersWithWeapons++
			} else if agg.hasLG {
				ta.playersWithLG++
				ta.playersWithWeapons++
			}

			// Powerups
			if agg.hasQuad {
				ta.playersWithQuad++
				ta.playersWithPowerups++
			}
			if agg.hasPent {
				ta.playersWithPent++
				ta.playersWithPowerups++
			}
			if agg.hasRing {
				ta.playersWithRing++
				ta.playersWithPowerups++
			}

			// Health/Armor
			ta.healthSamples = append(ta.healthSamples, agg.health)
			ta.armorSamples = append(ta.armorSamples, agg.armor)
			if agg.armorType != "" {
				ta.armorByType[agg.armorType]++
			}

			// Ammo
			ta.totalShells += agg.shells
			ta.totalNails += agg.nails
			ta.totalRockets += agg.rockets
			ta.totalCells += agg.cells
		}
	}

	// Build TeamData from aggregates
	for team, ta := range teamAggregates {
		result.TeamData[team] = &TeamBucketData{
			PlayersWithRL:       ta.playersWithRL,
			PlayersWithLG:       ta.playersWithLG,
			PlayersWithRLLG:     ta.playersWithRLLG,
			PlayersWithWeapons:  ta.playersWithWeapons,
			PlayersWithQuad:     ta.playersWithQuad,
			PlayersWithPent:     ta.playersWithPent,
			PlayersWithRing:     ta.playersWithRing,
			PlayersWithPowerups: ta.playersWithPowerups,
			AvgHealth:           average(ta.healthSamples),
			AvgArmor:            average(ta.armorSamples),
			TotalHealth:         sum(ta.healthSamples),
			TotalArmor:          sum(ta.armorSamples),
			ArmorByType:         ta.armorByType,
			TotalShells:         ta.totalShells,
			TotalNails:          ta.totalNails,
			TotalRockets:        ta.totalRockets,
			TotalCells:          ta.totalCells,
		}
	}

	return result
}

// playerWindowAggregate holds per-player data during window aggregation
type playerWindowAggregate struct {
	team      string
	hasRL     bool
	hasLG     bool
	hasQuad   bool
	hasPent   bool
	hasRing   bool
	health    int
	armor     int
	armorType string
	shells    int
	nails     int
	rockets   int
	cells     int
	x, y, z   float32
	location  string
}

// teamAggregator is used during finalization to aggregate player data into team data
type teamAggregator struct {
	playersWithRL       int
	playersWithLG       int
	playersWithRLLG     int
	playersWithWeapons  int
	playersWithQuad     int
	playersWithPent     int
	playersWithRing     int
	playersWithPowerups int
	healthSamples       []int
	armorSamples        []int
	armorByType         map[string]int
	totalShells         int
	totalNails          int
	totalRockets        int
	totalCells          int
}

// average calculates the average of a slice of ints
func average(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	s := 0
	for _, v := range values {
		s += v
	}
	return float64(s) / float64(len(values))
}

// sum calculates the sum of a slice of ints
func sum(values []int) int {
	s := 0
	for _, v := range values {
		s += v
	}
	return s
}

// normalizePlayerName removes non-alphanumeric chars and lowercases for fuzzy matching
// "bad.rotker" and "badrotker" will both become "badrotker"
func normalizePlayerName(name string) string {
	var result []byte
	for i := 0; i < len(name); i++ {
		c := name[i]
		// Convert uppercase to lowercase
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		// Keep only alphanumeric
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			result = append(result, c)
		}
	}
	return string(result)
}

// detectPowerupEvents scans buckets for powerup pickup/loss transitions
func (a *TimelineAnalyzer) detectPowerupEvents(nameToTeam map[string]string, slotToTeam map[int]string, slotToPlayer map[int]string) []PowerupEvent {
	if len(a.buckets) == 0 {
		return nil
	}

	events := []PowerupEvent{}

	type powerupInfo struct {
		field string
		name  string
	}
	powerupTypes := []powerupInfo{
		{"hasQuad", "quad"},
		{"hasPent", "pent"},
		{"hasRing", "ring"},
	}

	// Track active powerups per player slot per type
	// Map: slot -> powerupType -> startTime (0 if not active)
	activeRuns := make(map[int]map[string]float64)

	for _, bucket := range a.buckets {
		for slot, pData := range bucket.playerData {
			if activeRuns[slot] == nil {
				activeRuns[slot] = make(map[string]float64)
			}

			// Check each powerup type
			for _, pt := range powerupTypes {
				var hasIt bool
				switch pt.field {
				case "hasQuad":
					hasIt = pData.hasQuad
				case "hasPent":
					hasIt = pData.hasPent
				case "hasRing":
					hasIt = pData.hasRing
				}

				startTime := activeRuns[slot][pt.name]

				if hasIt && startTime == 0 {
					// Powerup just picked up
					activeRuns[slot][pt.name] = bucket.startTime
				} else if !hasIt && startTime > 0 {
					// Powerup just lost
					event := a.createPowerupEvent(slot, pt.name, startTime, bucket.startTime, nameToTeam, slotToTeam, slotToPlayer)
					events = append(events, event)
					activeRuns[slot][pt.name] = 0
				}
			}
		}
	}

	// Handle powerups still active at end of demo
	lastBucket := a.buckets[len(a.buckets)-1]
	for slot, runs := range activeRuns {
		for pType, startTime := range runs {
			if startTime > 0 {
				event := a.createPowerupEvent(slot, pType, startTime, lastBucket.endTime, nameToTeam, slotToTeam, slotToPlayer)
				events = append(events, event)
			}
		}
	}

	// Sort by time
	sort.Slice(events, func(i, j int) bool {
		return events[i].Time < events[j].Time
	})

	return events
}

// createPowerupEvent creates a PowerupEvent with resolved player info
func (a *TimelineAnalyzer) createPowerupEvent(slot int, powerupType string, startTime, endTime float64, nameToTeam map[string]string, slotToTeam map[int]string, slotToPlayer map[int]string) PowerupEvent {
	event := PowerupEvent{
		Time:        startTime,
		EndTime:     endTime,
		PlayerSlot:  slot,
		PowerupType: powerupType,
		Duration:    endTime - startTime,
	}

	// Set UserID from our tracking map (used for Hub viewer track param)
	if userID, ok := a.playerUserIDs[slot]; ok {
		event.PlayerUserID = userID
	}

	// Prefer the demoinfo-resolved name (via slotToPlayer) so the emitted
	// name matches what the frontend joins against. Only fall back to the
	// userinfo name when the strict (team, frags) match failed for this slot.
	if name := slotToPlayer[slot]; name != "" {
		event.PlayerName = name
	}
	if t := slotToTeam[slot]; t != "" {
		event.Team = t
	}
	if player := a.ctx.Players[slot]; player != nil {
		if event.PlayerName == "" {
			event.PlayerName = player.Name
		}
		if event.Team == "" {
			event.Team = player.Team
		}
		if event.PlayerUserID == 0 && player.UserID != 0 {
			event.PlayerUserID = player.UserID
		}
	}
	if event.PlayerName == "" {
		if name, ok := a.playerNames[slot]; ok {
			event.PlayerName = name
		}
	}
	if event.Team == "" && event.PlayerName != "" {
		event.Team = nameToTeam[event.PlayerName]
	}

	return event
}

// detectFragStreaks computes the top N longest frag streaks (spawn-to-death runs)
// ranked by number of frags. Each run starts at spawn and ends at death.
// Effective weapon (ewep) is the weapon with the most kills during the run.
func (a *TimelineAnalyzer) detectFragStreaks(topN int, nameToTeam map[string]string, playerUserIDsByName map[string]int) []FragStreakEvent {
	resolved := a.ctx.ResolveSlotDemoInfo()

	// Helper: resolve slot to player name
	slotName := func(slot int) string {
		if di, ok := resolved[slot]; ok && di.Name != "" {
			return di.Name
		}
		if player := a.ctx.Players[slot]; player != nil {
			return player.Name
		}
		if n, ok := a.playerNames[slot]; ok {
			return n
		}
		return ""
	}

	// Build per-player sorted spawn and death time lists
	type lifeEvent struct {
		time float64
	}
	spawnsByName := make(map[string][]float64)
	deathsByName := make(map[string][]float64)

	for _, s := range a.spawnEventsRaw {
		if name := slotName(s.PlayerNum); name != "" {
			spawnsByName[name] = append(spawnsByName[name], s.Time)
		}
	}
	for _, d := range a.deathEventsRaw {
		if name := slotName(d.PlayerNum); name != "" {
			deathsByName[name] = append(deathsByName[name], d.Time)
		}
	}
	for name := range spawnsByName {
		sort.Float64s(spawnsByName[name])
	}
	for name := range deathsByName {
		sort.Float64s(deathsByName[name])
	}

	// Build runs: pair each spawn with the next death
	type run struct {
		playerName string
		team       string
		spawnTime  float64
		deathTime  float64
	}
	var runs []run

	// Collect all player names
	allPlayers := make(map[string]bool)
	for name := range spawnsByName {
		allPlayers[name] = true
	}
	for name := range deathsByName {
		allPlayers[name] = true
	}

	for name := range allPlayers {
		spawns := spawnsByName[name]
		deaths := deathsByName[name]
		di := 0

		for _, spawnT := range spawns {
			// Find next death after this spawn
			for di < len(deaths) && deaths[di] <= spawnT {
				di++
			}
			deathT := 0.0
			if di < len(deaths) {
				deathT = deaths[di]
				di++
			} else {
				// No death found - run extends to end of match
				if len(a.buckets) > 0 {
					deathT = a.buckets[len(a.buckets)-1].endTime
				}
			}
			if deathT > spawnT {
				runs = append(runs, run{
					playerName: name,
					team:       nameToTeam[name],
					spawnTime:  spawnT,
					deathTime:  deathT,
				})
			}
		}
	}

	// For each run, count frags and determine effective weapon using FragEntries
	fragEntries := a.ctx.FragEntries
	var allStreaks []FragStreakEvent

	for _, r := range runs {
		frags := 0
		weaponCounts := make(map[string]int)

		for _, fe := range fragEntries {
			if fe.Killer != r.playerName {
				continue
			}
			if fe.Time < r.spawnTime || fe.Time > r.deathTime {
				continue
			}
			if fe.IsSuicide || fe.IsTeamKill {
				continue
			}
			frags++
			weaponCounts[fe.Weapon]++
		}

		if frags == 0 {
			continue
		}

		// Determine effective weapon (most kills)
		ewep := ""
		maxWepKills := 0
		for wep, count := range weaponCounts {
			if count > maxWepKills {
				maxWepKills = count
				ewep = wep
			}
		}

		allStreaks = append(allStreaks, FragStreakEvent{
			Time:       r.spawnTime,
			EndTime:    r.deathTime,
			PlayerName: r.playerName,
			Team:       r.team,
			Frags:      frags,
			Duration:   r.deathTime - r.spawnTime,
			Ewep:       ewep,
		})
	}

	// Sort by frags descending
	sort.Slice(allStreaks, func(i, j int) bool {
		if allStreaks[i].Frags != allStreaks[j].Frags {
			return allStreaks[i].Frags > allStreaks[j].Frags
		}
		return allStreaks[i].Duration < allStreaks[j].Duration // Tie-break: shorter run = more impressive
	})

	// Set UserIDs
	for i := range allStreaks {
		if uid, ok := playerUserIDsByName[allStreaks[i].PlayerName]; ok {
			allStreaks[i].PlayerUserID = uid
		}
	}

	// Return top N
	if len(allStreaks) > topN {
		allStreaks = allStreaks[:topN]
	}

	return allStreaks
}

// controlKeywords are the item keywords we track for region control
var controlKeywords = map[string]bool{
	"RA": true, "RL": true, "LG": true, "QUAD": true,
}

// locWithKeyword pairs a location with its matched keyword
type locWithKeyword struct {
	loc     loc.Location
	keyword string
}

// buildControlRegions groups locations by item keyword and clusters spatially
func (a *TimelineAnalyzer) buildControlRegions() []ControlRegion {
	locs := a.locFinder.Locations()
	if len(locs) == 0 {
		return nil
	}

	// Group locations by any matching keyword token in their name
	// e.g., "cellar.RL" matches RL, "RA.stairs" matches RA
	groups := make(map[string][]locWithKeyword)

	for _, l := range locs {
		tokens := strings.FieldsFunc(l.Name, func(r rune) bool {
			return r == '.' || r == ' '
		})
		for _, token := range tokens {
			upper := strings.ToUpper(token)
			if controlKeywords[upper] {
				groups[upper] = append(groups[upper], locWithKeyword{loc: l, keyword: upper})
				break // Only match first keyword per location
			}
		}
	}

	var regions []ControlRegion

	for keyword, locs := range groups {
		clusters := clusterLocations(locs, 1500)

		for _, cluster := range clusters {
			region := ControlRegion{}

			// Name the region
			if len(clusters) == 1 {
				region.Name = keyword
			} else {
				// Find most common second token for naming
				region.Name = nameCluster(keyword, cluster)
			}

			// Build points and centroid
			var sumX, sumY float32
			for _, lk := range cluster {
				region.Points = append(region.Points, MapLocation{
					X:    lk.loc.X,
					Y:    lk.loc.Y,
					Z:    lk.loc.Z,
					Name: lk.loc.Name,
				})
				sumX += lk.loc.X
				sumY += lk.loc.Y
			}
			region.CentroidX = sumX / float32(len(cluster))
			region.CentroidY = sumY / float32(len(cluster))

			regions = append(regions, region)
		}
	}

	// Sort regions by name for stable output
	sort.Slice(regions, func(i, j int) bool {
		return regions[i].Name < regions[j].Name
	})

	return regions
}

// clusterLocations groups locations by spatial proximity using single-linkage clustering
func clusterLocations(locs []locWithKeyword, threshold float64) [][]locWithKeyword {
	n := len(locs)
	if n == 0 {
		return nil
	}

	// Union-Find
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	threshSq := threshold * threshold
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			dx := float64(locs[i].loc.X - locs[j].loc.X)
			dy := float64(locs[i].loc.Y - locs[j].loc.Y)
			if dx*dx+dy*dy < threshSq {
				union(i, j)
			}
		}
	}

	// Group by root
	clusterMap := make(map[int][]locWithKeyword)
	for i, l := range locs {
		root := find(i)
		clusterMap[root] = append(clusterMap[root], l)
	}

	var result [][]locWithKeyword
	for _, c := range clusterMap {
		result = append(result, c)
	}
	return result
}

// nameCluster names a cluster based on the most common second token
func nameCluster(keyword string, cluster []locWithKeyword) string {
	tokenCounts := make(map[string]int)
	for _, lk := range cluster {
		name := lk.loc.Name
		// Extract second token after the keyword
		rest := name
		if idx := strings.IndexAny(name, ". "); idx > 0 {
			rest = name[idx+1:]
		} else {
			rest = ""
		}
		if rest != "" {
			// Get just the first sub-token
			if idx := strings.IndexAny(rest, ". "); idx > 0 {
				rest = rest[:idx]
			}
			tokenCounts[strings.ToLower(rest)]++
		}
	}

	bestToken := ""
	bestCount := 0
	for token, count := range tokenCounts {
		if count > bestCount {
			bestCount = count
			bestToken = token
		}
	}

	if bestToken != "" {
		return keyword + "." + bestToken
	}
	return keyword
}

// (Region control stats are computed client-side in JavaScript)
