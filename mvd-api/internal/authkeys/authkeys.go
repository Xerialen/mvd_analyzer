// Package authkeys is the API-key store for mvd-api's authenticated
// hosting mode (PLAN-hosting D2/D3/D4).
//
// The whole store is a single JSON file (keys.json) under a configurable
// directory, loaded into an in-memory map at Open and rewritten atomically
// on every mutation. Expected scale is tens-to-hundreds of keys, so a file
// is deliberately chosen over a database (D2).
//
// Keys are secrets: the plaintext key is shown exactly once, at Issue, and
// only its SHA-256 hash is ever persisted (D3). Lookup hashes the presented
// key and compares against the stored hash with a constant-time compare so
// a caller cannot learn a valid hash by timing the comparison.
package authkeys

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// KeyPrefix marks every issued key so it is self-identifying in configs and
// grep-able in logs/leaks (D3).
const KeyPrefix = "qwmvd_"

// ErrUnknownKey is returned by Lookup for a key that is absent, malformed, or
// revoked. It deliberately does not distinguish those cases: a caller (the
// auth middleware) must return the same generic 401 for all of them so it
// leaks nothing about which keys exist.
var ErrUnknownKey = errors.New("authkeys: unknown or revoked key")

// Record is the persisted metadata for one key. The plaintext key is never
// stored — only KeyHash (hex SHA-256 of the key).
type Record struct {
	KeyHash     string `json:"keyHash"`
	DiscordID   string `json:"discordId,omitempty"`
	DiscordName string `json:"discordName,omitempty"`
	Service     bool   `json:"service,omitempty"`
	Note        string `json:"note,omitempty"`
	Created     string `json:"created"`           // RFC3339
	Revoked     string `json:"revoked,omitempty"` // RFC3339 revoked-at; empty = active
}

// Active reports whether the record can authenticate.
func (r Record) Active() bool { return r.Revoked == "" }

// HashPrefix is the short, non-secret identifier for logs and `keys list`:
// the first 8 hex chars of the key hash. Never log the full hash (it is the
// verifier) or the key (it is the secret).
func (r Record) HashPrefix() string {
	if len(r.KeyHash) >= 8 {
		return r.KeyHash[:8]
	}
	return r.KeyHash
}

// Store is a concurrency-safe key store backed by keys.json under dir.
//
// Two layers of locking guard it. The in-process RWMutex (mu) serialises
// goroutines within one process. A cross-process advisory file lock (flock on
// dir/keys.json) serialises MUTATIONS across independent processes — the live
// server's portal and an operator's `keys` CLI can both hold a *Store on the
// same dir at once, and without the flock the loser of a concurrent
// read-modify-write would silently clobber the winner's key. Every mutation
// (Issue/Revoke) takes the flock, RELOADS keys.json from disk under it (so it
// applies its change to the latest on-disk state, not a possibly-stale
// in-memory map), applies, writes atomically, then releases.
//
// Lookup additionally does an mtime-checked TTL reload: at most once per
// reloadTTL it stats keys.json and, if the mtime changed, reloads under the
// flock before answering. This closes the fail-OPEN gap where a key revoked by
// a SEPARATE process (the `keys` CLI, per the day-2 runbook) would keep
// authenticating on the running server until a portal op or restart — the file
// is the only thing the CLI touches, and only this process's own mutations
// reloaded it. List still takes only the RWMutex and may lag one TTL, which is
// acceptable for a management listing. The steady-state auth path pays at most
// one cheap stat per TTL and no flock.
type Store struct {
	dir  string
	path string

	mu   sync.RWMutex
	recs map[string]Record // keyHashHex -> record

	// reloadTTL bounds how often Lookup will re-check keys.json for an
	// out-of-process change (see maybeReloadForLookup). 0 → defaultReloadTTL;
	// tests set a small value. nowFn is the injectable clock (mirrors
	// ratelimit.go's nowFn) so those tests need no real sleeps; nil → time.Now.
	reloadTTL time.Duration
	nowFn     func() time.Time

	// lastCheck is when Lookup last decided whether to re-stat keys.json;
	// lastMod is the keys.json mtime observed at the last (re)load. Both are
	// guarded by mu and drive the mtime-checked TTL reload.
	lastCheck time.Time
	lastMod   time.Time
}

// keysFileName is the single JSON file the store persists to.
const keysFileName = "keys.json"

