package harness

import (
	"context"
	"encoding/json"
	"os/exec"
)

// Opencode is the opencode adapter (`opencode run`). The --format json raw
// event schema is not fully documented upstream; the parser handles the known
// shapes and every unrecognized line is journaled raw. --auto auto-approves
// permissions that are not explicitly denied — opencode's headless-workable
// default, equivalent to codex's workspace-write posture.
type Opencode struct{}

func (o *Opencode) ID() string          { return "opencode" }
func (o *Opencode) DisplayName() string { return "opencode" }
func (o *Opencode) Binary() string      { return binaryEnv("opencode", "opencode") }

func (o *Opencode) Capabilities() Caps {
	return Caps{StreamJSON: true, ModelSelect: true, PermissionScoping: false, UsageReporting: false}
}

func (o *Opencode) Detect(ctx context.Context) Detection { return detect(ctx, o.Binary()) }

func (o *Opencode) Command(ctx context.Context, spec *RunSpec) *exec.Cmd {
	args := []string{"run"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	args = append(args, "--format", "json", "--auto", "--dir", spec.Cwd)
	args = append(args, spec.Prompt)
	cmd := exec.CommandContext(ctx, o.Binary(), args...)
	cmd.Dir = spec.Cwd
	cmd.Env = spec.Env
	return cmd
}

func (o *Opencode) Notes() []string {
	return []string{
		"--auto auto-approves permissions not explicitly denied (opencode's headless default)",
		"the opencode JSON event schema is not fully documented; unrecognized events are journaled raw",
		"token usage is not reliably reported by opencode run; spend declaration may be skipped",
	}
}

// ParseEvent handles the known opencode run --format json shapes.
func (o *Opencode) ParseEvent(line []byte) (Event, bool) {
	var m struct {
		Type string `json:"type"`
		Part *struct {
			Type           string `json:"type"`
			Text           string `json:"text"`
			ToolInvocation *struct {
				ToolName string `json:"toolName"`
				State    string `json:"state"`
			} `json:"tool-invocation"`
		} `json:"part"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return Event{}, false
	}
	switch m.Type {
	case "part":
		if m.Part == nil {
			return Event{}, false
		}
		switch m.Part.Type {
		case "text":
			if m.Part.Text != "" {
				return Event{Kind: EventText, Text: m.Part.Text}, true
			}
		case "tool-invocation":
			if m.Part.ToolInvocation != nil {
				return Event{Kind: EventToolCall, Tool: m.Part.ToolInvocation.ToolName}, true
			}
		}
		return Event{}, false
	case "error":
		return Event{Kind: EventError, Text: m.Message + " " + m.Error, IsError: true}, true
	}
	return Event{}, false
}
