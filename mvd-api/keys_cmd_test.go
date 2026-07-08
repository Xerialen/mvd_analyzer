package main

import (
	"os"
	"strings"
	"testing"

	"github.com/mvd-analyzer/mvd-api/internal/authkeys"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// it printed. The CLI writes the key to stdout, so tests need to read it.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fnErr := fn()
	_ = w.Close()
	os.Stdout = orig
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, e := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if e != nil {
			break
		}
	}
	if fnErr != nil {
		t.Fatalf("cmd error: %v", fnErr)
	}
	return string(buf)
}

// TestKeysCLI_Roundtrip: issue prints a key the store authenticates; list
// shows it without the plaintext; revoke stops it authenticating.
func TestKeysCLI_Roundtrip(t *testing.T) {
	dir := t.TempDir()

	out := captureStdout(t, func() error {
		return runKeysIssue([]string{"-auth-dir", dir, "-note", "clitest", "-discord-id", "42", "-discord-name", "cli"})
	})
	// Extract the key from "key: qwmvd_..."
	var key string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "key: ") {
			key = strings.TrimSpace(strings.TrimPrefix(line, "key: "))
		}
	}
	if key == "" || !strings.HasPrefix(key, authkeys.KeyPrefix) {
		t.Fatalf("issue did not print a key:\n%s", out)
	}

	// The store the server would open authenticates this key.
	store, err := authkeys.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lookup(key); err != nil {
		t.Fatalf("issued key does not authenticate: %v", err)
	}

	// list shows the prefix + note but never the plaintext key.
	listOut := captureStdout(t, func() error {
		return runKeysList([]string{"-auth-dir", dir})
	})
	if strings.Contains(listOut, key) {
		t.Fatalf("keys list leaked the plaintext key:\n%s", listOut)
	}
	if body := strings.TrimPrefix(key, authkeys.KeyPrefix); strings.Contains(listOut, body) {
		t.Fatalf("keys list leaked the key body:\n%s", listOut)
	}
	if !strings.Contains(listOut, "clitest") {
		t.Errorf("keys list missing the note:\n%s", listOut)
	}

	// revoke by discord id → key stops authenticating (reopen to be sure it
	// persisted).
	_ = captureStdout(t, func() error {
		return runKeysRevoke([]string{"-auth-dir", dir, "-discord-id", "42"})
	})
	store2, err := authkeys.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store2.Lookup(key); err != authkeys.ErrUnknownKey {
		t.Errorf("revoked key still authenticates: %v", err)
	}
}

func TestKeysCLI_RequiresAuthDir(t *testing.T) {
	if err := runKeysList(nil); err == nil {
		t.Error("keys list without -auth-dir should error")
	}
	if err := runKeys(nil); err == nil {
		t.Error("keys with no subcommand should error")
	}
	if err := runKeys([]string{"bogus"}); err == nil {
		t.Error("keys bogus should error")
	}
}
