package democache

import "sync"

// KeyedMutex hands out one mutex per string key, so work on independent
// keys never blocks. It is the shared primitive behind the per-demo lazy
// computes (LOS in the API server, the shot-stream rebuild in the cache):
// a request for demo B must not queue behind demo A's multi-second pass.
//
// Entries are reference-counted and reclaimed when the last holder
// unlocks, so a long-lived KeyedMutex does not accumulate one map entry
// per key ever seen. The zero value is ready to use; do not copy after
// first use (it holds sync.Mutex state) — hold it via a pointer-received
// struct field.
type KeyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu      sync.Mutex
	waiters int
}

// Lock acquires the mutex for key and returns the function that releases
// it. Callers must invoke the returned unlock exactly once (typically via
// defer). Two goroutines holding different keys proceed concurrently;
// two on the same key serialize.
func (k *KeyedMutex) Lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*keyedLock)
	}
	e := k.locks[key]
	if e == nil {
		e = &keyedLock{}
		k.locks[key] = e
	}
	e.waiters++
	k.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.waiters--
		if e.waiters == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
