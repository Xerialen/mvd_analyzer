package authkeys

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIssueLookupRoundtrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, rec, err := s.Issue("123", "alice", false, "portal")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, KeyPrefix) {
		t.Errorf("key %q missing prefix %q", key, KeyPrefix)
	}
	if rec.KeyHash == "" || strings.Contains(rec.KeyHash, key) {
		t.Errorf("record must carry a hash, never the key: %+v", rec)
	}
	got, err := s.Lookup(key)
	if err != nil {
		t.Fatalf("lookup issued key: %v", err)
	}
	if got.DiscordName != "alice" || got.Note != "portal" || got.Service {
		t.Errorf("looked-up record = %+v", got)
	}
}

func TestLookupUnknownAndRevoked(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(""); err != ErrUnknownKey {
		t.Errorf("empty key err = %v; want ErrUnknownKey", err)
	}
	if _, err := s.Lookup("qwmvd_garbage"); err != ErrUnknownKey {
		t.Errorf("garbage key err = %v; want ErrUnknownKey", err)
	}
	key, _, err := s.Issue("7", "bob", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(key); err != nil {
		t.Fatalf("valid key should authenticate: %v", err)
	}
	n, err := s.Revoke(key, "", "")
	if err != nil || n != 1 {
		t.Fatalf("revoke: n=%d err=%v", n, err)
	}
	if _, err := s.Lookup(key); err != ErrUnknownKey {
		t.Errorf("revoked key err = %v; want ErrUnknownKey", err)
	}
}

// TestIssueTwiceRevokesFirst pins the D4 invariant: a second Issue for the
// same Discord id revokes the first key.
func TestIssueTwiceRevokesFirst(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key1, _, err := s.Issue("42", "carol", false, "first")
	if err != nil {
		t.Fatal(err)
	}
	key2, _, err := s.Issue("42", "carol", false, "second")
	if err != nil {
		t.Fatal(err)
	}
	if key1 == key2 {
		t.Fatal("re-issue produced the same key")
	}
	if _, err := s.Lookup(key1); err != ErrUnknownKey {
		t.Errorf("old key still authenticates after re-issue: %v", err)
	}
	if _, err := s.Lookup(key2); err != nil {
		t.Errorf("new key should authenticate: %v", err)
	}
	// Only one active key remains for this user.
	active := 0
	for _, r := range s.List() {
		if r.DiscordID == "42" && r.Active() {
			active++
		}
	}
	if active != 1 {
		t.Errorf("active keys for discord 42 = %d; want 1", active)
	}
}

// TestServiceKeysDoNotCollide: service keys have empty discordID, so issuing
// many does not revoke each other.
func TestServiceKeysDoNotCollide(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k1, _, _ := s.Issue("", "", true, "svc-a")
	k2, _, _ := s.Issue("", "", true, "svc-b")
	if _, err := s.Lookup(k1); err != nil {
		t.Errorf("svc key 1 revoked unexpectedly: %v", err)
	}
	if _, err := s.Lookup(k2); err != nil {
		t.Errorf("svc key 2 revoked unexpectedly: %v", err)
	}
}

// TestPersistenceRoundtrip: a second Store on the same dir sees issued keys,
// and the plaintext key never appears in the on-disk file.
func TestPersistenceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := s1.Issue("99", "dave", true, "svc")
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, keysFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), key) {
		t.Fatalf("keys.json leaked the plaintext key:\n%s", raw)
	}
	// The random suffix must not appear either (belt-and-suspenders).
	if suffix := strings.TrimPrefix(key, KeyPrefix); strings.Contains(string(raw), suffix) {
		t.Fatalf("keys.json leaked the key body:\n%s", raw)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Lookup(key); err != nil {
		t.Errorf("reopened store does not authenticate persisted key: %v", err)
	}
}

func TestRevokeSelectorValidation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke("", "", ""); err == nil {
		t.Error("revoke with no selector should error")
	}
	if _, err := s.Revoke("k", "h", ""); err == nil {
		t.Error("revoke with two selectors should error")
	}
}

