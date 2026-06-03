// Package analyzer provides pluggable analysis modules for QuakeWorld
// demos. It is the implementation-side of the qwanalytics library:
// Analyzer implementations, the Context they share, and the Registry
// that runs them over an events.Source.
//
// The stable JSON contract produced by running a pipeline lives in
// qwanalytics/result; every Result-shaped type below is a type alias
// into that package so external consumers get one canonical schema
// regardless of which code path produces it.
package analyzer

import (
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-reader/events"
)

// Context provides shared state for analyzers during a single analysis
// run. ServerData and Players are populated by the registry as events
// stream through; FragsBySlot and DemoInfo are written during the
// finalize loop.
//
// Fields that one analyser writes during Finalize and another reads
// (frag entries, name tables, etc.) live on CoreOutputs in
// core_outputs.go — see CoreConsumer.UseCoreOutputs.
type Context struct {
	ServerData  *events.ServerData
	Players     [events.MaxClients]*events.PlayerInfo
	FragsBySlot map[int]int     // Final frag count per slot
	DemoInfo    *DemoInfoResult // Parsed demoinfo (set during finalization, used by ResolveSlotDemoInfo)
}

// SlotDemoInfo holds the resolved demoinfo player for a slot.
type SlotDemoInfo struct {
	Name string // Display name from demoinfo
	Team string // Team from demoinfo
}

// demoInfoIndex is the precomputed login/name lookup over a demoinfo
// player list. It backs both the slot-keyed ResolveSlotDemoInfo and the
// per-session resolution used by the identity analyzer, so the join
// rules (login first, then unambiguous normalized-name) live in one
// place.
type demoInfoIndex struct {
	byLogin map[string]*DemoInfoPlayer
	byName  map[string]*DemoInfoPlayer // ambiguous normalized names removed
}

// newDemoInfoIndex builds the lookup. Returns nil when di is nil so
// callers can treat "no demoinfo" as "no match" without special-casing.
func newDemoInfoIndex(di *DemoInfoResult) *demoInfoIndex {
	if di == nil {
		return nil
	}
	x := &demoInfoIndex{
		byLogin: make(map[string]*DemoInfoPlayer),
		byName:  make(map[string]*DemoInfoPlayer),
	}
	nameCount := make(map[string]int)
	for i := range di.Players {
		p := &di.Players[i]
		if p.Name == "" {
			continue
		}
		if p.Login != "" {
			x.byLogin[p.Login] = p
		}
		norm := normalizePlayerName(p.Name)
		nameCount[norm]++
		if nameCount[norm] == 1 {
			x.byName[norm] = p
		} else {
			delete(x.byName, norm) // ambiguous: same display name on >1 demoinfo entry
		}
	}
	return x
}

// resolve joins a single (netname, auth) identity to its demoinfo
// player: login join for authenticated players, then unambiguous
// normalized-name join. The returned *DemoInfoPlayer lets callers fold
// distinct sessions that map to the same entry into one identity.
func (x *demoInfoIndex) resolve(name, auth string) (*DemoInfoPlayer, bool) {
	if x == nil {
		return nil, false
	}
	if auth != "" {
		if dp, ok := x.byLogin[auth]; ok {
			return dp, true
		}
	}
	if dp, ok := x.byName[normalizePlayerName(name)]; ok {
		return dp, true
	}
	return nil, false
}

// ResolveSlotDemoInfo bridges slot↔demoinfo using login join (for
// authenticated players) then name join (for unauthenticated players).
// Returns a map from slot number to the matched demoinfo player's
// display name and team.
func (ctx *Context) ResolveSlotDemoInfo() map[int]SlotDemoInfo {
	out := make(map[int]SlotDemoInfo)
	idx := newDemoInfoIndex(ctx.DemoInfo)
	if idx == nil {
		return out
	}
	for slot, live := range ctx.Players {
		if live == nil {
			continue
		}
		if dp, ok := idx.resolve(live.Name, live.Auth); ok {
			out[slot] = SlotDemoInfo{Name: dp.Name, Team: dp.Team}
		}
	}
	return out
}

// Analyzer is the interface for analysis modules.
type Analyzer interface {
	// Name returns the unique identifier for this analyzer. Used for
	// diagnostics — the registry no longer dispatches on it.
	Name() string

	// Init is called before parsing begins.
	Init(ctx *Context) error

	// OnEvent receives parsed events during parsing.
	OnEvent(event events.Event) error

	// Finalize runs after the event stream is exhausted. Each analyser
	// writes its own slice of the result struct directly — no more
	// type-erased return + registry-side switch on Name().
	Finalize(result *Result) error
}

// --- Result type aliases ---
//
// The canonical JSON schema lives in qwanalytics/result. The aliases
// below expose the same types under their historical names in this
// package so intra-analyzer code keeps reading as plain local names
// (MatchResult, FragResult, ...) rather than qualified as result.*.
// Cross-package consumers should prefer importing the result package
// directly.

type (
	Result                 = result.Result
	MatchResult            = result.MatchResult
	PlayerStat             = result.PlayerStat
	TeamStat               = result.TeamStat
	FragResult             = result.FragResult
	FragEntry              = result.FragEntry
	PlayerFrags            = result.PlayerFrags
	MessagesResult         = result.MessagesResult
	MatchEvent             = result.MatchEvent
	DemoInfoResult         = result.DemoInfoResult
	DemoInfoPlayer         = result.DemoInfoPlayer
	DemoInfoBot            = result.DemoInfoBot
	DemoInfoStats          = result.DemoInfoStats
	DemoInfoDmg            = result.DemoInfoDmg
	DemoInfoSpree          = result.DemoInfoSpree
	DemoInfoSpeed          = result.DemoInfoSpeed
	DemoInfoWeapon         = result.DemoInfoWeapon
	DemoInfoAcc            = result.DemoInfoAcc
	DemoInfoKills          = result.DemoInfoKills
	DemoInfoPickups        = result.DemoInfoPickups
	DemoInfoDamage         = result.DemoInfoDamage
	DemoInfoItem           = result.DemoInfoItem
	TimelineAnalysisResult = result.TimelineAnalysisResult
	ControlRegion          = result.ControlRegion
	RegionControlResult    = result.RegionControlResult
	RegionStats            = result.RegionStats
	MapLocation            = result.MapLocation
	TimelineFragEvent      = result.TimelineFragEvent
	TimelineDeathEvent     = result.TimelineDeathEvent
	TimelineKillEvent      = result.TimelineKillEvent
	PowerupEvent           = result.PowerupEvent
	FragStreakEvent        = result.FragStreakEvent
	MetadataResult         = result.MetadataResult
	MatchSettings          = result.MatchSettings
	LocGraphResult         = result.LocGraphResult
	LocNode                = result.LocNode
	LocWeights             = result.LocWeights
	LocEdge                = result.LocEdge
	LocEdgeWeights         = result.LocEdgeWeights
	Interval               = result.Interval
	ItemsResult            = result.ItemsResult
	ItemTimeline           = result.ItemTimeline
	ItemPhase              = result.ItemPhase
	DamageResult           = result.DamageResult
	DamageEntry            = result.DamageEntry
	PositionalKill         = result.PositionalKill
	PlayerDamage           = result.PlayerDamage
	DamagePair             = result.DamagePair
	DamageReconciliation   = result.DamageReconciliation
	DamageDelta            = result.DamageDelta
	MapEntitiesResult      = result.MapEntitiesResult
	MapEntity              = result.MapEntity
	Bounds                 = result.Bounds
	BackpackDrop           = result.BackpackDrop
	WeaponPickup           = result.WeaponPickup
)
