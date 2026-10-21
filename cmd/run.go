package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/traust-security/traust-cli/internal/api"
	"github.com/traust-security/traust-cli/internal/bootstrap"
	"github.com/traust-security/traust-cli/internal/catalog"
	"github.com/traust-security/traust-cli/internal/harness"
	"github.com/traust-security/traust-cli/internal/runner"
	"github.com/traust-security/traust-cli/internal/theme"
	"github.com/traust-security/traust-cli/internal/tui"
)

var runFlags struct {
	harnessID            string
	model                string
	maxTurns             int
	unsafeFullPermission bool
	budgetOverride       string
	json                 bool
	strictValidation     bool
	noMonitor            bool
}

var runCmd = &cobra.Command{
	Use:   "run <skill> [args…]",
	Short: "Execute a skill headlessly through your agent harness",
	Long: `Composes the skill's SKILL.md into a prompt, resolves the model-floor
default from the registry, and drives the harness of your choice in headless
mode. The agent always runs from the harness root — never inside the audit
target (rule S9) — and every output line is journaled under
~/.traust/cli/runs/.

Exit codes: 0 ok · 1 error · 2 the skill needs interactive input · 3 budget
refuse band hit.`,
	Args: cobra.MinimumNArgs(1),
	Run:  runRun,
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().StringVar(&runFlags.harnessID, "harness", "", "agent harness (claude, codex, pi, opencode, crush, goose; default: state default)")
	runCmd.Flags().StringVar(&runFlags.model, "model", "", "model override (default: the registry floor for the skill's role)")
	runCmd.Flags().IntVar(&runFlags.maxTurns, "max-turns", 0, "cap agent turns (0 = harness default)")
	runCmd.Flags().BoolVar(&runFlags.unsafeFullPermission, "unsafe-full-permission", false, "grant the harness full permission for this run (also settable at init: --full-permission)")
	runCmd.Flags().StringVar(&runFlags.budgetOverride, "budget-override", "", "reason to proceed past the budget refuse band (recorded)")
	runCmd.Flags().BoolVar(&runFlags.json, "json", false, "machine-readable result summary on stdout")
	runCmd.Flags().BoolVar(&runFlags.strictValidation, "strict", false, "strict schema validation of artifacts")
	runCmd.Flags().BoolVar(&runFlags.noMonitor, "no-monitor", false, "plain output even on a TTY")
}

func runRun(cmd *cobra.Command, args []string) {
	state, err := loadState()
	if err != nil {
		fatalf(1, "%v", err)
	}
	cat, err := catalog.Load(state.TraustRepo())
	if err != nil {
		fatalf(1, "%v", err)
	}
	skill, ok := cat.ByName(args[0])
	if !ok {
		fatalf(1, "unknown skill %q — see `traust-cli skills`", args[0])
	}
	executeRun(state, skill, args[1:])
}

// runSkillInteractive is the picker continuation: ask for the invocation
// arguments, then run.
func runSkillInteractive(skill *catalog.Skill) {
	state, err := loadState()
	if err != nil {
		fatalf(1, "%v", err)
	}
	args := ""
	model := ""
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Arguments").
				Description("Passed to the skill as $ARGUMENTS (e.g. a repo path or URL).").
				Placeholder(skill.ArgumentHint).
				Value(&args),
			huh.NewInput().
				Title("Model override (optional)").
				Description("Leave empty to use the registry floor model for the skill's role.").
				Value(&model),
		),
	)
	if err := form.Run(); err != nil {
		fatalf(1, "run form: %v", err)
	}

	var runArgs []string
	if strings.TrimSpace(args) != "" {
		runArgs = strings.Fields(args)
	}
	savedModel := runFlags.model
	runFlags.model = model
	defer func() { runFlags.model = savedModel }()

	executeRun(state, skill, runArgs)
}

