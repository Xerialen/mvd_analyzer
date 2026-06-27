package analyzer

import (
	"github.com/mvd-analyzer/mvd-analytics/bspvis"
	"github.com/mvd-analyzer/mvd-analytics/config"
	"github.com/mvd-analyzer/mvd-analytics/locvis"
	"github.com/mvd-analyzer/mvd-analytics/mapclip"
	"github.com/mvd-analyzer/mvd-reader/events"
)

// TimelineAnalyzer collects per-event state into result.Streams and
// drives the derived event-shaped outputs (frag events, powerup runs,
// streaks). At schema v7 there is no parse-time bucket grid: every
// event flows into a streamBuilder per player, and finalize derives
// what consumers need (loc resolution + blip filter on the position
// track, region control, loc graph) directly from streams.
//
// The analyzer is split across several files in this package:
//
//   - timeline.go            (this file) state, types, OnEvent
//   - timeline_streams.go    streamBuilder + loc resolution + blip filter
//   - timeline_finalize.go   Finalize orchestration
//   - timeline_powerups.go   powerup pickup/loss event detection
//   - timeline_streaks.go    spawn-to-death frag streak detection
//   - timeline_regions.go    map region control auto-detection + custom defs
type TimelineAnalyzer struct {
	ctx           *Context
	core          *CoreOutputs
	playerState   map[int]*timelinePlayerState
	playerNames   map[int]string // Slot -> player name (from UserInfoEvent)
	playerUserIDs map[int]int    // Slot -> UserID (for Hub viewer track param)
	// slotUserID is the *current* occupant's userid per slot (last valid
	// wins, unlike playerUserIDs which pins the first). It lets
	// onUserInfo spot a mid-match handoff so handleFragUpdate can rebase
	// a reconnecting player's restored frag total — see fragResetPending.
	slotUserID map[int]int
	// fragResetPending[slot] means the slot's occupant just changed
	// mid-match, so the next frag update is a KTX stats restore / initial
	// scoreboard, not a kill. Consumed (cleared) by handleFragUpdate.
	fragResetPending map[int]bool
	rawFrags         []fragEvent  // Raw frag events (player num, time)
	rawDeaths        []deathEvent // Raw death events (player num, time)
	rawSpawns        []deathEvent // Raw spawn events (reusing deathEvent type)
	timing           MatchTimingDetector
	// demoStartUnixMs is the wall-clock (Unix epoch ms) at demo open,
	// captured from the mvdhidden 0x000B block. demoStartFromHidden records
	// that the millisecond-accurate source was present, so Finalize can set
	// the accuracy and the epoch-cvar fallback (in deriveDemoStartAnchor)
	// knows not to overwrite it.
	demoStartUnixMs     int64
	demoStartFromHidden bool
	// rawPauses collects every mvdhidden 0x000A paused_duration sample
	// (demo-relative time + real wall-clock ms for that idle frame), in
	// arrival order. Finalize coalesces contiguous runs into per-pause
	// segments (TimelineAnalysis.Pauses). Captured unconditionally — pauses
	// during the countdown matter for the wall-clock mapping too.
	rawPauses       []pauseSample
	locFinder       *locvis.Finder             // Visibility-aware loc finder for map (nil if no .loc file)
	clipHull        *mapclip.Hull              // Worldspawn player clip hull for floor-height traces (nil if no clip corpus for map)
	visBSP          *bspvis.BSP                // Hull-0 render BSP for liquid state / liquid-surface queries (nil if no BSP for map)
	blipThresholdMs int                        // Per-player loc smoothing threshold, 0 disables
	regionsOverride []config.MapRegionOverride // Optional caller-supplied region defs (e.g. CLI -regions). When non-nil, overrides config.RegionsForMap.
	// movers is each inline brush-model entity's wire-state timeline
	// (origin + visibility at demo-relative ms), accumulated from
	// MoverSpawn/MoverState events — NOT gated on match start, the
	// baseline pose arrives at demo open. moverHulls holds the matching
	// submodel clip hulls, built in Finalize alongside clipHull; the
	// floor-height pass poses them per sample (see resolveFloorHeights).
	movers     map[int]*moverTrack
	moverHulls map[int]*mapclip.Hull
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

// SetRegionsOverride supplies a caller-defined region list that
// replaces the embedded per-map regions (config.RegionsForMap) for the
// duration of this analyzer run. Used by the CLI -regions flag and by
// tests that want to pin a specific region layout. Must be called
// before Finalize(). Pass nil to clear and fall back to embedded.
func (a *TimelineAnalyzer) SetRegionsOverride(regs []config.MapRegionOverride) {
	a.regionsOverride = regs
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

// pauseSample is one mvdhidden 0x000A paused_duration block: the demo-relative
// (game) time of the idle frame and the real wall-clock ms it spanned. The
// game clock is frozen across a pause, so all samples of one pause share a
// Time; Finalize sums DurationMs over each contiguous run.
type pauseSample struct {
	Time       float64
	DurationMs int
}

// timelinePlayerState tracks current state for a single player as the
// parser walks the demo. items is the raw item bitfield from
// svc_updatestat; it's decoded into weapons/powerups/armor type
// before being recorded into the stream builder. isDead flips on
// DeathEvent / SpawnEvent and is consulted by other analyzers
// (frag streaks, etc.); not consumed by the stream emission path.
//
// The accompanying streamBuilder is the append-only historical record
// that becomes result.PlayerStream at finalize. The cursor (this
// struct's fields) tells "what is X right now"; the builder holds
// "every transition we've seen." See state.go for the dedup invariants.
type timelinePlayerState struct {
	items  int // raw item bitfield (weapons, powerups, armor type)
	vitals vitals
	isDead bool
	ammo   ammoCounts
	pos    playerPosition
	frags  int

	// streams accumulates the append-only historical record. Populated
	// in OnEvent alongside the running cursor; flushed to result.Streams
	// in Finalize.
	streams streamBuilder
}

// NewTimelineAnalyzer creates a new timeline analyzer.
func NewTimelineAnalyzer() *TimelineAnalyzer {
	return &TimelineAnalyzer{
		playerState:      make(map[int]*timelinePlayerState),
		playerNames:      make(map[int]string),
		playerUserIDs:    make(map[int]int),
		slotUserID:       make(map[int]int),
		fragResetPending: make(map[int]bool),
	}
}

func (a *TimelineAnalyzer) Name() string { return "timelineAnalysis" }

func (a *TimelineAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

// SetLocFinder sets the visibility-aware location finder for map
// position lookups. Used by callers that have already loaded the loc
// corpus (e.g. tooling that pre-builds finders for many maps).
func (a *TimelineAnalyzer) SetLocFinder(finder *locvis.Finder) {
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

			// Spot a mid-match occupant handoff: when the slot's live
			// userid changes after match start (a reconnect, or a new
			// player taking a vacated slot), the next frag update is a KTX
			// stats restore / initial scoreboard rather than a kill. Flag
			// it so handleFragUpdate rebases instead of feeding the value
			// to the corruption guard. Pre-match roster shuffles don't
			// count (frags are 0 then anyway); userid==0 resends are
			// ignored so the live id keeps the last valid value.
			if newUserID > 0 {
				prev := a.slotUserID[e.Player.Slot]
				if a.timing.Started && prev != 0 && newUserID != prev {
					a.fragResetPending[e.Player.Slot] = true
				}
				a.slotUserID[e.Player.Slot] = newUserID
			}
		}
	case *events.PlayerPositionEvent:
		// Track player positions
		a.handlePositionUpdate(e)
	case *events.DemoStartTimestampEvent:
		// mvdhidden 0x000B: server wall-clock (Unix epoch ms) at demo open,
		// the millisecond-accurate anchor for the demo timeline. Keep the
		// first one we see; the block is written once near demo start.
		//
		// Some 2026 demos carry a 0x000B block whose 1–2 byte payload is
		// NOT a Unix-ms timestamp (values like 61 / 11701 observed in corpus
		// games 211805 and 212545, both of which also carry a correct
		// whole-second `epoch` cvar). Decoded as a wall clock those land in
		// 1970, and a consumer cannot tell them apart from a real anchor — so
		// accept the block only when it falls in a plausible epoch-ms window;
		// otherwise leave the anchor to the `epoch` fallback in
		// deriveDemoStartAnchor.
		if !a.demoStartFromHidden && plausibleDemoStartUnixMs(e.UnixMs) {
			a.demoStartUnixMs = e.UnixMs
			a.demoStartFromHidden = true
		}
	case *events.PausedDurationEvent:
		// mvdhidden 0x000A: one real-ms sample per idle frame while the game
		// clock is paused. Collect raw; Finalize coalesces into per-pause
		// segments. See pauseSample.
		a.rawPauses = append(a.rawPauses, pauseSample{Time: e.Time, DurationMs: e.DurationMs})
	case *events.MoverSpawnEvent:
		a.handleMoverSpawn(e)
	case *events.MoverStateEvent:
		a.handleMoverState(e)
	}
	return nil
}

func (a *TimelineAnalyzer) handlePositionUpdate(e *events.PlayerPositionEvent) {
	// Always track position cursor, even during warmup (for continuity).
	state := a.getOrCreatePlayerState(e.PlayerNum)
	state.pos = playerPosition{x: e.Origin[0], y: e.Origin[1], z: e.Origin[2]}

	// Stream emission: append every native sample (D11 asymmetry —
	// positions don't dedup). Match-time only; warmup positions would
	// pollute the stream with garbage.
	if a.timing.Started && !a.timing.Ended {
		state.streams.recordPosition(e.TimeMs, e.Origin[0], e.Origin[1], e.Origin[2], e.Angles[0], e.Angles[1])
	}
}

func (a *TimelineAnalyzer) handleFragUpdate(e *events.FragUpdateEvent) {
	state := a.getOrCreatePlayerState(e.PlayerNum)
	if !a.timing.Started {
		return
	}

	// A mid-match occupant change on this wire slot (flagged by onUserInfo)
	// means this frag value is a KTX stats restore / initial scoreboard for
	// the new occupant, not a kill. Adopt it as the new baseline and emit
	// nothing. Without this, a reconnecting player whose frag total KTX
	// restores onto a new slot (gameId 216835: rusti rejoins onto a vacated
	// spectator slot with 16 frags) reads as a huge +delta, gets rejected by
	// the corruption guard below, and — because that guard leaves state.frags
	// at 0 — every later real +1 keeps reading as a huge delta and is also
	// rejected, freezing the player's timeline score for the rest of the match.
	if a.fragResetPending[e.PlayerNum] {
		a.fragResetPending[e.PlayerNum] = false
		state.frags = e.Frags
		return
	}

	// Track frag changes (both increases and decreases)
	// Frags increase on kills, decrease on suicides/teamkills
	if e.Frags != state.frags {
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
			state.streams.recordHealth(msTime(e.Time), int16(e.Value))
		}
	case events.StatArmor:
		// Same shape: KTX overwrites armorvalue in pre-match speed-meter
		// and in damage feedback paths with values > 200. Real armor caps
		// at 200 (RA). Reject anything larger.
		if e.Value <= 200 {
			state.vitals.armor = e.Value
			state.streams.recordArmor(msTime(e.Time), int16(e.Value))
		}
	case events.StatItems:
		state.items = e.Value
		w, p, at := itemBitsToLoadouts(e.Value)
		state.streams.recordItemFlags(msTime(e.Time), w, p)
		state.streams.recordArmorType(msTime(e.Time), at)
	case events.StatShells:
		state.ammo.shells = e.Value
		state.streams.recordShells(msTime(e.Time), int16(e.Value))
	case events.StatNails:
		state.ammo.nails = e.Value
		state.streams.recordNails(msTime(e.Time), int16(e.Value))
	case events.StatRockets:
		state.ammo.rockets = e.Value
		state.streams.recordRockets(msTime(e.Time), int16(e.Value))
	case events.StatCells:
		state.ammo.cells = e.Value
		state.streams.recordCells(msTime(e.Time), int16(e.Value))
	case events.StatActiveWeapon:
		// The selected/active weapon id (STAT_ACTIVEWEAPON — the wielded
		// weapon's IT_ bit). Unlike health/armor there is no sane upper
		// bound to reject against (IT_AXE = 4096); surface the raw value
		// and let the consumer map the bit to a weapon.
		state.streams.recordActiveWeapon(msTime(e.Time), int16(e.Value))
	}
	return nil
}

