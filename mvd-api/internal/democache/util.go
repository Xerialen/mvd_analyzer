package democache

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
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
