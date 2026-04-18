package analyzer

import (
	"strings"

	"github.com/mvd-analyzer/qwdemo/events"
)

// MessagesAnalyzer captures frags and chat messages for timeline display
type MessagesAnalyzer struct {
	ctx    *Context
	events []MatchEvent
}

// NewMessagesAnalyzer creates a new messages analyzer
func NewMessagesAnalyzer() *MessagesAnalyzer {
	return &MessagesAnalyzer{
		events: make([]MatchEvent, 0),
	}
}

func (a *MessagesAnalyzer) Name() string { return "messages" }

func (a *MessagesAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

func (a *MessagesAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.PrintEvent:
		return a.handlePrint(e)
	}
	return nil
}

func (a *MessagesAnalyzer) handlePrint(e *events.PrintEvent) error {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		return nil
	}

	// Level 3 is PRINT_CHAT (mm1/mm2 messages)
	if e.Level == events.PrintChat {
		// Parse chat message format: "name: message" or "(team) name: message"
		event := a.parseChatMessage(msg, e.Time)
		if event != nil {
			a.events = append(a.events, *event)
		}
		return nil
	}

	// Try to parse as frag (levels 1-2 are typically obituaries)
	if e.Level <= 2 {
		frag := a.parseObituarySimple(msg, e.Time)
		if frag != nil {
			a.events = append(a.events, *frag)
		}
	}

	return nil
}

// parseChatMessage parses a chat message and extracts player, team, and text
func (a *MessagesAnalyzer) parseChatMessage(msg string, time float64) *MatchEvent {
	// Skip server messages and status messages
	if strings.HasPrefix(msg, "[") || strings.Contains(msg, " joined the game") ||
		strings.Contains(msg, " left the game") || strings.Contains(msg, " is ready") ||
		strings.Contains(msg, "The match has") || strings.Contains(msg, "countdown") {
		return nil
	}

	// QW teamsay format: "(playername): message" or "(playername) message"
	if strings.HasPrefix(msg, "(") {
		// Try "(name): " format first (most common)
		if idx := strings.Index(msg, "): "); idx > 0 {
			playerName := msg[1:idx]
			chatText := msg[idx+3:]

			// Find player's team by looking up the player
			team := a.getPlayerTeam(playerName)

			return &MatchEvent{
				Time:    time,
				Type:    "teamsay",
				Player:  playerName,
				Team:    team,
				Message: chatText,
			}
		}
		// Try "(name) " format (space after paren)
		if idx := strings.Index(msg, ") "); idx > 0 {
			playerName := msg[1:idx]
			chatText := msg[idx+2:]

			// Find player's team by looking up the player
			team := a.getPlayerTeam(playerName)

			return &MatchEvent{
				Time:    time,
				Type:    "teamsay",
				Player:  playerName,
				Team:    team,
				Message: chatText,
			}
		}
	}

	// Regular chat format: "name: message"
	colonIdx := strings.Index(msg, ": ")
	if colonIdx <= 0 {
		return nil
	}

	playerName := msg[:colonIdx]
	chatText := msg[colonIdx+2:]

	// Find player's team
	team := a.getPlayerTeam(playerName)

	return &MatchEvent{
		Time:    time,
		Type:    "chat",
		Player:  playerName,
		Team:    team,
		Message: chatText,
	}
}

