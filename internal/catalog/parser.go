package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatter is the subset of the Agent Skills SKILL.md frontmatter the CLI
// consumes.
type frontmatter struct {
	Name          string   `yaml:"name"`
	Description   string   `yaml:"description"`
	ArgumentHint  string   `yaml:"argument-hint"`
	UserInvocable bool     `yaml:"user-invocable"`
	AllowedTools  []string `yaml:"allowed-tools"`
	Metadata      struct {
		Harness struct {
			Tier string `yaml:"tier"`
		} `yaml:"harness"`
	} `yaml:"metadata"`
}

var stageDir = regexp.MustCompile(`^(\d+)-[a-z-]+$`)

// parseSkillFile parses one SKILL.md into a Skill (stage filled by caller).
func parseSkillFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, body, err := splitFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s := &Skill{
		Name:         fm.Name,
		Description:  fm.Description,
		Tier:         fm.Metadata.Harness.Tier,
		ArgumentHint: fm.ArgumentHint,
		AllowedTools: fm.AllowedTools,
		Path:         path,
		Body:         body,
	}
	if s.Name == "" {
		s.Name = filepath.Base(filepath.Dir(path))
	}
	return s, nil
}

// splitFrontmatter separates the YAML frontmatter block from the body.
func splitFrontmatter(content string) (frontmatter, string, error) {
	var fm frontmatter
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return fm, normalized, nil
	}
	rest := normalized[4:]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return fm, "", fmt.Errorf("unterminated frontmatter")
	}
	block := rest[:idx]
	body := rest[idx+5:]
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return fm, "", fmt.Errorf("parse frontmatter: %w", err)
	}
	return fm, strings.TrimPrefix(body, "\n"), nil
}

// parseTree walks the two-level harnessing tree. Workflow skills live at
// harnessing/<N>-<stage>/<name>/SKILL.md; everything else sits at
// harnessing/<name>/SKILL.md. This mirrors the harness's own skill_dirs()
// walk — a one-level glob silently misses the staged skills.
func parseTree(root string) ([]*Skill, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read harnessing tree at %s: %w", root, err)
	}
	var skills []*Skill
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if skill, ok := skillAt(dir); ok {
			skill.Stage = ""
			skills = append(skills, skill)
			continue
		}
		stageEntries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, se := range stageEntries {
			if !se.IsDir() || strings.HasPrefix(se.Name(), ".") {
				continue
			}
			sub := filepath.Join(dir, se.Name())
			if skill, ok := skillAt(sub); ok {
				skill.Stage = e.Name()
				skill.StageNum = stageNum(e.Name())
				skills = append(skills, skill)
			}
		}
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

// skillAt returns the skill defined at dir, if any.
func skillAt(dir string) (*Skill, bool) {
	path := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		return nil, false
	}
	skill, err := parseSkillFile(path)
	if err != nil {
		return nil, false
	}
	return skill, true
}

func stageNum(name string) int {
	m := stageDir.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