// defaultReloadTTL is how stale a Lookup tolerates its in-memory map before it
// re-checks keys.json for an out-of-process mutation. Small enough that a CLI
// revocation takes effect promptly on the running server, large enough that the
// hot auth path almost never pays for a stat.
const defaultReloadTTL = 2 * time.Second

// now returns the current time via the injectable clock (time.Now in prod).
func (s *Store) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

// Open loads the store from dir/keys.json, creating dir (and starting empty)
// if the file does not exist.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("authkeys: empty dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("authkeys: mkdir %s: %w", dir, err)
	}
	// MkdirAll does not tighten a pre-existing dir, so an operator pointing
	// -auth-dir at an existing 0755/0777 dir would leave the key metadata
	// world-listable (keys.json is 0600, so no secret leaks, but the listing
	// and per-key metadata would be exposed). Force it to 0700.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("authkeys: chmod %s: %w", dir, err)
	}
	s := &Store{
		dir:  dir,
		path: filepath.Join(dir, keysFileName),
		recs: make(map[string]Record),
	}
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	s.noteReloaded()
	return s, nil
}

// noteReloaded records that s.recs now reflects the current on-disk keys.json:
// it stamps lastCheck to now and lastMod to the file's mtime, so the next
// Lookup's TTL window starts here and a subsequent stat only reloads on a real
// change. Caller holds s.mu for write. A missing file (empty store) leaves
// lastMod zero, which simply forces the first post-TTL Lookup to (cheaply)
// re-check.
func (s *Store) noteReloaded() {
	s.lastCheck = s.now()
	if fi, err := os.Stat(s.path); err == nil {
		s.lastMod = fi.ModTime()
	} else {
		s.lastMod = time.Time{}
	}
}

// reloadLocked replaces s.recs with the current on-disk contents of keys.json.
// A missing file resets to an empty store (matching first-run Open). The caller
// must hold s.mu for write; mutations additionally hold the cross-process flock
// so the reload observes the latest committed state before applying a change.
func (s *Store) reloadLocked() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.recs = make(map[string]Record)
		return nil
	}
	if err != nil {
		return fmt.Errorf("authkeys: read %s: %w", s.path, err)
	}
	var recs []Record
	if len(data) > 0 {
		if err := json.Unmarshal(data, &recs); err != nil {
			return fmt.Errorf("authkeys: parse %s: %w", s.path, err)
		}
	}
	m := make(map[string]Record, len(recs))
	for _, r := range recs {
		if r.KeyHash == "" {
			continue
		}
		m[r.KeyHash] = r
	}
	s.recs = m
	return nil
}