func executeRun(state *bootstrap.State, skill *catalog.Skill, args []string) {
	ctx := context.Background()

	harnessID := runFlags.harnessID
	if harnessID == "" {
		harnessID = state.Harness
	}
	h, err := harness.Get(harnessID)
	if err != nil {
		fatalf(1, "%v", err)
	}
	detection := h.Detect(ctx)
	if !detection.Available {
		fatalf(1, "%s binary %q not found — install it or pick another harness with --harness", h.DisplayName(), h.Binary())
	}

	client := api.NewPythonCLI(state.VenvPython(), state.ChildEnv(), state.TraustRepo())

	// Full-permission mode: explicit per-run flag, or the init-time consent
	// recorded in state. The harness's skills require unattended tool use.
	unsafeFullPermission := runFlags.unsafeFullPermission || state.FullPermission
	if unsafeFullPermission && !runFlags.unsafeFullPermission {
		fmt.Fprintln(os.Stderr, theme.Default().Warn.Render("note: full-permission mode active (consented at init; re-run `traust-cli init` to change) — harness runs with all tools auto-approved"))
	}

	if skill.Standalone == nil {
		styles := theme.Default()
		fmt.Fprintln(os.Stderr, styles.Warn.Render("warning: "+fmt.Sprintf("%q is not in the standalone profile — it may expect campaign infrastructure (siblings, ledger, tracker)", skill.Name)))
	}

	opts := runner.Options{
		State:                state,
		API:                  client,
		Harness:              h,
		Skill:                skill,
		Args:                 args,
		Model:                runFlags.model,
		MaxTurns:             runFlags.maxTurns,
		UnsafeFullPermission: unsafeFullPermission,
		BudgetOverrideReason: runFlags.budgetOverride,
		StrictValidation:     runFlags.strictValidation,
		DeclareSpend:         true,
	}

	var res *runner.RunResult
	interactive := isTTY() && !runFlags.json && !runFlags.noMonitor
	if interactive {
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		err = tui.RunMonitor(tui.MonitorHeader{
			Skill: skill.Name, Harness: h.ID(), Model: runFlags.model,
		}, cancel, func(sink runner.Sink) error {
			r, rerr := runner.Run(runCtx, opts, sink)
			if r != nil {
				res = r
			}
			return rerr
		})
	} else {
		res, err = runner.Run(ctx, opts, plainSink{})
	}
	if err != nil {
		fatalf(1, "run: %v", err)
	}
	if res == nil {
		fatalf(1, "run produced no result")
	}

	if runFlags.json {
		out, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(out))
	} else {
		// The summary prints after the monitor too: artifacts, journal
		// location and the failure reason must not vanish with the TUI.
		printRunSummary(res)
	}
	os.Exit(res.ExitCode)
}

func printRunSummary(res *runner.RunResult) {
	styles := theme.Default()
	section("Run result")
	kv("run id", res.RunID)
	kv("journal", res.Dir)
	kv("model", res.Model)
	if res.Usage.TokensIn+res.Usage.TokensOut > 0 {
		kv("tokens", fmt.Sprintf("in %d · out %d", res.Usage.TokensIn, res.Usage.TokensOut))
	}
	if res.BudgetCap > 0 {
		kv("budget", fmt.Sprintf("%s band · cap %d output tokens", res.BudgetBand, res.BudgetCap))
	}
	if res.HarnessError != "" {
		fmt.Println()
		fmt.Println(styles.Err.Render("harness error:"))
		fmt.Println(indent(res.HarnessError, "  "))
	}
	if len(res.Artifacts) > 0 {
		fmt.Println()
		for _, a := range res.Artifacts {
			mark := styles.OK.Render("✓")
			if !a.Valid {
				mark = styles.Err.Render("✗")
			}
			extra := ""
			if a.Rendered != "" {
				extra = styles.Muted.Render(" (rendered → " + a.Rendered + ")")
			}
			fmt.Printf("  %s %s%s\n", mark, a.Path, extra)
		}
	}
	if res.ResultText != "" {
		fmt.Println()
		fmt.Println(styles.Muted.Render("final message:"))
		fmt.Println(indent(tui.Wrap(res.ResultText, tui.TerminalWidth()-4), "  "))
	}
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// plainSink prints run updates as styled plain text (non-TTY or --no-monitor).
type plainSink struct{}

func (plainSink) Progress(msg string) {
	styles := theme.Default()
	style := styles.Accent
	if strings.HasPrefix(msg, "warning:") || strings.HasPrefix(msg, "note:") {
		style = styles.Warn
	}
	if strings.Contains(msg, "failed") || strings.Contains(msg, "refused") {
		style = styles.Err
	}
	fmt.Println(tui.Wrap(style.Render("› "+msg), tui.TerminalWidth()))
}

func (plainSink) Event(ev harness.Event) {
	styles := theme.Default()
	switch ev.Kind {
	case harness.EventText:
		fmt.Println(tui.Wrap(ev.Text, tui.TerminalWidth()))
	case harness.EventThinking:
		fmt.Println(tui.Wrap(styles.Muted.Render("· "+ev.Text), tui.TerminalWidth()))
	case harness.EventToolCall:
		fmt.Println(styles.Accent.Render("→ " + ev.Tool))
	case harness.EventResult:
		// Result text is printed in the summary.
	case harness.EventError:
		fmt.Println(tui.Wrap(styles.Err.Render("✗ "+ev.Text), tui.TerminalWidth()))
	}
}

func (plainSink) Raw(line string) {
	if strings.TrimSpace(line) != "" {
		fmt.Println(tui.Wrap(theme.Default().Muted.Render("  "+line), tui.TerminalWidth()))
	}
}

func (plainSink) Done() {}
