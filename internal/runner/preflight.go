package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/traust-security/traust-cli/internal/api"
	"github.com/traust-security/traust-cli/internal/catalog"
)

// Preflight is the outcome of model-floor resolution before a run.
type Preflight struct {
	// Role is the registry role that applies ("" when none).
	Role string `json:"role,omitempty"`
	// ResolvedModel is the registry floor model for the role ("" when the
	// role could not be resolved).
	ResolvedModel string `json:"resolved_model,omitempty"`
	// Model is the model the run will actually use.
	Model string `json:"model"`
	// Warning is a human-readable caveat; empty when everything is clean.
	Warning string `json:"warning,omitempty"`
}

// ResolveModel closes the documented runtime-enforcement gap
// (traust/docs/model-routing.md: "nothing enforces the floor at execution
// time") for the default path: the run's model defaults to the registry's
// floor resolution for the skill's role. An explicit --model override is
// allowed but warned about and recorded, because tier membership of arbitrary
// model ids cannot be verified mechanically.
func ResolveModel(ctx context.Context, client api.TraustAPI, skill *catalog.Skill, explicitModel string) Preflight {
	pf := Preflight{Model: explicitModel}

	role := ""
	if skill.Standalone != nil {
		role = skill.Standalone.Role
	}
	if role == "" {
		if explicitModel == "" {
			pf.Warning = fmt.Sprintf("no registry role mapped for %q — using the harness's default model; documented minimum: %s", skill.Name, minClass(skill))
		}
		return pf
	}
	pf.Role = role

	resolved, err := client.RegistryModelsResolve(ctx, role)
	if err != nil {
		pf.Warning = fmt.Sprintf("could not resolve registry role %q (%v) — using the harness's default model; documented minimum: %s", role, trimErr(err), minClass(skill))
		return pf
	}
	pf.ResolvedModel = resolved

	switch {
	case explicitModel == "":
		pf.Model = resolved
	case strings.EqualFold(explicitModel, resolved):
		// Explicitly requesting the floor model: fine.
	default:
		pf.Warning = fmt.Sprintf("explicit model %q overrides the registry floor resolution for role %q (%q) — model floors are not enforced at runtime; this override is recorded in the run journal", explicitModel, role, resolved)
	}
	return pf
}

func minClass(skill *catalog.Skill) string {
	if skill.Standalone != nil && skill.Standalone.MinModelClass != "" {
		return skill.Standalone.MinModelClass + "-class"
	}
	return "unspecified"
}

func trimErr(err error) string {
	s := err.Error()
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
