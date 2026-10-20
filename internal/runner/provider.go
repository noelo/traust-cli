package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/traust-security/traust-sdk/go/v1/skills"

	"github.com/traust-security/traust-cli/internal/api"
	"github.com/traust-security/traust-cli/internal/bootstrap"
	"github.com/traust-security/traust-cli/internal/catalog"
	"github.com/traust-security/traust-cli/internal/harness"
)

// ProviderOptions configures the SDK Provider.
type ProviderOptions struct {
	State   *bootstrap.State
	API     api.TraustAPI
	Harness harness.Harness
	// Model overrides the registry floor resolution (usually left empty).
	Model string
	// Out receives progress lines (default: discarded).
	Progress func(msg string)
}

// Provider implements the traust-sdk skills.Provider interface: typed input
// JSON in, raw artifact bytes out. The SDK owns input/output schema
// validation; this type owns execution transport — it maps the typed input
// onto the skill's argument grammar, runs the headless pipeline, and returns
// the primary artifact's bytes.
type Provider struct {
	opts ProviderOptions
}

// NewProvider constructs an SDK-compatible provider over the run pipeline.
func NewProvider(opts ProviderOptions) *Provider {
	if opts.Progress == nil {
		opts.Progress = func(string) {}
	}
	return &Provider{opts: opts}
}

// Execute implements skills.Provider.
func (p *Provider) Execute(ctx context.Context, meta skills.SkillMeta, input []byte) ([]byte, error) {
	cat, err := catalog.Load(p.opts.State.TraustRepo())
	if err != nil {
		return nil, fmt.Errorf("load catalog: %w", err)
	}
	skill, ok := cat.ByName(meta.Name)
	if !ok {
		return nil, fmt.Errorf("skill %q not found in the harness tree", meta.Name)
	}
	args, err := InputToArgs(meta.Name, input)
	if err != nil {
		return nil, err
	}

	progress := p.opts.Progress
	sink := &progressSink{fn: progress}
	res, err := Run(ctx, Options{
		State:        p.opts.State,
		API:          p.opts.API,
		Harness:      p.opts.Harness,
		Skill:        skill,
		Args:         args,
		Model:        p.opts.Model,
		DeclareSpend: true,
	}, sink)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != ExitOK {
		return nil, fmt.Errorf("skill %s run failed (exit %d); journal: %s", meta.Name, res.ExitCode, res.Dir)
	}

	primary := PrimaryArtifact(meta.Name, res.Artifacts)
	if primary == nil {
		if len(res.Artifacts) == 0 {
			return nil, fmt.Errorf("skill %s produced no artifacts; journal: %s", meta.Name, res.Dir)
		}
		return nil, fmt.Errorf("skill %s produced %d artifacts but none matches its primary pattern; journal: %s", meta.Name, len(res.Artifacts), res.Dir)
	}
	data, err := os.ReadFile(primary.Path)
	if err != nil {
		return nil, fmt.Errorf("read artifact %s: %w", primary.Path, err)
	}
	return data, nil
}

// primaryPatterns maps the typed skills to their conventional output
// filenames (suffix matches).
var primaryPatterns = map[string][]string{
	skills.SkillSecureCodeAudit:   {"-security-audit.json"},
	skills.SkillTriage:            {"TRIAGE.json", "-triage.json"},
	skills.SkillVulnScan:          {"-vuln-findings.json"},
	skills.SkillValidateFindings:  {"-validation.json"},
	skills.SkillVerifyRemediation: {"-verification.json"},
	skills.SkillRemediateFinding:  {"-remediation.json"},
}

// PrimaryArtifact picks the skill's conventional output from discovered
// artifacts, or nil.
func PrimaryArtifact(skillName string, artifacts []Artifact) *Artifact {
	patterns := primaryPatterns[skillName]
	for _, p := range patterns {
		for i := range artifacts {
			base := artifacts[i].Path
			if len(base) >= len(p) && base[len(base)-len(p):] == p {
				return &artifacts[i]
			}
		}
	}
	return nil
}

// InputToArgs maps a typed SDK input document onto the skill's argument
// grammar. The mappings are approximations of the slash-command grammars
// documented in the harness skills reference; they cover the positional
// target plus the flags the grammars expose.
func InputToArgs(skillName string, input []byte) ([]string, error) {
	switch skillName {
	case skills.SkillSecureCodeAudit:
		var in skills.ScanInput
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("decode ScanInput: %w", err)
		}
		return nonEmpty(in.Repo), nil
	case skills.SkillTriage:
		var in skills.TriageInput
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("decode TriageInput: %w", err)
		}
		return nonEmpty(firstNonEmpty(in.FindingsPath, in.Repo)), nil
	case skills.SkillVulnScan:
		var in skills.VulnScanInput
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("decode VulnScanInput: %w", err)
		}
		return nonEmpty(in.Repo), nil
	case skills.SkillValidateFindings:
		var in skills.ValidateInput
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("decode ValidateInput: %w", err)
		}
		return nonEmpty(firstNonEmpty(in.FindingsSource, in.Repo)), nil
	case skills.SkillVerifyRemediation:
		var in skills.VerifyInput
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("decode VerifyInput: %w", err)
		}
		return nonEmpty(in.Repo, in.AuditReport, in.PatchedRef, in.FixRepo), nil
	case skills.SkillRemediateFinding:
		var in skills.RemediateInput
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("decode RemediateInput: %w", err)
		}
		return nonEmpty(in.Repo, in.RemID, in.FindingID), nil
	default:
		return nil, fmt.Errorf("no typed input mapping for skill %q — use `traust-cli run %s <args>` directly", skillName, skillName)
	}
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// progressSink adapts a progress function into a runner Sink.
type progressSink struct {
	fn func(msg string)
}

func (s *progressSink) Progress(msg string)    { s.fn(msg) }
func (s *progressSink) Event(ev harness.Event) {}
func (s *progressSink) Raw(line string)        {}
func (s *progressSink) Done()                  {}
