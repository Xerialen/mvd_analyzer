package portal

import (
	"net/http"
)

// handleLanding: GET /portal — the public landing page. No auth. Describes the
// service, links to the API docs, and offers the Discord sign-in button.
func (p *Portal) handleLanding(w http.ResponseWriter, r *http.Request) {
	// If already signed in, send them straight to their key page.
	if _, err := p.readSession(r); err == nil {
		http.Redirect(w, r, "/portal/key", http.StatusFound)
		return
	}
	p.render(w, "landing.html", pageData{})
}

// handleLogin: GET /portal/login — mint a state nonce, store it in a signed
// cookie, and 302 to Discord's consent screen. The nonce is echoed back to the
// callback and must match the cookie (CSRF double-submit).
func (p *Portal) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := p.randState()
	if err != nil {
		p.serverError(w, r, "state nonce", err)
		return
	}
	p.setStateCookie(w, state)
	http.Redirect(w, r, p.authorizeURL(state), http.StatusFound)
}

// handleCallback: GET /portal/callback — the OAuth redirect target.
//
// CSRF: the `state` query param MUST equal the signed state cookie. A mismatch
// or an absent cookie is rejected with 400 BEFORE any token exchange — a forged
// callback (attacker's code, victim's browser) never reaches Discord.
func (p *Portal) handleCallback(w http.ResponseWriter, r *http.Request) {
	// Clear the one-shot state cookie regardless of outcome. This must be a
	// Set-Cookie header emitted BEFORE any body/redirect write (a header set
	// after WriteHeader is silently dropped), so do it up front — reading the
	// request cookie below is independent of this response header.
	p.clearStateCookie(w)

	if derr := r.URL.Query().Get("error"); derr != "" {
		// User denied consent, or Discord rejected the request. Not our error.
		p.clientError(w, http.StatusBadRequest, "Discord sign-in was cancelled or denied.")
		return
	}

	wantState, err := p.readStateCookie(r)
	if err != nil {
		p.clientError(w, http.StatusBadRequest, "Invalid or missing sign-in state. Please start again.")
		return
	}
	gotState := r.URL.Query().Get("state")
	// Constant-time-ish: states are single-use nonces, but compare fully.
	if gotState == "" || gotState != wantState {
		p.clientError(w, http.StatusBadRequest, "Sign-in state mismatch. Please start again.")
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		p.clientError(w, http.StatusBadRequest, "Missing authorization code.")
		return
	}

	token, err := p.exchangeCode(r.Context(), code)
	if err != nil {
		p.serverError(w, r, "token exchange", err)
		return
	}
	user, err := p.fetchUser(r.Context(), token)
	if err != nil {
		p.serverError(w, r, "fetch user", err)
		return
	}

	p.setSessionCookie(w, user.ID, user.Username)
	http.Redirect(w, r, "/portal/key", http.StatusFound)
}

// handleKeyPage: GET /portal/key — requires a valid session. Shows the user's
// current key STATUS (prefix + created date) if they have one, plus the
// generate/regenerate form. Never shows a full key here (only POST does, once).
func (p *Portal) handleKeyPage(w http.ResponseWriter, r *http.Request) {
	sess, err := p.readSession(r)
	if err != nil {
		http.Redirect(w, r, "/portal", http.StatusFound)
		return
	}
	data := pageData{DiscordName: sess.DiscordName}
	if rec, ok := p.store.ActiveByDiscordID(sess.DiscordID); ok {
		data.HasKey = true
		data.HashPrefix = rec.HashPrefix()
		data.Created = rec.Created
	}
	p.render(w, "key.html", data)
}

// handleKeyIssue: POST /portal/key — requires a valid session. Issues a new key
// (revoking any prior key for this Discord id, enforced in the store, D4) and
// renders the full plaintext key EXACTLY ONCE. SameSite=Lax on the session
// cookie is what makes this POST safe from cross-site CSRF (a cross-site form
// POST would not carry the cookie).
func (p *Portal) handleKeyIssue(w http.ResponseWriter, r *http.Request) {
	sess, err := p.readSession(r)
	if err != nil {
		http.Redirect(w, r, "/portal", http.StatusFound)
		return
	}
	key, rec, err := p.store.Issue(sess.DiscordID, sess.DiscordName, false, "portal")
	if err != nil {
		p.serverError(w, r, "issue key", err)
		return
	}
	p.render(w, "issued.html", pageData{
		DiscordName: sess.DiscordName,
		// The full key is rendered here, this once. It is never stored in
		// plaintext and cannot be shown again.
		FullKey:    key,
		HashPrefix: rec.HashPrefix(),
		Created:    rec.Created,
	})
}

// handleLogout: POST /portal/logout — clear the session cookie and return to
// the landing page. POST (not GET) + SameSite=Lax so it cannot be triggered
// cross-site.
func (p *Portal) handleLogout(w http.ResponseWriter, r *http.Request) {
	p.clearSessionCookie(w)
	http.Redirect(w, r, "/portal", http.StatusFound)
}