// parseObituarySimple does a simplified frag parse for the timeline.
// Uses the same comprehensive pattern list as frag.go (from KTX sv_mod_frags.h).
func (a *MessagesAnalyzer) parseObituarySimple(msg string, time float64) *MatchEvent {

	// --- Teamkill patterns (must check before kill patterns) ---
	tkKillerPatterns := []string{
		" gets a frag for the other team",
		" mows down a teammate",
		" squished a teammate",
		" checks his glasses",
		" checks her glasses",
		" loses another friend",
	}
	for _, pattern := range tkKillerPatterns {
		if idx := strings.Index(msg, pattern); idx > 0 {
			killer := strings.TrimSpace(msg[:idx])
			if killer != "" && !isGenericPlayer(killer) {
				return &MatchEvent{
					Time:    time,
					Type:    "frag",
					Player:  killer,
					Team:    a.getPlayerTeam(killer),
					Message: msg,
					Victim:  "teammate",
					Weapon:  "teamkill",
				}
			}
		}
	}

	// Teammate victim patterns
	tkVictimPatterns := []string{
		" was telefragged by his teammate",
		" was telefragged by her teammate",
		" was crushed by his teammate",
		" was crushed by her teammate",
		" was jumped by his teammate",
		" was jumped by her teammate",
	}
	for _, pattern := range tkVictimPatterns {
		if idx := strings.Index(msg, pattern); idx > 0 {
			victim := strings.TrimSpace(msg[:idx])
			if victim != "" && !isGenericPlayer(victim) {
				return &MatchEvent{
					Time:    time,
					Type:    "frag",
					Player:  "teammate",
					Team:    a.getPlayerTeam(victim),
					Message: msg,
					Victim:  victim,
					Weapon:  "teamkill",
				}
			}
		}
	}

	// --- Killer-first patterns (Y <verb> X) ---
	if idx := strings.Index(msg, " rips "); idx > 0 && strings.Contains(msg, " a new one") {
		killer := strings.TrimSpace(msg[:idx])
		rest := msg[idx+6:]
		if victimEnd := strings.Index(rest, " a new one"); victimEnd > 0 {
			victim := strings.TrimSpace(rest[:victimEnd])
			if killer != "" && victim != "" {
				return &MatchEvent{
					Time: time, Type: "frag", Player: killer, Team: a.getPlayerTeam(killer),
					Message: msg, Victim: victim, Weapon: "rl",
				}
			}
		}
	}
	for _, kf := range []struct{ pattern, weapon string }{{" stomps ", "stomp"}, {" squishes ", "squish"}} {
		if idx := strings.Index(msg, kf.pattern); idx > 0 {
			killer := strings.TrimSpace(msg[:idx])
			victim := strings.TrimSpace(msg[idx+len(kf.pattern):])
			if killer != "" && victim != "" {
				return &MatchEvent{
					Time: time, Type: "frag", Player: killer, Team: a.getPlayerTeam(killer),
					Message: msg, Victim: victim, Weapon: kf.weapon,
				}
			}
		}
	}

	// --- SSG buckshot pattern ("X ate N loads of Y's buckshot") ---
	if idx := strings.Index(msg, " ate "); idx > 0 {
		victim := strings.TrimSpace(msg[:idx])
		rest := msg[idx+5:]
		if strings.Contains(rest, "'s buckshot") || strings.Contains(rest, "' buckshot") {
			killerEnd := strings.Index(rest, "'s buckshot")
			if killerEnd < 0 {
				killerEnd = strings.Index(rest, "' buckshot")
			}
			loadsIdx := strings.Index(rest, " loads of ")
			if loadsIdx >= 0 && killerEnd > loadsIdx {
				killer := strings.TrimSpace(rest[loadsIdx+10 : killerEnd])
				if victim != "" && killer != "" && !isGenericPlayer(victim) && !isGenericPlayer(killer) {
					return &MatchEvent{
						Time: time, Type: "frag", Player: killer, Team: a.getPlayerTeam(killer),
						Message: msg, Victim: victim, Weapon: "ssg",
					}
				}
			}
		}
	}

	// --- Victim-first kill patterns (X <pattern> Y) ---
	// Complete list from KTX sv_mod_frags.h, order matters
	killPatterns := []struct {
		pattern string
		weapon  string
	}{
		// Telefrag
		{" was telefragged by ", "tele"},
		// Lightning Gun
		{" accepts ", "lg"},
		{" gets a natural disaster from ", "lg"},
		{" drains ", "lg"},
		// Rocket Launcher
		{" rides ", "rl"},
		{" was brutalized by ", "rl"},
		{" was smeared by ", "rl"},
		// Grenade Launcher
		{" eats ", "gl"},
		// Nailgun
		{" was body pierced by ", "ng"},
		{" was nailed by ", "ng"},
		// Super Nailgun
		{" was straw-cuttered by ", "sng"},
		{" was perforated by ", "sng"},
		{" was punctured by ", "sng"},
		{" was ventilated by ", "sng"},
		// Shotgun
		{" chewed on ", "sg"},
		{" was lead poisoned by ", "sg"},
		{" was instagibbed by ", "sg"},
		// Axe
		{" was ax-murdered by ", "axe"},
		{" was axed to pieces by ", "axe"},
		// Hook
		{" was hooked by ", "hook"},
		// Rail
		{" was railed by ", "rail"},
		// Stomp
		{" softens ", "stomp"},
		{" tried to catch ", "stomp"},
		{" was literally stomped into particles by ", "stomp"},
		{" was jumped by ", "stomp"},
		{" was crushed by ", "stomp"},
		// Generic
		{" was killed by ", "unknown"},
		{" was fragged by ", "unknown"},
	}

	for _, p := range killPatterns {
		if idx := strings.Index(msg, p.pattern); idx > 0 {
			victim := strings.TrimSpace(msg[:idx])
			rest := msg[idx+len(p.pattern):]
			killer := extractKillerName(rest)

			if victim != "" && killer != "" && !isGenericPlayer(victim) && !isGenericPlayer(killer) {
				return &MatchEvent{
					Time:    time,
					Type:    "frag",
					Player:  killer,
					Team:    a.getPlayerTeam(killer),
					Message: msg,
					Victim:  victim,
					Weapon:  p.weapon,
				}
			}
		}
	}

	// "was gibbed by" — grenade vs rocket
	if idx := strings.Index(msg, " was gibbed by "); idx > 0 {
		victim := strings.TrimSpace(msg[:idx])
		rest := msg[idx+15:]
		weapon := "rl"
		if strings.Contains(rest, "'s grenade") || strings.Contains(rest, "' grenade") {
			weapon = "gl"
		}
		killer := extractKillerName(rest)
		if victim != "" && killer != "" && !isGenericPlayer(victim) && !isGenericPlayer(killer) {
			return &MatchEvent{
				Time: time, Type: "frag", Player: killer, Team: a.getPlayerTeam(killer),
				Message: msg, Victim: victim, Weapon: weapon,
			}
		}
	}

	// --- Suicide patterns ---
	suicidePatterns := []struct {
		pattern string
		weapon  string
	}{
		{" suicides", "suicide"},
		{" discovers blast radius", "rl"},
		{" becomes bored with life", "rl"},
		{" tries to put the pin back in", "gl"},
		{" electrocutes himself", "lg"},
		{" electrocutes herself", "lg"},
		{" heats up the water", "lg"},
		{" discharges into the water", "lg"},
		{" discharges into the slime", "lg"},
		{" discharges into the lava", "lg"},
		{" sleeps with the fishes", "drown"},
		{" sucks it down", "drown"},
		{" gulped a load of slime", "slime"},
		{" can't exist on slime alone", "slime"},
		{" burst into flames", "lava"},
		{" turned into hot slag", "lava"},
		{" visits the Volcano God", "lava"},
		{" cratered", "fall"},
		{" fell to his death", "fall"},
		{" fell to her death", "fall"},
		{" was spiked", "world"},
		{" was zapped", "world"},
		{" ate a lavaball", "world"},
		{" blew up", "world"},
		{" was squished", "squish"},
		{" tried to leave", "world"},
	}

	for _, p := range suicidePatterns {
		if idx := strings.Index(msg, p.pattern); idx > 0 {
			victim := strings.TrimSpace(msg[:idx])
			if victim != "" && !isGenericPlayer(victim) {
				return &MatchEvent{
					Time:    time,
					Type:    "frag",
					Player:  victim,
					Team:    a.getPlayerTeam(victim),
					Message: msg,
					Victim:  victim,
					Weapon:  p.weapon,
				}
			}
		}
	}

	return nil
}

