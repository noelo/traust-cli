package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/traust-security/traust-cli/internal/bootstrap"
)

// setupWorkerWorkspace builds a minimal workspace: state.json, a one-skill
// harnessing fixture tree, config dir. Returns the loaded bootstrap state.
func setupWorkerWorkspace(t *testing.T, ws string) *bootstrap.State {
	t.Helper()
	for _, d := range []string{
		filepath.Join(ws, "cli"),
		filepath.Join(ws, "config"),
		filepath.Join(ws, "traust", "harnessing", "3-audit", "secure-code-audit"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	skill := "---\nname: secure-code-audit\ndescription: test skill\nmetadata:\n  harness:\n    tier: primary\n---\nAudit $ARGUMENTS.\n"
	if err := os.WriteFile(filepath.Join(ws, "traust", "harnessing", "3-audit", "secure-code-audit", "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	// A fake venv python: the worker delegates validation and spend to it,
	// and this double reports success for every engine call.
	if err := os.MkdirAll(filepath.Join(ws, "traust", ".venv", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakePy := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(filepath.Join(ws, "traust", ".venv", "bin", "python3"), []byte(fakePy), 0o755); err != nil {
		t.Fatal(err)
	}
	stateJSON := map[string]any{
		"version":     1,
		"workspace":   ws,
		"config_home": filepath.Join(ws, "config"),
		"traust_ref":  "test",
		"traust_sha":  "0000000000000000000000000000000000000000",
		"harness":     "claude",
		"created_at":  time.Now().UTC().Format(time.RFC3339),
		"updated_at":  time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(stateJSON)
	if err := os.WriteFile(filepath.Join(ws, "cli", "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	workspaceFlag = ws
	state, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func fakeClaudePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "testdata", "fake-harnesses", "fake-claude.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRunWorkerRoundTrip executes the worker in-process (exactly the code
// path the spawned process runs) against a temp workspace and the fake
// claude harness: options.json in, journal and result.json out.
func TestRunWorkerRoundTrip(t *testing.T) {
	t.Setenv("TRAUST_CLI_CLAUDE_BIN", fakeClaudePath(t))
	ws := t.TempDir()
	state := setupWorkerWorkspace(t, ws)

	runID := "20261021-120000-test-abcd"
	dir := filepath.Join(state.RunsDir(), runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	req := runRequest{
		RunID:        runID,
		Workspace:    ws,
		Skill:        "secure-code-audit",
		Args:         []string{"https://github.com/org/repo"},
		Harness:      "claude",
		DeclareSpend: true,
	}
	data, _ := json.MarshalIndent(req, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "options.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	code := runWorker(dir)
	if code != 0 {
		t.Fatalf("worker exit = %d", code)
	}

	for _, f := range []string{"result.json", "stream.jsonl", "spec.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "pid")); !os.IsNotExist(err) {
		t.Errorf("pid file should be removed after completion: %v", err)
	}

	var res map[string]any
	rd, _ := os.ReadFile(filepath.Join(dir, "result.json"))
	if err := json.Unmarshal(rd, &res); err != nil {
		t.Fatal(err)
	}
	if res["run_id"] != runID {
		t.Errorf("run_id = %v (the parent-minted id must flow through)", res["run_id"])
	}
	if res["exit_code"] != float64(0) {
		t.Errorf("exit_code = %v", res["exit_code"])
	}

	info, err := loadRunInfo(dir)
	if err != nil || info == nil {
		t.Fatal(err)
	}
	if info.State != stateDone {
		t.Errorf("state = %s, want done", info.State)
	}
}

// TestRunWorkerFailurePath verifies a failing harness records a result and
// exits non-zero.
func TestRunWorkerFailurePath(t *testing.T) {
	t.Setenv("TRAUST_CLI_CLAUDE_BIN", fakeClaudePath(t))
	t.Setenv("FAKE_CLAUDE_MODE", "fail")
	ws := t.TempDir()
	state := setupWorkerWorkspace(t, ws)

	runID := "20261021-120100-test-ffff"
	dir := filepath.Join(state.RunsDir(), runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	req := runRequest{RunID: runID, Workspace: ws, Skill: "secure-code-audit", Harness: "claude", DeclareSpend: true}
	data, _ := json.Marshal(req)
	os.WriteFile(filepath.Join(dir, "options.json"), data, 0o644)

	if code := runWorker(dir); code != 1 {
		t.Fatalf("worker exit = %d, want 1", code)
	}
	info, err := loadRunInfo(dir)
	if err != nil || info == nil {
		t.Fatal(err)
	}
	if info.State != stateFailed {
		t.Errorf("state = %s, want failed", info.State)
	}
}

// TestResolveRunPrefix covers id-prefix resolution and ambiguity.
func TestResolveRunPrefix(t *testing.T) {
	ws := t.TempDir()
	state := setupWorkerWorkspace(t, ws)

	a := filepath.Join(state.RunsDir(), "20261021-120000-test-aaaa")
	b := filepath.Join(state.RunsDir(), "20261021-120500-test-bbbb")
	for _, d := range []string{a, b} {
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "options.json"), []byte(`{"skill":"vuln-scan"}`), 0o644)
	}

	if info, err := resolveRun(state.RunsDir(), "aaaa"); err != nil || info.ID != filepath.Base(a) {
		t.Errorf("suffix resolve = %+v (%v)", info, err)
	}
	if info, err := resolveRun(state.RunsDir(), filepath.Base(a)); err != nil || info.ID != filepath.Base(a) {
		t.Errorf("full-id resolve = %+v (%v)", info, err)
	}
	if _, err := resolveRun(state.RunsDir(), "20261021-12"); err == nil {
		t.Error("ambiguous prefix should fail")
	}
	if _, err := resolveRun(state.RunsDir(), "zzzz"); err == nil {
		t.Error("unknown prefix should fail")
	}
}

// TestRunInfoStates covers the state machine over journal artifacts.
func TestRunInfoStates(t *testing.T) {
	ws := t.TempDir()
	state := setupWorkerWorkspace(t, ws)

	// Running: pid file pointing at this (live) test process.
	running := filepath.Join(state.RunsDir(), "20261021-130000-test-1111")
	os.MkdirAll(running, 0o755)
	os.WriteFile(filepath.Join(running, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)
	os.WriteFile(filepath.Join(running, "options.json"), []byte(`{"skill":"triage"}`), 0o644)
	if info, _ := loadRunInfo(running); info.State != stateRunning {
		t.Errorf("state = %s, want running", info.State)
	}

	// Orphaned: pid file pointing at a dead process, no result.
	orphan := filepath.Join(state.RunsDir(), "20261021-130100-test-2222")
	os.MkdirAll(orphan, 0o755)
	os.WriteFile(filepath.Join(orphan, "pid"), []byte("999999"), 0o644)
	if info, _ := loadRunInfo(orphan); info.State != stateOrphaned {
		t.Errorf("state = %s, want orphaned", info.State)
	}

	// Budget-stop: result.json with budget_refused.
	budget := filepath.Join(state.RunsDir(), "20261021-130200-test-3333")
	os.MkdirAll(budget, 0o755)
	os.WriteFile(filepath.Join(budget, "result.json"),
		[]byte(`{"run_id":"x","exit_code":3,"budget_refused":true}`), 0o644)
	if info, _ := loadRunInfo(budget); info.State != stateBudget {
		t.Errorf("state = %s, want budget-stop", info.State)
	}

	// Aborted: result.json with aborted=true.
	aborted := filepath.Join(state.RunsDir(), "20261021-130300-test-4444")
	os.MkdirAll(aborted, 0o755)
	os.WriteFile(filepath.Join(aborted, "result.json"),
		[]byte(`{"run_id":"x","exit_code":1,"aborted":true}`), 0o644)
	if info, _ := loadRunInfo(aborted); info.State != stateAborted {
		t.Errorf("state = %s, want aborted", info.State)
	}
}

// TestNewRunIDUniqueness guards the collision-safety of minted ids.
func TestNewRunIDUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := newRunID("vuln-scan")
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate run id %s", id)
		}
		seen[id] = true
		if short := shortRunID(id); short == id || len(short) != 4 {
			t.Errorf("short form of %s = %q", id, short)
		}
	}
}
