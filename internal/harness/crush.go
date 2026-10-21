package harness

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
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
	if spec.UnsafeFullPermission {
		args = append(args, "--yolo")
	}
	cmd := exec.CommandContext(ctx, c.Binary(), args...)
	cmd.Dir = spec.Cwd
	cmd.Env = spec.Env
	cmd.Stdin = strings.NewReader(spec.Prompt)
	return cmd
}

func (c *Crush) Notes() []string {
	return []string{
		"running through crush server mode: thinking, tool calls and token usage stream live into the monitor",
		"without full-permission mode (consent at `traust-cli init`), permission and question requests fail closed (exit 2) — with it, the workspace runs with permissions skipped (--yolo equivalent)",
		"if the server cannot start, the run falls back to `crush run` (plain text, no live events, no usage)",
	}
}

// ParseEvent always returns false: crush run output is unstructured.
func (c *Crush) ParseEvent(line []byte) (Event, bool) { return Event{}, false }

// CheckModel verifies the model appears in `crush models` before launch.
// crush accepts "model" or "provider/model", and silently routes unknown
// ids to its default provider — which turns an unrunnable registry floor
// model into an authentication failure two seconds into a run. Failing
// here, with the list in hand, is the cheap time to find out.
func (c *Crush) CheckModel(ctx context.Context, model string) error {
	if model == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, c.Binary(), "models").Output()
	if err != nil {
		// The listing is advisory: never block a run on it.
		return nil
	}
	return matchCrushModel(string(out), model)
}

// matchCrushModel reports whether model is runnable per a `crush models`
// listing (exact or provider-prefixed match).
func matchCrushModel(listing, model string) error {
	for _, line := range strings.Split(listing, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == model || strings.HasSuffix(line, "/"+model) {
			return nil
		}
	}
	return fmt.Errorf("model %q is not in `crush models`", model)
}
