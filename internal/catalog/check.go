package catalog

import (
	"embed"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

//go:embed data/standalone.yaml
var standaloneFS embed.FS

type standaloneFile struct {
	Skills map[string]StandaloneInfo `yaml:"skills"`
}

// embeddedStandalone parses the shipped standalone profile once.
func embeddedStandalone() map[string]StandaloneInfo {
	loadOnce.Do(func() {
		data, err := standaloneFS.ReadFile("data/standalone.yaml")
		if err != nil {
			loadErr = err
			return
		}
		var f standaloneFile
		if err := yaml.Unmarshal(data, &f); err != nil {
			loadErr = fmt.Errorf("parse embedded standalone.yaml: %w", err)
			return
		}
		embedded = f.Skills
	})
	if loadErr != nil {
		return map[string]StandaloneInfo{}
	}
	if embedded == nil {
		embedded = map[string]StandaloneInfo{}
	}
	return embedded
}

// StandaloneNames returns the sorted skill names in the shipped map.
func StandaloneNames() []string {
	m := embeddedStandalone()
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// DriftReport compares the shipped standalone map against a loaded catalog.
type DriftReport struct {
	// Missing are map entries with no matching skill in the tree.
	Missing []string
	// Extra are standalone-map skills absent from the map (informational).
	UnknownToMap []string
}

// CheckDrift compares the embedded standalone profile with a parsed catalog.
func CheckDrift(c *Catalog) DriftReport {
	var rep DriftReport
	for _, name := range StandaloneNames() {
		if _, ok := c.ByName(name); !ok {
			rep.Missing = append(rep.Missing, name)
		}
	}
	return rep
}
