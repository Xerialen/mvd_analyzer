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

const requestIDKey ctxKey = iota

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

// accessLogMiddleware emits one structured line per request. The
// optional Bearer label (or ?label= query param) is captured for
// request-source analytics — it's not a secret and is never validated.
func accessLogMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rr := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(rr, r)

		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rr.status,
			"bytes", rr.bytes,
			"latency_ms", time.Since(start).Milliseconds(),
			"remote", clientIP(r),
			"label", requestLabel(r),
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
func requestLabel(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return r.URL.Query().Get("label")
}
