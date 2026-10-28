package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/traust-security/traust-cli/internal/feeds"
)

var feedsMaxAge time.Duration
var feedsTimeout time.Duration

var feedsCmd = &cobra.Command{
	Use:   "feeds",
	Short: "Manage offline vulnerability feeds (grype DB, OSV databases)",
}

var feedsUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Refresh stale vulnerability databases (fail-open)",
	Long: `Refresh the grype vulnerability DB and OSV offline databases if they
are stale. Exits 0 even when updates fail, so baked databases remain usable.

Set --max-age to control the staleness threshold (default 120h = 5 days).
The grype DB is checked against this threshold; OSV databases are refreshed
unconditionally (they have no built-in staleness metadata).

Exit codes:
  0  databases are current (already fresh, or successfully updated, or
     update failed and baked databases remain in use)
  1  workspace not initialized`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx, cancel := context.WithTimeout(context.Background(), feedsTimeout)
		defer cancel()
		state, err := loadState()
		if err != nil {
			fatalf(1, "%v", err)
		}
		log := func(format string, a ...any) {
			fmt.Fprintf(cmd.ErrOrStderr(), "[feeds] %s\n", fmt.Sprintf(format, a...))
		}
		result := feeds.Update(ctx, state, feedsMaxAge, log)
		// Summary goes to stderr so the subsequent command's stdout is clean.
		fmt.Fprintf(cmd.ErrOrStderr(), "grype: %s  osv: %s\n", result.GrypeStatus, result.OsvStatus)
	},
}

func init() {
	rootCmd.AddCommand(feedsCmd)
	feedsCmd.AddCommand(feedsUpdateCmd)
	feedsUpdateCmd.Flags().DurationVar(&feedsMaxAge, "max-age", 120*time.Hour,
		"staleness threshold for the grype DB (e.g. 120h, 72h)")
	feedsUpdateCmd.Flags().DurationVar(&feedsTimeout, "timeout", 90*time.Second,
		"maximum time for the entire update (e.g. 90s, 2m)")
}
