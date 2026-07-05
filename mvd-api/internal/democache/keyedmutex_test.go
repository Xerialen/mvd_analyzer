package democache

import (
	"sync"
	"testing"
	"time"
)

// TestKeyedMutex_DifferentKeysDoNotBlock is the F8 property: holding one
// key must not stall another. (Demo B's /los must not queue behind A's.)
func TestKeyedMutex_DifferentKeysDoNotBlock(t *testing.T) {
	var k KeyedMutex
	unlockA := k.Lock("a")
	defer unlockA()

	done := make(chan struct{})
	go func() {
		unlockB := k.Lock("b")
		unlockB()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Lock(b) blocked behind Lock(a) — keys are not independent")
	}
}

// TestKeyedMutex_SameKeySerializes: holders of the same key never overlap.
func TestKeyedMutex_SameKeySerializes(t *testing.T) {
	var k KeyedMutex
	var mu sync.Mutex
	held, maxHeld := 0, 0

	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			unlock := k.Lock("x")
			defer unlock()
			mu.Lock()
			held++
			if held > maxHeld {
				maxHeld = held
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			held--
			mu.Unlock()
		}()
	}
	wg.Wait()
	if maxHeld != 1 {
		t.Errorf("same-key holders overlapped: maxHeld=%d; want 1", maxHeld)
	}
}

// TestKeyedMutex_ReclaimsEntries: balanced lock/unlock leaves no residue,
// so a long-lived KeyedMutex does not grow one entry per key ever seen.
func TestKeyedMutex_ReclaimsEntries(t *testing.T) {
	var k KeyedMutex
	for i := 0; i < 100; i++ {
		unlock := k.Lock("k")
		unlock()
	}
	k.mu.Lock()
	n := len(k.locks)
	k.mu.Unlock()
	if n != 0 {
		t.Errorf("KeyedMutex retained %d entries after balanced lock/unlock; want 0", n)
	}
}
