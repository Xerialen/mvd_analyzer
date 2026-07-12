package portal

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvd-analyzer/mvd-api/internal/authkeys"
)

// syncBuf is a goroutine-safe buffer for capturing the portal's logs so tests
// can assert secrets never reach a log line.
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

// discordStub stands in for discord.com in tests: it answers the token exchange
// and /users/@me. It is how the whole OAuth flow is proven WITHOUT a real
// Discord application (the deferred live gate). Fields let each test bend one
// behaviour (a 500, a missing id, a scripted username) without a new stub.
type discordStub struct {
	server   *httptest.Server
	token    string      // access token handed back by the token endpoint
	user     discordUser // identity returned by /users/@me
	tokenErr int         // if non-zero, token endpoint returns this status
	userErr  int         // if non-zero, /users/@me returns this status
	omitID   bool        // if true, /users/@me returns a user with no id

	gotTokenRequest bool // set when the token endpoint is hit
}

func newDiscordStub(t *testing.T) *discordStub {
	t.Helper()
	d := &discordStub{
		token: "test-access-token",
		user:  discordUser{ID: "discord-123", Username: "alice"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		d.gotTokenRequest = true
		if d.tokenErr != 0 {
			w.WriteHeader(d.tokenErr)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": d.token,
			"token_type":   "Bearer",
		})
	})
	mux.HandleFunc("GET /api/users/@me", func(w http.ResponseWriter, r *http.Request) {
		if d.userErr != 0 {
			w.WriteHeader(d.userErr)
			return
		}
		u := d.user
		if d.omitID {
			u.ID = ""
		}
		_ = json.NewEncoder(w).Encode(u)
	})
	d.server = httptest.NewServer(mux)
	t.Cleanup(d.server.Close)
	return d
}

const testCookieSecret = "0123456789abcdef-test-secret" // >= 16 bytes

// newTestPortal builds a Portal wired to the stub Discord and a real key store.
func newTestPortal(t *testing.T, d *discordStub) (*Portal, *authkeys.Store) {
	p, store, _ := newTestPortalLogged(t, d)
	return p, store
}

// newTestPortalLogged additionally returns a buffer capturing the portal's log
// output, so secret-hygiene tests can assert no secret reaches a log line.
func newTestPortalLogged(t *testing.T, d *discordStub) (*Portal, *authkeys.Store, *syncBuf) {
	t.Helper()
	store, err := authkeys.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	buf := &syncBuf{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	cfg, err := NewConfig(
		"https://portal.example.com",
		"client-id",
		"super-secret-value", // the client secret; asserted absent from bodies+logs
		[]byte(testCookieSecret),
		store,
		logger,
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DiscordBaseURL = d.server.URL
	p := New(cfg)
	return p, store, buf
}

// newPortalServer registers the portal on an httptest server and returns a
// client whose redirects are NOT followed (so we can inspect Location + Set-
// Cookie at each hop) but which carries a cookie jar across requests.
func newPortalServer(t *testing.T, p *Portal) (*httptest.Server, *http.Client) {
	t.Helper()
	mux := http.NewServeMux()
	p.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	jar := &recordingJar{cookies: map[string]*http.Cookie{}}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // do not auto-follow; inspect each hop
		},
	}
	return srv, client
}

// recordingJar is a minimal cookie jar: it ignores Path/Secure so tests can run
// over httptest's plaintext http while still exercising the Secure attribute on
// the wire. It keeps the last value per cookie name and clears on Max-Age<0.
type recordingJar struct {
	cookies map[string]*http.Cookie
}

func (j *recordingJar) SetCookies(_ *url.URL, cs []*http.Cookie) {
	for _, c := range cs {
		if c.MaxAge < 0 {
			delete(j.cookies, c.Name)
			continue
		}
		cc := *c
		j.cookies[c.Name] = &cc
	}
}

func (j *recordingJar) Cookies(_ *url.URL) []*http.Cookie {
	out := make([]*http.Cookie, 0, len(j.cookies))
	for _, c := range j.cookies {
		out = append(out, c)
	}
	return out
}

