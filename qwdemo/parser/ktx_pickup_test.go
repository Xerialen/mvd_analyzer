package parser

import "testing"

func TestTryEmitItemPickupHint_Armor(t *testing.T) {
	p := NewParser(nil)

	var captured *ItemPickupHintEvent
	p.OnEvent(func(e Event) error {
		if h, ok := e.(*ItemPickupHintEvent); ok {
			captured = h
		}
		return nil
	})

	if err := p.tryEmitItemPickupHint("//ktx took 17 20 5\n", 12.5); err != nil {
		t.Fatalf("tryEmitItemPickupHint: %v", err)
	}
	if captured == nil {
		t.Fatal("no hint event emitted")
	}
	if captured.ItemEnt != 17 || captured.RespawnSec != 20 || captured.PlayerEnt != 5 {
		t.Errorf("hint = %+v, want {17, 20, 5}", captured)
	}
	if captured.Time != 12.5 {
		t.Errorf("Time = %f, want 12.5", captured.Time)
	}
}

func TestTryEmitItemPickupHint_MegahealthRespawnZero(t *testing.T) {
	p := NewParser(nil)

	var captured *ItemPickupHintEvent
	p.OnEvent(func(e Event) error {
		if h, ok := e.(*ItemPickupHintEvent); ok {
			captured = h
		}
		return nil
	})

	// MH passes respawn_sec = 0 at ktx/src/items.c:355 — the 20 s
	// timer doesn't arm until rot completes.
	if err := p.tryEmitItemPickupHint("//ktx took 31 0 3\n", 45.0); err != nil {
		t.Fatalf("tryEmitItemPickupHint: %v", err)
	}
	if captured == nil || captured.RespawnSec != 0 {
		t.Errorf("MH hint: want RespawnSec=0, got %+v", captured)
	}
}

func TestTryEmitItemPickupHint_PowerupLongRespawn(t *testing.T) {
	p := NewParser(nil)

	var captured *ItemPickupHintEvent
	p.OnEvent(func(e Event) error {
		if h, ok := e.(*ItemPickupHintEvent); ok {
			captured = h
		}
		return nil
	})

	// Quad pickup in a 10-minute mode: respawn_sec = 300.
	if err := p.tryEmitItemPickupHint("//ktx took 42 300 2", 0); err != nil {
		t.Fatalf("tryEmitItemPickupHint: %v", err)
	}
	if captured == nil || captured.RespawnSec != 300 {
		t.Errorf("Quad hint: want RespawnSec=300, got %+v", captured)
	}
}

func TestTryEmitItemPickupHint_DropDoesNotEmit(t *testing.T) {
	p := NewParser(nil)

	emitted := 0
	p.OnEvent(func(e Event) error {
		if _, ok := e.(*ItemPickupHintEvent); ok {
			emitted++
		}
		return nil
	})

	if err := p.tryEmitItemPickupHint("//ktx drop 142 32 5\n", 0); err != nil {
		t.Fatalf("tryEmitItemPickupHint: %v", err)
	}
	if emitted != 0 {
		t.Errorf("got %d hint events, want 0 (//ktx drop is not //ktx took)", emitted)
	}
}

func TestTryEmitItemPickupHint_MalformedSilentlyDropped(t *testing.T) {
	p := NewParser(nil)

	emitted := 0
	p.OnEvent(func(e Event) error {
		emitted++
		return nil
	})

	for _, cmd := range []string{
		"//ktx took garbage",
		"//ktx took 1",
		"//ktx took 1 2",
		"//ktx took 1 abc 3",
		"//ktx took 1 2 def",
	} {
		if err := p.tryEmitItemPickupHint(cmd, 0); err != nil {
			t.Errorf("malformed %q returned error %v", cmd, err)
		}
	}
	if emitted != 0 {
		t.Errorf("got %d events, want 0 (all malformed inputs)", emitted)
	}
}

func TestTryEmitBackpackPickupHint_RL(t *testing.T) {
	p := NewParser(nil)

	var captured *BackpackPickupHintEvent
	p.OnEvent(func(e Event) error {
		if h, ok := e.(*BackpackPickupHintEvent); ok {
			captured = h
		}
		return nil
	})

	if err := p.tryEmitBackpackPickupHint("//ktx bp 142 3\n", 7.25); err != nil {
		t.Fatalf("tryEmitBackpackPickupHint: %v", err)
	}
	if captured == nil {
		t.Fatal("no hint event emitted")
	}
	if captured.BackpackEnt != 142 || captured.PlayerEnt != 3 {
		t.Errorf("hint = %+v, want {142, 3}", captured)
	}
	if captured.Time != 7.25 {
		t.Errorf("Time = %f, want 7.25", captured.Time)
	}
}

func TestTryEmitBackpackPickupHint_DropDoesNotEmit(t *testing.T) {
	p := NewParser(nil)

	emitted := 0
	p.OnEvent(func(e Event) error {
		if _, ok := e.(*BackpackPickupHintEvent); ok {
			emitted++
		}
		return nil
	})

	if err := p.tryEmitBackpackPickupHint("//ktx drop 142 32 5\n", 0); err != nil {
		t.Fatalf("tryEmitBackpackPickupHint: %v", err)
	}
	if emitted != 0 {
		t.Errorf("got %d hint events, want 0 (//ktx drop is not //ktx bp)", emitted)
	}
}

func TestTryEmitBackpackPickupHint_TookDoesNotEmit(t *testing.T) {
	p := NewParser(nil)

	emitted := 0
	p.OnEvent(func(e Event) error {
		if _, ok := e.(*BackpackPickupHintEvent); ok {
			emitted++
		}
		return nil
	})

	if err := p.tryEmitBackpackPickupHint("//ktx took 17 20 5\n", 0); err != nil {
		t.Fatalf("tryEmitBackpackPickupHint: %v", err)
	}
	if emitted != 0 {
		t.Errorf("got %d hint events, want 0 (//ktx took is not //ktx bp)", emitted)
	}
}

func TestTryEmitBackpackPickupHint_MalformedSilentlyDropped(t *testing.T) {
	p := NewParser(nil)

	emitted := 0
	p.OnEvent(func(e Event) error {
		emitted++
		return nil
	})

	for _, cmd := range []string{
		"//ktx bp",
		"//ktx bp 1",
		"//ktx bp abc 3",
		"//ktx bp 1 def",
	} {
		if err := p.tryEmitBackpackPickupHint(cmd, 0); err != nil {
			t.Errorf("malformed %q returned error %v", cmd, err)
		}
	}
	if emitted != 0 {
		t.Errorf("got %d events, want 0 (all malformed inputs)", emitted)
	}
}
