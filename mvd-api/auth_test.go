package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-api/internal/authkeys"
)

// newAuthTestServer builds an auth-mode server over a store seeded with the
// given keys, returning the server, the authenticator, and a buffer that
// captures the access + auth logs (guarded — the test server writes from
// multiple goroutines).
func newAuthTestServer(t *testing.T, store demoStore) (*httptest.Server, *authenticator, *syncBuf) {
	t.Helper()
	buf := &syncBuf{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	keys, err := authkeys.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	auth := &authenticator{
		store: keys,
		limiter: newKeyLimiter(
			rateClass{rate: 1000, burst: 1000}, // generous; rate tests use their own limiter
			rateClass{rate: 1000, burst: 1000},
		),
		logger: logger,
	}
	srv := httptest.NewServer(newRouter(store, logger, "", auth, nil))
	t.Cleanup(srv.Close)
	return srv, auth, buf
}

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func doGet(t *testing.T, url, bearer string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// TestAuth_RejectsWithoutKey: every /v1 path 401s without a key.
func TestAuth_RejectsWithoutKey(t *testing.T) {
	srv, _, _ := newAuthTestServer(t, storeWithStub())
	for _, path := range []string{
		"/v1/demos/gameId:42/overview",
		"/v1/auth/check",
		"/v1/artifacts",
	} {
		resp := doGet(t, srv.URL+path, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without key: status %d; want 401", path, resp.StatusCode)
		}
		if got := resp.Header.Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("GET %s: WWW-Authenticate = %q; want Bearer", path, got)
		}
	}
}

func TestAuth_RejectsGarbageAndRevoked(t *testing.T) {
	srv, auth, _ := newAuthTestServer(t, storeWithStub())
	key, _, _ := auth.store.Issue("1", "user", false, "note")

	// garbage
	resp := doGet(t, srv.URL+"/v1/demos/gameId:42/overview", "qwmvd_not_a_real_key")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("garbage key: status %d; want 401", resp.StatusCode)
	}

	// valid → 200
	resp = doGet(t, srv.URL+"/v1/demos/gameId:42/overview", key)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("valid key: status %d; want 200", resp.StatusCode)
	}

	// revoke → 401
	if _, err := auth.store.Revoke(key, "", ""); err != nil {
		t.Fatal(err)
	}
	resp = doGet(t, srv.URL+"/v1/demos/gameId:42/overview", key)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked key: status %d; want 401", resp.StatusCode)
	}
}

// TestAuth_UnknownKeysAllocateNoBuckets is the primary hosted-DoS regression
// guard: an unknown key must 401 BEFORE the limiter allocates a bucket, so
// unauthenticated traffic (with distinct garbage tokens) cannot grow the
// limiter map unboundedly. Reversing the auth/limiter order would fail here.
func TestAuth_UnknownKeysAllocateNoBuckets(t *testing.T) {
	srv, auth, _ := newAuthTestServer(t, storeWithStub())
	for i := 0; i < 50; i++ {
		resp := doGet(t, srv.URL+"/v1/auth/check", "qwmvd_garbage_"+strconv.Itoa(i))
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("garbage token %d: status %d; want 401", i, resp.StatusCode)
		}
	}
	if n := auth.limiter.numBuckets(); n != 0 {
		t.Errorf("limiter allocated %d bucket(s) for unknown keys; want 0 (DoS guard)", n)
	}

	// Sanity: an authenticated key DOES allocate exactly one bucket, proving
	// the assertion above isn't vacuous.
	key, _, _ := auth.store.Issue("1", "u", false, "")
	doGet(t, srv.URL+"/v1/auth/check", key).Body.Close()
	if n := auth.limiter.numBuckets(); n != 1 {
		t.Errorf("authenticated key allocated %d buckets; want 1", n)
	}
}

// TestAuth_TraversalNotExempt pins FIX 2: a path-traversal that textually
// prefixes /portal/ must NOT be treated as exempt — it path.Cleans to a
// protected route and gets 401 without a key.
func TestAuth_TraversalNotExempt(t *testing.T) {
	srv, _, _ := newAuthTestServer(t, storeWithStub())
	// Send the raw, un-normalised path so the server sees the traversal.
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.URL.Opaque = "/portal/../v1/auth/check"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Either the server 401s it (auth ran, not exempt) or the mux 3xx-redirects
	// the un-cleaned path — both are acceptable; what must NOT happen is a 204
	// (exempt fall-through to a keyless success). Assert not-204 and, when the
	// request reached auth, that it was 401.
	if resp.StatusCode == http.StatusNoContent {
		t.Fatalf("traversal /portal/../v1/auth/check returned 204 — exemption leaked")
	}
	// Direct unit check of the predicate, independent of mux redirect behaviour.
	if authExempt("/portal/../v1/auth/check") {
		t.Error("authExempt must not exempt a traversal that cleans to /v1/auth/check")
	}
	if !authExempt("/portal/login") || !authExempt("/healthz") || !authExempt("/v1/version") {
		t.Error("authExempt must still exempt the real portal prefix, healthz, and version")
	}
	if authExempt("/v1/auth/check") {
		t.Error("authExempt must not exempt /v1/auth/check")
	}
}

