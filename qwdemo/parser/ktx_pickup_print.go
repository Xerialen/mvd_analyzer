package parser

import (
	"strconv"
	"strings"

	"github.com/mvd-analyzer/qwdemo/mvd"
)

// ItemPickupPrintEvent is the typed representation of KTX's per-client
// `svc_print` pickup messages — the "You got the Red Armor" / "You
// receive 25 health" / "You got the Rockets" strings that KTX's
// QuakeC emits via `G_sprint(other, PRINT_LOW, ...)` in items.c. The
// MVD container wraps these in `dem_single` with the picking player's
// slot in the header; that slot is PlayerNum here.
//
// Coverage — this event complements ItemPickupHintEvent (the //ktx
// took directive) rather than replacing it. The hint is structured
// (carries an ItemEnt for correlation and a nominal RespawnSec) but
// KTX does not emit it for every touch. This print-based event fires
// for every pickup KTX sends a personal message for, which is a
// broader set:
//
//   - Every type //ktx took covers: MH, GA/YA/RA, RL/LG/GL/SSG/SNG/NG,
//     Quad/Pent/Ring (powerups use mi_print broadcast for the *group*
//     announcement, but some paths also personal-print; treat this
//     event as supplementary there).
//   - **Ammo boxes** (shells / nails / rockets / cells) — `ammo_touch`
//     at ktx/src/items.c:1288 has no //ktx took call, so this is the
//     authoritative pickup-attribution signal for ammo boxes.
//   - **Small healths** (H15 / H25) — `health_touch` at items.c:337
//     fires "You receive %.0f health" for every health pickup; //ktx
//     took only fires for megahealth.
//
// Kind vocabulary matches ItemSpawnEvent.Kind ("ga", "ya", "ra",
// "h15", "h25", "mh", "ssg", "ng", "sng", "gl", "rl", "lg", "shells",
// "nails", "rockets", "cells", "quad", "pent", "ring", "suit").
//
// **Major caveat — per-client server-side filter.** `SV_ClientPrintf`
// in mvdsv (`mvdsv/src/sv_send.c:225`) drops the message before
// recording if `level < cl->messagelevel`. Pickup prints go at
// PRINT_LOW (0), so *any player who has set `msg 1` or higher in
// their client config will have zero pickup prints in the MVD for
// their own pickups*. Competitive QW players widely use `msg 2` to
// suppress pickup spam — which means on typical 4on4 / duel demos
// this signal is entirely absent. Coverage is partial and
// per-player; test each demo (count Level=0 PrintEvents) before
// relying on it. For universal coverage, use ItemPickupHintEvent /
// BackpackPickupHintEvent — the `//ktx took` / `//ktx bp` directives
// bypass this filter because they're STUFFCMD_DEMOONLY, not prints.
//
// KTX-specific wording — the netnames and phrasings here come from
// ktx/src/items.c and are what current KTX servers emit. ktpro and
// CustomTF use similar but not identical strings; extend the tables
// in this file to cover them if needed.
type ItemPickupPrintEvent struct {
	PlayerNum int    // 0-based slot (from dem_single header)
	Kind      string // see Kind vocabulary above
	Time      float64
}

func (e *ItemPickupPrintEvent) EventType() EventType { return EventItemPickupPrint }
func (e *ItemPickupPrintEvent) EventTime() float64   { return e.Time }

// BackpackPickupPrintEvent fires when KTX's backpack opener line
// `"You get "` (ktx/src/items.c:2404) is addressed to a specific
// player. Unlike BackpackPickupHintEvent (which only fires for RL or
// LG packs via the `//ktx bp` STUFFCMD_DEMOONLY directive), this
// event covers *every* backpack pickup when present — including
// SSG/NG/SNG/GL packs and ammo-only packs that have no //ktx bp at
// all. **Subject to the same PRINT_LOW server-side filter as
// ItemPickupPrintEvent** (see that doc comment); absent on demos
// where the picking players have `msg >= 1`.
//
// Contents are NOT parsed: the ammo breakdown arrives as subsequent
// separate svc_print messages (lines 2480-2618 in items.c issue one
// G_sprint per piece: "the Rocket Launcher", ", 25 rockets", etc.,
// then a trailing "\n") which would require stateful reassembly per
// player per tick. Consumers that need exact contents should
// correlate with per-player stats deltas (STAT_SHELLS / STAT_NAILS /
// STAT_ROCKETS / STAT_CELLS / STAT_ITEMS bitfield changes on the same
// tick), which the MVD already transports for every player.
type BackpackPickupPrintEvent struct {
	PlayerNum int // 0-based slot
	Time      float64
}

