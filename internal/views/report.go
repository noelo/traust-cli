// Package views renders read-only data: report browsing and ledger queries.
package views

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Finding is a normalized report finding, tolerant of the report schema
// variants (security-audit, container-audit, cloud-config-audit, …).
type Finding struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Severity    string         `json:"severity"`
	Description string         `json:"description"`
	Raw         map[string]any `json:"-"`
}

// Report is a loaded JSON report.
type Report struct {
	Path     string         `json:"path"`
	Findings []Finding      `json:"findings"`
	Summary  map[string]any `json:"summary,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// LoadReport parses a JSON report and extracts its findings array.
func LoadReport(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read report: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s as JSON: %w", path, err)
	}
	rep := &Report{Path: path, Metadata: asMap(doc["metadata"])}
	if summary, ok := doc["executive_summary"].(map[string]any); ok {
		rep.Summary = summary
	} else if summary, ok := doc["summary"].(map[string]any); ok {
		rep.Summary = summary
	}
	rawFindings, _ := doc["findings"].([]any)
	for _, rf := range rawFindings {
		m, ok := rf.(map[string]any)
		if !ok {
			continue
		}
		rep.Findings = append(rep.Findings, Finding{
			ID:          str(m, "id", "finding_id", "finding_ref"),
			Title:       str(m, "title", "name", "summary"),
			Severity:    strings.ToLower(str(m, "severity", "priority", "impact")),
			Description: str(m, "description", "detail", "evidence"),
			Raw:         m,
		})
	}
	return rep, nil
}

// MarkdownPath returns the report's Markdown companion path, when it exists.
func (r *Report) MarkdownPath() string {
	p := strings.TrimSuffix(r.Path, ".json") + ".md"
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// CountsBySeverity returns finding counts keyed by lowercased severity.
func (r *Report) CountsBySeverity() map[string]int {
	out := map[string]int{}
	for _, f := range r.Findings {
		sev := f.Severity
		if sev == "" {
			sev = "unrated"
		}
		out[sev]++
	}
	return out
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// DiscoverReports finds report-like JSON files in a directory (depth one),
// used by `traust-cli report <dir>`.
func DiscoverReports(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}