// TestAuth_CheckEndpoint: /v1/auth/check → 204 with a valid key, 401 without.
func TestAuth_CheckEndpoint(t *testing.T) {
	srv, auth, _ := newAuthTestServer(t, storeWithStub())
	key, _, _ := auth.store.Issue("1", "user", false, "")

	resp := doGet(t, srv.URL+"/v1/auth/check", key)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("auth/check valid: status %d; want 204", resp.StatusCode)
	}

	resp = doGet(t, srv.URL+"/v1/auth/check", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("auth/check no key: status %d; want 401", resp.StatusCode)
	}
}

// TestAuth_ExemptPaths: healthz + version reachable without a key.
func TestAuth_ExemptPaths(t *testing.T) {
	srv, _, _ := newAuthTestServer(t, storeWithStub())
	for _, path := range []string{"/healthz", "/v1/version"} {
		resp := doGet(t, srv.URL+path, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("exempt GET %s: status %d; want 200", path, resp.StatusCode)
		}
	}
}

// TestAuth_PreflightNotBlocked: OPTIONS preflight returns 204 without a key
// even in auth mode (cors answers it before auth runs).
func TestAuth_PreflightNotBlocked(t *testing.T) {
	srv, _, _ := newAuthTestServer(t, storeWithStub())
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/v1/demos/gameId:42/overview", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight in auth mode: status %d; want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got != "" {
		t.Errorf("preflight must not carry WWW-Authenticate, got %q", got)
	}
}

// TestAuth_LogUsesIdentityNotKey is the security-critical assertion: the raw
// key must never appear in any log line, and the log identity must be the
// key's note.
func TestAuth_LogUsesIdentityNotKey(t *testing.T) {
	srv, auth, buf := newAuthTestServer(t, storeWithStub())
	const note = "mvd-web-service"
	key, rec, _ := auth.store.Issue("1", "discord-display-name", true, note)

	resp := doGet(t, srv.URL+"/v1/demos/gameId:42/overview", key)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d; want 200", resp.StatusCode)
	}

	logs := buf.String()
	if strings.Contains(logs, key) {
		t.Fatalf("access log leaked the raw API key:\n%s", logs)
	}
	if body := strings.TrimPrefix(key, authkeys.KeyPrefix); strings.Contains(logs, body) {
		t.Fatalf("access log leaked the key body:\n%s", logs)
	}
	if strings.Contains(logs, rec.KeyHash) {
		t.Fatalf("access log leaked the full key hash (the verifier):\n%s", logs)
	}
	if !strings.Contains(logs, note) {
		t.Errorf("access log missing the expected identity %q:\n%s", note, logs)
	}
}

// TestAuth_401BodyDoesNotLeak: the 401 body reveals nothing about the key.
func TestAuth_401BodyDoesNotLeak(t *testing.T) {
	srv, _, _ := newAuthTestServer(t, storeWithStub())
	const garbage = "qwmvd_super_secret_attempt"
	resp := doGet(t, srv.URL+"/v1/demos/gameId:42/overview", garbage)
	defer resp.Body.Close()
	var env errorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != "unauthorized" {
		t.Errorf("401 code = %q; want unauthorized", env.Error.Code)
	}
	if strings.Contains(env.Error.Message, garbage) ||
		strings.Contains(strings.ToLower(env.Error.Message), "revoked") ||
		strings.Contains(strings.ToLower(env.Error.Message), "absent") {
		t.Errorf("401 body leaks key state: %q", env.Error.Message)
	}
}

// --- rate limiting ---

func rlStore() *fakeStore {
	return &fakeStore{byID: map[string]*result.Result{"gameId:42": {SchemaVersion: result.CurrentSchemaVersion}}}
}

