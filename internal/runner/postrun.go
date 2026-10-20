package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/traust-security/traust-cli/internal/api"
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

// snapshotArtifacts records the .json/.md files at depth one of root (where
// standalone skills write their outputs), skipping dot-directories.
func snapshotArtifacts(root string) map[string]fileStamp {
	out := map[string]fileStamp{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".json" && ext != ".md" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out[e.Name()] = fileStamp{size: info.Size(), mod: info.ModTime().UnixNano()}
	}
	return out
}

// discoverArtifacts diffs two snapshots and returns new/modified candidates
// ranked by convention.
func discoverArtifacts(root string, before, after map[string]fileStamp) []Artifact {
	var out []Artifact
	for name, stamp := range after {
		prev, existed := before[name]
		if existed && prev == stamp {
			continue
		}
		kind := artifactKind(name)
		if kind == "" {
			continue
		}
		out = append(out, Artifact{Path: filepath.Join(root, name), Kind: kind})
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
