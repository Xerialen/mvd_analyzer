package analyzer

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/mvd-analyzer/qwdemo/events"
)

// MatchAnalyzer extracts match summary information
type MatchAnalyzer struct {
	ctx      *Context
	core     *CoreOutputs
	duration float64
	timing   MatchTimingDetector
}

// UseCoreOutputs lets Match read demoinfo-resolved display names from
// co.Slots when building the player-stats table.
func (a *MatchAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

// NewMatchAnalyzer creates a new match analyzer
func NewMatchAnalyzer() *MatchAnalyzer {
	return &MatchAnalyzer{}
}

func (a *MatchAnalyzer) Name() string { return "match" }

func (a *MatchAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

func (a *MatchAnalyzer) OnEvent(event events.Event) error {
	a.duration = event.EventTime()

	switch e := event.(type) {
	case *events.PrintEvent:
		a.timing.OnPrint(e)
	case *events.IntermissionEvent:
		a.timing.OnIntermission(e.EventTime())
	}

	return nil
}

func (a *MatchAnalyzer) Finalize(result *Result) error {
	// Calculate actual match duration
	matchDuration := a.duration
	if a.timing.Started && a.timing.StartTime > 0 {
		if a.timing.EndTime > a.timing.StartTime {
			matchDuration = a.timing.EndTime - a.timing.StartTime
		} else {
			// No end detected, use total - start
			matchDuration = a.duration - a.timing.StartTime
		}
	}

	mr := &MatchResult{
		Duration:  matchDuration,
		StartTime: a.timing.StartTime,
		EndTime:   a.timing.EndTime,
	}

	// Get map name from server data
	if a.ctx.ServerData != nil {
		mr.Map = extractMapName(a.ctx.ServerData.LevelName)
		mr.GameDir = a.ctx.ServerData.GameDir
	}

	// Collect team stats
	teamFrags := make(map[string]int)

	// Collect player stats. Display names are taken from
	// co.Slots[i].Name (demoinfo-resolved when matched, else userinfo)
	// so this output keys against the same names as the rest of the
	// pipeline.
	for i := 0; i < len(a.ctx.Players); i++ {
		p := a.ctx.Players[i]
		if p == nil || p.Spectator {
			continue
		}
		name := a.core.SlotName(i)
		if name == "" {
			name = p.Name
		}
		if name == "" {
			continue
		}

		// Skip players with invalid/spectator-like teams
		if isSpectatorTeam(p.Team) {
			continue
		}

		stat := PlayerStat{
			Name:  name,
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

		mr.Players = append(mr.Players, stat)

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
		mr.Teams = append(mr.Teams, TeamStat{
			Name:  team,
			Frags: teamFrags[team],
		})
	}

	result.Match = mr
	return nil
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
