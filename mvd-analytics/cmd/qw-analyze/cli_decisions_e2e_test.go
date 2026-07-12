package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mvd-analyzer/mvd-analytics/hubfetch"
)

const cliDecisionFixtureGameID = 212422 // public golden-corpus duel on skull

// TestDecisionFlagsEndToEnd crosses the actual flag/parser/process/JSON
// boundary. It intentionally shares the public demo cache contract used by
// analyzer.TestGoldenCorpus instead of committing another multi-megabyte MVD.
func TestDecisionFlagsEndToEnd(t *testing.T) {
	demo := ensureCLIDecisionFixture(t)

	inferred := runCLIForDecisionTest(t, "-infer-decisions", demo)
	if inferred.SchemaVersion != 57 || len(inferred.TimelineAnalysis.PlayerSlots) == 0 {
		t.Fatalf("schema/playerSlots missing: schema=%d slots=%v", inferred.SchemaVersion, inferred.TimelineAnalysis.PlayerSlots)
	}
	if inferred.Decisions == nil || inferred.Decisions.Source != "inferred" || len(inferred.Decisions.Records) == 0 {
		t.Fatalf("inferred decisions missing from final JSON: %+v", inferred.Decisions)
	}

	logPath := filepath.Join(t.TempDir(), "server.log")
	logLine := "[2026-07-05 19:09:59] KDLOG_ANCHOR v=1 emitter=cli-e2e map=skull level_time=15.5 match_start=15.5 dlog=1\n"
	if err := os.WriteFile(logPath, []byte(logLine), 0o600); err != nil {
		t.Fatal(err)
	}
	kdlog := runCLIForDecisionTest(t, "-decision-log", logPath, "-infer-decisions", demo)
	if len(kdlog.TimelineAnalysis.PlayerSlots) == 0 {
		t.Fatal("playerSlots missing from KDLOG CLI result")
	}
	if kdlog.Decisions == nil || kdlog.Decisions.Source != "kdlog" || kdlog.Decisions.EmitterVersion != "cli-e2e" {
		t.Fatalf("KDLOG did not win CLI precedence: %+v", kdlog.Decisions)
	}
}

type cliDecisionResult struct {
	SchemaVersion    int `json:"schemaVersion"`
	TimelineAnalysis struct {
		PlayerSlots map[string]int `json:"playerSlots"`
	} `json:"timelineAnalysis"`
	Decisions *struct {
		Source         string            `json:"source"`
		EmitterVersion string            `json:"emitterVersion"`
		Records        []json.RawMessage `json:"records"`
	} `json:"decisions"`
}

func runCLIForDecisionTest(t *testing.T, args ...string) cliDecisionResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	cmd := exec.CommandContext(ctx, goBin, append([]string{"run", "."}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("qw-analyze %v: %v\n%s", args, err, out)
	}
	var got cliDecisionResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode qw-analyze output: %v (prefix %.200q)", err, out)
	}
	return got
}

func ensureCLIDecisionFixture(t *testing.T) string {
	t.Helper()
	cacheDir := filepath.Join("..", "..", "testdata", "cache")
	path := filepath.Join(cacheDir, fmt.Sprintf("%d.mvd.gz", cliDecisionFixtureGameID))
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	client := hubfetch.NewClient()
	info, err := client.Resolve(cliDecisionFixtureGameID)
	if err != nil {
		t.Fatalf("resolve public CLI fixture: %v", err)
	}
	data, err := client.Download(info)
	if err != nil {
		t.Fatalf("download public CLI fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("cache public CLI fixture: %v", err)
	}
	return path
}
