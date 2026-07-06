package democache

import (
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// inflightEntry is the shared state for a single in-flight loadResult.
// Multiple goroutines racing the same SHA wait on done, then read the
// terminal triplet.
type inflightEntry struct {
	done   chan struct{}
	result *result.Result
	meta   CacheMeta
	err    error
}

// getOrCompute serialises concurrent requests for the same SHA: the
// first goroutine runs compute, subsequent goroutines wait and observe
// the same result. Once compute returns, the inflight entry is removed
// — the next request after that pays its own compute cost.
//
// A panicking compute (a parser bug on a malformed demo) is converted to
// an error rather than left to unwind: without the recover, the deferred
// cleanup would release every waiter with (nil, zero-meta, nil-err) — each
// then takes the success path and nil-derefs in its handler (F13). The
// panicking goroutine's own caller sees the same error. The stack is
// logged here because converting to an error hides it from
// recoverMiddleware.
func (c *Cache) getOrCompute(sha string, compute func() (*result.Result, CacheMeta, error)) (res *result.Result, meta CacheMeta, err error) {
	e := &inflightEntry{done: make(chan struct{})}
	actual, loaded := c.inflight.LoadOrStore(sha, e)
	if loaded {
		existing := actual.(*inflightEntry)
		<-existing.done
		return existing.result, existing.meta, existing.err
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("demo load panicked", "sha", sha, "panic", r, "stack", string(debug.Stack()))
			e.result, e.meta, e.err = nil, CacheMeta{}, fmt.Errorf("demo load panicked: %v", r)
		}
		// Named returns: on the panic path the function would otherwise
		// return zero values (nil error!) to the computing goroutine itself.
		res, meta, err = e.result, e.meta, e.err
		c.inflight.Delete(sha)
		close(e.done)
	}()
	e.result, e.meta, e.err = compute()
	return
}
