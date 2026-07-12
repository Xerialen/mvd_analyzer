package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func TestAttachDecisionsKDLogTakesPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	log := "[2026-07-05 19:09:59] KDLOG_ANCHOR v=1 emitter=test map=dm3 level_time=15.5 match_start=15.5 dlog=1\n"
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}

	res := &result.Result{}
	attachDecisions(res, path, true)
	if res.Decisions == nil || res.Decisions.Source != "kdlog" {
		t.Fatalf("KDLOG not attached or inference won precedence: %+v", res.Decisions)
	}
	if res.Decisions.EmitterVersion != "test" || len(res.Errors) != 0 {
		t.Fatalf("KDLOG anchor/error mismatch: decisions=%+v errors=%v", res.Decisions, res.Errors)
	}
}

func TestAttachDecisionsInferenceAndSidecarError(t *testing.T) {
	res := &result.Result{}
	attachDecisions(res, "", true)
	if res.Decisions == nil || res.Decisions.Source != "inferred" {
		t.Fatalf("inference not attached: %+v", res.Decisions)
	}

	missing := &result.Result{}
	attachDecisions(missing, filepath.Join(t.TempDir(), "missing.log"), false)
	if missing.Decisions != nil || len(missing.Errors) != 1 {
		t.Fatalf("sidecar failure should be non-fatal result error: decisions=%+v errors=%v", missing.Decisions, missing.Errors)
	}
}
