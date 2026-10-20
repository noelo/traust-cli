package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
)

// Goose is the Block/AAIF goose adapter. EXPERIMENTAL: goose's headless CLI
// surface was not verifiable at authoring time, so the argument template is
// overridable with TRAUST_CLI_GOOSE_ARGS (a space-separated template where
// {prompt} is replaced by the composed prompt). The default template targets
// `goose run --name assistant --text <prompt>`.
type Goose struct{}

func (g *Goose) ID() string          { return "goose" }
func (g *Goose) DisplayName() string { return "goose (experimental)" }
func (g *Goose) Binary() string      { return binaryEnv("goose", "goose") }

func (g *Goose) Capabilities() Caps {
	return Caps{StreamJSON: false, ModelSelect: false, PermissionScoping: false, UsageReporting: false}
}

func (g *Goose) Detect(ctx context.Context) Detection { return detect(ctx, g.Binary()) }

func (g *Goose) Command(ctx context.Context, spec *RunSpec) *exec.Cmd {
	template := os.Getenv("TRAUST_CLI_GOOSE_ARGS")
	if template == "" {
		template = "run --name assistant --text {prompt}"
	}
	var args []string
	for _, field := range strings.Fields(template) {
		if field == "{prompt}" {
			args = append(args, spec.Prompt)
			continue
		}
		args = append(args, field)
	}
	cmd := exec.CommandContext(ctx, g.Binary(), args...)
	cmd.Dir = spec.Cwd
	cmd.Env = spec.Env
	return cmd
}

func (g *Goose) Notes() []string {
	return []string{
		"EXPERIMENTAL: goose headless flags are unverified — override with TRAUST_CLI_GOOSE_ARGS if the default template is wrong for your goose version",
		"model selection is configured in goose itself (goose configure), not per invocation",
	}
}

// ParseEvent handles goose's JSON output shapes when present; anything else
// is treated as unstructured text.
func (g *Goose) ParseEvent(line []byte) (Event, bool) {
	var m struct {
		Type    string          `json:"type"`
		Event   string          `json:"event"`
		Message json.RawMessage `json:"message"`
		Text    string          `json:"text"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return Event{}, false
	}
	kind := m.Type
	if kind == "" {
		kind = m.Event
	}
	switch kind {
	case "message", "text", "agent_message", "response":
		if m.Text != "" {
			return Event{Kind: EventText, Text: m.Text}, true
		}
	case "error":
		return Event{Kind: EventError, Text: m.Text, IsError: true}, true
	}
	return Event{}, false
}
