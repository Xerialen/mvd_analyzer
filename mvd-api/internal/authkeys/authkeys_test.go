package authkeys

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