func TestRevokeByDiscordID(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, _, _ := s.Issue("555", "erin", false, "")
	n, err := s.Revoke("", "", "555")
	if err != nil || n != 1 {
		t.Fatalf("revoke by discordID: n=%d err=%v", n, err)
	}
	if _, err := s.Lookup(key); err != ErrUnknownKey {
		t.Errorf("key not revoked by discordID: %v", err)
	}
}

// TestStorePermissions pins that the store dir is 0700 and keys.json is 0600
// after Open+Issue, even when Open is pointed at a pre-existing loose dir
// (covers the MkdirAll-doesn't-tighten hazard).
func TestStorePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auth")
	// Pre-create it world-listable; Open must tighten it.
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Issue("1", "u", false, "n"); err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("auth dir perm = %o; want 700", perm)
	}
	fi, err := os.Stat(filepath.Join(dir, keysFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("keys.json perm = %o; want 600", perm)
	}
}

// TestOpenRejectsCorruptFile pins the loud-failure behaviour: a malformed
// keys.json must error out of Open, never silently present as an empty store
// (which would lock every user out without a trace).
func TestOpenRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, keysFileName), []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Error("Open on a corrupt keys.json should return an error, not a silent empty store")
	}
}

// TestCrossProcessIssueNoLostWrite pins the phase-15 cross-process guard: two
// INDEPENDENT Store instances on one dir (modelling the live server's portal
// and a separate `keys` CLI process, each with its own in-memory map) issuing
// concurrently must both survive. Without the flock + reload-under-lock, the
// two whole-file read-modify-writes race and the loser's key is clobbered off
// disk. With it, every issued key is present in a freshly reopened store and
// authenticates.
func TestCrossProcessIssueNoLostWrite(t *testing.T) {
	dir := t.TempDir()

	const perStore = 20
	stores := 4
	var wg sync.WaitGroup
	var mu sync.Mutex
	var issued []string

	for si := 0; si < stores; si++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine opens its OWN Store on the shared dir — a distinct
			// in-memory map, as a separate process would have. The flock is the
			// only thing serialising their writes.
			s, err := Open(dir)
			if err != nil {
				t.Errorf("open: %v", err)
				return
			}
			for j := 0; j < perStore; j++ {
				key, _, err := s.Issue("", "", true, "svc")
				if err != nil {
					t.Errorf("issue: %v", err)
					return
				}
				mu.Lock()
				issued = append(issued, key)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if want := stores * perStore; len(issued) != want {
		t.Fatalf("issued %d keys; want %d", len(issued), want)
	}

	// Reopen fresh (loads the final on-disk state) and confirm EVERY key
	// survived the concurrent writes.
	final, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(final.List()); got != len(issued) {
		t.Fatalf("on-disk store holds %d keys; want %d (lost write)", got, len(issued))
	}
	for _, key := range issued {
		if _, err := final.Lookup(key); err != nil {
			t.Fatalf("issued key lost across concurrent writes: %v", err)
		}
	}
}

// TestRevokeRollsBackOnSaveFailure pins that a Revoke whose disk write fails
// leaves the in-memory map unchanged — a still-active key must keep
// authenticating in-process, symmetric with Issue's rollback. We force
// saveLocked to fail by making the store dir unwritable after issuing.
func TestRevokeRollsBackOnSaveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: dir perms do not block writes")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := s.Issue("42", "carol", false, "")
	if err != nil {
		t.Fatal(err)
	}

	// Make the dir unwritable so the atomic temp-file create in saveLocked
	// fails. (The flock file already exists, so flock still succeeds.)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, err := s.Revoke(key, "", ""); err == nil {
		t.Fatal("expected Revoke to fail when the dir is unwritable")
	}
	// The key must STILL authenticate in-process: the failed save rolled the
	// revocation back.
	if _, err := s.Lookup(key); err != nil {
		t.Errorf("key wrongly revoked in memory after a failed save: %v", err)
	}
}