// TestRateLimit_BurstThen429 drives a small-burst user key past its bucket and
// asserts the 429 + a parseable Retry-After.
func TestRateLimit_BurstThen429(t *testing.T) {
	buf := &syncBuf{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	keys, _ := authkeys.Open(t.TempDir())
	// user burst = 2, no meaningful refill during the test.
	auth := &authenticator{
		store: keys,
		limiter: newKeyLimiter(
			rateClass{rate: 0.001, burst: 2},
			rateClass{rate: 0.001, burst: 100},
		),
		logger: logger,
	}
	srv := httptest.NewServer(newRouter(rlStore(), logger, "", auth, nil))
	defer srv.Close()

	key, _, _ := auth.store.Issue("1", "u", false, "")
	// version is exempt; hit a real /v1 endpoint. 2 should pass, 3rd 429s.
	statuses := make([]int, 0, 3)
	for i := 0; i < 3; i++ {
		resp := doGet(t, srv.URL+"/v1/auth/check", key)
		statuses = append(statuses, resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests {
			ra := resp.Header.Get("Retry-After")
			if ra == "" {
				t.Errorf("429 missing Retry-After")
			} else if n, err := strconv.Atoi(ra); err != nil || n < 1 {
				t.Errorf("Retry-After = %q; want a positive integer", ra)
			}
		}
		resp.Body.Close()
	}
	if statuses[0] != 204 || statuses[1] != 204 || statuses[2] != http.StatusTooManyRequests {
		t.Errorf("statuses = %v; want [204 204 429]", statuses)
	}
}

// TestRateLimit_ServiceLooser: a service key gets the looser class, so a burst
// that 429s a user key passes for a service key.
func TestRateLimit_ServiceLooser(t *testing.T) {
	buf := &syncBuf{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	keys, _ := authkeys.Open(t.TempDir())
	auth := &authenticator{
		store: keys,
		limiter: newKeyLimiter(
			rateClass{rate: 0.001, burst: 1},
			rateClass{rate: 0.001, burst: 50},
		),
		logger: logger,
	}
	srv := httptest.NewServer(newRouter(rlStore(), logger, "", auth, nil))
	defer srv.Close()

	svcKey, _, _ := auth.store.Issue("", "", true, "svc")
	for i := 0; i < 10; i++ {
		resp := doGet(t, srv.URL+"/v1/auth/check", svcKey)
		st := resp.StatusCode
		resp.Body.Close()
		if st != http.StatusNoContent {
			t.Fatalf("service key request %d: status %d; want 204 (looser class)", i, st)
		}
	}
}

// TestRateLimit_IndependentBuckets: exhausting one key's bucket does not
// affect another key.
func TestRateLimit_IndependentBuckets(t *testing.T) {
	buf := &syncBuf{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	keys, _ := authkeys.Open(t.TempDir())
	auth := &authenticator{
		store: keys,
		limiter: newKeyLimiter(
			rateClass{rate: 0.001, burst: 1},
			rateClass{rate: 0.001, burst: 1},
		),
		logger: logger,
	}
	srv := httptest.NewServer(newRouter(rlStore(), logger, "", auth, nil))
	defer srv.Close()

	k1, _, _ := auth.store.Issue("1", "a", false, "")
	k2, _, _ := auth.store.Issue("2", "b", false, "")

	// Drain k1.
	doGet(t, srv.URL+"/v1/auth/check", k1).Body.Close()
	r := doGet(t, srv.URL+"/v1/auth/check", k1)
	r.Body.Close()
	if r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("k1 second request: status %d; want 429", r.StatusCode)
	}
	// k2 unaffected.
	r = doGet(t, srv.URL+"/v1/auth/check", k2)
	r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		t.Errorf("k2 first request: status %d; want 204 (independent bucket)", r.StatusCode)
	}
}

// TestRateLimit_SingleRequestOK: a single request never trips the limiter.
func TestRateLimit_SingleRequestOK(t *testing.T) {
	srv, auth, _ := newAuthTestServer(t, storeWithStub())
	key, _, _ := auth.store.Issue("1", "u", false, "")
	resp := doGet(t, srv.URL+"/v1/auth/check", key)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("single request: status %d; want 204", resp.StatusCode)
	}
}

// --- token bucket unit tests (deterministic clock) ---

func TestTokenBucket_RefillAndRetryAfter(t *testing.T) {
	now := time.Unix(0, 0)
	b := newTokenBucket(1, 1) // 1 tok/s, cap 1
	b.last = now              // align the bucket clock with the injected clock
	if ok, _ := b.allow(now); !ok {
		t.Fatal("first allow should pass (bucket starts full)")
	}
	ok, retry := b.allow(now)
	if ok {
		t.Fatal("second allow at same instant should be denied")
	}
	if retry <= 0 || retry > time.Second {
		t.Errorf("retryAfter = %v; want ~1s", retry)
	}
	// After a full second, one token accrues.
	if ok, _ := b.allow(now.Add(time.Second)); !ok {
		t.Error("allow after 1s refill should pass")
	}
}