func (e *BackpackPickupPrintEvent) EventType() EventType { return EventBackpackPickupPrint }
func (e *BackpackPickupPrintEvent) EventTime() float64   { return e.Time }

// ktxNetnameToKind maps KTX item `netname` strings (the %s in
// `"You got the %s\n"`) to the Kind vocabulary. Source refs are
// ktx/src/items.c self->netname assignments.
var ktxNetnameToKind = map[string]string{
	// Armors (items.c:586, 600, 614)
	"Green Armor":  "ga",
	"Yellow Armor": "ya",
	"Red Armor":    "ra",

	// Weapons (items.c:1068, 1085, 1102, 1120, 1137, 1154)
	"Double-barrelled Shotgun": "ssg",
	"nailgun":                  "ng",
	"Super Nailgun":            "sng",
	"Grenade Launcher":         "gl",
	"Rocket Launcher":          "rl",
	"Thunderbolt":              "lg",

	// Ammo boxes (items.c:1362, 1391, 1418, 1445)
	"shells":  "shells",
	"nails":   "nails",
	"spikes":  "nails", // old_style variant, same item class
	"rockets": "rockets",
	"cells":   "cells",

	// Powerups (items.c:2239, 2267, 2289, 2317)
	"Pentagram of Protection": "pent",
	"Biosuit":                 "suit",
	"Ring of Shadows":         "ring",
	"Quad Damage":             "quad",
	"OctaPower":               "quad", // deathmatch 4 variant, same slot
}

const (
	ktxGotThePrefix  = "You got the "
	ktxReceivePrefix = "You receive "
	ktxBackpackOpen  = "You get "
)

// tryEmitPickupPrint matches a `dem_single`-targeted svc_print payload
// against the KTX pickup-print patterns and emits a typed pickup
// event on success. A missing target (targetPlayerNum == -1) or a
// non-PRINT_LOW level suppresses matching; chat, obituaries, and
// other broadcast prints flow past untouched.
func (p *Parser) tryEmitPickupPrint(level int, msg string, targetPlayerNum int, time float64) error {
	if targetPlayerNum < 0 {
		return nil
	}
	// Pickup prints fire via G_sprint(PRINT_LOW). Refuse to match at
	// any other level so chat (PrintChat) can't trigger false positives
	// even if a player types "You got the Red Armor" into team chat.
	if level != mvd.PrintLow {
		return nil
	}

	trimmed := strings.TrimRight(msg, "\n\r")

	// "You got the <netname>" — items.c:568, 970, 1288, 1542, 2049.
	if rest, ok := strings.CutPrefix(trimmed, ktxGotThePrefix); ok {
		kind, known := ktxNetnameToKind[rest]
		if !known {
			return nil
		}
		return p.emit(&ItemPickupPrintEvent{
			PlayerNum: targetPlayerNum,
			Kind:      kind,
			Time:      time,
		})
	}

	// "You receive N health" — items.c:337. Fires for every health
	// type (H15 / H25 / MH). The number distinguishes them.
	if rest, ok := strings.CutPrefix(trimmed, ktxReceivePrefix); ok {
		kind := healthMessageKind(rest)
		if kind == "" {
			return nil
		}
		return p.emit(&ItemPickupPrintEvent{
			PlayerNum: targetPlayerNum,
			Kind:      kind,
			Time:      time,
		})
	}

	// "You get " — backpack opener (items.c:2404). Subsequent
	// per-piece prints ("the Rocket Launcher", ", 25 rockets") are
	// separate svc_print messages and deliberately ignored; see the
	// BackpackPickupPrintEvent doc comment.
	if strings.HasPrefix(msg, ktxBackpackOpen) && !strings.HasPrefix(trimmed, ktxGotThePrefix) {
		return p.emit(&BackpackPickupPrintEvent{
			PlayerNum: targetPlayerNum,
			Time:      time,
		})
	}

	return nil
}

// healthMessageKind parses the numeric heal amount out of the "You
// receive <N> health" tail and maps it to H15 / H25 / MH. Returns
// "" for unrecognised amounts (which means the message wasn't a
// health pickup at all — KTX never prints non-standard values).
func healthMessageKind(rest string) string {
	idx := strings.IndexByte(rest, ' ')
	if idx <= 0 {
		return ""
	}
	amount, err := strconv.Atoi(rest[:idx])
	if err != nil {
		return ""
	}
	switch amount {
	case 15:
		return "h15"
	case 25:
		return "h25"
	case 100:
		return "mh"
	default:
		return ""
	}
}
