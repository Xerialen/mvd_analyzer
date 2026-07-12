package democache

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mvd-analyzer/mvd-analytics/hubfetch"
	"github.com/mvd-analyzer/mvd-analytics/result"
)

// --- ParseDemoID ---

func TestParseDemoID(t *testing.T) {
	t.Parallel()

	const goodSHA = "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234"

	cases := []struct {
		in      string
		want    DemoID
		wantErr bool
		errSub  string
	}{
		{"gameId:42", DemoID{Kind: "gameId", GameID: 42}, false, ""},
		{"sha:" + goodSHA, DemoID{Kind: "sha256", SHA: goodSHA}, false, ""},
		{"sha:" + strings.ToUpper(goodSHA), DemoID{Kind: "sha256", SHA: goodSHA}, false, ""},
		{"", DemoID{}, true, "empty"},
		{"banana", DemoID{}, true, "expected"},
		{"gameId:0", DemoID{}, true, "positive"},
		{"gameId:-1", DemoID{}, true, "positive"},
		{"sha:short", DemoID{}, true, "64 hex"},
	}

	for _, c := range cases {
		got, err := ParseDemoID(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseDemoID(%q): expected error, got nil", c.in)
				continue
			}
			if !errors.Is(err, ErrInvalidDemoID) {
				t.Errorf("ParseDemoID(%q): expected ErrInvalidDemoID, got %v", c.in, err)
			}
			if c.errSub != "" && !strings.Contains(err.Error(), c.errSub) {
				t.Errorf("ParseDemoID(%q): error %q missing substring %q", c.in, err.Error(), c.errSub)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDemoID(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseDemoID(%q) = %+v; want %+v", c.in, got, c.want)
		}
	}
}

func TestDemoID_String(t *testing.T) {
	t.Parallel()

	const sha = "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234"
	cases := []struct {
		id   DemoID
		want string
	}{
		{DemoID{Kind: "gameId", GameID: 42}, "gameId:42"},
		{DemoID{Kind: "sha256", SHA: sha}, "sha:" + sha},
		{DemoID{}, ""},
	}
	for _, c := range cases {
		if got := c.id.String(); got != c.want {
			t.Errorf("DemoID.String %+v = %q; want %q", c.id, got, c.want)
		}
	}
}

// --- hub stub ---

// fakeHub builds a single httptest.Server that doubles as both the
// Supabase resolve endpoint and the CDN download endpoint. It records
// hits so tests can assert call counts.
type fakeHub struct {
	srv         *httptest.Server
	supabaseURL string
	cdnBase     string

	resolveCalls atomic.Int32
	cdnCalls     atomic.Int32

	mu       sync.Mutex
	games    map[int]hubfetch.GameInfo // gameId → row
	mvdBlobs map[string][]byte         // sha → bytes
}

func newFakeHub() *fakeHub {
	f := &fakeHub{
		games:    make(map[int]hubfetch.GameInfo),
		mvdBlobs: make(map[string][]byte),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/v1/v1_games", f.handleResolve)
	mux.HandleFunc("/cdn/", f.handleCDN)
	f.srv = httptest.NewServer(mux)
	f.supabaseURL = f.srv.URL + "/rest/v1/v1_games"
	f.cdnBase = f.srv.URL + "/cdn"
	return f
}

func (f *fakeHub) Close() { f.srv.Close() }

func (f *fakeHub) addGame(gameID int, sha, mvd string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.games[gameID] = hubfetch.GameInfo{ID: gameID, DemoSHA256: sha, DemoSourceURL: ""}
	f.mvdBlobs[sha] = []byte(mvd)
}

func (f *fakeHub) handleResolve(w http.ResponseWriter, r *http.Request) {
	f.resolveCalls.Add(1)
	q := r.URL.Query()
	filter := q.Get("id") // e.g. "eq.42"
	id := 0
	fmt.Sscanf(filter, "eq.%d", &id)
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	g, ok := f.games[id]
	f.mu.Unlock()
	if !ok {
		w.Write([]byte("[]"))
		return
	}
	fmt.Fprintf(w, `[{"id":%d,"demo_sha256":%q,"demo_source_url":%q}]`,
		g.ID, g.DemoSHA256, g.DemoSourceURL)
}

func (f *fakeHub) handleCDN(w http.ResponseWriter, r *http.Request) {
	f.cdnCalls.Add(1)
	// path is /cdn/<sha[:3]>/<sha>.mvd.gz
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/cdn/"), "/")
	if len(parts) != 2 || !strings.HasSuffix(parts[1], ".mvd.gz") {
		http.NotFound(w, r)
		return
	}
	sha := strings.TrimSuffix(parts[1], ".mvd.gz")
	f.mu.Lock()
	data, ok := f.mvdBlobs[sha]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Write(data)
}

func (f *fakeHub) hubClient() *hubfetch.Client {
	c := hubfetch.NewClient()
	c.SupabaseURL = f.supabaseURL
	c.CDNBase = f.cdnBase
	return c
}

// stubParser records every parse call and returns a deterministic
// *Result keyed on filename so tests can pin equality.
type stubParser struct {
	calls atomic.Int32
	delay time.Duration // simulate parse cost for the singleflight test
}

func (s *stubParser) parse(_ context.Context, mvd []byte, filename string) (*result.Result, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return &result.Result{
		SchemaVersion: result.CurrentSchemaVersion,
		FilePath:      filename,
		Errors:        []string{fmt.Sprintf("stubParser: %d bytes", len(mvd))},
	}, nil
}

// --- GetResult flows ---

const (
	// testSHA must be the real sha256 of testMVD: the cold-download path now
	// verifies downloaded bytes against the SHA that keys the cache (F1), so a
	// mismatching fixture would be rejected as ErrHubUpstream.
	testSHA = "8bc625ab64dbbeaf872e4491c46fb110d4143c97daf87ddfbca52ff188c86886"
	testMVD = "BOGUS-MVD-BYTES" // sha256 == testSHA; stub parser doesn't actually parse
)

func newTestCache(t *testing.T, hub *hubfetch.Client, parser *stubParser) (*Cache, string) {
	t.Helper()
	root := t.TempDir()
	c := New(root, hub)
	c.Parse = parser.parse
	return c, root
}

func TestGetResult_GameID_ColdFetch_FullChain(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()
	hub.addGame(42, testSHA, testMVD)

	parser := &stubParser{}
	c, root := newTestCache(t, hub.hubClient(), parser)

	r, meta, err := c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: 42})
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if r.SchemaVersion != result.CurrentSchemaVersion {
		t.Errorf("schemaVersion = %d; want %d", r.SchemaVersion, result.CurrentSchemaVersion)
	}
	if meta.SHA256 != testSHA {
		t.Errorf("meta.SHA256 = %q; want %q", meta.SHA256, testSHA)
	}
	if meta.FromCache {
		t.Errorf("meta.FromCache = true on cold fetch")
	}
	if meta.FromMVDTier {
		t.Errorf("meta.FromMVDTier = true on cold fetch (no tier-1 hit)")
	}
	if parser.calls.Load() != 1 {
		t.Errorf("parser.calls = %d; want 1", parser.calls.Load())
	}
	if hub.resolveCalls.Load() != 1 {
		t.Errorf("resolveCalls = %d; want 1", hub.resolveCalls.Load())
	}
	if hub.cdnCalls.Load() != 1 {
		t.Errorf("cdnCalls = %d; want 1", hub.cdnCalls.Load())
	}

	// Verify all three artifacts on disk.
	mustExist(t, mvdPath(root, testSHA), "tier-1 MVD")
	mustExist(t, resultPath(root, result.CurrentSchemaVersion, testSHA), "tier-2 Result gob")
	mustExist(t, gameIndexPath(root, 42), "gameId index")
}

