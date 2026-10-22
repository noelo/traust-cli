package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultForge is the public traust repository.
const DefaultForge = "https://github.com/traust-security/traust.git"

// InitOptions configures `traust-cli init`.
type InitOptions struct {
	// Workspace is the managed workspace root (default ~/.traust).
	Workspace string
	// Forge is the git URL to clone traust from.
	Forge string
	// Ref is a tag or branch to check out; empty means the latest stable tag.
	Ref string
	// ConfigHome overrides TRAUST_CONFIG_HOME (default <workspace>/config).
	ConfigHome string
	// ToolchainProfile installs a scanner toolchain profile; empty or "none" skips.
	ToolchainProfile string
	// WriteProfile passes --write-profile through to install_traust so the
	// shell profile records TRAUST_CONFIG_HOME (off by default; the CLI
	// injects the environment itself).
	WriteProfile bool
	// Harness is the default harness id to store in state; empty auto-detects.
	Harness string
	// FullPermission records the consent to run harnesses with all tools
	// auto-approved (the traust skills require it).
	FullPermission bool
}

// LogFn receives progress lines during init.
type LogFn func(format string, args ...any)

// Init performs the full bootstrap: uv, clone, uv sync, config home, optional
// toolchain. It is idempotent — re-running repairs and updates.
func Init(ctx context.Context, opts InitOptions, log LogFn) (*State, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	if opts.Workspace == "" {
		var err error
		opts.Workspace, err = DefaultWorkspace()
		if err != nil {
			return nil, err
		}
	}
	if opts.Forge == "" {
		opts.Forge = DefaultForge
	}

	existing, loadErr := LoadState(opts.Workspace)
	var state *State
	if loadErr == nil {
		state = existing
		log("existing workspace found at %s (traust %s)", opts.Workspace, state.TraustRef)
	} else {
		state = DefaultState(opts.Workspace)
	}
	if opts.ConfigHome != "" {
		state.ConfigHome = opts.ConfigHome
	}

	for _, dir := range []string{state.Workspace, state.CliBinDir(), filepath.Dir(StatePath(state.Workspace))} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	// 1. uv + Python.
	uvPath, err := EnsureUv(state.CliBinDir(), log)
	if err != nil {
		return nil, fmt.Errorf("bootstrap uv: %w", err)
	}
	state.UvVersion = UvVersion

	// 2. Clone or update the traust checkout.
	repo := state.TraustRepo()
	ref := opts.Ref
	if ref == "" {
		ref = "latest"
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".git")); statErr == nil {
		log("updating existing traust checkout at %s", repo)
		if err := gitRun(ctx, repo, "fetch", "--tags", "--quiet"); err != nil {
			return nil, fmt.Errorf("git fetch: %w", err)
		}
	} else {
		log("cloning %s", opts.Forge)
		if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
			return nil, err
		}
		if err := gitRun(ctx, filepath.Dir(repo), "clone", opts.Forge, repo); err != nil {
			return nil, fmt.Errorf("git clone: %w", err)
		}
	}
	resolved, err := resolveRef(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
	if err := gitRun(ctx, repo, "checkout", "--quiet", resolved); err != nil {
		return nil, fmt.Errorf("git checkout %s: %w", resolved, err)
	}
	sha, err := gitOut(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	state.TraustRef = strings.TrimSpace(resolved)
	state.TraustSHA = strings.TrimSpace(sha)
	log("traust pinned at %s (%s)", state.TraustRef, state.TraustSHA[:12])

	// 3. uv sync (uv manages its own Python when the host's is too old).
	log("uv sync — installing the engine, ledger and contracts from their pins")
	sync := exec.CommandContext(ctx, uvPath, "sync", "--python", "3.12")
	sync.Dir = repo
	sync.Stdout = lineWriter(log)
	sync.Stderr = lineWriter(log)
	if err := sync.Run(); err != nil {
		return nil, fmt.Errorf("uv sync: %w", err)
	}

	// 4. TRAUST_CONFIG_HOME from the shipped templates, via install_traust.
	if _, statErr := os.Stat(filepath.Join(state.ConfigHome, "corpus-config.yaml")); statErr == nil {
		log("config home %s already populated — keeping existing files", state.ConfigHome)
	} else {
		log("creating TRAUST_CONFIG_HOME at %s from the shipped templates", state.ConfigHome)
		args := []string{"scripts/install_traust", "--yes", "--config-home", state.ConfigHome, "--no-toolchain"}
		if !opts.WriteProfile {
			args = append(args, "--no-profile-write")
		}
		inst := exec.CommandContext(ctx, "bash", args...)
		inst.Dir = repo
		inst.Stdout = lineWriter(log)
		inst.Stderr = lineWriter(log)
		if err := inst.Run(); err != nil {
			return nil, fmt.Errorf("install_traust: %w", err)
		}
	}

	// 4b. Anchor analysis_results inside the managed workspace (set only
	// when empty): campaign-layout skills resolve their output root through
	// locations.yaml, and an empty value sends the agent hunting for any
	// analysis-results checkout it can find. A self-contained workspace
	// keeps every artifact inside it — and inside artifact discovery.
	if err := ensureAnalysisResults(state, log); err != nil {
		log("warning: could not set analysis_results in locations.yaml: %v", err)
	}

	// 5. Scanner toolchain profile: secure-code-audit by default — it
	// covers every stage of the default flow (a superset of the vuln-scan
	// and triage profiles). Already-installed profiles are skipped so
	// re-running init stays fast and idempotent.
	if opts.ToolchainProfile != "" && opts.ToolchainProfile != "none" {
		installed := false
		for _, p := range state.ToolchainProfiles {
			if p == opts.ToolchainProfile {
				installed = true
				break
			}
		}
		if installed {
			log("toolchain profile %s already installed — skipping", opts.ToolchainProfile)
		} else {
			log("installing toolchain profile %s", opts.ToolchainProfile)
			py := state.VenvPython()
			tc := exec.CommandContext(ctx, py, "-m", "traust.cli", "admin", "toolchain", "setup", "--profile", opts.ToolchainProfile)
			tc.Dir = repo
			tc.Env = state.ChildEnv()
			tc.Stdout = lineWriter(log)
			tc.Stderr = lineWriter(log)
			if err := tc.Run(); err != nil {
				return nil, fmt.Errorf("toolchain setup (%s): %w", opts.ToolchainProfile, err)
			}
			state.ToolchainProfiles = appendUnique(state.ToolchainProfiles, opts.ToolchainProfile)
		}
	}

	// 6. Default harness.
	if opts.Harness != "" {
		state.Harness = opts.Harness
	}
	if state.Harness == "" {
		state.Harness = "claude"
	}

	// 7. Full-permission consent (explicit --full-permission or a prior
	// consent survives re-inits; the wizard's answer arrives via opts).
	if opts.FullPermission {
		state.FullPermission = true
	}

	if err := state.Save(); err != nil {
		return nil, err
	}
	log("workspace ready: %s", state.Workspace)
	return state, nil
}

// Update moves the pinned checkout to a newer tag (or the given ref) and
// re-syncs.
func Update(ctx context.Context, state *State, ref string, log LogFn) (*State, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	return Init(ctx, InitOptions{
		Workspace:        state.Workspace,
		Forge:            DefaultForge,
		Ref:              ref,
		ConfigHome:       state.ConfigHome,
		ToolchainProfile: "none",
		Harness:          state.Harness,
	}, log)
}

var stableTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// resolveRef maps "latest" onto the newest stable vX.Y.Z tag.
func resolveRef(ctx context.Context, repo, ref string) (string, error) {
	if ref != "" && ref != "latest" {
		return ref, nil
	}
	out, err := gitOut(ctx, repo, "tag", "--list", "v*")
	if err != nil {
		return "", err
	}
	var tags []string
	for _, t := range strings.Split(strings.TrimSpace(out), "\n") {
		if stableTag.MatchString(strings.TrimSpace(t)) {
			tags = append(tags, strings.TrimSpace(t))
		}
	}
	if len(tags) == 0 {
		return "main", nil
	}
	sort.Slice(tags, func(i, j int) bool { return versionLess(tags[i], tags[j]) })
	return tags[len(tags)-1], nil
}

// versionLess compares dotted v-prefixed tags numerically.
func versionLess(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		var na, nb int
		fmt.Sscanf(pa[i], "%d", &na)
		fmt.Sscanf(pb[i], "%d", &nb)
		if na != nb {
			return na < nb
		}
	}
	return len(pa) < len(pb)
}

