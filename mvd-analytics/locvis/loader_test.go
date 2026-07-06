package locvis

import (
	"sync"
	"testing"
)

// TestFinderCache verifies the one-entry Finder memo returns the same
// *Finder for repeated loads of one map, rebuilds after eviction by a
// different map, and is dropped by SetBspDir. Uses the embedded loc
// corpus only (no BSP required), so it runs everywhere.
func TestFinderCache(t *testing.T) {
	invalidateFinderCache()
	defer invalidateFinderCache()

	f1, err := LoadForMap("dm6")
	if err != nil {
		t.Fatalf("LoadForMap(dm6): %v", err)
	}
	f2, err := LoadForMap("dm6")
	if err != nil {
		t.Fatalf("LoadForMap(dm6) again: %v", err)
	}
	if f1 != f2 {
		t.Errorf("repeated load returned a different *Finder (cache miss)")
	}

	// A different map evicts dm6.
	if _, err := LoadForMap("dm2"); err != nil {
		t.Fatalf("LoadForMap(dm2): %v", err)
	}
	f3, err := LoadForMap("dm6")
	if err != nil {
		t.Fatalf("LoadForMap(dm6) after eviction: %v", err)
	}
	if f3 == f1 {
		t.Errorf("dm6 should have been rebuilt after dm2 evicted it")
	}

	// SetBspDir drops the memoised Finder.
	f4, _ := LoadForMap("dm6")
	SetBspDir("")
	f5, err := LoadForMap("dm6")
	if err != nil {
		t.Fatalf("LoadForMap(dm6) after SetBspDir: %v", err)
	}
	if f5 == f4 {
		t.Errorf("SetBspDir should have invalidated the Finder cache")
	}
}

// TestFinderCacheConcurrent exercises the Finder memo under -race:
// concurrent loads of two maps interleaved with SetBspDir invalidations.
// Correctness bar: every load errors or returns a Finder whose loc table
// matches the requested map (no cross-key value), with no data race.
func TestFinderCacheConcurrent(t *testing.T) {
	invalidateFinderCache()
	defer invalidateFinderCache()

	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		name := "dm6"
		if i%2 == 1 {
			name = "dm2"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				f, err := LoadForMap(name)
				if err != nil {
					t.Errorf("LoadForMap(%s): %v", name, err)
					return
				}
				if got := f.MapName(); got != name {
					t.Errorf("LoadForMap(%s) returned finder for %q", name, got)
					return
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		SetBspDir("")
	}
	close(done)
	wg.Wait()
}