func TestGetResult_GameID_IndexHit_NoHubResolve(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()
	hub.addGame(42, testSHA, testMVD)

	parser := &stubParser{}
	c, root := newTestCache(t, hub.hubClient(), parser)

	// Pre-seed gameId index.
	if err := os.MkdirAll(filepath.Dir(gameIndexPath(root, 42)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gameIndexPath(root, 42), []byte(testSHA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pre-seed tier-2 so it's a pure cache hit.
	r0 := &result.Result{SchemaVersion: result.CurrentSchemaVersion, FilePath: "seeded"}
	data, err := encodeResult(r0)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(resultPath(root, result.CurrentSchemaVersion, testSHA), data, 0o644); err != nil {
		t.Fatal(err)
	}

	r, meta, err := c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: 42})
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if !meta.FromCache {
		t.Errorf("meta.FromCache = false on warm hit")
	}
	if r.FilePath != "seeded" {
		t.Errorf("FilePath = %q; want 'seeded'", r.FilePath)
	}
	if hub.resolveCalls.Load() != 0 {
		t.Errorf("resolveCalls = %d; want 0 (index hit)", hub.resolveCalls.Load())
	}
	if parser.calls.Load() != 0 {
		t.Errorf("parser.calls = %d; want 0 (tier-2 hit)", parser.calls.Load())
	}
}

func TestGetResult_Tier1Hit_ResultMiss_ReParses(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()
	hub.addGame(42, testSHA, testMVD)

	parser := &stubParser{}
	c, root := newTestCache(t, hub.hubClient(), parser)

	// Pre-seed tier-1 + gameId index. Skip tier-2 entirely.
	_ = os.MkdirAll(filepath.Dir(mvdPath(root, testSHA)), 0o755)
	_ = os.WriteFile(mvdPath(root, testSHA), []byte(testMVD), 0o644)
	_ = os.MkdirAll(filepath.Dir(gameIndexPath(root, 42)), 0o755)
	_ = os.WriteFile(gameIndexPath(root, 42), []byte(testSHA+"\n"), 0o644)

	_, meta, err := c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: 42})
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if !meta.FromMVDTier {
		t.Errorf("meta.FromMVDTier = false; expected tier-1 hit")
	}
	if meta.FromCache {
		t.Errorf("meta.FromCache = true; parser should have run")
	}
	if parser.calls.Load() != 1 {
		t.Errorf("parser.calls = %d; want 1", parser.calls.Load())
	}
	if hub.cdnCalls.Load() != 0 {
		t.Errorf("cdnCalls = %d; want 0 (tier-1 hit)", hub.cdnCalls.Load())
	}
	// Tier-2 now exists.
	mustExist(t, resultPath(root, result.CurrentSchemaVersion, testSHA), "tier-2 after parse")
}

