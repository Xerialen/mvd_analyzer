package analyzer

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// MatchAnalyzer extracts match summary information
type MatchAnalyzer struct {
	ctx      *Context
	core     *CoreOutputs
	duration float64
	timing   MatchTimingDetector

	// frags is the per-slot svc_updatefrags scoreboard, frozen at match
	// end (see OnEvent) so post-match slot re-inits cannot clobber the
	// final score.
	frags map[int]int
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
	case *events.FragUpdateEvent:
		// The match scoreboard is immutable once the match ends; a frag
		// update after that is next-game bookkeeping, most commonly a
		// reconnecting client's slot re-init to 0 during intermission,
		// which would otherwise clobber the final score (hub 212483:
		// Doomie's 34 reset to 0 by a post-match reconnect; hub 212545:
		// squeeze's 55 likewise). Mid-match reconnects need no special
		// case — KTX re-asserts the restored count via svc_updatefrags
		// right after the rejoin.
		if !a.timing.Ended {
			if a.frags == nil {
				a.frags = make(map[int]int)
			}
			a.frags[e.PlayerNum] = e.Frags
		}
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
		Duration: msTime(matchDuration),
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

		// Use tracked frags if available (keyed by slot, not name;
		// frozen at match end — see OnEvent).
		if frags, ok := a.frags[i]; ok {
			stat.Frags = frags
		}

		// A player who legitimately finishes on 0 frags (kills cancelled by
		// suicides, a short but real appearance) is still a participant — the
		// surface-authoritative-data policy says report them rather than guess
		// they "didn't play". True spectators are excluded above by the
		// Spectator/empty-team gates (final parser state). Known limitation:
		// those gates are end-of-demo state, so a participant who goes
		// spectator after the match (sub-out, post-game spec) is dropped here
		// even though demoinfo lists them; recovering them needs the
		// demoinfo-authoritative participant merge (see duel_normalize's
		// Match.Players rebuild for the duel-mode version).
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

	// Duel: rebuild the participant list and team labels around the
	// player-name-per-player layout the roster owns. This is the old
	// normalizeDuelTeams Match block, moved here so match.Players is born
	// correct — and, importantly, so the demoinfo-authoritative participant
	// merge recovers players the spectator gate above dropped (a frogbot with
	// team "" and no svc_updatefrags), which demoinfo lists but this Finalize
	// filtered out.
	if a.core != nil && a.core.Roster != nil {
		r := a.core.Roster
		// No usable demoinfo: let a 2-participant match decide the duel verdict,
		// the case the old isDuelResult covered via its match-players fallback.
		// Runs before every label-emitting derived producer, so the verdict
		// propagates.
		if a.core.DemoInfo == nil || len(a.core.DemoInfo.Players) == 0 {
			names := make([]string, 0, len(mr.Players))
			for _, p := range mr.Players {
				names = append(names, p.Name)
			}
			r.noteMatchParticipants(names)
		}
		if r.Duel() {
			rebuildDuelMatch(mr, a.core.DemoInfo)
		}
	}
	return nil
}

// rebuildDuelMatch reconstructs MatchResult.Players and .Teams for a 1v1 around
// the synthetic one-player-per-team layout (team == the player's own name).
//
// When demoinfo is present it is the source of truth for participants — its
// end-of-match snapshot always lists every player it tracked stats for, so this
// recovers a participant the Finalize spectator gate dropped (a teamless
// frogbot) while merging in any per-player frag count already tracked. With no
// demoinfo it falls back to the existing two-player list, just relabelling
// teams. Mirrors the old normalizeDuelTeams Match block exactly.
func rebuildDuelMatch(mr *MatchResult, di *DemoInfoResult) {
	existing := make(map[string]PlayerStat, len(mr.Players))
	for _, p := range mr.Players {
		existing[p.Name] = p
	}
	rebuilt := make([]PlayerStat, 0, len(existing))
	if di != nil && len(di.Players) > 0 {
		for _, dp := range di.Players {
			ps, ok := existing[dp.Name]
			if !ok {
				ps = PlayerStat{Name: dp.Name}
			}
			ps.Team = dp.Name
			if dp.Stats != nil {
				ps.Frags = dp.Stats.Frags
			}
			rebuilt = append(rebuilt, ps)
		}
	} else {
		for _, p := range mr.Players {
			p.Team = p.Name
			rebuilt = append(rebuilt, p)
		}
	}
	mr.Players = rebuilt

	teams := make([]TeamStat, 0, len(mr.Players))
	for _, p := range mr.Players {
		teams = append(teams, TeamStat{Name: p.Name, Frags: p.Frags})
	}
	mr.Teams = teams
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