// handleDeath records the authoritative death transition from the
// parser. Same guard as handleStatUpdate: only match-time events are
// recorded so warmup cycles don't pollute state.
func (a *TimelineAnalyzer) handleDeath(e *events.DeathEvent) {
	if !a.timing.Started || a.timing.Ended {
		return
	}
	state := a.getOrCreatePlayerState(e.PlayerNum)
	a.rawDeaths = append(a.rawDeaths, deathEvent{Time: e.Time, PlayerNum: e.PlayerNum})
	state.streams.recordDeath(e.TimeMs)
	state.isDead = true
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
	state.streams.recordSpawn(e.TimeMs)
	state.isDead = false
}

// resolveAt resolves a wire slot to its (name, team) at time tMs using
// the reconnect-aware CoreOutputs session table, with the same fallback
// chain the timeline has always used: userinfo (ctx.Players), then the
// last-seen userinfo name (playerNames), then a name→team lookup in the
// demoinfo NameTable. Centralises what fragEvents / powerups / streaks
// all need so a player's pre-reconnect events resolve to the player who
// was actually on the slot then, not the slot's final occupant.
func (a *TimelineAnalyzer) resolveAt(slot int, tMs int32) (name, team string) {
	if a.core != nil {
		id := a.core.SlotIdentityAt(slot, tMs)
		name, team = id.Name, id.Team
	}
	if name == "" {
		if p := a.ctx.Players[slot]; p != nil {
			name = p.Name
			if team == "" {
				team = p.Team
			}
		}
	}
	if name == "" {
		if n, ok := a.playerNames[slot]; ok {
			name = n
		}
	}
	if name != "" && team == "" && a.core != nil {
		team = a.core.Names.TeamForName(name)
	}
	return name, team
}

func (a *TimelineAnalyzer) getOrCreatePlayerState(playerNum int) *timelinePlayerState {
	if s, ok := a.playerState[playerNum]; ok {
		return s
	}
	s := &timelinePlayerState{}
	a.playerState[playerNum] = s
	return s
}
