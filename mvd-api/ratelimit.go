package main

import (
	"math"
	"sync"
	"time"
)

// tokenBucket is a lazy (no background goroutine) token bucket. Tokens refill
// continuously at `rate` per second up to `burst`; each admitted request costs
// one token. It is kept in mvd-api rather than pulling in golang.org/x/time/rate
// so the module stays dependency-free (its only requires are the sibling
// workspace modules) — the algorithm is ~40 lines and needs no eviction at the
// tens-of-keys scale this store operates at (PLAN-hosting D8).
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64 // bucket capacity
	tokens float64
	last   time.Time
}

func newTokenBucket(ratePerSec float64, burst int) *tokenBucket {
	return &tokenBucket{
		rate:   ratePerSec,
		burst:  float64(burst),
		tokens: float64(burst), // start full so a fresh key isn't immediately throttled
		last:   time.Now(),
	}
}

// allow refills for elapsed time and, if a whole token is available, spends one
// and returns (true, 0). Otherwise it returns (false, retryAfter) where
// retryAfter is how long until one token accrues. now is injected so tests are
// deterministic; production callers pass time.Now().
func (b *tokenBucket) allow(now time.Time) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(b.burst, b.tokens+elapsed*b.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	if b.rate <= 0 {
		// A zero/negative rate never refills; report a nominal 1s backoff.
		return false, time.Second
	}
	need := 1 - b.tokens
	return false, time.Duration(need / b.rate * float64(time.Second))
}

// rateClass bundles the per-key limits for one traffic class.
type rateClass struct {
	rate  float64
	burst int
}

// keyLimiter holds one token bucket per key hash, split into user and service
// classes. Buckets are created lazily on first sight of an *authenticated* key,
// so unknown keys (rejected with 401 before the limiter runs) never allocate —
// the map cannot be grown by unauthenticated traffic. At tens of keys no
// eviction is needed.
type keyLimiter struct {
	user    rateClass
	service rateClass

	mu      sync.Mutex
	buckets map[string]*tokenBucket
	nowFn   func() time.Time // injectable for tests
}

func newKeyLimiter(user, service rateClass) *keyLimiter {
	return &keyLimiter{
		user:    user,
		service: service,
		buckets: make(map[string]*tokenBucket),
		nowFn:   time.Now,
	}
}

// allow admits one request for keyHash in the given class, returning the
// Retry-After hint when it does not.
func (l *keyLimiter) allow(keyHash string, service bool) (bool, time.Duration) {
	l.mu.Lock()
	b := l.buckets[keyHash]
	if b == nil {
		class := l.user
		if service {
			class = l.service
		}
		b = newTokenBucket(class.rate, class.burst)
		l.buckets[keyHash] = b
	}
	l.mu.Unlock()
	return b.allow(l.nowFn())
}

// numBuckets reports how many per-key buckets exist. Used by tests to pin the
// DoS-guard invariant that unknown (401'd) keys never allocate a bucket — the
// limiter map must not be growable by unauthenticated traffic.
func (l *keyLimiter) numBuckets() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
