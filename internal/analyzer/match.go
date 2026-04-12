package analyzer

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/mvd-analyzer/internal/parser"
)

// MatchAnalyzer extracts match summary information
type MatchAnalyzer struct {
	ctx            *Context
	duration       float64
	matchStartTime float64
	matchEndTime   float64
	matchStarted   bool
}

// NewMatchAnalyzer creates a new match analyzer
func NewMatchAnalyzer() *MatchAnalyzer {
	return &MatchAnalyzer{}
}

func (a *MatchAnalyzer) Name() string { return "match" }

func (a *MatchAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

func (a *MatchAnalyzer) OnEvent(event parser.Event) error {
	a.duration = event.EventTime()

	if printEvent, ok := event.(*parser.PrintEvent); ok {
		a.checkMatchTiming(printEvent)
	}

	return nil
}

// checkMatchTiming detects match start/end from print messages
func (a *MatchAnalyzer) checkMatchTiming(event *parser.PrintEvent) {
	msg := strings.ToLower(event.Message)

	// Match start patterns
	matchStartPatterns := []string{
		"the match has begun",
		"match started",
		"fight!",
		"game start",
	}

	for _, pattern := range matchStartPatterns {
		if strings.Contains(msg, pattern) {
			if !a.matchStarted {
				a.matchStartTime = event.Time
				a.matchStarted = true
			}
			return
		}
	}

	// Match end patterns
	matchEndPatterns := []string{
		"the match is over",
		"match ended",
		"game over",
		"match complete",
		"timelimit hit",
		"fraglimit hit",
	}

	for _, pattern := range matchEndPatterns {
		if strings.Contains(msg, pattern) {
			a.matchEndTime = event.Time
			return
		}
	}
}

func (a *MatchAnalyzer) Finalize() (interface{}, error) {
	// Calculate actual match duration
	matchDuration := a.duration
	if a.matchStarted && a.matchStartTime > 0 {
		if a.matchEndTime > a.matchStartTime {
			matchDuration = a.matchEndTime - a.matchStartTime
		} else {
			// No end detected, use total - start
			matchDuration = a.duration - a.matchStartTime
		}
	}

	result := &MatchResult{
		Duration:  matchDuration,
		StartTime: a.matchStartTime,
		EndTime:   a.matchEndTime,
	}

	// Get map name from server data
	if a.ctx.ServerData != nil {
		result.Map = extractMapName(a.ctx.ServerData.LevelName)
		result.GameDir = a.ctx.ServerData.GameDir
	}

	// Collect team stats
	teamFrags := make(map[string]int)

	// Collect player stats.
	// ctx.Players[slot].Name is already patched to the display name
	// by registry.go after DemoInfo finalization.
	for i := 0; i < len(a.ctx.Players); i++ {
		p := a.ctx.Players[i]
		if p == nil || p.Name == "" || p.Spectator {
			continue
		}

		// Skip players with invalid/spectator-like teams
		if isSpectatorTeam(p.Team) {
			continue
		}

		stat := PlayerStat{
			Name:  p.Name,
			Team:  p.Team,
			Frags: p.Frags,
		}

		// Use tracked frags if available (keyed by slot, not name)
		if frags, ok := a.ctx.FragsBySlot[i]; ok {
			stat.Frags = frags
		}

		// Skip players with 0 frags (likely joined briefly but didn't play)
		if stat.Frags == 0 {
			continue
		}

		result.Players = append(result.Players, stat)

		// Aggregate team frags
		if p.Team != "" {
			teamFrags[p.Team] += stat.Frags
		}
	}

	// Build team stats - only include valid team names. Sort by name so the
	// output is byte-stable across runs (Go map iteration is randomized).
	teamNames := make([]string, 0, len(teamFrags))
	for team := range teamFrags {
		if !isSpectatorTeam(team) {
			teamNames = append(teamNames, team)
		}
	}
	sort.Strings(teamNames)
	for _, team := range teamNames {
		result.Teams = append(result.Teams, TeamStat{
			Name:  team,
			Frags: teamFrags[team],
		})
	}

	return result, nil
}

// isSpectatorTeam returns true if the team name indicates a spectator
func isSpectatorTeam(team string) bool {
	// Empty team is often a spectator
	if team == "" {
		return true
	}

	// Common spectator team names
	spectatorTeams := []string{
		"spec", "spectator", "specs", "spectators",
		"coop", "observe", "observer",
	}

	teamLower := strings.ToLower(team)
	for _, st := range spectatorTeams {
		if teamLower == st {
			return true
		}
	}

	// Check for non-ASCII characters (garbled text from spectator names)
	for _, r := range team {
		if r < 32 || r > 126 {
			return true
		}
	}

	return false
}

// extractMapName extracts the map name from the level name
func extractMapName(levelName string) string {
	// Level name might be like "Schloss Adler by Zaka" or just "dm4"
	// We want to extract just the map identifier

	// First, try to get base filename if it looks like a path
	name := filepath.Base(levelName)

	// Remove common suffixes
	name = strings.TrimSuffix(name, ".bsp")

	// If there's " by " in it, it's a description - try to get first word
	if idx := strings.Index(name, " by "); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}

	// If there's a newline, take first line
	if idx := strings.Index(name, "\n"); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}

	return name
}
