package parser

import (
	"testing"

	"github.com/mvd-analyzer/qwdemo/mvd"
)

func TestTryEmitPickupPrint_Armor(t *testing.T) {
	cases := []struct{ msg, kind string }{
		{"You got the Green Armor\n", "ga"},
		{"You got the Yellow Armor\n", "ya"},
		{"You got the Red Armor\n", "ra"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			p := NewParser(nil)
			var captured *ItemPickupPrintEvent
			p.OnEvent(func(e Event) error {
				if h, ok := e.(*ItemPickupPrintEvent); ok {
					captured = h
				}
				return nil
			})
			if err := p.tryEmitPickupPrint(mvd.PrintLow, tc.msg, 3, 42.5); err != nil {
				t.Fatalf("tryEmitPickupPrint: %v", err)
			}
			if captured == nil {
				t.Fatalf("no event for %q", tc.msg)
			}
			if captured.Kind != tc.kind || captured.PlayerNum != 3 || captured.Time != 42.5 {
				t.Errorf("got %+v, want {PlayerNum=3, Kind=%q, Time=42.5}", captured, tc.kind)
			}
		})
	}
}

func TestTryEmitPickupPrint_Weapons(t *testing.T) {
	cases := []struct{ msg, kind string }{
		{"You got the Rocket Launcher\n", "rl"},
		{"You got the Thunderbolt\n", "lg"},
		{"You got the Grenade Launcher\n", "gl"},
		{"You got the Super Nailgun\n", "sng"},
		{"You got the Double-barrelled Shotgun\n", "ssg"},
		{"You got the nailgun\n", "ng"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			p := NewParser(nil)
			var captured *ItemPickupPrintEvent
			p.OnEvent(func(e Event) error {
				if h, ok := e.(*ItemPickupPrintEvent); ok {
					captured = h
				}
				return nil
			})
			if err := p.tryEmitPickupPrint(mvd.PrintLow, tc.msg, 5, 10); err != nil {
				t.Fatalf("tryEmitPickupPrint: %v", err)
			}
			if captured == nil || captured.Kind != tc.kind {
				t.Errorf("%q: got %+v, want kind=%q", tc.msg, captured, tc.kind)
			}
		})
	}
}

func TestTryEmitPickupPrint_AmmoBoxes(t *testing.T) {
	// KTX emits "You got the shells / nails / rockets / cells"; //ktx
	// took does NOT fire for ammo boxes (ammo_touch has no stuffcmd
	// call at ktx/src/items.c:1171), so this print is the authoritative
	// attribution signal for ammo pickups.
	cases := []struct{ msg, kind string }{
		{"You got the shells\n", "shells"},
		{"You got the nails\n", "nails"},
		{"You got the spikes\n", "nails"}, // old_style variant at items.c:1391
		{"You got the rockets\n", "rockets"},
		{"You got the cells\n", "cells"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			p := NewParser(nil)
			var captured *ItemPickupPrintEvent
			p.OnEvent(func(e Event) error {
				if h, ok := e.(*ItemPickupPrintEvent); ok {
					captured = h
				}
				return nil
			})
			if err := p.tryEmitPickupPrint(mvd.PrintLow, tc.msg, 1, 0); err != nil {
				t.Fatalf("tryEmitPickupPrint: %v", err)
			}
			if captured == nil || captured.Kind != tc.kind {
				t.Errorf("%q: got %+v, want kind=%q", tc.msg, captured, tc.kind)
			}
		})
	}
}

func TestTryEmitPickupPrint_Powerups(t *testing.T) {
	cases := []struct{ msg, kind string }{
		{"You got the Quad Damage\n", "quad"},
		{"You got the OctaPower\n", "quad"},
		{"You got the Pentagram of Protection\n", "pent"},
		{"You got the Ring of Shadows\n", "ring"},
		{"You got the Biosuit\n", "suit"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			p := NewParser(nil)
			var captured *ItemPickupPrintEvent
			p.OnEvent(func(e Event) error {
				if h, ok := e.(*ItemPickupPrintEvent); ok {
					captured = h
				}
				return nil
			})
			if err := p.tryEmitPickupPrint(mvd.PrintLow, tc.msg, 0, 0); err != nil {
				t.Fatalf("tryEmitPickupPrint: %v", err)
			}
			if captured == nil || captured.Kind != tc.kind {
				t.Errorf("%q: got %+v, want kind=%q", tc.msg, captured, tc.kind)
			}
		})
	}
}

func TestTryEmitPickupPrint_Healths(t *testing.T) {
	// "You receive N health" — ktx/src/items.c:337 fires before the
	// healtype==2 branch so *every* health pickup emits it.
	cases := []struct {
		msg  string
		kind string
	}{
		{"You receive 15 health\n", "h15"},
		{"You receive 25 health\n", "h25"},
		{"You receive 100 health\n", "mh"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			p := NewParser(nil)
			var captured *ItemPickupPrintEvent
			p.OnEvent(func(e Event) error {
				if h, ok := e.(*ItemPickupPrintEvent); ok {
					captured = h
				}
				return nil
			})
			if err := p.tryEmitPickupPrint(mvd.PrintLow, tc.msg, 2, 11.1); err != nil {
				t.Fatalf("tryEmitPickupPrint: %v", err)
			}
			if captured == nil || captured.Kind != tc.kind {
				t.Errorf("%q: got %+v, want kind=%q", tc.msg, captured, tc.kind)
			}
		})
	}
}

