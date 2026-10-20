package harness

import (
	"context"
	"os/exec"
	"strings"
)

// Crush is the Charm Crush adapter (`crush run`). Crush's run mode emits the
// final response as plain text — no structured stream and no token usage —
// so events are limited to the raw output and the process exit code. The
// headless permission posture comes from the user's crushrc `permissions`
// entries; pre-allow the tools the skills need before running headlessly.
type Crush struct{}

func (c *Crush) ID() string          { return "crush" }
func (c *Crush) DisplayName() string { return "Crush" }
func (c *Crush) Binary() string      { return binaryEnv("crush", "crush") }

func (c *Crush) Capabilities() Caps {
	return Caps{StreamJSON: false, ModelSelect: true, PermissionScoping: false, UsageReporting: false}
}

func (c *Crush) Detect(ctx context.Context) Detection { return detect(ctx, c.Binary()) }

func (c *Crush) Command(ctx context.Context, spec *RunSpec) *exec.Cmd {
	args := []string{"run", "--quiet"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	cmd := exec.CommandContext(ctx, c.Binary(), args...)
	cmd.Dir = spec.Cwd
	cmd.Env = spec.Env
	cmd.Stdin = strings.NewReader(spec.Prompt)
	return cmd
}

func (c *Crush) Notes() []string {
	return []string{
		"crush run emits plain text, not a structured stream: tool calls are not distinguished live",
		"token usage is not reported by crush run: the spend declaration is skipped",
		"headless permission posture comes from your crushrc `permissions allow` entries — pre-allow the tools the skills need",
	}
}

// ParseEvent always returns false: crush run output is unstructured.
func (c *Crush) ParseEvent(line []byte) (Event, bool) { return Event{}, false }