func TestGetResult_SHA_LocalHit(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()

	parser := &stubParser{}
	c, root := newTestCache(t, hub.hubClient(), parser)

	// Pre-seed tier-2.
	r0 := &result.Result{SchemaVersion: result.CurrentSchemaVersion, FilePath: "seeded"}
	data, _ := encodeResult(r0)
	_ = writeFileAtomic(resultPath(root, result.CurrentSchemaVersion, testSHA), data, 0o644)

	_, meta, err := c.GetResult(context.Background(), DemoID{Kind: "sha256", SHA: testSHA})
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if !meta.FromCache {
		t.Errorf("meta.FromCache = false on tier-2 hit")
	}
	if hub.resolveCalls.Load() != 0 {
		t.Errorf("resolveCalls = %d; expected zero on SHA-direct hit", hub.resolveCalls.Load())
	}
}

func TestGetResult_SHA_MissNoSource(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()

	parser := &stubParser{}
	c, _ := newTestCache(t, hub.hubClient(), parser)

	_, _, err := c.GetResult(context.Background(), DemoID{Kind: "sha256", SHA: testSHA})
	if !errors.Is(err, ErrDemoNotFound) {
		t.Errorf("expected ErrDemoNotFound, got %v", err)
	}
}

func TestGetResult_HubNotFound(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()
	// No game added.

	parser := &stubParser{}
	c, _ := newTestCache(t, hub.hubClient(), parser)

	_, _, err := c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: 9999})
	if !errors.Is(err, ErrDemoNotFound) {
		t.Errorf("expected ErrDemoNotFound, got %v", err)
	}
}

func TestGetResult_HubUpstreamError(t *testing.T) {
	// 5xx supabase → wrapped as ErrHubUpstream.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("oh no"))
	}))
	defer srv.Close()

	hub := hubfetch.NewClient()
	hub.SupabaseURL = srv.URL
	hub.CDNBase = srv.URL + "/cdn"

	parser := &stubParser{}
	c := New(t.TempDir(), hub)
	c.Parse = parser.parse

	_, _, err := c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: 42})
	if !errors.Is(err, ErrHubUpstream) {
		t.Errorf("expected ErrHubUpstream, got %v", err)
	}
}

