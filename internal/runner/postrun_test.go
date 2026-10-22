package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/traust-security/traust-cli/internal/bootstrap"
)

// TestDiscoverCampaignLayoutArtifacts is the regression test for the
// audit-triage flow failure: skills whose output root resolves through
// locations.yaml write findings/<org>/<repo>/<repo>-security-audit.json
// several levels deep — depth-one discovery never saw them, so a
// successful audit produced "no artifact" and failed the flow.
func TestDiscoverCampaignLayoutArtifacts(t *testing.T) {
	ws := t.TempDir()
	state := bootstrap.DefaultState(ws)
	if err := os.MkdirAll(state.TraustRepo(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state.ConfigHome, 0o755); err != nil {
		t.Fatal(err)
	}

	// locations.yaml points analysis_results at a findings tree (the shape
	// init now writes into managed workspaces).
	ar := filepath.Join(ws, "analysis-results")
	if err := os.WriteFile(filepath.Join(state.ConfigHome, "locations.yaml"),
		[]byte("analysis_results: "+ar+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings := filepath.Join(ar, "findings", "example", "demo-shop")
	if err := os.MkdirAll(findings, 0o755); err != nil {
		t.Fatal(err)
	}

	before := snapshotArtifacts(state)

	report := filepath.Join(findings, "demo-shop-security-audit.json")
	if err := os.WriteFile(report, []byte(`{"findings": []}`), 0o644); err != nil {
		t.Fatal(err)
	}

	after := snapshotArtifacts(state)
	arts := discoverArtifacts(before, after)
	if len(arts) != 1 || arts[0].Path != report {
		t.Fatalf("artifacts = %+v, want the campaign-layout report", arts)
	}
	if arts[0].Kind != "report" {
		t.Errorf("kind = %s", arts[0].Kind)
	}
}

// TestSnapshotSkipsNoiseDirectories verifies the recursive walk ignores
// non-report trees (graph dbs, metrics, manifests) but keeps findings.
func TestSnapshotSkipsNoiseDirectories(t *testing.T) {
	ws := t.TempDir()
	state := bootstrap.DefaultState(ws)
	os.MkdirAll(state.TraustRepo(), 0o755)
	ar := filepath.Join(ws, "analysis-results")
	os.WriteFile(filepath.Join(state.ConfigHome, "locations.yaml"), []byte("analysis_results: "+ar+"\n"), 0o644)

	keep := filepath.Join(ar, "findings", "org", "repo", "repo-triage.json")
	noise := []string{
		filepath.Join(ar, "graph", "portfolio-graph.db"),
		filepath.Join(ar, "metrics", "spend.jsonl"),
		filepath.Join(ar, "findings", "_manifest", "manifest.json"),
	}
	for _, p := range append(noise, keep) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}

	snap := snapshotArtifacts(state)
	if _, ok := snap[keep]; !ok {
		t.Errorf("findings file not tracked: %+v", snap)
	}
	for _, p := range noise {
		if _, ok := snap[p]; ok {
			t.Errorf("noise tracked: %s", p)
		}
	}
}
