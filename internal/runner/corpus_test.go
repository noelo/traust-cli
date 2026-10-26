package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/traust-security/traust-cli/internal/bootstrap"
	"gopkg.in/yaml.v3"
)

// TestRegisterCorpusTrees is the regression test for the doctor failure
// every standalone flow user hit: runs write findings trees that nothing
// registers, and `traust-cli doctor` fails with "trees is empty" until the
// operator hand-registers (the harness's own workflow closes this loop with
// /corpus-intake; our flow path bypassed it).
func TestRegisterCorpusTrees(t *testing.T) {
	ws := t.TempDir()
	state := bootstrap.DefaultState(ws)
	os.MkdirAll(state.TraustRepo(), 0o755)
	os.MkdirAll(state.ConfigHome, 0o755)

	// analysis_results points inside the workspace; the artifact lands
	// under findings/<org>/<repo>/.
	ar := filepath.Join(ws, "analysis-results")
	os.WriteFile(filepath.Join(state.ConfigHome, "locations.yaml"), []byte("analysis_results: "+ar+"\n"), 0o644)
	findings := filepath.Join(ar, "findings", "example", "demo-shop")
	os.MkdirAll(findings, 0o755)
	artifact := filepath.Join(findings, "demo-shop-security-audit.json")
	os.WriteFile(artifact, []byte(`{}`), 0o644)

	// Empty shipped corpus-config.
	corpusPath := filepath.Join(state.ConfigHome, "corpus-config.yaml")
	os.WriteFile(corpusPath, []byte("trees: {}\n"), 0o644)

	registered := registerCorpusTrees(state, []Artifact{{Path: artifact, Kind: "report"}})
	if len(registered) != 1 || registered[0] != filepath.Join("findings", "example") {
		t.Fatalf("registered = %v, want [findings/example]", registered)
	}

	// The tree is in the config with safe defaults.
	var cfg map[string]any
	data, _ := os.ReadFile(corpusPath)
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	trees, _ := cfg["trees"].(map[string]any)
	tree, ok := trees[filepath.Join("findings", "example")].(map[string]any)
	if !ok {
		t.Fatalf("tree not registered: %v", cfg)
	}
	if tree["ownership"] != "owned" || tree["label"] != "example" || tree["business_unit"] != "unassigned" {
		t.Errorf("tree defaults = %v", tree)
	}

	// Re-running does not duplicate or overwrite.
	if again := registerCorpusTrees(state, []Artifact{{Path: artifact}}); len(again) != 0 {
		t.Errorf("re-registration = %v (existing trees must be untouched)", again)
	}
}

// TestRegisterCorpusTreesSkipsExistingAndForeign verifies hand-registered
// trees are never touched and artifacts outside the findings tree are
// ignored.
func TestRegisterCorpusTreesSkipsExistingAndForeign(t *testing.T) {
	ws := t.TempDir()
	state := bootstrap.DefaultState(ws)
	os.MkdirAll(state.TraustRepo(), 0o755)
	os.MkdirAll(state.ConfigHome, 0o755)

	ar := filepath.Join(ws, "analysis-results")
	os.WriteFile(filepath.Join(state.ConfigHome, "locations.yaml"), []byte("analysis_results: "+ar+"\n"), 0o644)
	findings := filepath.Join(ar, "findings", "example", "repo")
	os.MkdirAll(findings, 0o755)
	artifact := filepath.Join(findings, "repo-security-audit.json")
	os.WriteFile(artifact, []byte(`{}`), 0o644)

	// The operator already registered this tree with THEIR values.
	corpusPath := filepath.Join(state.ConfigHome, "corpus-config.yaml")
	os.WriteFile(corpusPath, []byte("trees:\n  findings/example:\n    label: custom\n    ownership: upstream\n    business_unit: Supply Chain\n"), 0o644)

	if registered := registerCorpusTrees(state, []Artifact{{Path: artifact}}); len(registered) != 0 {
		t.Fatalf("hand-registered tree was re-registered: %v", registered)
	}
	data, _ := os.ReadFile(corpusPath)
	if d := string(data); !strings.Contains(d, "ownership: upstream") {
		t.Errorf("operator registration overwritten: %s", data)
	}

	// A harness-root artifact (standalone convention) registers nothing.
	if registered := registerCorpusTrees(state, []Artifact{{Path: filepath.Join(state.TraustRepo(), "x-security-audit.json")}}); len(registered) != 0 {
		t.Errorf("non-findings artifact registered a tree: %v", registered)
	}
}
