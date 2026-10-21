// Package catalog reads the traust harness's skill tree: every SKILL.md under
// harnessing/ (the single source of truth), plus a shipped map of standalone
// usability and model-floor roles seeded from the harness docs. The frontmatter
// is parsed from the same source the harness itself reads, so names,
// descriptions and tiers cannot drift.
package catalog

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
)

// Skill is one parsed harness skill.
type Skill struct {
	// Name is the skill identifier (frontmatter name == directory name).
	Name string `json:"name"`
	// Description is the frontmatter description, verbatim.
	Description string `json:"description"`
	// Stage is the pipeline stage directory name (e.g. "3-audit"), or "" for
	// root-level skills.
	Stage string `json:"stage,omitempty"`
	// StageNum is the numeric stage order (0 when not staged).
	StageNum int `json:"stage_num"`
	// Tier is metadata.harness.tier: primary, secondary, tertiary, ci.
	Tier string `json:"tier,omitempty"`
	// ArgumentHint is the frontmatter argument-hint.
	ArgumentHint string `json:"argument_hint,omitempty"`
	// AllowedTools is the frontmatter allowed-tools list (Claude Code grant
	// syntax).
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// Path is the SKILL.md file path.
	Path string `json:"path"`
	// Body is the SKILL.md markdown body (frontmatter stripped).
	Body string `json:"-"`

	// Standalone carries the shipped standalone profile; nil when the skill is
	// not in the standalone map (campaign-only).
	Standalone *StandaloneInfo `json:"standalone,omitempty"`
}

// StandaloneInfo is the CLI's shipped per-skill profile, seeded from
// traust/docs/standalone-usage.md and docs/model-classes.md.
type StandaloneInfo struct {
	// Role is the model-registry role whose floor applies (empty = none).
	Role string `json:"role,omitempty" yaml:"role"`
	// MinModelClass is the documented minimum viable model class.
	MinModelClass string `json:"min_model_class,omitempty" yaml:"min_model_class"`
	// Toolchain is the install-toolchain profile the skill's scanners
	// live in (empty = scanner-free); the runner pre-flights it.
	Toolchain string `json:"toolchain,omitempty" yaml:"toolchain"`
	// Env lists extra environment/infrastructure requirements.
	Env []string `json:"env,omitempty" yaml:"env"`
}

// Catalog is the loaded skill set.
type Catalog struct {
	skills []*Skill
	byName map[string]*Skill
}

var (
	loadOnce sync.Once
	embedded map[string]StandaloneInfo
	loadErr  error
)

// Load walks the harnessing/ tree of a traust checkout.
func Load(traustRoot string) (*Catalog, error) {
	skills, err := parseTree(filepath.Join(traustRoot, "harnessing"))
	if err != nil {
		return nil, err
	}
	standalone := embeddedStandalone()
	c := &Catalog{byName: map[string]*Skill{}}
	for _, s := range skills {
		if info, ok := standalone[s.Name]; ok {
			s.Standalone = &info
		}
		c.skills = append(c.skills, s)
		c.byName[s.Name] = s
	}
	sort.Slice(c.skills, func(i, j int) bool {
		a, b := c.skills[i], c.skills[j]
		if a.StageNum != b.StageNum {
			return a.StageNum < b.StageNum
		}
		return a.Name < b.Name
	})
	return c, nil
}

// Skills returns all skills in stage order.
func (c *Catalog) Skills() []*Skill { return c.skills }

// StandaloneSkills returns only the skills in the shipped standalone map.
func (c *Catalog) StandaloneSkills() []*Skill {
	var out []*Skill
	for _, s := range c.skills {
		if s.Standalone != nil {
			out = append(out, s)
		}
	}
	return out
}

// ByName looks a skill up by its flat agent-facing name.
func (c *Catalog) ByName(name string) (*Skill, bool) {
	s, ok := c.byName[name]
	return s, ok
}

// Must panics with a clear message when a known skill name is missing — used
// by callers that validated input earlier.
func (c *Catalog) Must(name string) *Skill {
	s, ok := c.byName[name]
	if !ok {
		panic(fmt.Sprintf("catalog: unknown skill %q", name))
	}
	return s
}
