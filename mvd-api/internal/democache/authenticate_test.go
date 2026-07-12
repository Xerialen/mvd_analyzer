package democache

import (
	"bytes"
	"compress/gzip"
	"strings"
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

// TestAuthenticatesToSHA_CapEnforcedOnRawMatch pins FIX 5: the decompressed-size
// cap must hold even when the RAW (compressed) bytes already hash to want. The
// parser gunzips on a magic sniff with no limit, so a hub row whose demo_sha256
// hashes the gzip itself would otherwise smuggle an unbounded decompression
// into the parse slot. A tiny injected cap stands in for the 512MiB production
// one so the fixture stays small.
func TestAuthenticatesToSHA_CapEnforcedOnRawMatch(t *testing.T) {
	const limit = 64

	// A gzip whose decompressed content (200 bytes) exceeds the cap, addressed
	// by the hash of its COMPRESSED bytes (the smuggling vector).
	bomb := gzipOf(t, strings.Repeat("A", 200))
	rawWant := sha256Hex(bomb)
	if !authenticatesToSHALimit(bomb, rawWant, 1<<30) {
		t.Fatal("precondition: raw-hash match should authenticate under a huge cap")
	}
	if authenticatesToSHALimit(bomb, rawWant, limit) {
		t.Error("gzip over the cap authenticated via its raw hash — decompression bomb admitted")
	}

	// A gzip whose CONTENT is within the cap and hashes to want still passes
	// (normal CDN case), proving the cap check does not over-reject.
	small := "within-cap demo content"
	if len(small) > limit {
		t.Fatalf("test bug: small content %d exceeds cap %d", len(small), limit)
	}
	if !authenticatesToSHALimit(gzipOf(t, small), sha256Hex([]byte(small)), limit) {
		t.Error("gzip whose content is within the cap and hashes to want was rejected")
	}
}

// TestAuthenticatesToSHA_RawMatchAcceptsNonGzip pins the two raw-match cases
// that must still authenticate under the cap logic: a plain (non-gzip) body,
// and a body that starts with the gzip magic but is not a decodable gzip (the
// parser's own gunzip will fail and surface that, so we authenticate the raw
// bytes and let the parser report the real error).
func TestAuthenticatesToSHA_RawMatchAcceptsNonGzip(t *testing.T) {
	const limit = 64

	plain := []byte("a plain uncompressed .mvd body")
	if !authenticatesToSHALimit(plain, sha256Hex(plain), limit) {
		t.Error("non-gzip body matching want by raw hash was rejected")
	}

	// Gzip magic + a valid 10-byte header (method=deflate) but a garbage
	// deflate stream: gzipContentSHA fails, so we fall back to the raw decision.
	fakeGzip := append([]byte{0x1f, 0x8b, 0x08, 0, 0, 0, 0, 0, 0, 0x03}, []byte("not-a-deflate-stream")...)
	if !hasGzipMagic(fakeGzip) {
		t.Fatal("test bug: fixture lacks gzip magic")
	}
	if !authenticatesToSHALimit(fakeGzip, sha256Hex(fakeGzip), limit) {
		t.Error("gzip-magic-but-corrupt body matching want by raw hash was rejected")
	}
}