// hashKey returns the lowercase hex SHA-256 of the plaintext key — the value
// persisted and compared against.
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// newKey mints a fresh plaintext key: KeyPrefix + base64url(32 random bytes),
// no padding. 32 bytes = 256 bits of entropy from crypto/rand.
func newKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("authkeys: crypto/rand: %w", err)
	}
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Issue mints a new key, persists its record, and returns the plaintext key
// (shown exactly once — it is never stored or recoverable afterwards).
//
// D4 invariant, enforced here in the store so no caller can forget it: at
// most one active key per Discord user. Issuing for a discordID that already
// has an active key revokes the prior one first, so the old key stops
// authenticating the moment the new one is issued.
func (s *Store) Issue(discordID, discordName string, service bool, note string) (string, Record, error) {
	key, err := newKey()
	if err != nil {
		return "", Record{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := Record{
		KeyHash:     hashKey(key),
		DiscordID:   discordID,
		DiscordName: discordName,
		Service:     service,
		Note:        note,
		Created:     now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	unlock, err := s.flockLocked()
	if err != nil {
		return "", Record{}, err
	}
	defer unlock()

	// Apply the mutation to the LATEST on-disk state, not a stale in-memory
	// map: another process (the CLI, or another server worker) may have issued
	// or revoked since we last wrote. reloadLocked runs under the flock so no
	// concurrent mutation can slip between the reload and the write.
	if err := s.reloadLocked(); err != nil {
		return "", Record{}, err
	}

	// Enforce one-active-key-per-Discord-user (D4). Service keys are ops-
	// issued and not portal-bound, so an empty discordID never collides.
	// Snapshot each prior active key before revoking it so a saveLocked
	// failure below can restore the full pre-Issue state — not just delete the
	// new record — keeping the in-memory map identical to disk. Mirrors
	// Revoke's rollback.
	prev := make(map[string]Record)
	if discordID != "" {
		for hash, existing := range s.recs {
			if existing.DiscordID == discordID && existing.Active() {
				prev[hash] = existing // pre-mutation copy (Record is a value type)
				existing.Revoked = now
				s.recs[hash] = existing
			}
		}
	}
	// A hash collision would silently overwrite a live record; 256-bit keys
	// make this impossible in practice, but guard anyway.
	if _, exists := s.recs[rec.KeyHash]; exists {
		return "", Record{}, errors.New("authkeys: key hash collision")
	}
	s.recs[rec.KeyHash] = rec
	if err := s.saveLocked(); err != nil {
		// Roll back to the pre-Issue state: drop the new record AND un-revoke
		// the prior active key. Without the latter, memory would show the old
		// key revoked while disk still has it active — an asymmetric divergence
		// (Revoke rolls back fully).
		delete(s.recs, rec.KeyHash)
		for hash, r := range prev {
			s.recs[hash] = r
		}
		return "", Record{}, err
	}
	s.noteReloaded() // s.recs now equals the file we just wrote
	return key, rec, nil
}

// maybeReloadForLookup refreshes s.recs from keys.json when the file has
// changed since the last check, but at most once per reloadTTL so the hot auth
// path stays cheap. Without it, a key revoked by a separate process (the `keys`
// CLI) never reaches this process's map — only its own Issue/Revoke reload —
// so revocation would fail OPEN until a portal op or restart. keys.json is
// replaced atomically via rename, so an mtime change is a sound change signal.
//
// It never upgrades a read lock in place: it reads lastCheck under the read
// lock for the fast path, then re-checks and mutates under the write lock.
func (s *Store) maybeReloadForLookup() {
	ttl := s.reloadTTL
	if ttl <= 0 {
		ttl = defaultReloadTTL
	}
	now := s.now()

	// Fast path: within the TTL window, trust the in-memory map (no stat, no
	// flock). This is the steady state of the auth hot path.
	s.mu.RLock()
	fresh := now.Sub(s.lastCheck) < ttl
	s.mu.RUnlock()
	if fresh {
		return
	}

	// TTL elapsed: cheaply stat the file before paying for the flock+reload, so
	// an unchanged keys.json costs only a stat.
	fi, statErr := os.Stat(s.path)

	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check under the write lock: another Lookup may have refreshed while we
	// blocked on the lock. Do not reload twice for one change.
	if now.Sub(s.lastCheck) < ttl {
		return
	}
	s.lastCheck = now

	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			// The file vanished out from under us (an operator deleted it, or a
			// botched deploy). Treat as no keys — fail closed, not open — and log
			// once per non-empty->empty transition.
			if len(s.recs) != 0 {
				slog.Warn("authkeys: keys.json disappeared; treating as empty store", "path", s.path)
				s.recs = make(map[string]Record)
			}
			s.lastMod = time.Time{}
			return
		}
		slog.Warn("authkeys: stat keys.json failed; keeping current keys", "path", s.path, "err", statErr)
		return
	}
	if fi.ModTime().Equal(s.lastMod) {
		return // unchanged since the last reload
	}

	// mtime changed: reload under the cross-process flock so we observe a fully
	// committed file, never one mid-rename by another process's mutation.
	unlock, err := s.flockLocked()
	if err != nil {
		slog.Warn("authkeys: flock for reload failed; keeping current keys", "err", err)
		return
	}
	defer unlock()
	if err := s.reloadLocked(); err != nil {
		slog.Warn("authkeys: reload failed; keeping current keys", "err", err)
		return
	}
	// Record the mtime of exactly what we loaded (re-stat under the flock, where
	// the file is stable); fall back to the pre-flock stat if that fails.
	if fi2, err := os.Stat(s.path); err == nil {
		s.lastMod = fi2.ModTime()
	} else {
		s.lastMod = fi.ModTime()
	}
}

// Lookup returns the active record for a presented plaintext key, or
// ErrUnknownKey if the key is absent or revoked.
//
// The map lookup already gates on the hash, and we additionally run a
// constant-time compare of the presented hash against the stored hash. This
// makes the comparison of a MATCHED hash constant-time and documents the
// intent that key verification is constant-time (belt-and-suspenders over the
// map). It does NOT hide the map lookup's own timing — a miss returns before
// the compare — but that leaks nothing exploitable: presenting a stored hash
// already requires knowing the corresponding key.
func (s *Store) Lookup(presentedKey string) (Record, error) {
	if presentedKey == "" {
		return Record{}, ErrUnknownKey
	}
	h := hashKey(presentedKey)

	s.maybeReloadForLookup()

	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.recs[h]
	if !ok || !rec.Active() {
		return Record{}, ErrUnknownKey
	}
	if subtle.ConstantTimeCompare([]byte(h), []byte(rec.KeyHash)) != 1 {
		return Record{}, ErrUnknownKey
	}
	return rec, nil
}

// Revoke marks matching active keys revoked (revoked-at = now) and persists.
// Exactly one selector must be non-empty. Returns the number of records
// revoked; revoking an already-revoked or absent key is not an error (0).
func (s *Store) Revoke(byKey, byHash, byDiscordID string) (int, error) {
	selectors := 0
	for _, sel := range []string{byKey, byHash, byDiscordID} {
		if sel != "" {
			selectors++
		}
	}
	if selectors != 1 {
		return 0, errors.New("authkeys: revoke needs exactly one of key/hash/discordId")
	}
	targetHash := byHash
	if byKey != "" {
		targetHash = hashKey(byKey)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	unlock, err := s.flockLocked()
	if err != nil {
		return 0, err
	}
	defer unlock()

	// Reload under the flock so we revoke against the latest on-disk state (see
	// Issue for why).
	if err := s.reloadLocked(); err != nil {
		return 0, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	// Snapshot the records we are about to mutate so a saveLocked failure can
	// roll the in-memory map back to its pre-Revoke state — otherwise an
	// in-process Lookup would wrongly reject a key still active on disk until
	// the next reload-under-lock repaired it. Mirrors Issue's rollback.
	prev := make(map[string]Record)
	n := 0
	for hash, rec := range s.recs {
		match := false
		switch {
		case targetHash != "":
			match = hash == targetHash
		case byDiscordID != "":
			match = rec.DiscordID == byDiscordID
		}
		if match && rec.Active() {
			prev[hash] = rec // pre-mutation copy (Record is a value type)
			rec.Revoked = now
			s.recs[hash] = rec
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	if err := s.saveLocked(); err != nil {
		for hash, rec := range prev {
			s.recs[hash] = rec // keep memory and disk consistent
		}
		return 0, err
	}
	s.noteReloaded() // s.recs now equals the file we just wrote
	return n, nil
}

// List returns a snapshot of all records (active and revoked) sorted by
// creation time then hash, for the `keys list` CLI. Records carry only the
// hash, never a recoverable key.
func (s *Store) List() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Record, 0, len(s.recs))
	for _, r := range s.recs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created < out[j].Created
		}
		return out[i].KeyHash < out[j].KeyHash
	})
	return out
}

