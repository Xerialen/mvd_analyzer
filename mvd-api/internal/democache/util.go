package democache

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

var shaRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// isValidSHA reports whether s is 64 hex chars.
func isValidSHA(s string) bool { return shaRe.MatchString(s) }

// sha256Hex returns the lowercase hex SHA-256 of b — the same encoding
// used for the cache key, the sha: public address, and the ETag.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// maxDemoUncompressed caps the decompressed size the integrity check will
// admit from a gzip payload, so a decompression bomb can't OOM the process
// (the download itself is already capped in hubfetch). The parser gunzips on a
// magic-byte sniff with NO size limit, so this cap is the only thing bounding
// decompression before the parse slot — it must be enforced whenever the
// payload is gzip, even when the raw bytes already match the expected hash.
// Demos are a few MB; this is very generous headroom.
const maxDemoUncompressed = 512 << 20

// errOverCap signals that a gzip payload's decompressed content exceeds the
// caller's byte cap. Distinguished from a malformed-gzip error so authenticate
// can reject a bomb but fall back to the raw decision for a non-gzip body.
var errOverCap = errors.New("democache: gzip content exceeds decompression cap")

// hasGzipMagic reports whether data starts with the gzip magic (0x1f 0x8b) —
// the same 2-byte sniff mvd-reader/mvdfile uses to decide whether to gunzip.
func hasGzipMagic(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

// gzipContentSHA streams the gzip in data through a SHA-256 hasher, enforcing a
// decompressed-size cap WITHOUT materialising the content (O(32KiB) working set
// via io.Copy, versus the ~1GB transient a 512MiB io.ReadAll would allocate).
// Returns the lowercase hex content hash on success, errOverCap if the stream
// exceeds limit, or the gzip/deflate error for a malformed stream.
func gzipContentSHA(data []byte, limit int64) (string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer gz.Close()
	h := sha256.New()
	// Copy through a LimitReader of limit+1: a stream exactly at limit passes,
	// one byte over is detectable. io.Copy reads to gz EOF, which validates the
	// gzip CRC/ISIZE, so a truncated or corrupt stream surfaces as an error.
	n, err := io.Copy(h, io.LimitReader(gz, limit+1))
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", errOverCap
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// authenticatesToSHA reports whether the downloaded demo bytes authenticate
// against the hub's demo_sha256 (`want`).
//
// The hub's demo_sha256 is the SHA-256 of the UNCOMPRESSED .mvd content, not
// of the gzip that the CDN serves (a gzip's own hash is non-deterministic —
// mtime/OS header, compressor differences). So the CDN download (gzip) is
// authenticated by decompressing it and hashing the content. The
// demo_source_url fallback can already be a raw .mvd, so a direct match on the
// raw bytes is also accepted. A genuinely corrupt/wrong object matches neither
// and is rejected — preserving the phase-3 anti-cache-poisoning guarantee.
func authenticatesToSHA(data []byte, want string) bool {
	return authenticatesToSHALimit(data, want, maxDemoUncompressed)
}

// authenticatesToSHALimit is authenticatesToSHA with an injectable cap so tests
// can exercise the over-cap rejection without a multi-hundred-MB fixture.
//
// A raw-bytes match alone is NOT sufficient when the payload is gzip: the
// parser gunzips on a magic sniff with no limit, so a hub row whose
// demo_sha256 hashes the gzip ITSELF (an attacker-influenced demo_source_url
// fallback) would otherwise smuggle an unbounded decompression into the parse
// slot. So whenever the body has the gzip magic we enforce the decompressed
// cap regardless of a raw match:
//
//   - valid gzip within the cap  → accept if the content OR the raw bytes hash
//     to want (the content match is the normal CDN case);
//   - valid gzip over the cap    → reject even if the raw bytes matched (the
//     decompression-bomb path);
//   - gzip magic but not a decodable gzip → fall back to the raw decision: the
//     parser's own gunzip will fail and surface that error, so a raw match is
//     still allowed to authenticate the bytes as-is.
func authenticatesToSHALimit(data []byte, want string, limit int64) bool {
	rawMatch := sha256Hex(data) == want

	if hasGzipMagic(data) {
		sum, err := gzipContentSHA(data, limit)
		switch {
		case err == nil:
			return sum == want || rawMatch
		case errors.Is(err, errOverCap):
			return false
		default:
			return rawMatch
		}
	}

	// Not gzip: the only valid case is an already-uncompressed .mvd whose hash
	// matches want (the demo_source_url fallback).
	return rawMatch
}

// ParseDemoID parses URL-style identifiers used by the qw-mvd REST
// path segment: "gameId:NNNN" or "sha:HEX". Empty or malformed input
// returns ErrInvalidDemoID.
func ParseDemoID(s string) (DemoID, error) {
	if s == "" {
		return DemoID{}, fmt.Errorf("%w: empty", ErrInvalidDemoID)
	}
	switch {
	case strings.HasPrefix(s, "gameId:"):
		n, err := strconv.Atoi(s[len("gameId:"):])
		if err != nil || n <= 0 {
			return DemoID{}, fmt.Errorf("%w: gameId must be positive integer", ErrInvalidDemoID)
		}
		return DemoID{Kind: "gameId", GameID: n}, nil
	case strings.HasPrefix(s, "sha:"):
		hex := s[len("sha:"):]
		if !isValidSHA(hex) {
			return DemoID{}, fmt.Errorf("%w: sha must be 64 hex chars", ErrInvalidDemoID)
		}
		return DemoID{Kind: "sha256", SHA: strings.ToLower(hex)}, nil
	default:
		return DemoID{}, fmt.Errorf("%w: expected 'gameId:N' or 'sha:HEX'", ErrInvalidDemoID)
	}
}

// String returns the canonical URL form of the DemoID.
func (id DemoID) String() string {
	switch id.Kind {
	case "gameId":
		return fmt.Sprintf("gameId:%d", id.GameID)
	case "sha256":
		return "sha:" + strings.ToLower(id.SHA)
	default:
		return ""
	}
}

// encodeResult / decodeResult round-trip a *Result through gob. Used
// for tier-2 disk storage. Gob is the right choice over JSON here:
// faster, smaller on disk, and lossless for the numeric types in
// Streams (which JSON would coerce to float64).
func encodeResult(r *result.Result) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(r); err != nil {
		return nil, fmt.Errorf("gob encode: %w", err)
	}
	return buf.Bytes(), nil
}

func decodeResult(data []byte) (*result.Result, error) {
	var r result.Result
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&r); err != nil {
		return nil, fmt.Errorf("gob decode: %w", err)
	}
	return &r, nil
}

// writeFileAtomic writes data to path via a temp file in the same
// directory + rename, so a concurrent reader never observes a partial
// file. Creates parent directories as needed.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// On any failure path, remove the temp file if it still exists.
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
