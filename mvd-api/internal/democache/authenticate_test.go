package democache

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func gzipOf(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestAuthenticatesToSHA covers the hub's semantics: demo_sha256 is the hash
// of the UNCOMPRESSED demo, while the CDN serves gzip. The real bug this
// guards: hashing the raw gzip bytes rejects every cold CDN download.
func TestAuthenticatesToSHA(t *testing.T) {
	const content = "MVD demo uncompressed content \x00\x01\x02"
	want := sha256Hex([]byte(content))

	// CDN case: gzip whose *decompressed* content hashes to demo_sha256.
	if gz := gzipOf(t, content); !authenticatesToSHA(gz, want) {
		t.Errorf("gzip download whose content hashes to demo_sha256 was rejected")
	}
	// Source fallback case: already-uncompressed .mvd equal to demo_sha256.
	if !authenticatesToSHA([]byte(content), want) {
		t.Errorf("raw .mvd matching demo_sha256 was rejected")
	}
	// Corruption: neither raw nor decompressed matches.
	if authenticatesToSHA([]byte("totally different bytes"), want) {
		t.Errorf("corrupt bytes authenticated")
	}
	if authenticatesToSHA(gzipOf(t, "a different demo entirely"), want) {
		t.Errorf("gzip of the wrong content authenticated")
	}
	// The raw gzip's own hash must NOT be what we compare against (the phase-3
	// regression): a gzip whose *raw* bytes equal want but whose *content*
	// does not would only pass the raw branch, which is fine; but the CDN case
	// above is the one that was broken.
}
