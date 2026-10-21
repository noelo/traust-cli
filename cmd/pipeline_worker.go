package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
)

var pipelineWorkerCmd = &cobra.Command{
	Use:    "_pipeline <flow-dir>",
	Short:  "internal: execute one background pipeline",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		os.Exit(runPipelineWorker(args[0]))
	},
}

func init() {
	rootCmd.AddCommand(pipelineWorkerCmd)
}

// runPipelineWorker executes a flow: stages run sequentially as ordinary
// runs, each stage's inputs derived from the previous stage's artifacts,
// with the manifest updated after every transition. Stop signals cancel the
// current stage's run (the worker tears it down gracefully) and fail the
// flow.
func runPipelineWorker(dir string) int {
	manifest, err := loadFlowManifest(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline: load manifest: %v\n", err)
		return 1
	}
	data, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline: read request: %v\n", err)
		return 1
	}
	var req flowRequest
	if err := json.Unmarshal(data, &req); err != nil {
		fmt.Fprintf(os.Stderr, "pipeline: parse request: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A cancelled context fails the flow after the current stage unwinds.
	cancelled := func() bool { return ctx.Err() != nil }

	runsDir := filepath.Join(filepath.Dir(filepath.Dir(dir)), "runs")
	auditReport := ""
	triageJSON := req.FindingsPath

	fail := func(stage *flowStage, code int, detail string) int {
		stage.State = stateFailed
		stage.Detail = detail
		manifest.State = stateFailed
		manifest.save(dir)
		return code
	}

	for i := range manifest.Stages {
		stage := &manifest.Stages[i]
		if stage.State == stateDone || stage.State == "skipped" {
			continue
		}

		// Derive this stage's arguments from what previous stages produced.
		var args []string
		switch stage.Name {
		case "audit":
			args = []string{req.Target}
		case "triage":
			if auditReport == "" {
				// Entry via remediate-verify or a completed audit-triage
				// flow: the findings path may itself be a report.
				src := triageJSON
				if src == "" {
					return fail(stage, 1, "no audit report to triage")
				}
				args = []string{src, "--auto"}
			} else {
				args = []string{auditReport, "--auto"}
			}
		case "patch":
			src := triageJSON
			if src == "" {
				return fail(stage, 1, "no triaged findings to patch")
			}
			args = []string{src}
			if req.Target != "" {
				args = append(args, "--repo", req.Target)
			}
		case "verify":
			if auditReport == "" {
				// remediate-verify entry: the findings path carries the
				// report when the user passes a security-audit json.
				if len(filepath.Ext(triageJSON)) == 4 { // .json
					auditReport = triageJSON
				}
			}
			if auditReport == "" {
				stage.State = "skipped"
				stage.Detail = "no original audit report available for verification"
				manifest.save(dir)
				continue
			}
			applied, err := buildPatchedRepo(ctx, req.Target, dir, filepath.Join(dir, "patched-repo"))
			if err != nil {
				return fail(stage, 1, fmt.Sprintf("build patched repo: %v", err))
			}
			if applied == 0 {
				stage.State = "skipped"
				stage.Detail = "no patches were applicable — nothing to verify"
				manifest.save(dir)
				continue
			}
			stage.Detail = fmt.Sprintf("%d patch(es) applied", applied)
			args = []string{auditReport, filepath.Join(dir, "patched-repo")}
		default:
			return fail(stage, 1, "unknown stage "+stage.Name)
		}

		stage.State = stateRunning
		manifest.save(dir)

		runID, err := newRunID(stage.Skill)
		if err != nil {
			return fail(stage, 1, err.Error())
		}
		stage.RunID = runID
		manifest.save(dir)

		code, err := runStage(dir, req, stage.Skill, runID, args)
		if err != nil {
			return fail(stage, 1, err.Error())
		}
		if cancelled() {
			return fail(stage, code, "pipeline cancelled")
		}
		if code != 0 {
			return fail(stage, code, fmt.Sprintf("stage exited %d — see `traust-cli result %s`", code, shortRunID(runID)))
		}

		stageRunDir := filepath.Join(runsDir, runID)
		switch stage.Name {
		case "audit":
			artifact, err := findRunArtifact(stageRunDir, "-security-audit.json")
			if err != nil {
				return fail(stage, 1, err.Error())
			}
			auditReport = artifact
			stage.Artifact = artifact
		case "triage":
			artifact, err := findRunArtifact(stageRunDir, "-triage.json")
			if err != nil {
				return fail(stage, 1, err.Error())
			}
			triageJSON = artifact
			stage.Artifact = artifact
		case "patch":
			stage.Artifact = filepath.Join(harnessRootOf(dir), "PATCHES.json")
		}
		stage.State = stateDone
		manifest.save(dir)
	}

	manifest.State = stateDone
	manifest.save(dir)
	return 0
}