// login drives GET /portal/login and returns the state nonce echoed in the
// authorize redirect (which equals the signed state cookie's payload).
func login(t *testing.T, srv *httptest.Server, client *http.Client) string {
	t.Helper()
	resp, err := client.Get(srv.URL + "/portal/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login status = %d; want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("authorize URL missing state")
	}
	return state
}

// TestFullFlow is the happy path: login → callback → session → key page (no key
// yet) → issue → the issued key AUTHENTICATES against the real auth store →
// regenerate revokes the old key.
func TestFullFlow(t *testing.T) {
	d := newDiscordStub(t)
	p, store := newTestPortal(t, d)
	srv, client := newPortalServer(t, p)

	state := login(t, srv, client)

	// Callback with the matching state (the jar carries the state cookie).
	resp, err := client.Get(srv.URL + "/portal/callback?code=abc&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/portal/key" {
		resp.Body.Close()
		t.Fatalf("callback = %d %q; want 302 /portal/key", resp.StatusCode, resp.Header.Get("Location"))
	}
	if !d.gotTokenRequest {
		resp.Body.Close()
		t.Fatal("token endpoint was not hit on a valid callback")
	}
	// The one-shot state cookie must be cleared by the callback (Set-Cookie with
	// a negative Max-Age), and it must be emitted despite the 302 — a Set-Cookie
	// after the redirect header would be dropped.
	if !stateCookieCleared(resp) {
		resp.Body.Close()
		t.Error("callback did not clear the state cookie")
	}
	resp.Body.Close()

	// Key page: no key yet.
	body := getBody(t, client, srv.URL+"/portal/key")
	if strings.Contains(body, "Current key") {
		t.Errorf("key page shows a key before any was issued:\n%s", body)
	}
	if !strings.Contains(body, "Generate key") {
		t.Errorf("key page missing the generate form:\n%s", body)
	}

	// Issue a key.
	issued := postBody(t, client, srv.URL+"/portal/key")
	key := extractKey(t, issued)
	if !strings.HasPrefix(key, authkeys.KeyPrefix) {
		t.Fatalf("issued key %q missing prefix", key)
	}

	// The issued key authenticates against the SAME store the auth middleware
	// uses — end-to-end proof the portal issues real keys.
	if _, err := store.Lookup(key); err != nil {
		t.Fatalf("portal-issued key does not authenticate: %v", err)
	}

	// Regenerate: a second issue revokes the first (D4).
	issued2 := postBody(t, client, srv.URL+"/portal/key")
	key2 := extractKey(t, issued2)
	if key2 == key {
		t.Fatal("regenerate returned the same key")
	}
	if _, err := store.Lookup(key); err == nil {
		t.Error("old key still authenticates after regenerate")
	}
	if _, err := store.Lookup(key2); err != nil {
		t.Errorf("new key does not authenticate: %v", err)
	}
}

// TestCallbackStateMismatch: a callback whose state does not match the cookie is
// rejected with 400 and NO token exchange is attempted (CSRF defence).
func TestCallbackStateMismatch(t *testing.T) {
	d := newDiscordStub(t)
	p, _ := newTestPortal(t, d)
	srv, client := newPortalServer(t, p)

	login(t, srv, client) // sets a valid state cookie...

	// ...but present a DIFFERENT state in the query.
	resp, err := client.Get(srv.URL + "/portal/callback?code=abc&state=not-the-cookie")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched state = %d; want 400", resp.StatusCode)
	}
	if d.gotTokenRequest {
		t.Fatal("token exchange attempted despite state mismatch (CSRF hole)")
	}
}

// TestCallbackNoStateCookie: a callback with a state param but no state cookie
// (a forged callback, victim's browser has no cookie) is rejected, no exchange.
func TestCallbackNoStateCookie(t *testing.T) {
	d := newDiscordStub(t)
	p, _ := newTestPortal(t, d)
	srv, _ := newPortalServer(t, p)

	// Fresh client with no cookie jar — no state cookie is ever sent.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(srv.URL + "/portal/callback?code=abc&state=anything")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no state cookie = %d; want 400", resp.StatusCode)
	}
	if d.gotTokenRequest {
		t.Fatal("token exchange attempted without a state cookie")
	}
}

