package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestRunSkillFlagPassthrough is the regression test for `run triage
// <report> --auto` failing with "unknown flag": skill flags must pass
// through verbatim, with our own flags still parsed before the skill name.
func TestRunSkillFlagPassthrough(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    []string
		wantHid string
	}{
		{
			name: "skill flags after the skill name pass through",
			args: []string{"triage", "/tmp/report.json", "--auto"},
			want: []string{"triage", "/tmp/report.json", "--auto"},
		},
		{
			name: "multiple skill flags with values pass through",
			args: []string{"vuln-scan", "/src/app", "--diff", "--since", "abc123"},
			want: []string{"vuln-scan", "/src/app", "--diff", "--since", "abc123"},
		},
		{
			name:    "our flags before the skill name still parse",
			args:    []string{"--harness", "crush", "--model", "m1", "triage", "/r.json", "--auto"},
			want:    []string{"triage", "/r.json", "--auto"},
			wantHid: "crush",
		},
		{
			name:    "flags after the skill name belong to the skill, not us",
			args:    []string{"--harness", "crush", "triage", "/r.json", "--model", "skill-model"},
			want:    []string{"triage", "/r.json", "--model", "skill-model"},
			wantHid: "crush",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotArgs []string
			var hid string
			c := &cobra.Command{Use: "run", Run: func(cmd *cobra.Command, args []string) { gotArgs = args }}
			c.Flags().StringVar(&hid, "harness", "", "")
			c.Flags().String("model", "", "")
			c.Flags().SetInterspersed(false)
			c.SetArgs(tc.args)
			if err := c.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if strings.Join(gotArgs, " ") != strings.Join(tc.want, " ") {
				t.Errorf("args = %v, want %v", gotArgs, tc.want)
			}
			if tc.wantHid != "" && hid != tc.wantHid {
				t.Errorf("--harness = %q, want %q", hid, tc.wantHid)
			}
		})
	}
}

// TestRunSkillFlagPassthroughRejectsOurFlagsBeforeSkill guards the
// namespace: an unknown flag BEFORE the skill name is still a CLI error.
func TestRunSkillFlagPassthroughRejectsOurFlagsBeforeSkill(t *testing.T) {
	c := &cobra.Command{Use: "run", Run: func(cmd *cobra.Command, args []string) {}}
	c.Flags().Bool("json", false, "")
	c.Flags().SetInterspersed(false)
	c.SetArgs([]string{"--auto", "triage", "/r.json"})
	if err := c.Execute(); err == nil {
		t.Error("unknown flag before the skill name should error")
	}
}
