package runner

import (
	"strings"
	"testing"

	"github.com/traust-security/traust-cli/internal/catalog"
)

func TestCompose(t *testing.T) {
	skill := &catalog.Skill{
		Name:  "secure-code-audit",
		Stage: "3-audit",
		Body:  "Audit the target at $ARGUMENTS. Then validate the report.",
	}
	prompt := Compose(skill, []string{"/repos/target"}, "/ws/traust", "/ws/config")

	for _, want := range []string{
		`You are executing the traust harness skill "secure-code-audit" (pipeline stage 3-audit)`,
		"Your working directory is the traust harness root: /ws/traust",
		"TRAUST_CONFIG_HOME=/ws/config",
		"never configuration",
		"Audit the target at /repos/target. Then validate the report.",
		"--- END SKILL ---",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "$ARGUMENTS") {
		t.Error("$ARGUMENTS not substituted")
	}
}