// ActiveByDiscordID returns the current active record for a Discord user and
// true, or a zero Record and false if the user has no active key. Used by the
// portal to show a signed-in user their key STATUS (hash prefix + created
// date) without exposing the key, which is unrecoverable. The record carries
// only the hash, never a key.
//
// This is a read: it takes only the RWMutex, so it can observe a map that is
// slightly stale relative to another process's just-committed Issue (see the
// Store doc). That is acceptable for a status display — the mutating POST
// /portal/key path reloads under the flock and is authoritative.
func (s *Store) ActiveByDiscordID(discordID string) (Record, bool) {
	if discordID == "" {
		return Record{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.recs {
		if r.DiscordID == discordID && r.Active() {
			return r, true
		}
	}
	return Record{}, false
}

// saveLocked serialises the in-memory map to keys.json atomically. Caller
// holds s.mu.
func (s *Store) saveLocked() error {
	recs := make([]Record, 0, len(s.recs))
	for _, r := range s.recs {
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].KeyHash < recs[j].KeyHash })
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return fmt.Errorf("authkeys: marshal: %w", err)
	}
	return writeFileAtomic(s.path, data, 0o600)
}

// writeFileAtomic writes data to path via a temp file in the same directory,
// fsync, then rename, so a concurrent reader (or a crash mid-write) never sees
// a partial file. Mirrors democache/util.go's pattern but is kept self-
// contained so authkeys imports nothing from the cache.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".keys-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
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
