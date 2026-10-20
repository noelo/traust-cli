package cmd

import (
	"github.com/traust-security/traust-cli/internal/bootstrap"
)

// workspaceFlag overrides the default managed workspace (~/.traust) for every
// command — useful for multiple deployments and for testing.
var workspaceFlag string

func init() {
	rootCmd.PersistentFlags().StringVar(&workspaceFlag, "workspace", "", "managed workspace root (default ~/.traust)")
}

// loadState loads the workspace state, exiting with a helpful message when
// uninitialized.
func loadState() (*bootstrap.State, error) {
	return bootstrap.LoadState(workspaceFlag)
}
