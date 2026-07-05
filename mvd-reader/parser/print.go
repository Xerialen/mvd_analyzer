package parser

import (
	"strings"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// PrintEvent is emitted when a print message is received. For prints
// wrapped in a `dem_single` MVD message, TargetPlayerNum identifies
// the player slot the server addressed (pickup messages, personal
// damage feedback, centerprint-equivalents). For broadcast prints
// (`dem_all`, `dem_multiple`, or `dem_read` in non-MVD streams) the
// field is -1 — no single target.
type PrintEvent struct {
	Level           int
	Message         string
	TargetPlayerNum int // 0-based slot for dem_single; -1 for broadcast prints
	Time            float64
}

func (e *PrintEvent) EventType() EventType { return EventPrint }
func (e *PrintEvent) EventTime() float64   { return e.Time }

// parsePrint parses svc_print message. `targetPlayerNum` is the
// dem_single slot from the MVD container (or -1 for non-dem_single
// wrappers); the caller in parser.go derives it from msg.Header.
//
// Aside from emitting PrintEvent (and any KTX pickup-print follow-up),
// the broadcast obituary path is mined here for DeathEvent: KTX's
// fragfile lines (X was rocketed by Y, etc.) are the only signal that
// fires when the server compresses a death/respawn cycle into a single
// svc_playerinfo gap — short enough that DF_DEAD never flips on the
// wire and the dem_stats block carrying the health drop is addressed
// to a different POV. maybeEmitDeath dedupes against the other two
// sources so we don't double-count when they do fire.
func (p *Parser) parsePrint(r *mvd.BufferReader, time float64, timeMs int32, targetPlayerNum int) error {
	level, err := r.ReadByte()
	if err != nil {
		return err
	}

	message, err := r.ReadString()
	if err != nil {
		return err
	}

	cleanedMessage := cleanString(message)

	if err := p.emit(&PrintEvent{
		Level:           int(level),
		Message:         cleanedMessage,
		TargetPlayerNum: targetPlayerNum,
		Time:            time,
	}); err != nil {
		return err
	}
	p.updateMatchStartedFromPrint(cleanedMessage)
	if err := p.tryEmitObituaryDeath(cleanedMessage, time, timeMs); err != nil {
		return err
	}
	return p.tryEmitPickupPrint(int(level), cleanedMessage, targetPlayerNum, time)
}

// tryEmitObituaryDeath inspects an obituary print line, resolves the
// named victim to a player slot via the userinfo table, and fires
// DeathEvent via forceEmitDeath (bypassing the
// skip-if-already-dead dedup). KTX is authoritative for whether a
// death happened: obits map 1:1 to "deaths++" on the server-side
// scoreboard, even in the pent-deflection corner case where the
// player's entity state never visibly leaves the previous dead
// interval. See forceEmitDeath's doc for the full rationale and
// the two scenarios (tight respawn cycles, dtTELE2 deflections).
//
// Gated on p.matchStarted: warmup-era obits (and the telefrag obits
// that fire at the *exact* wire time of the start print but earlier
// in the message order — see comment on Parser.matchStarted) are
// silenced so they cannot pre-seed the parser dedup state and starve
// the stat-based detector of its match-start emission. After the
// gate opens, the follow-up SpawnEvent arrives naturally on the
// player's next svc_playerinfo frame with DF_DEAD clear — the same
// state-transition the existing maybeEmitSpawn path detects.
func (p *Parser) tryEmitObituaryDeath(msg string, time float64, timeMs int32) error {
	if !p.matchStarted {
		return nil
	}
	victim, _ := FindObituaryVictim(msg)
	if victim == "" {
		return nil
	}
	slot := p.lookupSlotByName(victim)
	if slot < 0 {
		return nil
	}
	return p.forceEmitDeath(slot, time, timeMs)
}

// matchStartedPhrases mirrors the analyzer's MatchTimingDetector
// matchStartPatterns. Kept here as a small duplicate so the parser
// doesn't have to import the analytics package — the canonical list
// lives at mvd-analytics/analyzer/matchtiming.go; any addition there
// should be mirrored here so the obit corroborator's gate stays
// aligned with the analyzer's recording gate.
var matchStartedPhrases = []string{
	"match has begun",
	"match started",
	"fight!",
	"go!",
	"begins in 1",
	"game start",
}

// updateMatchStartedFromPrint flips p.matchStarted on the first
// observed match-start phrase (case-insensitive). Idempotent.
func (p *Parser) updateMatchStartedFromPrint(msg string) {
	if p.matchStarted {
		return
	}
	lower := strings.ToLower(msg)
	for _, phrase := range matchStartedPhrases {
		if strings.Contains(lower, phrase) {
			p.matchStarted = true
			return
		}
	}
}

// lookupSlotByName finds the player slot whose userinfo name matches
// the supplied display name. Returns -1 when no slot matches. Names
// are case-sensitive — KTX prints render the userinfo name verbatim so
// the obit line's name and the userinfo name are byte-identical.
func (p *Parser) lookupSlotByName(name string) int {
	for slot, info := range p.players {
		if info == nil {
			continue
		}
		if info.Name == name {
			return slot
		}
	}
	return -1
}
