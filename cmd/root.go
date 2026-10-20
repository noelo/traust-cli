package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/traust-security/traust-cli/internal/theme"
)

// version is set at build time via -ldflags.
var version = "0.1.0"

var rootCmd = &cobra.Command{
	Use:   "traust-cli",
	Short: "One binary for the traust security workflow",
	Long: `traust-cli wraps the traust harness end to end:

  init      set up the managed workspace (uv, traust checkout, config home)
  doctor    verify the install
  skills    browse the skill catalog
  run       execute a skill headlessly through your agent harness
  report    validate and browse a report
  ledger    read-only ledger views

The CLI delegates every deterministic operation to the traust engine CLI and
adds the user experience: catalog, harness adapters (claude, codex, pi,
opencode, crush, goose), model-floor pre-flight, budget bands and run
journals.`,
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		styles := theme.Default()
		fmt.Fprintln(os.Stderr, styles.Err.Render("error: "+err.Error()))
		os.Exit(1)
	}
}

func init() {
	rootCmd.SetVersionTemplate(theme.Default().App.Render(" traust-cli ") + " {{.Version}}\n")
}

// fatalf prints a styled error and exits with the given code.
func fatalf(code int, format string, args ...any) {
	fmt.Fprintln(os.Stderr, theme.Default().Err.Render("error: "+fmt.Sprintf(format, args...)))
	os.Exit(code)
}

// section prints a section header.
func section(title string) {
	fmt.Println()
	fmt.Println(theme.Default().Header.Render(title))
}

// kv prints a label/value line.
func kv(label, value string) {
	styles := theme.Default()
	fmt.Printf("  %s %s\n", styles.Label.Render(pad(label)), styles.Value.Render(value))
}

func pad(s string) string {
	if len(s) >= 14 {
		return s
	}
	return s + strings.Repeat(" ", 14-len(s))
}
