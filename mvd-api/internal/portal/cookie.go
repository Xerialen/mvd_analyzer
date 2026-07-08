package portal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Cookie names. All are Path=/portal, HttpOnly, Secure, SameSite=Lax.
const (
	sessionCookie = "mvd_portal_session"
	stateCookie   = "mvd_portal_state"
)

// cookiePath scopes every portal cookie to /portal so it is never sent to the
// API surface (/v1/*) — the API authenticates with Bearer keys, not cookies.
const cookiePath = "/portal"

// session is the payload carried by the signed session cookie. It is the only
// server-trusted state (there is no server-side session store, D5): identity is
// re-derived from the cookie on every request and re-verified (HMAC + expiry).
type session struct {
	DiscordID   string `json:"id"`
	DiscordName string `json:"name"`
	Exp         int64  `json:"exp"` // unix seconds
}

// errBadCookie is the single opaque error for every cookie failure (absent,
// malformed, bad signature, expired). Callers must not branch on the cause —
// the response is uniformly "not signed in".
var errBadCookie = errors.New("portal: invalid session cookie")

// signValue encodes payload and appends an HMAC-SHA256 tag over it:
//
//	base64url(payload) "." base64url(HMAC(secret, base64url(payload)))
//
// The MAC is computed over the ENCODED payload string (what is transmitted),
// so verification never has to re-encode to compare. RawURLEncoding avoids
// '/', '+', '=' so the value is cookie-safe without quoting.
func signValue(secret, payload []byte) string {
	enc := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmacTag(secret, []byte(enc))
	return enc + "." + base64.RawURLEncoding.EncodeToString(mac)
}

// verifyValue checks the HMAC of a signed value in constant time and returns
// the decoded payload. It does NOT interpret the payload (no expiry check) —
// that is the caller's job, so this helper serves both the session cookie
// (which has an expiry) and the state cookie (which does not).
func verifyValue(secret []byte, value string) ([]byte, error) {
	dot := strings.LastIndexByte(value, '.')
	if dot <= 0 || dot == len(value)-1 {
		return nil, errBadCookie
	}
	enc, sig := value[:dot], value[dot+1:]
	gotMAC, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, errBadCookie
	}
	wantMAC := hmacTag(secret, []byte(enc))
	// Constant-time compare: a byte-by-byte early return would leak how much of
	// a forged tag is correct via timing.
	if !hmac.Equal(gotMAC, wantMAC) {
		return nil, errBadCookie
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, errBadCookie
	}
	return payload, nil
}

func hmacTag(secret, msg []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(msg)
	return h.Sum(nil)
}

// setSessionCookie signs a session valid for sessionTTL and writes it.
func (p *Portal) setSessionCookie(w http.ResponseWriter, discordID, discordName string) {
	s := session{
		DiscordID:   discordID,
		DiscordName: discordName,
		Exp:         p.now().Add(sessionTTL).Unix(),
	}
	payload, _ := json.Marshal(s) // a struct of strings+int never fails to marshal
	http.SetCookie(w, p.cookie(sessionCookie, signValue(p.cookieSecret, payload), int(sessionTTL.Seconds())))
}

// readSession verifies the session cookie and returns the identity, or
// errBadCookie for any failure (absent/tampered/expired). Expiry is checked
// against p.now() on every read — a valid HMAC on a stale payload is still
// rejected.
func (p *Portal) readSession(r *http.Request) (session, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, errBadCookie
	}
	payload, err := verifyValue(p.cookieSecret, c.Value)
	if err != nil {
		return session{}, errBadCookie
	}
	var s session
	if err := json.Unmarshal(payload, &s); err != nil {
		return session{}, errBadCookie
	}
	if s.DiscordID == "" || p.now().Unix() >= s.Exp {
		return session{}, errBadCookie
	}
	return s, nil
}

// clearSessionCookie expires the session cookie. A negative Max-Age is
// serialised by net/http as the literal `Max-Age=0` on the wire, a valid cookie
// deletion directive.
func (p *Portal) clearSessionCookie(w http.ResponseWriter) {
	c := p.cookie(sessionCookie, "", -1)
	http.SetCookie(w, c)
}

// setStateCookie stores a signed OAuth state nonce for the CSRF double-submit
// check in the callback. Short-lived (stateTTL) — it only has to survive the
// round trip to Discord's consent screen.
func (p *Portal) setStateCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, p.cookie(stateCookie, signValue(p.cookieSecret, []byte(state)), int(stateTTL.Seconds())))
}

// readStateCookie returns the verified state nonce from the state cookie, or
// errBadCookie. No expiry is embedded; the cookie's Max-Age bounds its life.
func (p *Portal) readStateCookie(r *http.Request) (string, error) {
	c, err := r.Cookie(stateCookie)
	if err != nil {
		return "", errBadCookie
	}
	payload, err := verifyValue(p.cookieSecret, c.Value)
	if err != nil {
		return "", errBadCookie
	}
	return string(payload), nil
}

func (p *Portal) clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, p.cookie(stateCookie, "", -1))
}

// cookie builds a portal cookie with the security attributes every portal
// cookie shares (D5):
//
//   - HttpOnly: JS cannot read it (defence against XSS session theft).
//   - Secure: set when the base URL is https (production, the norm). It is
//     disabled ONLY for an http base URL (local dev), because a browser refuses
//     to SEND a Secure cookie over plain http — so the documented
//     http://localhost dev redirect could otherwise never receive the session/
//     state cookie. Such a config must never be used in production; the server
//     logs a startup warning when it happens (see New). p.secureCookies carries
//     the decision.
//   - SameSite=Lax: the browser withholds the cookie on cross-site POSTs, which
//     is what protects POST /portal/key and /portal/logout from CSRF. (The
//     OAuth flow is a top-level GET navigation, which Lax DOES send — so the
//     login/callback round trip works; the `state` nonce, not SameSite, is what
//     guards the callback.)
//   - Path=/portal: never leaks to /v1/*.
func (p *Portal) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     cookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   p.secureCookies,
		SameSite: http.SameSiteLaxMode,
	}
}