// TestLookupReloadsAfterCrossProcessRevoke pins FIX 2: a key revoked by a
// SEPARATE process (the `keys` CLI, modelled by a second Store on the same dir)
// must stop authenticating on the running server after the reload TTL, without
// any portal op or restart. Before the mtime-checked TTL reload, the server's
// Lookup read only its stale in-memory map and revocation failed OPEN.
func TestLookupReloadsAfterCrossProcessRevoke(t *testing.T) {
	dir := t.TempDir()

	server, err := Open(dir) // the live server process
	if err != nil {
		t.Fatal(err)
	}
	// Tiny TTL so the reload window elapses within the test; nowFn stays real.
	server.reloadTTL = 20 * time.Millisecond

	cli, err := Open(dir) // a separate `keys` CLI process, distinct in-mem map
	if err != nil {
		t.Fatal(err)
	}

	key, _, err := server.Issue("42", "carol", false, "")
	if err != nil {
		t.Fatal(err)
	}
	// The server authenticates its freshly issued key.
	if _, err := server.Lookup(key); err != nil {
		t.Fatalf("issued key should authenticate on the server: %v", err)
	}

	// Revoke via the CLI process. Revoke reloads under the flock, so cli picks
	// up the server-issued key from disk even though cli opened before Issue.
	n, err := cli.Revoke(key, "", "")
	if err != nil || n != 1 {
		t.Fatalf("cli revoke: n=%d err=%v", n, err)
	}

	// Let the server's reload TTL elapse, then Lookup must re-stat, see the
	// changed mtime, reload under the flock, and reject the revoked key.
	time.Sleep(40 * time.Millisecond)
	if _, err := server.Lookup(key); err != ErrUnknownKey {
		t.Errorf("server still authenticates a CLI-revoked key after the TTL: err=%v", err)
	}
}

// TestLookupHandlesKeysFileDisappearing pins that a keys.json deleted out from
// under a running Store fails CLOSED after the TTL: Lookup treats a missing
// file as an empty store, so no key authenticates.
func TestLookupHandlesKeysFileDisappearing(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.reloadTTL = 20 * time.Millisecond

	key, _, err := s.Issue("1", "u", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(key); err != nil {
		t.Fatalf("issued key should authenticate: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, keysFileName)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := s.Lookup(key); err != ErrUnknownKey {
		t.Errorf("key still authenticates after keys.json vanished: err=%v", err)
	}
}

// TestIssueRollsBackPriorKeyOnSaveFailure pins that a re-Issue whose disk write
// fails leaves the in-memory map identical to disk: the prior active key must
// still authenticate (its D4 revocation is rolled back), symmetric with
// Revoke's full rollback. Before the fix, Issue only deleted the new record and
// left the prior key wrongly revoked in memory.
func TestIssueRollsBackPriorKeyOnSaveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: dir perms do not block writes")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key1, _, err := s.Issue("42", "carol", false, "first")
	if err != nil {
		t.Fatal(err)
	}

	// Make the dir unwritable so the atomic temp-file create in saveLocked fails
	// on the SECOND Issue (the flock file already exists, so flock still works).
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, _, err := s.Issue("42", "carol", false, "second"); err == nil {
		t.Fatal("expected re-Issue to fail when the dir is unwritable")
	}
	// The prior key must STILL authenticate in-process: the failed save rolled
	// its D4 revocation back.
	if _, err := s.Lookup(key1); err != nil {
		t.Errorf("prior key wrongly revoked in memory after a failed re-Issue: %v", err)
	}
}

// TestConcurrentIssueLookup exercises the mutex under -race.
func TestConcurrentIssueLookup(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seed, _, _ := s.Issue("seed", "seed", true, "")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, _, err := s.Issue("", "", true, "svc"); err != nil {
					t.Errorf("issue: %v", err)
				}
				_, _ = s.Lookup(seed)
				_ = s.List()
			}
		}(i)
	}
	wg.Wait()
	if _, err := s.Lookup(seed); err != nil {
		t.Errorf("seed key lost after concurrent load: %v", err)
	}
}
