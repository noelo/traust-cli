package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Check is one doctor finding.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// DoctorReport is the aggregate result of `traust-cli doctor`.
type DoctorReport struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Doctor runs the CLI's own checks and delegates the deployment-config checks
// to `scripts/install_traust --doctor`, which exits 1 on any failure.
func Doctor(ctx context.Context, state *State, log LogFn) (*DoctorReport, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	report := &DoctorReport{Checks: nil}
	add := func(name string, ok bool, detail string) {
		report.Checks = append(report.Checks, Check{Name: name, OK: ok, Detail: detail})
		if !ok {
			report.OK = false
		}
	}
	report.OK = true

	// State file present and parseable (caller already loaded it).
	add("state", true, StatePath(state.Workspace))
	if state.FullPermission {
		add("permission-mode", true, "full (consented at init): harnesses run with all tools auto-approved")
	} else {
		add("permission-mode", true, "scoped (fail-closed): permission requests abort runs with exit 2 — skills and flows (audit → triage → patch → verify) need `traust-cli init --full-permission`")
	}

	// Traust checkout + venv.
	repo := state.TraustRepo()
	if fi, err := os.Stat(filepath.Join(repo, ".git")); err != nil || !fi.IsDir() {
		add("traust-checkout", false, fmt.Sprintf("no git checkout at %s — re-run `traust-cli init`", repo))
		return report, nil
	}
	add("traust-checkout", true, fmt.Sprintf("%s @ %s (%s)", repo, state.TraustRef, shortSHA(state.TraustSHA)))

	if _, err := os.Stat(state.VenvPython()); err != nil {
		add("venv", false, fmt.Sprintf("missing %s — run `traust-cli init` to uv sync", state.VenvPython()))
		return report, nil
	}
	add("venv", true, state.VenvPython())

	// Engine importable.
	imp := exec.CommandContext(ctx, state.VenvPython(), "-c", "import traust.cli, traust_engine, traust_contracts")
	imp.Dir = repo
	imp.Env = state.ChildEnv()
	if out, err := imp.CombinedOutput(); err != nil {
		add("engine-import", false, strings.TrimSpace(string(out)))
	} else {
		add("engine-import", true, "traust.cli + traust_engine + traust_contracts import cleanly")
	}

	// install_traust --doctor (deployment config, resolver, toolchain).
	inst := exec.CommandContext(ctx, "bash", "scripts/install_traust", "--doctor", "--config-home", state.ConfigHome)
	inst.Dir = repo
	inst.Env = state.ChildEnv()
	out, err := inst.CombinedOutput()
	detail := strings.TrimSpace(string(out))
	if err != nil {
		add("install-traust-doctor", false, tail(detail, 40))
	} else {
		add("install-traust-doctor", true, tail(detail, 5))
	}

	return report, nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func tail(s string, lines int) string {
	all := strings.Split(s, "\n")
	if len(all) <= lines {
		return s
	}
	return strings.Join(all[len(all)-lines:], "\n")
}
