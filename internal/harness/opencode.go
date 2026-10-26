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
	}
}

// ParseEvent handles the known opencode run --format json shapes: the
// current wire format (opencode >= 1.19) puts the event type at the top
// level with the payload in "part" and usage on "step_finish"; the older
// format wrapped the same payloads in {"type":"part",...}.
func (o *Opencode) ParseEvent(line []byte) (Event, bool) {
	var m struct {
		Type string `json:"type"`
		Part *struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Tokens *struct {
				Input  int64 `json:"input"`
				Output int64 `json:"output"`
			} `json:"tokens"`
			ToolInvocation *struct {
				ToolName string `json:"toolName"`
				State    string `json:"state"`
			} `json:"tool-invocation"`
		} `json:"part"`
		Tokens *struct {
			Input  int64 `json:"input"`
			Output int64 `json:"output"`
		} `json:"tokens"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return Event{}, false
	}
	part := func() (string, string) {
		if m.Part == nil {
			return "", ""
		}
		return m.Part.Type, m.Part.Text
	}
	switch m.Type {
	case "text", "step_start", "step_finish", "tool_invocation":
		// Current format: top-level type, payload in part.
		switch m.Type {
		case "text":
			if _, text := part(); text != "" {
				return Event{Kind: EventText, Text: text}, true
			}
		case "tool_invocation":
			if m.Part != nil && m.Part.ToolInvocation != nil {
				return Event{Kind: EventToolCall, Tool: m.Part.ToolInvocation.ToolName}, true
			}
		case "step_finish":
			ev := Event{Kind: EventResult}
			// Usage rides inside part.tokens (and sometimes top-level).
			if m.Part != nil && m.Part.Tokens != nil {
				ev.TokensIn, ev.TokensOut = m.Part.Tokens.Input, m.Part.Tokens.Output
			} else if m.Tokens != nil {
				ev.TokensIn, ev.TokensOut = m.Tokens.Input, m.Tokens.Output
			}
			return ev, true
		case "step_start":
			// Bookkeeping: recognized, ignored.
			return Event{}, true
		}
		return Event{}, false
	case "part":
		// Legacy format.
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