// TestTamperedSessionRejected: flipping a byte of the session cookie payload
// invalidates the HMAC, so /portal/key redirects to /portal (not signed in).
func TestTamperedSessionRejected(t *testing.T) {
	d := newDiscordStub(t)
	p, _ := newTestPortal(t, d)
	srv, client := newPortalServer(t, p)
	establishSession(t, srv, client, d)

	// Tamper the session cookie in the jar.
	jar := client.Jar.(*recordingJar)
	sc := jar.cookies[sessionCookie]
	if sc == nil {
		t.Fatal("no session cookie to tamper")
	}
	sc.Value = flipFirstByte(sc.Value)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/portal/key", nil)
	req.AddCookie(sc)
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/portal" {
		t.Fatalf("tampered session = %d %q; want 302 /portal", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestExpiredSessionRejected: a session past its exp is rejected even with a
// valid HMAC. We forge a session cookie with p.now set into the past.
func TestExpiredSessionRejected(t *testing.T) {
	d := newDiscordStub(t)
	p, _ := newTestPortal(t, d)

	// Mint a session that expired an hour ago by setting now into the past.
	p.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	rec := httptest.NewRecorder()
	p.setSessionCookie(rec, "discord-123", "alice")
	expired := rec.Result().Cookies()[0]

	// Reset now to real time; the cookie's exp is now in the past.
	p.now = time.Now

	req := httptest.NewRequest(http.MethodGet, "/portal/key", nil)
	req.AddCookie(expired)
	if _, err := p.readSession(req); err == nil {
		t.Fatal("expired session accepted")
	}
}

// TestNoSessionRedirects: /portal/key with no session cookie 302s to /portal.
func TestNoSessionRedirects(t *testing.T) {
	d := newDiscordStub(t)
	p, _ := newTestPortal(t, d)
	srv, _ := newPortalServer(t, p)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(srv.URL + "/portal/key")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/portal" {
		t.Fatalf("keyless /portal/key = %d %q; want 302 /portal", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestTokenEndpointFailure: a 500 from Discord's token endpoint yields a clean
// 502 page, no panic, and the client secret is absent from the body.
func TestTokenEndpointFailure(t *testing.T) {
	d := newDiscordStub(t)
	d.tokenErr = http.StatusInternalServerError
	p, _, logs := newTestPortalLogged(t, d)
	srv, client := newPortalServer(t, p)
	state := login(t, srv, client)

	resp, err := client.Get(srv.URL + "/portal/callback?code=abc&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("token failure = %d; want 502", resp.StatusCode)
	}
	assertNoSecret(t, string(body))
	assertNoSecret(t, logs.String()) // secrets must not reach the log either
}

// TestUserEndpointMissingID: /users/@me without an id is a clean 502, no panic,
// no secret leak.
func TestUserEndpointMissingID(t *testing.T) {
	d := newDiscordStub(t)
	d.omitID = true
	p, _, logs := newTestPortalLogged(t, d)
	srv, client := newPortalServer(t, p)
	state := login(t, srv, client)

	resp, err := client.Get(srv.URL + "/portal/callback?code=abc&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("missing id = %d; want 502", resp.StatusCode)
	}
	assertNoSecret(t, string(body))
	assertNoSecret(t, logs.String())
}

// TestUsernameEscaped: a Discord username containing markup must be HTML-escaped
// on the issued page (XSS defence — the username is attacker-influenced).
func TestUsernameEscaped(t *testing.T) {
	d := newDiscordStub(t)
	d.user = discordUser{ID: "discord-xss", Username: `<script>alert(1)</script>`}
	p, _ := newTestPortal(t, d)
	srv, client := newPortalServer(t, p)
	establishSession(t, srv, client, d)

	issued := postBody(t, client, srv.URL+"/portal/key")
	if strings.Contains(issued, "<script>alert(1)</script>") {
		t.Errorf("username rendered unescaped (XSS):\n%s", issued)
	}
	// The escaped form must be present, proving the name was rendered at all.
	if !strings.Contains(issued, "&lt;script&gt;") {
		t.Errorf("escaped username not found; template may not render it:\n%s", issued)
	}
}

// TestLogoutClearsSession: after logout, /portal/key redirects to /portal.
func TestLogoutClearsSession(t *testing.T) {
	d := newDiscordStub(t)
	p, _ := newTestPortal(t, d)
	srv, client := newPortalServer(t, p)
	establishSession(t, srv, client, d)

	// Confirm signed in first.
	resp := mustGet(t, client, srv.URL+"/portal/key")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected signed-in key page, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Logout.
	lo, err := client.Post(srv.URL+"/portal/logout", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	lo.Body.Close()
	if lo.StatusCode != http.StatusFound {
		t.Fatalf("logout = %d; want 302", lo.StatusCode)
	}

	resp2 := mustGet(t, client, srv.URL+"/portal/key")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusFound || resp2.Header.Get("Location") != "/portal" {
		t.Fatalf("after logout /portal/key = %d %q; want 302 /portal", resp2.StatusCode, resp2.Header.Get("Location"))
	}
}

// ---- helpers ----

// establishSession runs login + callback so the client holds a valid session.
func establishSession(t *testing.T, srv *httptest.Server, client *http.Client, d *discordStub) {
	t.Helper()
	state := login(t, srv, client)
	resp, err := client.Get(srv.URL + "/portal/callback?code=abc&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("establishSession callback = %d; want 302", resp.StatusCode)
	}
}

func mustGet(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getBody(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp := mustGet(t, client, url)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func postBody(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Post(url, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s = %d:\n%s", url, resp.StatusCode, b)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// extractKey pulls the qwmvd_ key out of the issued-page HTML.
func extractKey(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, authkeys.KeyPrefix)
	if i < 0 {
		t.Fatalf("issued page has no key:\n%s", body)
	}
	rest := body[i:]
	// The key runs until the next non-key character (base64url + prefix chars).
	end := strings.IndexFunc(rest, func(r rune) bool {
		return !(r == '_' || r == '-' ||
			(r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z'))
	})
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

// stateCookieCleared reports whether resp deletes the state cookie (Max-Age<0).
func stateCookieCleared(resp *http.Response) bool {
	for _, c := range resp.Cookies() {
		if c.Name == stateCookie && c.MaxAge < 0 {
			return true
		}
	}
	return false
}

func flipFirstByte(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

// assertNoSecret fails if the response body contains the client secret or the
// cookie secret — the secret-hygiene invariant for error pages.
func assertNoSecret(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, "super-secret-value") {
		t.Errorf("response body leaked the Discord client secret:\n%s", body)
	}
	if strings.Contains(body, testCookieSecret) {
		t.Errorf("response body leaked the cookie secret:\n%s", body)
	}
}

// TestLandingPage: the portal landing must point at the self-served docs
// (/docs, /openapi.yaml, /docs/result-schema), carry the MCP endpoint
// built from the configured base URL, and keep exactly one GitHub link.
func TestLandingPage(t *testing.T) {
	d := newDiscordStub(t)
	p, _ := newTestPortal(t, d)
	srv, client := newPortalServer(t, p)

	resp, err := client.Get(srv.URL + "/portal")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /portal = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	for _, want := range []string{
		`href="/docs"`,
		`href="/openapi.yaml"`,
		`href="/docs/result-schema"`,
		"https://portal.example.com/mcp",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("landing page missing %q", want)
		}
	}
	if got := strings.Count(page, "github.com"); got != 2 { // one href + its visible text
		t.Errorf("landing page github.com mentions = %d, want exactly 2 (one link)", got)
	}
}

// TestPolicyPages: the GDPR disclosure pages render without a session and
// say what they must — data collected, cookies, contact, rights.
func TestPolicyPages(t *testing.T) {
	d := newDiscordStub(t)
	p, _ := newTestPortal(t, d)
	srv, client := newPortalServer(t, p)

	for path, wants := range map[string][]string{
		"/portal/privacy": {
			"Discord user id and username",
			"SHA-256 hash",
			"strictly necessary cookies only",
			"access log",
			"hub.quakeworld.nu",
			"Your rights",
		},
		"/portal/terms": {
			"as is",
			"rate-limited per key",
			`href="/portal/privacy"`,
		},
	} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		for _, want := range wants {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
	}

	// Every portal page footer must link the policies (base template).
	resp, err := client.Get(srv.URL + "/portal")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `href="/portal/privacy"`) ||
		!strings.Contains(string(body), `href="/portal/terms"`) {
		t.Error("landing footer does not link the policy pages")
	}
}
