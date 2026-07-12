package mapbsp

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestLoadBytesCache verifies the one-entry memo: the same map name loads
// the underlying file once, a different name reloads (evicting the prior
// entry), and a "not found" result is cached too.
func TestLoadBytesCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.bsp"), []byte("ALPHA-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.bsp"), []byte("BETA-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}

	SetDir(dir) // also invalidates any prior cache entry
	defer SetDir("")
	loadCalls = 0

	a1 := LoadBytes("alpha")
	a2 := LoadBytes("alpha")
	if loadCalls != 1 {
		t.Errorf("same name twice: loadCalls=%d, want 1 (cache hit expected)", loadCalls)
	}
	if string(a1) != "ALPHA-BYTES" || string(a2) != "ALPHA-BYTES" {
		t.Errorf("alpha bytes = %q / %q, want ALPHA-BYTES", a1, a2)
	}
	if len(a1) > 0 && &a1[0] != &a2[0] {
		t.Errorf("cache returned a different backing slice on hit")
	}

	// Different name -> reload, evicting alpha.
	if b := LoadBytes("beta"); string(b) != "BETA-BYTES" {
		t.Errorf("beta bytes = %q, want BETA-BYTES", b)
	}
	if loadCalls != 2 {
		t.Errorf("different name: loadCalls=%d, want 2 (reload expected)", loadCalls)
	}

	// alpha was evicted -> reloads.
	if a := LoadBytes("alpha"); string(a) != "ALPHA-BYTES" {
		t.Errorf("alpha reload = %q, want ALPHA-BYTES", a)
	}
	if loadCalls != 3 {
		t.Errorf("evicted name: loadCalls=%d, want 3", loadCalls)
	}

	// A missing map caches its nil result too: two calls, one underlying load.
	loadCalls = 0
	if g := LoadBytes("ghost"); g != nil {
		t.Errorf("missing map = %v, want nil", g)
	}
	if g := LoadBytes("ghost"); g != nil {
		t.Errorf("missing map (2nd) = %v, want nil", g)
	}
	if loadCalls != 1 {
		t.Errorf("missing map cached: loadCalls=%d, want 1", loadCalls)
	}
}

// TestLoadBytesConcurrent exercises the cache under -race: concurrent
// loads of two maps interleaved with SetDir invalidations. Correctness
// bar: any returned bytes must match the requested name (no torn pairs,
// no cross-key value), and after the final SetDir an in-flight old-dir
// load must not have re-populated the cache (generation guard).
func TestLoadBytesConcurrent(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	for _, d := range []string{dirA, dirB} {
		tag := filepath.Base(d)
		if err := os.WriteFile(filepath.Join(d, "alpha.bsp"), []byte("ALPHA-"+tag), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "beta.bsp"), []byte("BETA-"+tag), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	SetDir(dirA)
	defer SetDir("")

	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		name, prefix := "alpha", "ALPHA-"
		if i%2 == 1 {
			name, prefix = "beta", "BETA-"
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
				if data := LoadBytes(name); data != nil && !strings.HasPrefix(string(data), prefix) {
					t.Errorf("LoadBytes(%q) returned bytes for another map: %q", name, data)
					return
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		if i%2 == 0 {
			SetDir(dirB)
		} else {
			SetDir(dirA)
		}
	}
	close(done)
	wg.Wait()

	// After the last SetDir(dirA) any surviving cache entry must be
	// dir-A bytes: a racing old-dir load is discarded by the gen guard.
	if data := LoadBytes("alpha"); !strings.HasSuffix(string(data), filepath.Base(dirA)) {
		t.Errorf("post-invalidation alpha = %q, want dir-A bytes", data)
	}
}
