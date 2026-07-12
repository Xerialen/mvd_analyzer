package democache

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// F13: a panicking compute must release the computing goroutine AND any
// singleflight waiters with a real error — not the (nil result, zero meta,
// nil error) triplet that sends every caller down the success path into a
// nil-deref. The entry must also be cleaned up so the next request
// recomputes normally.
func TestGetOrComputePanicReleasesWaitersWithError(t *testing.T) {
	c := &Cache{}
	started := make(chan struct{})
	release := make(chan struct{})

	var wg sync.WaitGroup
	var err1, err2 error
	var res1, res2 *result.Result

	wg.Add(1)
	go func() {
		defer wg.Done()
		res1, _, err1 = c.getOrCompute("deadbeef", func() (*result.Result, CacheMeta, error) {
			close(started)
			<-release
			panic("parser bug")
		})
	}()
	<-started

	// Second caller: either it joins the in-flight entry (the interesting
	// path) or — if it loses the race with the cleanup — runs its own
	// compute, which also panics; both paths must yield a real error.
	wg.Add(1)
	go func() {
		defer wg.Done()
		res2, _, err2 = c.getOrCompute("deadbeef", func() (*result.Result, CacheMeta, error) {
			panic("parser bug (second)")
		})
	}()
	time.Sleep(10 * time.Millisecond) // let the waiter block on done
	close(release)
	wg.Wait()

	for i, got := range []struct {
		res *result.Result
		err error
	}{{res1, err1}, {res2, err2}} {
		if got.err == nil {
			t.Errorf("caller %d: err = nil after a panicking compute — waiters would nil-deref (F13)", i+1)
		} else if !strings.Contains(got.err.Error(), "panicked") {
			t.Errorf("caller %d: err = %v, want a demo-load-panicked error", i+1, got.err)
		}
		if got.res != nil {
			t.Errorf("caller %d: res non-nil alongside the panic error", i+1)
		}
	}

	// The inflight entry is gone: a fresh compute runs and succeeds.
	want := &result.Result{}
	res3, _, err3 := c.getOrCompute("deadbeef", func() (*result.Result, CacheMeta, error) {
		return want, CacheMeta{}, nil
	})
	if err3 != nil || res3 != want {
		t.Errorf("post-panic compute = (%v, %v), want the fresh result with nil error", res3, err3)
	}
}