// getPlayerTeam returns the team name for a player using fuzzy lookup.
func (a *MessagesAnalyzer) getPlayerTeam(name string) string {
	if p := findPlayerByName(a.ctx.Players, name); p != nil {
		return p.Team
	}
	return ""
}

func (a *MessagesAnalyzer) Finalize() (interface{}, error) {
	// Backfill missing team attributions using DemoInfo. Some demos have a
	// userinfo "name" that doesn't match the player's actual displayed
	// netname (KTX auth-override case): the chat parser pulls the displayed
	// name out of the print message but ctx.Players[slot].Name is still the
	// auth name, so the live lookup in handlePrint returns "". DemoInfo is
	// finalized before this analyzer, so by now we have the canonical
	// {displayed name -> team} mapping and can repair the gaps.
	if a.ctx.DemoInfo != nil {
		nameToTeam := make(map[string]string, len(a.ctx.DemoInfo.Players))
		normToTeam := make(map[string]string, len(a.ctx.DemoInfo.Players))
		for _, p := range a.ctx.DemoInfo.Players {
			if p.Name == "" || p.Team == "" {
				continue
			}
			nameToTeam[p.Name] = p.Team
			normToTeam[normalizePlayerName(p.Name)] = p.Team
		}
		for i := range a.events {
			ev := &a.events[i]
			if ev.Team != "" || ev.Player == "" {
				continue
			}
			if t := nameToTeam[ev.Player]; t != "" {
				ev.Team = t
				continue
			}
			if t := normToTeam[normalizePlayerName(ev.Player)]; t != "" {
				ev.Team = t
			}
		}
	}

	return &MessagesResult{
		Events: a.events,
	}, nil
}