// ensureAnalysisResults sets locations.yaml's analysis_results to the
// workspace's analysis-results directory when (and only when) it is empty:
// user-customized values are never touched. The line-oriented edit mirrors
// the template's simple "key: value" shape.
func ensureAnalysisResults(state *State, log LogFn) error {
	path := filepath.Join(state.ConfigHome, "locations.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	want := filepath.Join(state.Workspace, "analysis-results")
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "analysis_results:") {
			continue
		}
		val := strings.TrimPrefix(trimmed, "analysis_results:")
		if idx := strings.Index(val, "#"); idx >= 0 {
			val = val[:idx]
		}
		val = strings.Trim(strings.TrimSpace(val), "\"'")
		if val != "" {
			return nil // user-configured; leave it alone
		}
		lines[i] = "analysis_results: " + want
		out := strings.Join(lines, "\n")
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			return err
		}
		if err := os.MkdirAll(want, 0o755); err != nil {
			return err
		}
		log("analysis_results anchored at %s (locations.yaml was empty)", want)
		return nil
	}
	return nil
}

func gitRun(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %v: %w", args, err)
	}
	return string(out), nil
}

// lineWriter forwards subprocess output to the log function one line at a time.
func lineWriter(log LogFn) *logLineWriter { return &logLineWriter{log: log} }

type logLineWriter struct {
	log LogFn
	buf bytes.Buffer
}

func (w *logLineWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			// Partial line: put it back for the next flush.
			w.buf.WriteString(line)
			break
		}
		w.log("  %s", strings.TrimRight(line, "\r\n"))
	}
	return n, nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
