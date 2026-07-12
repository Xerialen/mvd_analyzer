package portal

import (
	"net/http"
	"strings"
	"testing"

	"github.com/mvd-analyzer/mvd-api/internal/authkeys"
)

// fakeStore satisfies KeyStore for config tests that just need a non-nil store.
type fakeStore struct{}

func (fakeStore) Issue(string, string, bool, string) (string, authkeys.Record, error) {
	return "", authkeys.Record{}, nil
}
func (fakeStore) ActiveByDiscordID(string) (authkeys.Record, bool) { return authkeys.Record{}, false }

func TestNewConfigValidation(t *testing.T) {
	ok := func() (string, string, string, []byte, KeyStore) {
		return "https://x", "cid", "csec", []byte("0123456789abcdef"), fakeStore{}
	}
	cases := []struct {
		name    string
		mutate  func() (string, string, string, []byte, KeyStore)
		wantErr string
	}{
		{"valid", ok, ""},
		{"no store (no auth-dir)", func() (string, string, string, []byte, KeyStore) {
			b, c, s, k, _ := ok()
			return b, c, s, k, nil
		}, "auth-dir"},
		{"no base url", func() (string, string, string, []byte, KeyStore) {
			_, c, s, k, st := ok()
			return "", c, s, k, st
		}, "base-url"},
		{"no client id", func() (string, string, string, []byte, KeyStore) {
			b, _, s, k, st := ok()
			return b, "", s, k, st
		}, "CLIENT_ID"},
		{"no client secret", func() (string, string, string, []byte, KeyStore) {
			b, c, _, k, st := ok()
			return b, c, "", k, st
		}, "CLIENT_SECRET"},
		{"short cookie secret", func() (string, string, string, []byte, KeyStore) {
			b, c, s, _, st := ok()
			return b, c, s, []byte("tooshort"), st
		}, "COOKIE_SECRET"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, c, s, k, st := tc.mutate()
			_, err := NewConfig(b, c, s, k, st, nil)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v; want containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestSecureCookieFromScheme pins that the cookie Secure flag follows the base
// URL scheme: https ⇒ Secure (production), http ⇒ not Secure (local dev, so the
// browser will actually send the cookie over plain http). A schemeless/other
// base URL fails safe to Secure.
func TestSecureCookieFromScheme(t *testing.T) {
	cases := []struct {
		baseURL    string
		wantSecure bool
	}{
		{"https://qw.example.com", true},
		{"http://localhost:8080", false},
		{"https://localhost:8080", true},
	}
	for _, tc := range cases {
		t.Run(tc.baseURL, func(t *testing.T) {
			cfg, err := NewConfig(tc.baseURL, "cid", "csec",
				[]byte("0123456789abcdef"), fakeStore{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SecureCookies != tc.wantSecure {
				t.Errorf("SecureCookies = %v; want %v", cfg.SecureCookies, tc.wantSecure)
			}
			p := New(cfg)
			c := p.cookie("x", "v", 3600)
			if c.Secure != tc.wantSecure {
				t.Errorf("cookie.Secure = %v; want %v", c.Secure, tc.wantSecure)
			}
			// SameSite/HttpOnly/Path are unconditional.
			if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != cookiePath {
				t.Errorf("cookie attributes wrong: %+v", c)
			}
		})
	}
}

// TestCookieRoundTrip pins the sign/verify contract independent of the HTTP
// flow: a signed value verifies; any tampering fails.
func TestCookieRoundTrip(t *testing.T) {
	secret := []byte("0123456789abcdef-secret")
	payload := []byte(`{"id":"1","name":"a","exp":9999999999}`)
	signed := signValue(secret, payload)

	got, err := verifyValue(secret, signed)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("round trip = %q; want %q", got, payload)
	}
	// Wrong secret fails.
	if _, err := verifyValue([]byte("different-secret-XXXXX"), signed); err == nil {
		t.Error("verify accepted a value signed with a different secret")
	}
	// Tampered payload fails.
	if _, err := verifyValue(secret, flipFirstByte(signed)); err == nil {
		t.Error("verify accepted a tampered value")
	}
	// Missing separator fails.
	if _, err := verifyValue(secret, "no-dot-here"); err == nil {
		t.Error("verify accepted a value with no signature separator")
	}
}
