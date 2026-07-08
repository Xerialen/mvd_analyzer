package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// ctxKey is the private type for this package's context values.
type ctxKey int

const (
	requestIDKey ctxKey = iota
	reqInfoKey
)

// reqInfo is a small mutable per-request scratch that inner middleware fills
// in and the outer accessLog reads after the handler returns. It exists to
// bridge a context-propagation gap: a middleware that does r =
// r.WithContext(...) mutates only its own *http.Request, so the outer
// accessLog (which wraps auth) never sees a value auth set on its copy. By
// stashing a *reqInfo pointer in the context *before* calling next, auth can
// write through the pointer and accessLog reads the same struct.
type reqInfo struct {
	// identity is the non-secret access-log label in auth mode: the key's
	// note / Discord name / hash-prefix. Never the key or the full hash.
	identity string
	// authApplied is set by authMiddleware when it runs. It tells accessLog
	// the request went through auth, so accessLog must NOT fall back to
	// requestLabel(r) (which returns the raw Bearer — the secret key in this
	// mode) when identity is empty (an unauthenticated 401).
	authApplied bool
}

// reqInfoFrom returns the request's *reqInfo, or nil if accessLog did not run
// (e.g. in a unit test that exercises a bare handler).
func reqInfoFrom(ctx context.Context) *reqInfo {
	if v, ok := ctx.Value(reqInfoKey).(*reqInfo); ok {
		return v
	}
	return nil
}

// requestID returns the per-request id set by requestIDMiddleware, or "".
func requestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// newRequestID returns 8 random bytes as hex. crypto/rand failure (never
// expected) falls back to a timestamp so the id is still non-empty.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// requestIDMiddleware assigns each request an id, exposes it as the
// X-Request-Id response header, and stashes it in the context so handlers
// and the 5xx paths can correlate a generic client message with the
// detailed server log line (F19).
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// corsMiddleware makes the read-only API callable from browser apps on any
// origin (F17). The API is unauthenticated today and the Bearer value is a
// non-secret label, so `*` is safe. Expose-Headers is required for browser
// JS to read ETag/X-Cache/X-Schema-Version/X-Request-Id off the response.
// OPTIONS preflight is answered here — 204, no auth, on every path.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Expose-Headers", "ETag, X-Cache, X-Schema-Version, X-Request-Id")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-None-Match")
			h.Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recoverMiddleware turns a panic into a 500 + slog error line so a single
// buggy handler can't take down the server. The response body is generic
// (the request id, not the panic value) — the panic + stack goes to the
// log keyed by the same id (F19).
func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				id := requestID(r.Context())
				logger.Error("panic in handler",
					"request_id", id, "method", r.Method, "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "internal", genericInternalMsg(id))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// responseRecorder captures status + bytes written for the access log.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.status = code
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if rr.status == 0 {
		rr.status = http.StatusOK
	}
	n, err := rr.ResponseWriter.Write(b)
	rr.bytes += n
	return n, err
}

// accessLogMiddleware emits one structured line per request.
//
// The `label` field is the request's identity. Its source depends on the
// mode, and the distinction is security-critical:
//
//   - No-auth (localhost) mode: the label is the non-secret Bearer *label* or
//     ?label= param (requestLabel) — a traffic-source hint that is never
//     validated and carries no secret.
//   - Auth mode: the Bearer value IS the secret API key and must never be
//     logged. authMiddleware (which this wraps) writes a safe identity — the
//     key's note / Discord name / hash-prefix — into the shared *reqInfo, and
//     we log that instead. requestLabel is NOT consulted in auth mode.
//
// The *reqInfo is seeded here, before next runs, so the inner auth middleware
// can fill it in through the pointer (see reqInfo's doc for why a plain
// context value would not propagate outward).
func accessLogMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &reqInfo{}
		r = r.WithContext(context.WithValue(r.Context(), reqInfoKey, info))
		rr := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(rr, r)

		// In auth mode `info.identity` is set (possibly to "" for an
		// unauthenticated 401, which is correct — we have no identity and
		// must not fall back to the raw Bearer). In no-auth mode auth never
		// ran, so info.identity is "" and we use the non-secret label.
		label := info.identity
		if label == "" && !info.authApplied {
			label = requestLabel(r)
		}

		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rr.status,
			"bytes", rr.bytes,
			"latency_ms", time.Since(start).Milliseconds(),
			"remote", clientIP(r),
			"label", label,
			"cache", w.Header().Get("X-Cache"),
			"request_id", requestID(r.Context()),
		)
	})
}

// clientIP is a best-effort remote-address for the access log only. It
// trusts the first X-Forwarded-For entry, which is attacker-controlled
// unless a trusted proxy overwrites XFF at the edge — so it must NOT be
// used for any security decision (e.g. rate limiting keys on the API key,
// not this; see PLAN-hosting D8/D9). Log-only.
func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if comma := strings.IndexByte(xf, ','); comma > 0 {
			return strings.TrimSpace(xf[:comma])
		}
		return strings.TrimSpace(xf)
	}
	return r.RemoteAddr
}

// requestLabel extracts the non-secret traffic-source label from
// Authorization: Bearer <label> or ?label=<label>. Returns "" when
// neither is set.
//
// This is only consulted in NO-AUTH (localhost) mode. In auth mode the Bearer
// value is the secret API key and must never be logged — accessLog uses the
// safe identity authMiddleware writes into reqInfo instead (see
// accessLogMiddleware).
func requestLabel(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return r.URL.Query().Get("label")
}
