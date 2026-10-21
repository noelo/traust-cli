package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/traust-security/traust-cli/internal/bootstrap"
)

// flowRequest is the serialized instruction for a pipeline worker: which
// flow, the target repo, and the run options every stage inherits.
type flowRequest struct {
	Flow                 string `json:"flow"` // audit-triage | remediate-verify | full
	Target               string `json:"target"`
	FindingsPath         string `json:"findings_path,omitempty"` // remediate-verify entry point
	Harness              string `json:"harness"`
	Model                string `json:"model,omitempty"`
	UnsafeFullPermission bool   `json:"unsafe_full_permission,omitempty"`
	BudgetOverride       string `json:"budget_override,omitempty"`
	Workspace            string `json:"workspace"`
}

// flowStage is one stage's record in the pipeline manifest.
type flowStage struct {
	Name     string `json:"name"`
	Skill    string `json:"skill"`
	RunID    string `json:"run_id,omitempty"`
	State    string `json:"state"` // pending | running | done | failed | skipped
	Detail   string `json:"detail,omitempty"`
	Artifact string `json:"artifact,omitempty"`
}

// flowManifest is the pipeline's persisted state, at
// <workspace>/cli/pipelines/<id>/pipeline.json. Stage runs are ordinary run
// directories, so `runs`, `attach`, `result` and `logs` all work on them.
type flowManifest struct {
	ID        string      `json:"id"`
	Flow      string      `json:"flow"`
	Target    string      `json:"target"`
	State     string      `json:"state"` // running | done | failed
	CreatedAt string      `json:"created_at"`
	Stages    []flowStage `json:"stages"`
}

func flowsDir(workspace string) string { return filepath.Join(workspace, "cli", "pipelines") }

func newFlowID() (string, error) { return newRunID("flow") }

// loadFlowManifest reads a pipeline manifest by directory.
func loadFlowManifest(dir string) (*flowManifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "pipeline.json"))
	if err != nil {
		return nil, err
	}
	var m flowManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *flowManifest) save(dir string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "pipeline.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "pipeline.json"))
}

// currentStage returns the running stage, else the last non-pending one.
func (m *flowManifest) currentStage() *flowStage {
	var last *flowStage
	for i := range m.Stages {
		s := &m.Stages[i]
		if s.State == stateRunning {
			return s
		}
		if s.State != "pending" {
			last = s
		}
	}
	return last
}

// startFlow spawns a detached pipeline worker and returns the flow id.
func startFlow(state *bootstrap.State, req flowRequest) (string, error) {
	id, err := newFlowID()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(flowsDir(state.Workspace), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	req.Workspace = state.Workspace
	data, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "request.json"), data, 0o644); err != nil {
		return "", err
	}

	// Initial manifest so `flow status` works from the first second.
	manifest := &flowManifest{ID: id, Flow: req.Flow, Target: req.Target, State: stateRunning, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	manifest.Stages = flowStagePlan(req.Flow)
	if err := manifest.save(dir); err != nil {
		return "", err
	}

	// Write the pid file up front: the handshake below accepts it.
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(fmt.Sprintf("%d", os.Getpid())), 0o644); err != nil {
		return "", err
	}

	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	log, err := os.Create(filepath.Join(dir, "worker.log"))
	if err != nil {
		return "", err
	}
	defer log.Close()
	cmd := exec.Command(exe, "_pipeline", dir)
	cmd.SysProcAttr = detachedSysProcAttr()
	cmd.Env = os.Environ()
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() { _ = cmd.Wait() }()
	return id, nil
}

// flowStagePlan is the static stage list per flow; the worker fills in run
// ids and states as it goes.
func flowStagePlan(flow string) []flowStage {
	switch flow {
	case "audit-triage":
		return []flowStage{
			{Name: "audit", Skill: "secure-code-audit", State: "pending"},
			{Name: "triage", Skill: "triage", State: "pending"},
		}
	case "remediate-verify":
		return []flowStage{
			{Name: "patch", Skill: "patch", State: "pending"},
			{Name: "verify", Skill: "verify-remediation", State: "pending"},
		}
	case "full":
		return []flowStage{
			{Name: "audit", Skill: "secure-code-audit", State: "pending"},
			{Name: "triage", Skill: "triage", State: "pending"},
			{Name: "patch", Skill: "patch", State: "pending"},
			{Name: "verify", Skill: "verify-remediation", State: "pending"},
		}
	}
	return nil
}

// runStage executes one pipeline stage as an ordinary background-style run
// (in-process, exactly like the run worker) and returns its exit code.
func runStage(dir string, req flowRequest, skill, runID string, args []string) (int, error) {
	stageDir := filepath.Join(filepath.Dir(filepath.Dir(dir)), "runs", runID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return 0, err
	}
	stageReq := runRequest{
		RunID:                runID,
		Workspace:            req.Workspace,
		Skill:                skill,
		Args:                 args,
		Harness:              req.Harness,
		Model:                req.Model,
		UnsafeFullPermission: req.UnsafeFullPermission,
		BudgetOverride:       req.BudgetOverride,
		DeclareSpend:         true,
	}
	data, err := json.MarshalIndent(stageReq, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(stageDir, "options.json"), data, 0o644); err != nil {
		return 0, err
	}
	return runWorker(stageDir), nil
}

// findRunArtifact returns the path of a run's first valid artifact matching
// suffix, from its result.json.
func findRunArtifact(runDir, suffix string) (string, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "result.json"))
	if err != nil {
		return "", err
	}
	var res struct {
		Artifacts []struct {
			Path  string `json:"path"`
			Valid bool   `json:"valid"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", err
	}
	for _, a := range res.Artifacts {
		if (a.Valid || suffix == ".diff") && len(a.Path) >= len(suffix) && a.Path[len(a.Path)-len(suffix):] == suffix {
			return a.Path, nil
		}
	}
	return "", fmt.Errorf("no %s artifact in %s", suffix, runDir)
}

// buildPatchedRepo clones the target and applies every generated patch,
// producing the patched checkout the verify stage audits.
func buildPatchedRepo(ctx context.Context, target, flowDir, dst string) (applied int, err error) {
	if out, err := exec.CommandContext(ctx, "git", "clone", "--quiet", target, dst).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("clone %s: %v: %s", target, err, string(out))
	}
	patchesRoot := filepath.Join(harnessRootOf(flowDir), "PATCHES")
	entries, err := filepath.Glob(filepath.Join(patchesRoot, "bug_*", "patch.diff"))
	if err != nil {
		return 0, err
	}
	for _, diff := range entries {
		check := exec.CommandContext(ctx, "git", "-C", dst, "apply", "--check", diff)
		if err := check.Run(); err != nil {
			continue // a conflicting patch is skipped, not fatal
		}
		if err := exec.CommandContext(ctx, "git", "-C", dst, "apply", diff).Run(); err != nil {
			continue
		}
		applied++
	}
	return applied, nil
}

// harnessRootOf resolves the harness root (where stage artifacts like
// PATCHES/ land) from a run or flow directory inside the workspace:
// <workspace>/cli/runs/<id> or <workspace>/cli/pipelines/<id>.
func harnessRootOf(dir string) string {
	cli := filepath.Dir(filepath.Dir(dir))
	ws := filepath.Dir(cli)
	return filepath.Join(ws, "traust")
}
