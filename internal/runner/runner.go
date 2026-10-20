// Package runner orchestrates a headless skill run: pre-flight model-floor
// resolution, prompt composition, harness execution with live event
// streaming, budget-band enforcement, artifact discovery and validation, and
// spend declaration. It also implements the traust-sdk skills.Provider
// interface so typed SDK consumers get schema-validated results.
package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/traust-security/traust-cli/internal/api"
	"github.com/traust-security/traust-cli/internal/bootstrap"
	"github.com/traust-security/traust-cli/internal/catalog"
	"github.com/traust-security/traust-cli/internal/harness"
)

// Exit codes are a contract for CI and scripts:
//
//	0 — run completed
//	1 — error (harness failure, invalid artifacts, aborted)
//	2 — the skill needs interactive input that a headless run cannot provide
//	3 — the budget refuse band was hit
const (
	ExitOK         = 0
	ExitError      = 1
	ExitNeedsInput = 2
	ExitBudget     = 3
)

// Sink receives live run updates. The TUI monitor and the plain printer both
// implement it; a nil sink is allowed.
type Sink interface {
	// Progress reports a run-stage message (preflight, bands, post-run).
	Progress(msg string)
	// Event reports one normalized harness event.
	Event(ev harness.Event)
	// Raw reports one unparsed harness output line.
	Raw(line string)
	// Done signals the run (including post-processing) is finished.
	Done()
}

type nopSink struct{}

func (nopSink) Progress(string)     {}
func (nopSink) Event(harness.Event) {}
func (nopSink) Raw(string)          {}
func (nopSink) Done()               {}

// NopSink returns a sink that discards everything.
func NopSink() Sink { return nopSink{} }

// Options configures one run.
type Options struct {
	State                *bootstrap.State
	API                  api.TraustAPI
	Harness              harness.Harness
	Skill                *catalog.Skill
	Args                 []string
	Model                string
	MaxTurns             int
	UnsafeFullPermission bool
	BudgetOverrideReason string
	StrictValidation     bool
	DeclareSpend         bool
}

// Usage is the accumulated token usage of a run.
type Usage struct {
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
}

// RunResult is the complete outcome of one run.
type RunResult struct {
	RunID         string     `json:"run_id"`
	Dir           string     `json:"dir"`
	Skill         string     `json:"skill"`
	Harness       string     `json:"harness"`
	Model         string     `json:"model"`
	Preflight     Preflight  `json:"preflight"`
	BudgetCap     int64      `json:"budget_cap,omitempty"`
	BudgetBand    string     `json:"budget_band,omitempty"`
	Usage         Usage      `json:"usage"`
	Artifacts     []Artifact `json:"artifacts,omitempty"`
	SpendRecorded bool       `json:"spend_recorded"`
	NeedsInput    bool       `json:"needs_input"`
	BudgetRefused bool       `json:"budget_refused"`
	Aborted       bool       `json:"aborted"`
	HarnessExit   int        `json:"harness_exit"`
	ResultText    string     `json:"result_text,omitempty"`
	ExitCode      int        `json:"exit_code"`
}

