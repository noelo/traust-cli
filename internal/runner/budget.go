package runner

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Budget band thresholds, matching the harness budget-policy defaults when
// the policy file does not declare them.
const (
	defaultWarnBand    = 1.5
	defaultDegradeBand = 2.0
	defaultRefuseBand  = 3.0
	defaultRunCap      = 1_000_000
)

// Band is the budget state of a run relative to its cap.
type Band int

const (
	BandWithin Band = iota
	BandWarn
	BandDegrade
	BandRefuse
)

func (b Band) String() string {
	switch b {
	case BandWarn:
		return "warn"
	case BandDegrade:
		return "degrade"
	case BandRefuse:
		return "refuse"
	default:
		return "within"
	}
}

// Policy is the parsed budget-policy.yaml. The harness ships the policy as an
// observation artifact ("STATUS: POLICY ARTIFACT ONLY"); the CLI is its first
// runtime consumer, applying the per-run output-token caps and bands.
type Policy struct {
	Enforcement string
	PerRun      map[string]int64
	DefaultCap  int64
	Warn        float64
	Degrade     float64
	Refuse      float64
}

type rawPolicy struct {
	Enforcement    string           `yaml:"enforcement"`
	Units          string           `yaml:"units"`
	PerRunDefaults map[string]int64 `yaml:"per_run_defaults"`
	PerRunDefault  *int64           `yaml:"per_run_default"`
	// Bands accepts both shipped shapes: the map form ({warn: 1.5, ...})
	// and the sequence form the harness actually ships
	// (- {at: 1.5, action: warn}).
	Bands any `yaml:"bands"`
}

type policyFile struct {
	BudgetPolicy *rawPolicy `yaml:"budget_policy"`
}

// LoadPolicy reads budget-policy.yaml from a config home. A missing file is
// not an error: it returns (nil, nil) and the run is unbudgeted.
func LoadPolicy(configHome string) (*Policy, error) {
	path := filepath.Join(configHome, "budget-policy.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read budget policy: %w", err)
	}
	var f policyFile
	// yaml.v3 ignores unknown fields, so a wrapper-less policy parses without
	// error but leaves BudgetPolicy nil — fall back to the bare shape.
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse budget policy %s: %w", path, err)
	}
	if f.BudgetPolicy == nil {
		var direct rawPolicy
		if err := yaml.Unmarshal(data, &direct); err != nil {
			return nil, fmt.Errorf("parse budget policy %s: %w", path, err)
		}
		if direct.PerRunDefaults == nil && direct.PerRunDefault == nil && direct.Enforcement == "" && direct.Bands == nil {
			return nil, nil
		}
		f.BudgetPolicy = &direct
	}
	rp := f.BudgetPolicy

	p := &Policy{
		Enforcement: rp.Enforcement,
		PerRun:      map[string]int64{},
		Warn:        defaultWarnBand,
		Degrade:     defaultDegradeBand,
		Refuse:      defaultRefuseBand,
		DefaultCap:  defaultRunCap,
	}
	for k, v := range rp.PerRunDefaults {
		p.PerRun[k] = v
	}
	if rp.PerRunDefault != nil {
		p.DefaultCap = *rp.PerRunDefault
	}
	if v, ok := p.PerRun["default"]; ok {
		p.DefaultCap = v
		delete(p.PerRun, "default")
	}
	if rp.Bands != nil {
		applyBands(p, rp.Bands)
	}
	return p, nil
}

// applyBands folds the two shipped band shapes into the policy.
func applyBands(p *Policy, bands any) {
	set := func(action string, at float64) {
		switch action {
		case "warn":
			p.Warn = at
		case "degrade":
			p.Degrade = at
		case "refuse":
			p.Refuse = at
		}
	}
	switch b := bands.(type) {
	case []any: // sequence form: - {at: 1.5, action: warn}
		for _, entry := range b {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			set(toString(m["action"]), toFloat(m["at"]))
		}
	case map[string]any: // map form: {warn: 1.5, degrade: 2.0, refuse: 3.0}
		for action, v := range b {
			set(action, toFloat(v))
		}
	}
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}

// CapFor returns the per-run output-token cap for a skill.
func (p *Policy) CapFor(skill string) int64 {
	if v, ok := p.PerRun[skill]; ok {
		return v
	}
	return p.DefaultCap
}

// BandFor classifies cumulative output tokens against the skill's cap.
func (p *Policy) BandFor(skill string, outputTokens int64) Band {
	cap := p.CapFor(skill)
	if cap <= 0 || outputTokens <= 0 {
		return BandWithin
	}
	ratio := float64(outputTokens) / float64(cap)
	switch {
	case ratio >= p.Refuse:
		return BandRefuse
	case ratio >= p.Degrade:
		return BandDegrade
	case ratio >= p.Warn:
		return BandWarn
	default:
		return BandWithin
	}
}
