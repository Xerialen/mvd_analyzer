package main

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mvd-analyzer/mvd-api/internal/authkeys"
)

// authenticator is the auth + per-key rate-limit middleware installed only
// when `-auth-dir` is set (PLAN-hosting D8). When it is empty the middleware
// is never constructed and the chain is byte-identical to localhost mode.
type authenticator struct {
	store   *authkeys.Store
	limiter *keyLimiter
	logger  *slog.Logger
}

// authExempt reports whether a path may be reached without a key. Everything
// under /v1/ (and POST /v1/demos/{id}) requires a key; these are the carve-
// outs: liveness, the build stamp, and the (reserved) portal prefix. The
// portal (phase 15) does its own Discord-cookie auth, so it must not sit
// behind the API-key gate. Note /v1/auth/check is deliberately NOT exempt —
// it is the check itself.
func authExempt(path string) bool {
	switch path {
	case "/healthz", "/v1/version":
		return true
	}
	return strings.HasPrefix(path, "/portal/") || path == "/portal"
}

// bearerToken returns the raw token from an "Authorization: Bearer <token>"
// header, or "" if absent/malformed. The scheme match is case-insensitive per
// RFC 7235; the token is returned verbatim (it is the secret key).
func bearerToken(r *http.Request) string {
	const prefix = "bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}

// unauthorized writes the generic 401. The body must not reveal whether the
// key was absent, malformed, or revoked (writeError + a fixed message), and
// WWW-Authenticate advertises the scheme per RFC 7235.
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API key")
}

// tooManyRequests writes the generic 429 with a Retry-After (whole seconds,
// rounded up, minimum 1) so a client knows when to retry.
func tooManyRequests(w http.ResponseWriter, retryAfter time.Duration) {
	secs := int(retryAfter / time.Second)
	if time.Duration(secs)*time.Second < retryAfter {
		secs++ // round up any fractional remainder
	}
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded")
}

// middleware gates every non-exempt request on a valid API key, then applies
// the per-key rate limit. It sits INSIDE cors (so preflight is answered before
// auth) and INSIDE accessLog (so 401s/429s are logged), per the phase-13
// handoff chain requestID → cors → accessLog → auth → recover → mux.
func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Tell accessLog that auth ran, so it never falls back to the raw
		// Bearer label. Do this for every request that reaches auth, incl.
		// exempt paths and preflight.
		if info := reqInfoFrom(r.Context()); info != nil {
			info.authApplied = true
		}

		// OPTIONS preflight is answered by cors before we get here; belt-and-
		// suspenders, never gate a preflight on a key.
		if r.Method == http.MethodOptions || authExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		rec, err := a.store.Lookup(bearerToken(r))
		if err != nil {
			// Same generic 401 for absent / malformed / revoked (do not leak
			// which). ErrUnknownKey is the only expected error.
			if !errors.Is(err, authkeys.ErrUnknownKey) {
				a.logger.Error("authkeys lookup", "err", err.Error())
			}
			unauthorized(w)
			return
		}

		// Log identity is the safe label — note, else Discord name, else the
		// hash prefix. Never the key or the full hash.
		if info := reqInfoFrom(r.Context()); info != nil {
			info.identity = logIdentity(rec)
		}

		// Per-key rate limit (D8). Only authenticated keys reach here, so the
		// bucket map cannot be grown by unknown traffic.
		if ok, retry := a.limiter.allow(rec.KeyHash, rec.Service); !ok {
			tooManyRequests(w, retry)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// logIdentity picks the non-secret access-log identity for a key: note, else
// Discord name, else the hash prefix. Always non-secret.
func logIdentity(rec authkeys.Record) string {
	switch {
	case rec.Note != "":
		return rec.Note
	case rec.DiscordName != "":
		return rec.DiscordName
	default:
		return rec.HashPrefix()
	}
}

// handleAuthCheck: GET /v1/auth/check — 204 for a valid key, 401 otherwise.
// The auth middleware has already validated the key by the time this runs (the
// route is not exempt), so reaching the handler means the key is good.
func (s *server) handleAuthCheck(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