// Run executes the skill through the harness.
func Run(ctx context.Context, opts Options, sink Sink) (*RunResult, error) {
	if sink == nil {
		sink = NopSink()
	}
	state, skill, h := opts.State, opts.Skill, opts.Harness
	if state == nil || skill == nil || h == nil || opts.API == nil {
		return nil, errors.New("runner: state, api, harness and skill are required")
	}

	runID := time.Now().Format("20060102-150405") + "-" + skill.Name
	res := &RunResult{RunID: runID, Skill: skill.Name, Harness: h.ID(), ExitCode: ExitOK}

	// Pre-flight: model floor.
	pf := ResolveModel(ctx, opts.API, skill, opts.Model)
	res.Preflight = pf
	res.Model = pf.Model
	if pf.Warning != "" {
		sink.Progress("warning: " + pf.Warning)
	}

	// Budget policy.
	policy, err := LoadPolicy(state.ConfigHome)
	if err != nil {
		sink.Progress("warning: " + err.Error())
	}
	if policy != nil {
		res.BudgetCap = policy.CapFor(skill.Name)
		res.BudgetBand = BandWithin.String()
		if opts.BudgetOverrideReason != "" {
			sink.Progress(fmt.Sprintf("budget override active: %s", opts.BudgetOverrideReason))
		}
	}

	// Journal + prompt + snapshot.
	root := state.TraustRepo()
	prompt := Compose(skill, opts.Args, root, state.ConfigHome)
	journal, err := NewJournal(state.RunsDir(), runID, Spec{
		RunID: runID, Skill: skill.Name, Args: opts.Args,
		Harness: h.ID(), Model: pf.Model,
		RegistryRole: pf.Role, FloorModel: pf.ResolvedModel, ModelWarning: pf.Warning,
		BudgetCap: res.BudgetCap, BudgetOverride: opts.BudgetOverrideReason,
		UnsafeFullPermission: opts.UnsafeFullPermission,
		TraustRef:            state.TraustRef, TraustSHA: state.TraustSHA,
	})
	if err != nil {
		return nil, err
	}
	res.Dir = journal.Dir
	before := snapshotArtifacts(root)

	// Capability caveats, up front.
	for _, note := range h.Notes() {
		sink.Progress("note: " + note)
	}

	// Launch.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := h.Command(runCtx, &harness.RunSpec{
		Prompt: prompt, Model: pf.Model, Cwd: root, Env: state.ChildEnv(),
		AllowedTools: skill.AllowedTools, MaxTurns: opts.MaxTurns,
		UnsafeFullPermission: opts.UnsafeFullPermission, OutDir: journal.Dir,
	})
	stderrPath := filepath.Join(journal.Dir, "harness-stderr.log")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		return nil, fmt.Errorf("create stderr log: %w", err)
	}
	defer stderrFile.Close()
	cmd.Stderr = stderrFile
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", h.Binary(), err)
	}

	// Stream loop.
	streaming := h.Capabilities().StreamJSON
	var plainOut strings.Builder
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	band := BandWithin
	for scanner.Scan() {
		line := scanner.Text()
		journal.Raw(line)
		if !streaming {
			plainOut.WriteString(line)
			plainOut.WriteString("\n")
			sink.Raw(line)
		} else if ev, ok := h.ParseEvent(scanner.Bytes()); ok {
			journal.Event(string(ev.Kind), ev.Text, ev.Tool, ev.TokensIn, ev.TokensOut, ev.CostUSD, ev.IsError)
			sink.Event(ev)
			applyEvent(res, ev)
			if ev.Kind == harness.EventNeedsInput {
				res.NeedsInput = true
				cancel()
			}
		} else {
			sink.Raw(line)
		}

		// Budget bands on cumulative output tokens.
		if policy != nil && res.Usage.TokensOut > 0 {
			b := policy.BandFor(skill.Name, res.Usage.TokensOut)
			if b != band {
				band = b
				res.BudgetBand = b.String()
				sink.Progress(fmt.Sprintf("budget band %s: %d output tokens of %d cap", b, res.Usage.TokensOut, res.BudgetCap))
			}
			if b == BandRefuse && opts.BudgetOverrideReason == "" {
				res.BudgetRefused = true
				cancel()
			}
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	if exitErr, ok := waitErr.(*exec.ExitError); ok {
		res.HarnessExit = exitErr.ExitCode()
	}

	switch {
	case res.BudgetRefused:
		res.ExitCode = ExitBudget
		sink.Progress(fmt.Sprintf("budget refuse band hit (%d output tokens, cap %d) — harness terminated; re-run with --budget-override \"<reason>\" if this is expected", res.Usage.TokensOut, res.BudgetCap))
	case res.NeedsInput:
		res.ExitCode = ExitNeedsInput
		sink.Progress("the skill asked for interactive input, which a headless run cannot provide — run it interactively in your harness and answer its questions, then re-run")
	case ctx.Err() != nil:
		res.Aborted = true
		res.ExitCode = ExitError
		sink.Progress("run aborted")
	case waitErr != nil:
		res.ExitCode = ExitError
		sink.Progress(fmt.Sprintf("harness exited with error: %v (stderr: %s)", waitErr, stderrPath))
	case scanErr != nil:
		res.ExitCode = ExitError
		sink.Progress(fmt.Sprintf("stream read error: %v", scanErr))
	}

	if !streaming && res.ResultText == "" {
		res.ResultText = strings.TrimRight(plainOut.String(), "\n")
	}

	// Post-run: artifacts, validation, rendering, spend — unless the run was
	// aborted or refused before producing trustworthy output.
	if res.ExitCode == ExitOK || (res.ExitCode == ExitError && res.HarnessExit == 0) {
		after := snapshotArtifacts(root)
		res.Artifacts = discoverArtifacts(root, before, after)
		validateArtifacts(ctx, opts.API, res.Artifacts, opts.StrictValidation)
		renderArtifacts(ctx, opts.API, res.Artifacts)
		for _, a := range res.Artifacts {
			switch {
			case a.Kind == "report" && !a.Valid:
				res.ExitCode = ExitError
				sink.Progress(fmt.Sprintf("report artifact failed schema validation: %s", a.Path))
			case a.Kind == "threat-model" && !a.Valid:
				res.ExitCode = ExitError
				sink.Progress(fmt.Sprintf("threat model failed lint: %s", a.Path))
			}
		}
		if len(res.Artifacts) > 0 {
			sink.Progress(fmt.Sprintf("%d artifact(s) discovered under %s", len(res.Artifacts), root))
		}
	}

	// Spend declaration (best effort).
	if opts.DeclareSpend && res.Usage.TokensIn+res.Usage.TokensOut > 0 && res.Model != "" && res.ExitCode != ExitBudget {
		row := api.SpendRow{Skill: skill.Name, Model: res.Model, TokensIn: res.Usage.TokensIn, TokensOut: res.Usage.TokensOut}
		if len(opts.Args) > 0 {
			row.Repo = filepath.Base(strings.TrimRight(opts.Args[0], "/"))
		}
		if err := opts.API.RegistryModelsSpend(ctx, row); err != nil {
			sink.Progress("warning: spend declaration failed: " + err.Error())
		} else {
			res.SpendRecorded = true
		}
	}

	if err := journal.WriteResult(res); err != nil {
		sink.Progress("warning: could not write run journal: " + err.Error())
	}
	sink.Done()
	return res, nil
}

// applyEvent folds a stream event into the run result. Usage is cumulative in
// every reporting harness, so tokens are maxed, not summed.
func applyEvent(res *RunResult, ev harness.Event) {
	if ev.TokensIn > res.Usage.TokensIn {
		res.Usage.TokensIn = ev.TokensIn
	}
	if ev.TokensOut > res.Usage.TokensOut {
		res.Usage.TokensOut = ev.TokensOut
	}
	if ev.CostUSD > res.Usage.CostUSD {
		res.Usage.CostUSD = ev.CostUSD
	}
	switch ev.Kind {
	case harness.EventResult:
		if ev.Text != "" {
			res.ResultText = ev.Text
		}
	case harness.EventUsage:
		// Token maxima already folded above.
	}
}
