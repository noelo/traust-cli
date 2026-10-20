// Package bootstrap owns the traust-cli managed workspace: the pinned traust
// checkout, the uv/Python toolchain, TRAUST_CONFIG_HOME, and the CLI's own
// state. Nothing here writes to shell profiles — the CLI injects the
// environment into the child processes it spawns.
package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StateVersion is the schema version of state.json.
const StateVersion = 1

// ErrNotInitialized is returned when the workspace has not been set up yet.
var ErrNotInitialized = errors.New("traust-cli is not initialized — run `traust-cli init` first")

// State is the CLI's persisted workspace state, stored at
// <workspace>/cli/state.json. It is deliberately outside TRAUST_CONFIG_HOME,
// which is schema-gated deployment configuration owned by the harness.
type State struct {
	Version           int       `json:"version"`
	Workspace         string    `json:"workspace"`
	ConfigHome        string    `json:"config_home"`
	TraustRef         string    `json:"traust_ref"`
	TraustSHA         string    `json:"traust_sha"`
	UvVersion         string    `json:"uv_version,omitempty"`
	Harness           string    `json:"harness,omitempty"`
	ToolchainProfiles []string  `json:"toolchain_profiles,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// DefaultWorkspace returns the default managed workspace root (~/.traust).
func DefaultWorkspace() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".traust"), nil
}

// StatePath returns the state file location for a workspace.
func StatePath(workspace string) string {
	return filepath.Join(workspace, "cli", "state.json")
}

// DefaultState constructs a fresh state with the given workspace root.
func DefaultState(workspace string) *State {
	configHome := filepath.Join(workspace, "config")
	return &State{
		Version:    StateVersion,
		Workspace:  workspace,
		ConfigHome: configHome,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
}

// LoadState reads the state for a workspace, or ErrNotInitialized.
func LoadState(workspace string) (*State, error) {
	if workspace == "" {
		var err error
		workspace, err = DefaultWorkspace()
		if err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(StatePath(workspace))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", StatePath(workspace), err)
	}
	if s.Version > StateVersion {
		return nil, fmt.Errorf("state version %d is newer than this binary understands (%d) — upgrade traust-cli", s.Version, StateVersion)
	}
	return &s, nil
}

// Save writes the state atomically.
func (s *State) Save() error {
	s.UpdatedAt = time.Now().UTC()
	if err := os.MkdirAll(filepath.Dir(StatePath(s.Workspace)), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := StatePath(s.Workspace) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return os.Rename(tmp, StatePath(s.Workspace))
}

// TraustRepo is the pinned traust checkout inside the workspace.
func (s *State) TraustRepo() string { return filepath.Join(s.Workspace, "traust") }

// VenvBin is the harness virtualenv's bin directory.
func (s *State) VenvBin() string { return filepath.Join(s.TraustRepo(), ".venv", "bin") }

// VenvPython is the venv interpreter, preferring python3.
func (s *State) VenvPython() string {
	for _, name := range []string{"python3", "python"} {
		p := filepath.Join(s.VenvBin(), name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return filepath.Join(s.VenvBin(), "python3")
}

// CliBinDir is the CLI-managed binary directory (uv, scanner toolchain).
func (s *State) CliBinDir() string { return filepath.Join(s.Workspace, "bin") }

// RunsDir is where run journals are written.
func (s *State) RunsDir() string { return filepath.Join(s.Workspace, "cli", "runs") }

// ChildEnv returns the environment for child processes: the venv and CLI bin
// directories prepended to PATH, TRAUST_CONFIG_HOME and HARNESS_BIN_DIR set.
// This replaces the shell-profile writes that scripts/install_traust performs;
// the CLI owns the environment it injects.
func (s *State) ChildEnv() []string {
	env := os.Environ()
	path := os.Getenv("PATH")
	prepend := []string{s.VenvBin(), s.CliBinDir()}
	var kept []string
	for _, dir := range strings.Split(path, string(os.PathListSeparator)) {
		if dir == "" {
			continue
		}
		dup := false
		for _, p := range prepend {
			if dir == p {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, dir)
		}
	}
	newPath := strings.Join(prepend, string(os.PathListSeparator))
	if len(kept) > 0 {
		newPath += string(os.PathListSeparator) + strings.Join(kept, string(os.PathListSeparator))
	}
	env = setEnv(env, "PATH", newPath)
	env = setEnv(env, "TRAUST_CONFIG_HOME", s.ConfigHome)
	env = setEnv(env, "HARNESS_BIN_DIR", s.CliBinDir())
	return env
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := env[:0:0] // copy-on-write slice
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, key+"="+value)
}
