package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/traust-security/traust-cli/internal/bootstrap"

	"gopkg.in/yaml.v3"
)

// registerCorpusTrees closes the loop the harness's own workflow closes with
// /corpus-intake: when a run writes artifacts under an UNREGISTERED findings
// tree (analysis-results/findings/<org>/…), that tree is added to
// corpus-config.yaml with safe defaults — ownership "owned" (the run was
// ours), an "unassigned" business unit the operator can rename. Without
// this, every standalone flow user fails `traust-cli doctor` with "trees is
// empty" until they hand-register. Existing registrations are never
// touched, and nothing outside the findings tree is registered.
//
// Returns the trees registered during this call.
func registerCorpusTrees(state *bootstrap.State, artifacts []Artifact) []string {
	var registered []string
	touched := map[string]bool{}

	for _, a := range artifacts {
		tree, ok := findingsTreeFor(state, a.Path)
		if !ok || touched[tree] {
			continue
		}
		touched[tree] = true
		added, err := registerCorpusTree(state.ConfigHome, tree)
		if err != nil || !added {
			continue
		}
		registered = append(registered, tree)
	}
	return registered
}

// findingsTreeFor maps an artifact path onto its corpus tree key
// (findings/<org>) when the path lives under a known analysis-results root.
func findingsTreeFor(state *bootstrap.State, path string) (string, bool) {
	for _, root := range analysisResultsDirs(state) {
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		parts := strings.Split(rel, string(os.PathSeparator))
		// <findings>/<org>/… — the org level is the tree granularity.
		if len(parts) >= 2 && parts[0] == "findings" && parts[1] != "" {
			return filepath.Join("findings", parts[1]), true
		}
	}
	return "", false
}

// registerCorpusTree adds one tree to corpus-config.yaml. False when the
// tree is already registered, the config is absent/unparseable (the engine's
// own gates own that failure), or the write fails.
func registerCorpusTree(configHome, tree string) (bool, error) {
	path := filepath.Join(configHome, "corpus-config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return false, fmt.Errorf("parse corpus-config.yaml: %w", err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	rawTrees, _ := cfg["trees"].(map[string]any)
	if rawTrees == nil {
		rawTrees = map[string]any{}
	}
	if _, exists := rawTrees[tree]; exists {
		return false, nil
	}

	org := strings.TrimPrefix(tree, "findings/")
	rawTrees[tree] = map[string]any{
		"label":         org,
		"ownership":     "owned",
		"business_unit": "unassigned",
	}
	cfg["trees"] = rawTrees

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}