func TestTryEmitPickupPrint_HealthNonStandardAmount(t *testing.T) {
	p := NewParser(nil)
	emitted := 0
	p.OnEvent(func(e Event) error {
		if _, ok := e.(*ItemPickupPrintEvent); ok {
			emitted++
		}
		return nil
	})
	// KTX never prints "You receive 50 health" — the only item-driven
	// values are 15 / 25 / 100. A bogus amount should not match.
	if err := p.tryEmitPickupPrint(mvd.PrintLow, "You receive 50 health\n", 2, 0); err != nil {
		t.Fatalf("tryEmitPickupPrint: %v", err)
	}
	if emitted != 0 {
		t.Errorf("unexpected emit for non-standard health amount")
	}
}

func TestTryEmitPickupPrint_Backpack(t *testing.T) {
	p := NewParser(nil)
	var captured *BackpackPickupPrintEvent
	p.OnEvent(func(e Event) error {
		if h, ok := e.(*BackpackPickupPrintEvent); ok {
			captured = h
		}
		return nil
	})
	// The opener arrives as its own svc_print; subsequent per-piece
	// prints ("the Rocket Launcher", ", 25 rockets") are separate and
	// not matched here by design.
	if err := p.tryEmitPickupPrint(mvd.PrintLow, "You get ", 4, 3.14); err != nil {
		t.Fatalf("tryEmitPickupPrint: %v", err)
	}
	if captured == nil {
		t.Fatal("no backpack pickup event")
	}
	if captured.PlayerNum != 4 || captured.Time != 3.14 {
		t.Errorf("got %+v, want {PlayerNum=4, Time=3.14}", captured)
	}
}

func TestTryEmitPickupPrint_BroadcastIgnored(t *testing.T) {
	// targetPlayerNum = -1 means dem_all / dem_multiple — broadcast
	// prints never carry authoritative attribution.
	p := NewParser(nil)
	emitted := 0
	p.OnEvent(func(e Event) error {
		if _, ok := e.(*ItemPickupPrintEvent); ok {
			emitted++
		}
		return nil
	})
	if err := p.tryEmitPickupPrint(mvd.PrintLow, "You got the Red Armor\n", -1, 0); err != nil {
		t.Fatalf("tryEmitPickupPrint: %v", err)
	}
	if emitted != 0 {
		t.Errorf("broadcast print should not emit a pickup event")
	}
}

func TestTryEmitPickupPrint_ChatIgnored(t *testing.T) {
	// A player typing "You got the Red Armor" into team chat must not
	// be mis-attributed as a pickup. Chat is PrintChat (3), pickups
	// are PrintLow (0).
	p := NewParser(nil)
	emitted := 0
	p.OnEvent(func(e Event) error {
		if _, ok := e.(*ItemPickupPrintEvent); ok {
			emitted++
		}
		return nil
	})
	if err := p.tryEmitPickupPrint(mvd.PrintChat, "You got the Red Armor\n", 3, 0); err != nil {
		t.Fatalf("tryEmitPickupPrint: %v", err)
	}
	if emitted != 0 {
		t.Errorf("chat-level print should not emit a pickup event")
	}
}

func TestTryEmitPickupPrint_UnknownItem(t *testing.T) {
	// Runes, keys, and anything else not in the kind table is
	// silently skipped — no false-positive events.
	p := NewParser(nil)
	emitted := 0
	p.OnEvent(func(e Event) error {
		if _, ok := e.(*ItemPickupPrintEvent); ok {
			emitted++
		}
		return nil
	})
	for _, msg := range []string{
		"You got the silver key\n",
		"You got the gold runekey\n",
		"You got the MegaNotARealItem\n",
	} {
		if err := p.tryEmitPickupPrint(mvd.PrintLow, msg, 1, 0); err != nil {
			t.Fatalf("tryEmitPickupPrint: %v", err)
		}
	}
	if emitted != 0 {
		t.Errorf("unknown items should not emit a pickup event (got %d)", emitted)
	}
}

func TestTryEmitPickupPrint_BackpackContentsNotBackpackEvent(t *testing.T) {
	// A stray ", 25 rockets" or "the Rocket Launcher" that arrives as
	// a continuation of the backpack sequence must NOT be treated as
	// a fresh backpack pickup — only the literal "You get " opener is.
	p := NewParser(nil)
	emitted := 0
	p.OnEvent(func(e Event) error {
		if _, ok := e.(*BackpackPickupPrintEvent); ok {
			emitted++
		}
		return nil
	})
	for _, msg := range []string{
		", 25 rockets",
		"the Rocket Launcher",
		"50 shells",
		"\n",
	} {
		if err := p.tryEmitPickupPrint(mvd.PrintLow, msg, 4, 0); err != nil {
			t.Fatalf("tryEmitPickupPrint: %v", err)
		}
	}
	if emitted != 0 {
		t.Errorf("continuation messages should not emit a backpack event (got %d)", emitted)
	}
}
