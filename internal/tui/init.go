package tui

import (
	"github.com/charmbracelet/huh"
)

// InitAnswers is the outcome of the init wizard.
type InitAnswers struct {
	Workspace        string
	Forge            string
	Ref              string
	ToolchainProfile string
	Harness          string
	FullPermission   bool
}

// RunInitWizard asks the interactive setup questions. detected lists the
// harness binaries found on PATH (id → version hint); defaultHarness is the
// preselected one.
func RunInitWizard(defaultWorkspace, defaultForge string, detected []string, defaultHarness string) (*InitAnswers, error) {
	answers := &InitAnswers{
		Workspace:        defaultWorkspace,
		Forge:            defaultForge,
		Ref:              "latest",
		ToolchainProfile: "none",
		Harness:          defaultHarness,
	}

	harnessOptions := []huh.Option[string]{}
	for _, id := range detected {
		harnessOptions = append(harnessOptions, huh.NewOption(id, id))
	}
	if len(harnessOptions) == 0 {
		harnessOptions = append(harnessOptions, huh.NewOption("claude (not detected — install it before running)", "claude"))
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Workspace directory").
				Description("Everything the CLI manages lives here: the traust checkout, config, binaries, run journals.").
				Placeholder(defaultWorkspace).
				Value(&answers.Workspace),
			huh.NewInput().
				Title("traust repository").
				Description("git URL to clone the harness from.").
				Placeholder(defaultForge).
				Value(&answers.Forge),
			huh.NewInput().
				Title("Ref to pin").
				Description(`A vX.Y.Z tag, a branch, or "latest" for the newest stable tag.`).
				Placeholder("latest").
				Value(&answers.Ref),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Scanner toolchain profile").
				Description("Baseline skills (threat-model, vuln-scan) need nothing; secure-code-audit benefits from its pinned scanners.").
				Options(
					huh.NewOption("none — baseline only (threat-model, vuln-scan)", "none"),
					huh.NewOption("secure-code-audit — opengrep, gitleaks, osv-scanner, tokei", "secure-code-audit"),
					huh.NewOption("vuln-scan", "vuln-scan"),
					huh.NewOption("triage", "triage"),
				).
				Value(&answers.ToolchainProfile),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Default agent harness").
				Description("Detected on PATH. You can switch per run with --harness.").
				Options(harnessOptions...).
				Value(&answers.Harness),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Run skills with full permissions?").
				Description("The traust harness's skills REQUIRE unattended tool use (bash, file writes) — "+
					"without full permission they fail closed on the first permission request. "+
					"With it, your harness runs with all tools auto-approved "+
					"(claude --dangerously-skip-permissions, crush --yolo, codex --dangerously-bypass-approvals-and-sandbox). "+
					"S9 isolation still applies: agents run from the harness root and never load the audit target's configuration.").
				Value(&answers.FullPermission),
		),
	)
	if err := form.Run(); err != nil {
		return nil, err
	}
	return answers, nil
}
