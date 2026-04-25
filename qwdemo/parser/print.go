package parser

import (
	"github.com/mvd-analyzer/qwdemo/mvd"
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
func (p *Parser) parsePrint(r *mvd.BufferReader, time float64, targetPlayerNum int) error {
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
	return p.tryEmitPickupPrint(int(level), cleanedMessage, targetPlayerNum, time)
}
