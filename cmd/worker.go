package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/traust-security/traust-cli/internal/api"
	"github.com/traust-security/traust-cli/internal/bootstrap"
	"github.com/traust-security/traust-cli/internal/catalog"
	"github.com/traust-security/traust-cli/internal/harness"
	"github.com/traust-security/traust-cli/internal/runner"
)

// processAlive reports whether a pid is alive (signal 0 probe).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func init() {
	rootCmd.AddCommand(workerCmd)
}

var workerCmd = &cobra.Command{
	Use:    "_worker <run-dir>",
	Short:  "internal: execute one background run",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		os.Exit(runWorker(args[0]))
	},
}

// runWorker executes one background run from its run directory: options.json
// in, journal and result.json out. It owns the run's lifetime; stop signals
// (SIGINT/SIGTERM) cancel the run context, which tears the harness down
// through the same graceful paths an interactive abort uses.
func runWorker(dir string) int {
	pidPath := filepath.Join(dir, "pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "worker: write pid: %v\n", err)
		return 1
	}
	defer os.Remove(pidPath)

	var req runRequest
	data, err := os.ReadFile(filepath.Join(dir, "options.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker: read options: %v\n", err)
		return 1
	}
	if err := json.Unmarshal(data, &req); err != nil {
		fmt.Fprintf(os.Stderr, "worker: parse options: %v\n", err)
		return 1
	}

	state, err := bootstrap.LoadState(req.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker: load state: %v\n", err)
		return 1
	}
	cat, err := catalog.Load(state.TraustRepo())
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker: load catalog: %v\n", err)
		return 1
	}
	skill, ok := cat.ByName(req.Skill)
	if !ok {
		fmt.Fprintf(os.Stderr, "worker: unknown skill %q\n", req.Skill)
		return 1
	}
	h, err := harness.Get(req.Harness)
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker: %v\n", err)
		return 1
	}

	client := api.NewPythonCLI(state.VenvPython(), state.ChildEnv(), state.TraustRepo())

	// Stop signals cancel the run; the runner's teardown (crush cancel
	// endpoint, server shutdown, process kill) is entirely context-driven.
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		stop()
	}()

	res, err := runner.Run(ctx, runner.Options{
		State:                state,
		API:                  client,
		Harness:              h,
		Skill:                skill,
		Args:                 req.Args,
		Model:                req.Model,
		MaxTurns:             req.MaxTurns,
		UnsafeFullPermission: req.UnsafeFullPermission,
		BudgetOverrideReason: req.BudgetOverride,
		StrictValidation:     req.StrictValidation,
		DeclareSpend:         req.DeclareSpend,
		RunID:                req.RunID,
	}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker: %v\n", err)
		return 1
	}
	return res.ExitCode
}
