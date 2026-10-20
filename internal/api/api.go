// Package api is the delegation seam between traust-cli and the traust
// workflow engine. Every deterministic operation the CLI needs is expressed
// as a call against the existing Python CLI (`python3 -m traust.cli <group>
// <op>` run from the harness venv). The CLI never reimplements engine logic;
// this interface is the seam an all-Go engine implementation can later
// replace operation by operation.
package api

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Result captures the outcome of one delegated engine operation.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Output returns the combined stdout and stderr of the operation.
func (r Result) Output() string {
	return strings.TrimSuffix(r.Stdout+r.Stderr, "\n")
}

// SpendRow is one model-spend declaration appended to the metrics ledger via
// `traust registry models spend`. Calibration fields (--repo/--loc) are
// optional.
type SpendRow struct {
	Skill     string
	Model     string
	TokensIn  int64
	TokensOut int64
	Repo      string
	Loc       int64
}

// TraustAPI is the subset of the workflow engine CLI that traust-cli wraps.
type TraustAPI interface {
	// ReportingValidate validates report JSON against its schema
	// (`traust reporting validate`).
	ReportingValidate(ctx context.Context, path string, strict bool) (Result, error)
	// ReportingLint lints threat models (`traust reporting lint`).
	ReportingLint(ctx context.Context, paths []string, strict bool) (Result, error)
	// ReportingRender renders a report's Markdown companion
	// (`traust reporting render`). An empty outPath renders to stdout.
	ReportingRender(ctx context.Context, report, outPath string) (Result, error)
	// RegistryModelsResolve resolves a registry role to its floor model
	// (`traust registry models resolve <role>`). Returns the model id.
	RegistryModelsResolve(ctx context.Context, role string) (string, error)
	// RegistryModelsSpend declares a model-spend row
	// (`traust registry models spend`). Best effort: a missing
	// progress-tracker configuration is a warning in the engine, not an error.
	RegistryModelsSpend(ctx context.Context, row SpendRow) error
}

// PythonCLI runs the traust engine CLI through the harness venv's Python.
type PythonCLI struct {
	// Python is the venv interpreter path.
	Python string
	// Env is the environment for child processes (venv PATH, TRAUST_CONFIG_HOME).
	Env []string
	// Dir is the working directory for child processes (harness root).
	Dir string
}

// NewPythonCLI constructs a delegating TraustAPI.
func NewPythonCLI(python string, env []string, dir string) *PythonCLI {
	return &PythonCLI{Python: python, Env: env, Dir: dir}
}

func (p *PythonCLI) run(ctx context.Context, args []string) (Result, error) {
	cmd := exec.CommandContext(ctx, p.Python, append([]string{"-m", "traust.cli"}, args...)...)
	cmd.Dir = p.Dir
	cmd.Env = p.Env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
			res.Stderr = err.Error()
		}
	}
	return res, nil
}

// ReportingValidate implements TraustAPI.
func (p *PythonCLI) ReportingValidate(ctx context.Context, path string, strict bool) (Result, error) {
	args := []string{"reporting", "validate", path}
	if strict {
		args = append(args, "--strict")
	}
	return p.run(ctx, args)
}

// ReportingLint implements TraustAPI.
func (p *PythonCLI) ReportingLint(ctx context.Context, paths []string, strict bool) (Result, error) {
	args := append([]string{"reporting", "lint"}, paths...)
	if strict {
		args = append(args, "--strict")
	}
	return p.run(ctx, args)
}

// ReportingRender implements TraustAPI.
func (p *PythonCLI) ReportingRender(ctx context.Context, report, outPath string) (Result, error) {
	args := []string{"reporting", "render", report}
	if outPath != "" {
		args = append(args, "-o", outPath)
	}
	return p.run(ctx, args)
}

// RegistryModelsResolve implements TraustAPI.
func (p *PythonCLI) RegistryModelsResolve(ctx context.Context, role string) (string, error) {
	res, err := p.run(ctx, []string{"registry", "models", "resolve", role})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("registry models resolve %s: %s", role, res.Output())
	}
	model := strings.TrimSpace(res.Stdout)
	if model == "" {
		return "", fmt.Errorf("registry models resolve %s: empty result", role)
	}
	return model, nil
}

// RegistryModelsSpend implements TraustAPI.
func (p *PythonCLI) RegistryModelsSpend(ctx context.Context, row SpendRow) error {
	args := []string{
		"registry", "models", "spend",
		"--skill", row.Skill,
		"--model", row.Model,
		"--tokens-in", strconv.FormatInt(row.TokensIn, 10),
		"--tokens-out", strconv.FormatInt(row.TokensOut, 10),
	}
	if row.Repo != "" {
		args = append(args, "--repo", row.Repo)
	}
	if row.Loc > 0 {
		args = append(args, "--loc", strconv.FormatInt(row.Loc, 10))
	}
	res, err := p.run(ctx, args)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("registry models spend: %s", res.Output())
	}
	return nil
}
