package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/traust-security/traust-cli/internal/api"
	"github.com/traust-security/traust-cli/internal/bootstrap"
)

// Artifact is one output file a run produced or modified.
type Artifact struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"` // report | threat-model | json | markdown
	Valid    bool   `json:"valid,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Rendered string `json:"rendered,omitempty"`
}

// reportPatterns ranks artifact candidates: skill-conventional output names
// first, then generic report shapes.
var reportPatterns = []string{
	"-security-audit.json",
	"-container-audit.json",
	"-cloud-config-audit.json",
	"-vuln-findings.json",
	"-validation.json",
	"-verification.json",
	"-remediation.json",
	"TRIAGE.json",
	"-triage.json",
	"-findings-current.json",
}

func artifactKind(name string) string {
	switch {
	case strings.HasSuffix(name, "-threat-model.md"), name == "THREAT_MODEL.md":
		return "threat-model"
	case strings.HasSuffix(name, ".json"):
		if hasReportPattern(name) {
			return "report"
		}
		return "json"
	case strings.HasSuffix(name, ".md"):
		return "markdown"
	default:
		return ""
	}
}

func hasReportPattern(name string) bool {
	for _, p := range reportPatterns {
		if strings.Contains(name, strings.TrimPrefix(p, "-")) || strings.HasSuffix(name, p) || name == p {
			return true
		}
	}
	return false
}

func artifactRank(a Artifact) int {
	for i, p := range reportPatterns {
		if strings.HasSuffix(a.Path, p) || filepath.Base(a.Path) == p {
			return i
		}
	}
	switch a.Kind {
	case "report":
		return len(reportPatterns)
	case "threat-model":
		return len(reportPatterns) + 1
	case "json":
		return len(reportPatterns) + 2
	default:
		return len(reportPatterns) + 3
	}
}

// fileStamp captures the identity of a file for before/after diffing.
type fileStamp struct {
	size int64
	mod  int64
}

// artifactRoots returns every root whose .json/.md outputs are tracked for
// artifact discovery:
//
//  1. the harness root, depth one — the standalone convention
//     (<repo>-security-audit.json written to the agent's cwd);
//  2. the findings tree — campaign-layout skills resolve their output root
//     through locations.yaml's analysis_results and write
//     findings/<org>/<repo>/<repo>-security-audit.json several levels deep,
//     so that tree is walked recursively (bounded).
//
// A managed workspace points analysis_results inside itself (init sets it
// when empty), so both conventions resolve inside the workspace.
func artifactRoots(state *bootstrap.State) []scanRoot {
	roots := []scanRoot{{path: state.TraustRepo(), recursive: false}}
	for _, ar := range analysisResultsDirs(state) {
		if ar != "" {
			roots = append(roots, scanRoot{path: ar, recursive: true})
		}
	}
	return roots
}

// scanRoot is one artifact scan location.
type scanRoot struct {
	path      string
	recursive bool
}

// analysisResultsDirs resolves candidate findings-tree roots: the
// locations.yaml analysis_results value (absolute, or relative to the
// config home) when set, and the workspace default when it exists.
func analysisResultsDirs(state *bootstrap.State) []string {
	var out []string
	if v := locationsAnalysisResults(state.ConfigHome); v != "" {
		p := v
		if !filepath.IsAbs(p) {
			p = filepath.Join(state.ConfigHome, p)
		}
		out = append(out, p, filepath.Join(p, "findings"))
	}
	ws := state.Workspace
	for _, p := range []string{filepath.Join(ws, "analysis-results"), filepath.Join(state.TraustRepo(), "analysis-results")} {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// locationsAnalysisResults reads the analysis_results value from a config
// home's locations.yaml ("" when unset or unreadable).
func locationsAnalysisResults(configHome string) string {
	data, err := os.ReadFile(filepath.Join(configHome, "locations.yaml"))
	if err != nil {
		return ""
	}
	var loc struct {
		AnalysisResults string `yaml:"analysis_results"`
	}
	if err := yaml.Unmarshal(data, &loc); err != nil {
		return ""
	}
	return strings.TrimSpace(loc.AnalysisResults)
}

// skipDirs are never walked in recursive artifact scans.
var skipDirs = map[string]bool{
	".git": true, ".venv": true, ".crush": true, ".claude": true,
	"node_modules": true, "_manifest": true, "graph": true,
	"metrics": true, "progress-tracker": true,
}

// snapshotArtifacts records the .json/.md files under every scan root
// (recursive roots are walked to a bounded depth). Snapshot keys are
// absolute paths.
func snapshotArtifacts(state *bootstrap.State) map[string]fileStamp {
	out := map[string]fileStamp{}
	for _, root := range artifactRoots(state) {
		if root.recursive {
			walkReports(root.path, 0, 6, out)
			continue
		}
		entries, err := os.ReadDir(root.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if ext := strings.ToLower(filepath.Ext(e.Name())); ext != ".json" && ext != ".md" {
				continue
			}
			if info, err := e.Info(); err == nil {
				out[filepath.Join(root.path, e.Name())] = fileStamp{size: info.Size(), mod: info.ModTime().UnixNano()}
			}
		}
	}
	return out
}

// walkReports collects .json/.md files under dir up to maxDepth.
func walkReports(dir string, depth, maxDepth int, out map[string]fileStamp) {
	if depth > maxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		if e.IsDir() {
			if !strings.HasPrefix(name, ".") && !skipDirs[name] {
				walkReports(full, depth+1, maxDepth, out)
			}
			continue
		}
		if ext := strings.ToLower(filepath.Ext(name)); ext != ".json" && ext != ".md" {
			continue
		}
		if info, err := e.Info(); err == nil {
			out[full] = fileStamp{size: info.Size(), mod: info.ModTime().UnixNano()}
		}
	}
}

// discoverArtifacts diffs two snapshots and returns new/modified candidates
// ranked by convention. Snapshot keys are absolute paths.
func discoverArtifacts(before, after map[string]fileStamp) []Artifact {
	var out []Artifact
	for path, stamp := range after {
		prev, existed := before[path]
		if existed && prev == stamp {
			continue
		}
		kind := artifactKind(filepath.Base(path))
		if kind == "" {
			continue
		}
		out = append(out, Artifact{Path: path, Kind: kind})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := artifactRank(out[i]), artifactRank(out[j])
		if ri != rj {
			return ri < rj
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// validateArtifacts routes each artifact through the right engine gate:
// JSON reports via `reporting validate`, threat models via `reporting lint`.
// Validation is best-effort per artifact; failures are recorded, not fatal
// (the run report itself is the source of truth for the failure).
func validateArtifacts(ctx context.Context, client api.TraustAPI, artifacts []Artifact, strict bool) {
	for i := range artifacts {
		a := &artifacts[i]
		switch a.Kind {
		case "report", "json":
			res, err := client.ReportingValidate(ctx, a.Path, strict)
			if err != nil {
				a.Detail = fmt.Sprintf("validation error: %v", err)
				continue
			}
			a.Valid = res.ExitCode == 0
			a.Detail = firstLines(res.Output(), 3)
		case "threat-model":
			res, err := client.ReportingLint(ctx, []string{a.Path}, strict)
			if err != nil {
				a.Detail = fmt.Sprintf("lint error: %v", err)
				continue
			}
			a.Valid = res.ExitCode == 0
			a.Detail = firstLines(res.Output(), 3)
		default:
			// Plain markdown companions are not gated.
			a.Valid = true
		}
	}
}

// renderArtifacts regenerates the Markdown companion for every valid JSON
// report. Render refusals (e.g. a re-render that would drop filled sections)
// are recorded on the artifact, not fatal.
func renderArtifacts(ctx context.Context, client api.TraustAPI, artifacts []Artifact) {
	for i := range artifacts {
		a := &artifacts[i]
		if a.Kind != "report" || !a.Valid {
			continue
		}
		out := strings.TrimSuffix(a.Path, ".json") + ".md"
		res, err := client.ReportingRender(ctx, a.Path, out)
		if err != nil {
			a.Detail = fmt.Sprintf("%s; render error: %v", a.Detail, err)
			continue
		}
		if res.ExitCode == 0 {
			a.Rendered = out
		} else {
			a.Detail = fmt.Sprintf("%s; render refused: %s", a.Detail, firstLines(res.Output(), 2))
		}
	}
}

func firstLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + " …"
}
