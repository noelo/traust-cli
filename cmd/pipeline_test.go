package cmd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPipelineAuditTriageChain runs a two-stage flow in-process with the
// fake claude harness: the audit stage's artifact must become the triage
// stage's input (plus --auto), and the manifest must record both stages.
func TestPipelineAuditTriageChain(t *testing.T) {
	t.Setenv("TRAUST_CLI_CLAUDE_BIN", fakeClaudePath(t))
	ws := t.TempDir()
	state := setupWorkerWorkspace(t, ws)

	// The fake harness writes fixture-security-audit.json into its cwd (the
	// harness root) for every skill, so both stages "succeed" and produce
	// the report artifact; that is enough to verify the chaining.
	flowID := "20261021-170000-flow-aa01"
	flowDir := filepath.Join(flowsDir(ws), flowID)
	if err := os.MkdirAll(flowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	req := flowRequest{Flow: "audit-triage", Target: "https://github.com/example/demo-shop", Harness: "claude", Workspace: ws}
	data, _ := json.Marshal(req)
	os.WriteFile(filepath.Join(flowDir, "request.json"), data, 0o644)
	manifest := &flowManifest{ID: flowID, Flow: req.Flow, Target: req.Target, State: stateRunning, Stages: flowStagePlan("audit-triage")}
	if err := manifest.save(flowDir); err != nil {
		t.Fatal(err)
	}

	if code := runPipelineWorker(flowDir); code != 0 {
		t.Fatalf("pipeline exit = %d", code)
	}

	m, err := loadFlowManifest(flowDir)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != stateDone {
		t.Fatalf("flow state = %s, want done", m.State)
	}
	if len(m.Stages) != 2 {
		t.Fatalf("stages = %+v", m.Stages)
	}
	for _, s := range m.Stages {
		if s.State != stateDone || s.RunID == "" {
			t.Errorf("stage %s = %+v", s.Name, s)
		}
	}

	// The triage stage's options.json must carry the audit artifact and
	// --auto: phase-1 output wired into phase-2 input.
	triageStage := m.Stages[1]
	optsData, err := os.ReadFile(filepath.Join(state.RunsDir(), triageStage.RunID, "options.json"))
	if err != nil {
		t.Fatal(err)
	}
	var triageReq runRequest
	if err := json.Unmarshal(optsData, &triageReq); err != nil {
		t.Fatal(err)
	}
	if len(triageReq.Args) != 2 || !strings.HasSuffix(triageReq.Args[0], "-security-audit.json") || triageReq.Args[1] != "--auto" {
		t.Errorf("triage args = %v (want [<audit-report>, --auto])", triageReq.Args)
	}
	if triageReq.Args[0] != m.Stages[0].Artifact {
		t.Errorf("triage input %q != audit artifact %q", triageReq.Args[0], m.Stages[0].Artifact)
	}
	if m.Stages[1].Artifact != m.Stages[0].Artifact {
		// The fake writes the same fixture file for both skills, so the
		// triage artifact equals the audit artifact; what matters is that
		// chaining used it.
		t.Logf("note: triage artifact = %q (fake harness writes the same file)", m.Stages[1].Artifact)
	}
}

// TestPipelineStopsOnStageFailure verifies a failing stage fails the flow
// and later stages stay pending.
func TestPipelineStopsOnStageFailure(t *testing.T) {
	t.Setenv("TRAUST_CLI_CLAUDE_BIN", fakeClaudePath(t))
	t.Setenv("FAKE_CLAUDE_MODE", "fail")
	ws := t.TempDir()
	setupWorkerWorkspace(t, ws)

	flowID := "20261021-170100-flow-bb02"
	flowDir := filepath.Join(flowsDir(ws), flowID)
	os.MkdirAll(flowDir, 0o755)
	req := flowRequest{Flow: "full", Target: "https://github.com/example/demo-shop", Harness: "claude", Workspace: ws}
	data, _ := json.Marshal(req)
	os.WriteFile(filepath.Join(flowDir, "request.json"), data, 0o644)
	manifest := &flowManifest{ID: flowID, Flow: req.Flow, Target: req.Target, State: stateRunning, Stages: flowStagePlan("full")}
	manifest.save(flowDir)

	if code := runPipelineWorker(flowDir); code == 0 {
		t.Fatal("pipeline should fail when the audit stage fails")
	}
	m, _ := loadFlowManifest(flowDir)
	if m.State != stateFailed || m.Stages[0].State != stateFailed {
		t.Fatalf("manifest = %+v", m)
	}
	for _, s := range m.Stages[1:] {
		if s.State != "pending" {
			t.Errorf("stage %s should be pending, got %s", s.Name, s.State)
		}
	}
}

// TestBuildPatchedRepo covers the patch-application step against a real git
// repository: applicable patches apply, conflicting ones are skipped.
func TestBuildPatchedRepo(t *testing.T) {
	ctx := context.Background()

	// Source repo with one committed file.
	src := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(src, "b.txt"), []byte("bee\n"), 0o644)
	run("add", ".")
	run("commit", "-qm", "init")

	// PATCHES layout under a fake workspace harness root.
	ws := t.TempDir()
	patches := filepath.Join(ws, "traust", "PATCHES", "bug_01")
	os.MkdirAll(patches, 0o755)
	os.WriteFile(filepath.Join(patches, "patch.diff"), []byte("diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-one\n+two\n"), 0o644)
	conflict := filepath.Join(ws, "traust", "PATCHES", "bug_02")
	os.MkdirAll(conflict, 0o755)
	os.WriteFile(filepath.Join(conflict, "patch.diff"), []byte("diff --git a/missing.txt b/missing.txt\n--- a/missing.txt\n+++ b/missing.txt\n@@ -1 +1 @@\n-absent\n+present\n"), 0o644)

	dst := filepath.Join(t.TempDir(), "patched")
	flowDir := filepath.Join(ws, "cli", "pipelines", "x")
	os.MkdirAll(flowDir, 0o755)
	applied, err := buildPatchedRepo(ctx, src, flowDir, dst)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1 (conflicting patch skipped)", applied)
	}
	data, _ := os.ReadFile(filepath.Join(dst, "a.txt"))
	if string(data) != "two\n" {
		t.Errorf("a.txt = %q, patch not applied", data)
	}
}