// TestGetResult_SHAMismatch_NotCached covers F1: a hub that claims one
// demo_sha256 but serves bytes hashing to something else (corrupted CDN
// object / wrong source URL) must be rejected as ErrHubUpstream and must
// not poison the cache under the claimed sha.
func TestGetResult_SHAMismatch_NotCached(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()
	// Claim testSHA, but serve bytes that do not hash to it.
	hub.addGame(42, testSHA, "CORRUPT-BYTES-DO-NOT-MATCH-testSHA")

	parser := &stubParser{}
	c, root := newTestCache(t, hub.hubClient(), parser)

	_, _, err := c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: 42})
	if !errors.Is(err, ErrHubUpstream) {
		t.Fatalf("expected ErrHubUpstream on sha mismatch, got %v", err)
	}
	if _, statErr := os.Stat(mvdPath(root, testSHA)); statErr == nil {
		t.Errorf("mismatched bytes were cached at %s — must not be", mvdPath(root, testSHA))
	}
	if parser.calls.Load() != 0 {
		t.Errorf("parser ran on a mismatched download: calls=%d", parser.calls.Load())
	}
}

// TestGetResult_HubUpstream_BodyContainsNotFound covers F2: a hub 5xx
// whose body text merely contains "not found" must classify as an upstream
// outage (ErrHubUpstream), never as a genuine 404 (ErrDemoNotFound).
func TestGetResult_HubUpstream_BodyContainsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("upstream failure: resource not found in backend store"))
	}))
	defer srv.Close()

	hub := hubfetch.NewClient()
	hub.SupabaseURL = srv.URL
	hub.CDNBase = srv.URL + "/cdn"

	parser := &stubParser{}
	c := New(t.TempDir(), hub)
	c.Parse = parser.parse

	_, _, err := c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: 42})
	if errors.Is(err, ErrDemoNotFound) {
		t.Fatalf("a 5xx body containing 'not found' was misclassified as ErrDemoNotFound: %v", err)
	}
	if !errors.Is(err, ErrHubUpstream) {
		t.Fatalf("expected ErrHubUpstream, got %v", err)
	}
}

func TestGetResult_InvalidDemoID(t *testing.T) {
	c := New(t.TempDir(), nil)

	cases := []DemoID{
		{Kind: "gameId", GameID: 0},
		{Kind: "gameId", GameID: -3},
		{Kind: "sha256", SHA: "short"},
		{Kind: "weird"},
	}
	for _, id := range cases {
		_, _, err := c.GetResult(context.Background(), id)
		if !errors.Is(err, ErrInvalidDemoID) {
			t.Errorf("GetResult(%+v): want ErrInvalidDemoID, got %v", id, err)
		}
	}
}

func TestGetResult_Singleflight_OneParseUnderFanout(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()
	hub.addGame(42, testSHA, testMVD)

	parser := &stubParser{delay: 50 * time.Millisecond}
	c, _ := newTestCache(t, hub.hubClient(), parser)

	const N = 12
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _, err := c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: 42})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent GetResult: %v", err)
		}
	}
	if got := parser.calls.Load(); got != 1 {
		t.Errorf("parser.calls = %d; want 1 (singleflight)", got)
	}
}

func TestResultLRU_Eviction(t *testing.T) {
	l := newResultLRU(2)
	r1 := &result.Result{FilePath: "a"}
	r2 := &result.Result{FilePath: "b"}
	r3 := &result.Result{FilePath: "c"}

	l.put("a", r1)
	l.put("b", r2)
	l.put("c", r3) // evicts a

	if l.get("a") != nil {
		t.Errorf("a should have been evicted")
	}
	if l.get("b") != r2 {
		t.Errorf("b should still be cached")
	}
	if l.get("c") != r3 {
		t.Errorf("c should still be cached")
	}

	// Access b → c should now be evict target.
	_ = l.get("b")
	l.put("d", &result.Result{FilePath: "d"})
	if l.get("c") != nil {
		t.Errorf("c should have been evicted after b touch + d put")
	}
}

// --- helpers ---

func mustExist(t *testing.T, path, label string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("%s missing: %s (%v)", label, path, err)
	}
}
