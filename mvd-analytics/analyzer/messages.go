package analyzer

import (
	"strings"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// MessagesAnalyzer captures frags and chat messages for timeline display
type MessagesAnalyzer struct {
	ctx    *Context
	core   *CoreOutputs
	events []MatchEvent
	seen   map[chatKey]struct{} // dedup for per-recipient chat copies
}

// chatKey identifies a distinct chat/teamsay line for deduplication.
type chatKey struct {
	time    int32
	typ     string
	player  string
	message string
}

// UseCoreOutputs is part of the CoreConsumer contract — Messages
// consumes co.Names during its Finalize to backfill team attribution
// on chat / obituary events whose live name lookup missed.
func (a *MessagesAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

// NewMessagesAnalyzer creates a new messages analyzer
func NewMessagesAnalyzer() *MessagesAnalyzer {
	return &MessagesAnalyzer{
		events: make([]MatchEvent, 0),
		seen:   make(map[chatKey]struct{}),
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
		if event != nil && !a.seenChat(event) {
			a.appendEvent(event)
		}
		return nil
	}

	// Try to parse as frag (levels 1-2 are typically obituaries)
	if e.Level <= 2 {
		frag := a.parseObituarySimple(msg, e.Time)
		if frag != nil {
			a.appendEvent(frag)
		}
	}

	return nil
}

// appendEvent stores a MatchEvent and fills MessageClean when it would
// differ from the raw Message. Frag obit descriptions are already
// plain text, so Clean == Message and we leave Clean empty to let the
// omitempty wire elision keep the payload small. Chat / teamsay text
// often carries ezQuake markup (color codes, sound triggers, macro
// delimiters) — there StripChatMarkup produces a plain-text twin.
func (a *MessagesAnalyzer) appendEvent(ev *MatchEvent) {
	if cleaned := events.StripChatMarkup(ev.Message); cleaned != ev.Message {
		ev.MessageClean = cleaned
	}
	a.events = append(a.events, *ev)
}

// seenChat reports whether an identical chat/teamsay line has already been
// recorded, registering it on first sight. KTX handles say/say_team in QC
// (ClientSay, ktx/src/g_cmd.c) and sprints the line to each eligible recipient
// individually; every G_sprint becomes a dem_single svc_print in the MVD
// (SV_ClientPrintf, mvdsv/src/sv_send.c), so the parser faithfully emits one
// PrintEvent per recipient. Public say reaches every client and duplicates the
// most; say_team only teammates. All copies share an identical wire-ms, so an
// exact (time, type, player, message) match is a safe duplicate — a human
// cannot send the same line twice in the same millisecond. This is the
// CLAUDE.md filter exception: a chat reader cannot itself tell N identical
// copies from N distinct messages. (Edge: KTX sends the colored text to
// colour-capable clients and a stripped copy to the rest — g_cmd.c:558 — so a
// mixed lobby can leave one colored + one stripped survivor. That is rare on
// modern ezquake and never drops a real message, so we accept it.) Frags are
// not routed here; obituaries arrive as a single broadcast copy and stay
// verbatim.
func (a *MessagesAnalyzer) seenChat(ev *MatchEvent) bool {
	k := chatKey{ev.Time, ev.Type, ev.Player, ev.Message}
	if _, ok := a.seen[k]; ok {
		return true
	}
	a.seen[k] = struct{}{}
	return false
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
				Time:    msTime(time),
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
				Time:    msTime(time),
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
		Time:    msTime(time),
		Type:    "chat",
		Player:  playerName,
		Team:    team,
		Message: chatText,
	}
}

// parseObituarySimple parses a print line as a timeline frag MatchEvent,
// mapping the shared neutral obituary parse (obituary_parse.go) — the same
// table and order the frag analyzer uses. Phrasing-based teamkills keep the
// generic "teammate" placeholder for the party the obituary didn't name;
// every other kind needs both parties to resolve to real names (MatchEvent
// carries no IsTeamKill flag, so a membership test isn't needed here).
func (a *MessagesAnalyzer) parseObituarySimple(msg string, time float64) *MatchEvent {
	o := parseObituaryLine(msg)
	if o == nil {
		return nil
	}

	if o.TeamKill {
		// One party is the generic "teammate". Emit around the KNOWN party.
		if isGenericPlayer(o.Victim) {
			// Killer-named ("X loses another friend").
			if o.Killer == "" || isGenericPlayer(o.Killer) {
				return nil
			}
			return &MatchEvent{
				Time: msTime(time), Type: "frag",
				Player: o.Killer, Team: a.getPlayerTeam(o.Killer),
				Message: msg, Victim: o.Victim, Weapon: o.Weapon,
			}
		}
		// Victim-named ("X was telefragged by his teammate").
		if o.Victim == "" || isGenericPlayer(o.Victim) {
			return nil
		}
		return &MatchEvent{
			Time: msTime(time), Type: "frag",
			Player: o.Killer, Team: a.getPlayerTeam(o.Victim),
			Message: msg, Victim: o.Victim, Weapon: o.Weapon,
		}
	}

	// Regular kill / suicide — both parties must resolve to real names.
	if o.Killer == "" || o.Victim == "" || isGenericPlayer(o.Killer) || isGenericPlayer(o.Victim) {
		return nil
	}
	return &MatchEvent{
		Time: msTime(time), Type: "frag",
		Player: o.Killer, Team: a.getPlayerTeam(o.Killer),
		Message: msg, Victim: o.Victim, Weapon: o.Weapon,
	}
}

// getPlayerTeam returns the team name for a player using fuzzy lookup.
func (a *MessagesAnalyzer) getPlayerTeam(name string) string {
	if p := findPlayerByName(a.ctx.Players, name); p != nil {
		return p.Team
	}
	return ""
}

func (a *MessagesAnalyzer) Finalize(result *Result) error {
	// Backfill missing team attributions using DemoInfo. Some demos have a
	// userinfo "name" that doesn't match the player's actual displayed
	// netname (KTX auth-override case): the chat parser pulls the displayed
	// name out of the print message but ctx.Players[slot].Name is still the
	// auth name, so the live lookup in handlePrint returns "". DemoInfo is
	// finalized before this analyzer, so by now we have the canonical
	// {displayed name -> team} mapping and can repair the gaps.
	if a.core != nil && a.core.DemoInfo != nil {
		names := a.core.Names
		for i := range a.events {
			ev := &a.events[i]
			if ev.Team != "" || ev.Player == "" {
				continue
			}
			if t := names.TeamForName(ev.Player); t != "" {
				ev.Team = t
			}
		}
	}

	result.Messages = &MessagesResult{
		Events: a.events,
	}

	// Born-correct timestamps: rebase chat/message times to the match clock.
	if ms := a.core.MatchStartMs(); ms > 0 {
		for i := range result.Messages.Events {
			result.Messages.Events[i].Time -= ms
		}
	}
	return nil
}
