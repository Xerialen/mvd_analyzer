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
	"github.com/mvd-analyzer/qwdemo/events"
	"github.com/mvd-analyzer/qwanalytics/result"
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
	FragsBySlot map[int]int         // Final frag count per slot
	DemoInfo    *DemoInfoResult     // Parsed demoinfo (set during finalization, used by ResolveSlotDemoInfo)
}

// SlotDemoInfo holds the resolved demoinfo player for a slot.
type SlotDemoInfo struct {
	Name string // Display name from demoinfo
	Team string // Team from demoinfo
}

// ResolveSlotDemoInfo bridges slot↔demoinfo using login join (for
// authenticated players) then name join (for unauthenticated players).
// Returns a map from slot number to the matched demoinfo player's
// display name and team.
func (ctx *Context) ResolveSlotDemoInfo() map[int]SlotDemoInfo {
	out := make(map[int]SlotDemoInfo)
	if ctx.DemoInfo == nil {
		return out
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
				out[slot] = SlotDemoInfo{Name: dp.Name, Team: dp.Team}
				continue
			}
		}
		if dp, ok := demoByName[normalizePlayerName(live.Name)]; ok {
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
	HighResBucket          = result.HighResBucket
	HighResTeamData        = result.HighResTeamData
	HighResPlayerData      = result.HighResPlayerData
	MapLocation            = result.MapLocation
	TimelineFragEvent      = result.TimelineFragEvent
	PowerupEvent           = result.PowerupEvent
	FragStreakEvent        = result.FragStreakEvent
	MetadataResult         = result.MetadataResult
	MatchSettings          = result.MatchSettings
	LocGraphResult         = result.LocGraphResult
	LocNode                = result.LocNode
	LocEdge                = result.LocEdge
	ItemsResult            = result.ItemsResult
	ItemTimeline           = result.ItemTimeline
	ItemPhase              = result.ItemPhase
	BackpackDrop           = result.BackpackDrop
	WeaponPickup           = result.WeaponPickup
)
