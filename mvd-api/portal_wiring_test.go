package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mvd-analyzer/mvd-api/internal/authkeys"
	"github.com/mvd-analyzer/mvd-api/internal/portal"
)

// noRedirect is a client that never follows redirects, so a 302 is observable.
func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// TestPortalOffByDefault: when no portal is wired (p == nil), /portal is not a
// registered route and returns 404 — today's behaviour is unchanged.
func TestPortalOffByDefault(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(newRouter(&fakeStore{}, logger, "", nil, nil))
	defer srv.Close()

	resp, err := noRedirect().Get(srv.URL + "/portal")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/portal with portal off = %d; want 404", resp.StatusCode)
	}
}

// TestPortalExemptFromAPIKey: with BOTH auth and the portal enabled, /portal is
// reachable WITHOUT an API key (the phase-14 exemption), while a protected /v1
// route still 401s. Proves the portal sits in front of the API-key gate.
func TestPortalExemptFromAPIKey(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := authkeys.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	auth := &authenticator{
		store: store,
		limiter: newKeyLimiter(
			rateClass{rate: 1000, burst: 1000},
			rateClass{rate: 1000, burst: 1000},
		),
		logger: logger,
	}
	cfg, err := portal.NewConfig(
		"https://portal.example.com",
		"client-id", "client-secret",
		[]byte("0123456789abcdef-secret"),
		store, logger,
	)
	if err != nil {
		t.Fatal(err)
	}
	p := portal.New(cfg)

	srv := httptest.NewServer(newRouter(&fakeStore{}, logger, "", auth, p))
	defer srv.Close()

	// /portal: 200 without any key.
	resp, err := noRedirect().Get(srv.URL + "/portal")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/portal keyless = %d; want 200 (exempt from API-key auth)", resp.StatusCode)
	}

	// A protected /v1 route still requires a key.
	resp2, err := noRedirect().Get(srv.URL + "/v1/auth/check")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/v1/auth/check keyless = %d; want 401", resp2.StatusCode)
	}
}
