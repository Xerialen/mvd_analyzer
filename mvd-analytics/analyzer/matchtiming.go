package analyzer

import (
	"strings"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// MatchTimingDetector is the canonical match-boundary state machine
// for analyzers that need to know whether the match is currently
// running. Every analyzer that previously maintained its own
// matchStarted / matchEnded flags + per-file keyword list now embeds
// one of these so the keyword sets cannot drift apart.
//
// Pattern coverage is the union of the lists previously hard-coded
// across match.go, timeline.go, backpacks.go, items.go, weapon_pickups.go,
// and metadata.go. Matches are case-insensitive — the previous match.go
// did ToLower while every other site compared raw, which produced
// "fight!" vs "Fight!" coverage gaps that depended on which server
// printed the line.
//
// The "ended only after started" invariant is preserved: end keywords
// are ignored until a start has been seen. This matches what 4 of the 5
// callers were already doing.
type MatchTimingDetector struct {
	Started   bool
	Ended     bool
	StartTime float64
	EndTime   float64
}

var matchStartPatterns = []string{
	"match has begun",
	"match started",
	"fight!",
	"go!",
	"begins in 1",
	"game start",
}

var matchEndPatterns = []string{
	"match is over",
	"match ended",
	"match complete",
	"game over",
	"timelimit hit",
	"fraglimit hit",
}

// OnPrint feeds a print event into the detector. Idempotent: a second
// matching start (or end) print is ignored.
func (d *MatchTimingDetector) OnPrint(e *events.PrintEvent) {
	// Only broadcast prints (bprint) start or end a match. KTX emits every
	// match-boundary line at PRINT_MEDIUM/PRINT_HIGH (level <= 2); PRINT_CHAT
	// (level 3) is player say/say_team, which must never flip Started/Ended
	// — otherwise a pre-match "go go go!" or a mid-match "gg game over" chat
	// line would start recording warmup or freeze every stream for the rest
	// of the demo. (ktx/include/g_consts.h: PRINT_MEDIUM=1 death, PRINT_CHAT=3.)
	if e.Level == events.PrintChat {
		return
	}
	msg := strings.ToLower(e.Message)
	if !d.Started {
		for _, p := range matchStartPatterns {
			if strings.Contains(msg, p) {
				d.Started = true
				d.StartTime = e.Time
				return
			}
		}
		return
	}
	if d.Ended {
		return
	}
	for _, p := range matchEndPatterns {
		if strings.Contains(msg, p) {
			d.Ended = true
			d.EndTime = e.Time
			return
		}
	}
}

// OnIntermission marks the match as ended when the server fires
// svc_intermission. KTX emits this on timelimit/fraglimit hit even when
// there is no matching bprint string, so it is a more reliable end
// signal than print-keyword scanning alone.
func (d *MatchTimingDetector) OnIntermission(t float64) {
	if d.Started && !d.Ended {
		d.Ended = true
		d.EndTime = t
	}
}
