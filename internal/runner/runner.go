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
	// RunID, when set, overrides the generated run id — the background
	// launcher creates the run directory before spawning the worker, so it
	// owns the id.
	RunID string
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
	// HarnessError carries the process failure plus the tail of the
	// harness stderr log, so the actual provider error (e.g. a 401) is
	// visible without opening the journal by hand.
	HarnessError string `json:"harness_error,omitempty"`
	ResultText   string `json:"result_text,omitempty"`
	ExitCode     int    `json:"exit_code"`
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

	runID := opts.RunID
	if runID == "" {
		runID = time.Now().Format("20060102-150405") + "-" + skill.Name
	}
	res := &RunResult{RunID: runID, Skill: skill.Name, Harness: h.ID(), ExitCode: ExitOK}

	// Full-permission consent: the per-run flag OR the init-time consent
	// recorded in state (the SDK Provider path has no cmd layer to resolve
	// it).
	unsafeFullPermission := opts.UnsafeFullPermission || state.FullPermission

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
		UnsafeFullPermission: unsafeFullPermission,
		TraustRef:            state.TraustRef, TraustSHA: state.TraustSHA,
	})
	if err != nil {
		return nil, err
	}
	res.Dir = journal.Dir

	// Pre-flight model availability: when the harness can verify model ids,
	// fail fast with guidance instead of an opaque provider 401 two
	// seconds into the run.
	if pf.Model != "" {
		if mc, ok := h.(harness.ModelChecker); ok {
			if err := mc.CheckModel(ctx, pf.Model); err != nil {
				msg := fmt.Sprintf("model %q is not runnable in %s (%v)", pf.Model, h.DisplayName(), err)
				if pf.Role != "" {
					msg += fmt.Sprintf(" — the registry floor for role %q resolved to it; map the role to a model your harness can run in %s (traust registry models resolve %s), or pass --model", pf.Role, filepath.Join(state.ConfigHome, "model-registry.yaml"), pf.Role)
				} else {
					msg += " — pass a --model your harness can run"
				}
				res.ExitCode = ExitError
				res.HarnessError = msg
				sink.Progress(msg)
				if jerr := journal.WriteResult(res); jerr != nil {
					sink.Progress("warning: could not write run journal: " + jerr.Error())
				}
				sink.Done()
				return res, nil
			}
		}
	}

	before := snapshotArtifacts(state)

	// Pre-flight toolchain: skills whose scanners live in a toolchain
	// profile degrade (thinner evidence, weaker coverage) when the profile
	// is incomplete. The skills still run — this is a warning with the fix,
	// not a gate.
	if skill.Standalone != nil && skill.Standalone.Toolchain != "" {
		profile := skill.Standalone.Toolchain
		if tcres, err := opts.API.ToolchainDoctor(ctx, profile); err == nil && tcres.ExitCode != 0 {
			msg := fmt.Sprintf("toolchain profile %q for %s is incomplete — the skill will run with reduced scanner coverage; install it with `traust-cli init` (or `--toolchain %s`)", profile, skill.Name, profile)
			if tail := firstLines(tcres.Output(), 3); tail != "" {
				msg += "\n" + tail
			}
			sink.Progress("warning: " + msg)
		}
	}

	// Capability caveats, up front.
	for _, note := range h.Notes() {
		sink.Progress("note: " + note)
	}
	if unsafeFullPermission {
		sink.Progress("note: full permission mode (consented) — the harness runs with all tools auto-approved; S9 isolation, budget bands and the run journal still apply")
	}

	// Launch.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	spec := &harness.RunSpec{
		Prompt: prompt, Model: pf.Model, Cwd: root, Env: state.ChildEnv(),
		AllowedTools: skill.AllowedTools, MaxTurns: opts.MaxTurns,
		UnsafeFullPermission: unsafeFullPermission, OutDir: journal.Dir,
	}

	// handleEvent folds one normalized event into journal, sink, result and
	// the budget bands — shared by the session and process paths.
	band := BandWithin
	handleEvent := func(ev harness.Event) {
		journal.Event(string(ev.Kind), ev.Text, ev.Tool, ev.TokensIn, ev.TokensOut, ev.CostUSD, ev.IsError)
		sink.Event(ev)
		applyEvent(res, ev)
		if ev.Kind == harness.EventNeedsInput {
			res.NeedsInput = true
			cancel()
		}
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

	// Session mode first: harnesses with a managed backend (crush server)
	// stream structured events, usage and a clean cancel. Falls back to the
	// one-shot process path when the backend cannot start.
	var sessErr error
	sessionDone := false
	if sr, ok := h.(harness.SessionRunner); ok {
		sres, err := sr.RunSession(runCtx, spec, handleEvent)
		switch {
		case errors.Is(err, harness.ErrServerUnsupported):
			sink.Progress("note: crush server mode unavailable — falling back to `crush run` (plain text, no live events)")
		case err != nil:
			sessErr = err
			sessionDone = true
		default:
			sessionDone = true
			if sres.Text != "" {
				res.ResultText = sres.Text
			}
			if sres.Err != "" {
				res.HarnessError = sres.Err
			}
			if sres.Cancelled {
				res.HarnessExit = -1
			}
		}
	}

	var waitErr, scanErr error
	stderrPath := filepath.Join(journal.Dir, "harness-stderr.log")
	var plainOut strings.Builder
	if !sessionDone && sessErr == nil {
		cmd := h.Command(runCtx, spec)
		stderrFile, err := os.Create(stderrPath)
		if err != nil {
			return nil, fmt.Errorf("create stderr log: %w", err)
		}
		cmd.Stderr = stderrFile
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			stderrFile.Close()
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			stderrFile.Close()
			return nil, fmt.Errorf("start %s: %w", h.Binary(), err)
		}

		// Stream loop.
		streaming := h.Capabilities().StreamJSON
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !streaming {
				journal.Raw(line)
				plainOut.WriteString(line)
				plainOut.WriteString("\n")
				sink.Raw(line)
			} else if ev, ok := h.ParseEvent(scanner.Bytes()); ok {
				handleEvent(ev)
			} else {
				// Unparsed stream lines are journaled raw so attach replays
				// show exactly what the harness emitted; parsed lines are
				// recorded once, as events.
				journal.Raw(line)
				sink.Raw(line)
			}
		}
		scanErr = scanner.Err()
		waitErr = cmd.Wait()
		stderrFile.Close()
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			res.HarnessExit = exitErr.ExitCode()
		}
	}

	switch {
	case res.BudgetRefused:
		res.ExitCode = ExitBudget
		sink.Progress(fmt.Sprintf("budget refuse band hit (%d output tokens, cap %d) — harness terminated; re-run with --budget-override \"<reason>\" if this is expected", res.Usage.TokensOut, res.BudgetCap))
	case res.NeedsInput:
		res.ExitCode = ExitNeedsInput
		sink.Progress("the skill raised a permission request or question that a headless run cannot answer — enable full-permission mode (re-run `traust-cli init`, or --unsafe-full-permission for this run), or pre-allow the tool in your harness's config")
	case ctx.Err() != nil:
		res.Aborted = true
		res.ExitCode = ExitError
		sink.Progress("run aborted")
	case sessErr != nil:
		res.ExitCode = ExitError
		res.HarnessError = sessErr.Error()
		sink.Progress("harness session failed: " + res.HarnessError)
	case res.HarnessError != "":
		res.ExitCode = ExitError
		sink.Progress("harness run failed: " + res.HarnessError)
	case waitErr != nil:
		res.ExitCode = ExitError
		res.HarnessError = waitErr.Error()
		if tail := fileTail(stderrPath, 12); tail != "" {
			res.HarnessError += "\n" + tail
		}
		sink.Progress("harness exited with error: " + res.HarnessError)
	case scanErr != nil:
		res.ExitCode = ExitError
		sink.Progress(fmt.Sprintf("stream read error: %v", scanErr))
	}

	// Authentication failures get actionable guidance: they almost always
	// mean the registry floor resolved to a model whose provider the
	// harness cannot authenticate to (a model can be listed AND
	// unauthenticated — availability checks cannot see credentials).
	if res.HarnessError != "" && looksLikeAuthFailure(res.HarnessError) {
		guide := fmt.Sprintf("authentication failed for model %q", res.Model)
		if pf.Role != "" {
			guide += fmt.Sprintf(" (registry floor for role %q)", pf.Role)
		}
		guide += fmt.Sprintf(" — map the role to a model you can authenticate to in %s, or pass --model BEFORE the skill name (flags after the skill name belong to the skill)", filepath.Join(state.ConfigHome, "model-registry.yaml"))
		res.HarnessError += "\n" + guide
		sink.Progress("note: " + guide)
	}

	if !sessionDone && !h.Capabilities().StreamJSON && res.ResultText == "" {
		res.ResultText = strings.TrimRight(plainOut.String(), "\n")
	}

	// Post-run: artifacts, validation, rendering, spend. Discovery runs even
	// for failed, refused or aborted runs — partial outputs are exactly what
	// the user needs to see when a run dies mid-flight, and skills checkpoint
	// their state for resume.
	{
		after := snapshotArtifacts(state)
		res.Artifacts = discoverArtifacts(before, after)
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

// fileTail returns the last n lines of a file, or "" when unreadable.
func fileTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// looksLikeAuthFailure reports whether a harness error is an authentication
// failure (provider 401s and their localized variants).
func looksLikeAuthFailure(s string) bool {
	l := strings.ToLower(s)
	for _, marker := range []string{"401", "unauthorized", "authentication_error", "invalid x-api-key", "invalid api key", "permission denied: api"} {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return false
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
		if ev.IsError && ev.Text != "" {
			// Error results (claude error subtypes, crush run_complete
			// errors) can arrive with a zero process exit code; without
			// this the run would read as successful.
			res.HarnessError = ev.Text
		}
		if ev.Text != "" {
			res.ResultText = ev.Text
		}
	case harness.EventUsage:
		// Token maxima already folded above.
	}
}
