// Package analyzer provides pluggable analysis modules for MVD demos.
package analyzer

import (
	"github.com/mvd-analyzer/internal/mvd"
	"github.com/mvd-analyzer/internal/parser"
)

// Context provides shared state for analyzers
type Context struct {
	ServerData *mvd.ServerData
	Players    [mvd.MaxClients]*mvd.PlayerInfo
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
	FilePath string           `json:"filePath"`
	Duration float64          `json:"duration"`
	Match    *MatchResult     `json:"match,omitempty"`
	Frags    *FragResult      `json:"frags,omitempty"`
	Stats    *StatsResult     `json:"stats,omitempty"`
	Errors   []string         `json:"errors,omitempty"`
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

// StatsResult contains stats tracking results
type StatsResult struct {
	PlayerStats map[string]*PlayerStatsEntry `json:"playerStats"`
}

// PlayerStatsEntry holds final stats for a player
type PlayerStatsEntry struct {
	MaxHealth int `json:"maxHealth"`
	MaxArmor  int `json:"maxArmor"`
}
