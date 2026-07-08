// Package portal is the Discord-authenticated key portal for mvd-api's hosting
// mode (PLAN-hosting D5). A signed-in Discord user gets exactly one API key,
// self-service, without an operator in the loop.
//
// It is OFF by default: the server registers the /portal routes only when the
// operator passes -portal (serve.go). When off, none of this code runs and the
// server behaves exactly as before.
//
// Security model (D5):
//   - Auth is Discord OAuth2 with scope `identify` only — no email, no guilds.
//   - The only server-trusted state is an HMAC-signed session cookie (no
//     server-side session store). See cookie.go.
//   - The OAuth `state` nonce is double-submitted (cookie vs query param) to
//     stop login CSRF; SameSite=Lax stops CSRF on the mutating POSTs.
//   - Secrets (the cookie HMAC key, the Discord client secret) live only in the
//     Portal struct, never in a log line, error body, or rendered page.
package portal

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mvd-analyzer/mvd-api/internal/authkeys"
)

const (
	// sessionTTL bounds how long a Discord sign-in is honoured before the user
	// must log in again (D5: 1h).
	sessionTTL = time.Hour
	// stateTTL bounds the OAuth state nonce's life — it only has to survive the
	// redirect to Discord's consent screen and back.
	stateTTL = 10 * time.Minute
	// defaultDiscordBase is the real Discord API/authorize host. Overridden in
	// tests to point at an httptest stub (Config.DiscordBaseURL).
	defaultDiscordBase = "https://discord.com"
)

// KeyStore is the subset of *authkeys.Store the portal needs. An interface so
// tests can observe issuance without a real store, though the default flow uses
// the real store end-to-end.
type KeyStore interface {
	Issue(discordID, discordName string, service bool, note string) (string, authkeys.Record, error)
	ActiveByDiscordID(discordID string) (authkeys.Record, bool)
}

// Config is the validated portal configuration. Build it with NewConfig, which
// enforces the required-fields rule (all secrets present, secret length) before
// the server starts — a misconfigured portal must refuse to boot, not run
// half-open.
type Config struct {
	// BaseURL is the public origin (e.g. https://qw.example.com). Used to build
	// the OAuth redirect_uri (<BaseURL>/portal/callback) and absolute links.
	BaseURL string
	// ClientID / ClientSecret are the Discord application credentials (env).
	ClientID     string
	ClientSecret string
	// CookieSecret is the HMAC key for session/state cookies (env). Must be
	// >= minCookieSecretLen bytes.
	CookieSecret []byte
	// Store is the key store the portal issues into (the same store the auth
	// middleware validates against).
	Store KeyStore
	// Logger is optional; a nil logger discards.
	Logger *slog.Logger
	// DiscordBaseURL overrides the Discord host for tests (httptest stub).
	// Empty uses defaultDiscordBase.
	DiscordBaseURL string
}

// minCookieSecretLen is the floor on PORTAL_COOKIE_SECRET length. 16 bytes
// (128 bits) is the minimum defensible HMAC key; the operator is encouraged to
// use 32.
const minCookieSecretLen = 16

// Portal is the HTTP handler for the /portal surface.
type Portal struct {
	baseURL      string
	clientID     string
	clientSecret string
	cookieSecret []byte
	store        KeyStore
	logger       *slog.Logger
	discordBase  string

	// now and randState are injection points for tests (deterministic expiry
	// and state). Nil-valued fields fall back to the real implementations.
	now        func() time.Time
	randState  func() (string, error)
	httpClient *http.Client
}

// NewConfig validates raw portal settings and returns a Config, or an error
// naming the first missing/invalid field. Called at startup so the process
// fails fast and loud on misconfiguration (D5: refuse to start if any of the
// Discord/cookie inputs is absent).
func NewConfig(baseURL, clientID, clientSecret string, cookieSecret []byte, store KeyStore, logger *slog.Logger) (Config, error) {
	switch {
	case store == nil:
		// -portal REQUIRES -auth-dir: the portal has nowhere to issue keys
		// otherwise. serve.go maps a nil store to the missing -auth-dir case.
		return Config{}, errors.New("portal: requires -auth-dir (a key store to issue into)")
	case strings.TrimSpace(baseURL) == "":
		return Config{}, errors.New("portal: -portal-base-url is required")
	case clientID == "":
		return Config{}, errors.New("portal: DISCORD_CLIENT_ID is required")
	case clientSecret == "":
		return Config{}, errors.New("portal: DISCORD_CLIENT_SECRET is required")
	case len(cookieSecret) < minCookieSecretLen:
		return Config{}, fmt.Errorf("portal: PORTAL_COOKIE_SECRET must be at least %d bytes", minCookieSecretLen)
	}
	if _, err := url.Parse(baseURL); err != nil {
		return Config{}, fmt.Errorf("portal: -portal-base-url invalid: %w", err)
	}
	return Config{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		CookieSecret: cookieSecret,
		Store:        store,
		Logger:       logger,
	}, nil
}

// New builds a Portal from a validated Config.
func New(c Config) *Portal {
	logger := c.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	base := c.DiscordBaseURL
	if base == "" {
		base = defaultDiscordBase
	}
	p := &Portal{
		baseURL:      strings.TrimRight(c.BaseURL, "/"),
		clientID:     c.ClientID,
		clientSecret: c.ClientSecret,
		cookieSecret: c.CookieSecret,
		store:        c.Store,
		logger:       logger,
		discordBase:  strings.TrimRight(base, "/"),
		now:          time.Now,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
	p.randState = randNonce
	return p
}

// Register installs the portal routes on mux. The paths are the ones the
// phase-14 auth exemption already carves out (/portal and /portal/*), so they
// are reachable without an API key.
func (p *Portal) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /portal", p.handleLanding)
	mux.HandleFunc("GET /portal/login", p.handleLogin)
	mux.HandleFunc("GET /portal/callback", p.handleCallback)
	mux.HandleFunc("GET /portal/key", p.handleKeyPage)
	mux.HandleFunc("POST /portal/key", p.handleKeyIssue)
	mux.HandleFunc("POST /portal/logout", p.handleLogout)
}

// redirectURI is the absolute OAuth callback URL Discord redirects back to. It
// MUST match a redirect URI registered in the Discord application.
func (p *Portal) redirectURI() string {
	return p.baseURL + "/portal/callback"
}

// randNonce returns 32 random bytes as base64url — the OAuth state nonce and
// the cookie value it is compared against.
func randNonce() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// discard is an io.Writer sink for the fallback logger.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
