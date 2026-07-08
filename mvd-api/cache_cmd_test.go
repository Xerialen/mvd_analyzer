package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunCachePrune_ArgValidation covers the foot-guns (FIX 2): action
// selection and the -max-bytes 0 rejection.
func TestRunCachePrune_ArgValidation(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"no action", []string{"-cache-dir", dir}, "exactly one"},
		{"two actions", []string{"-cache-dir", dir, "-all", "-older-than", "30d"}, "exactly one"},
		{"max-bytes zero", []string{"-cache-dir", dir, "-max-bytes", "0"}, "use -all to wipe"},
		{"bad age", []string{"-cache-dir", dir, "-older-than", "banana"}, "invalid age"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := runCachePrune(c.args)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q; want substring %q", err.Error(), c.wantErr)
			}
		})
	}
}

// TestRunCachePrune_DryRunKeepsFiles proves -dry-run deletes nothing even
// when the selected action would otherwise wipe both tiers.
func TestRunCachePrune_DryRunKeepsFiles(t *testing.T) {
	dir := t.TempDir()
	mvd := filepath.Join(dir, "mvd", "aa", "aaaa.mvd.gz")
	if err := os.MkdirAll(filepath.Dir(mvd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mvd, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runCachePrune([]string{"-cache-dir", dir, "-all", "-dry-run"}); err != nil {
		t.Fatalf("dry-run prune: %v", err)
	}
	if _, err := os.Stat(mvd); err != nil {
		t.Errorf("-dry-run -all must not delete %s: %v", mvd, err)
	}
}
