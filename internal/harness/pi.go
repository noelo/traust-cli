package harness

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
)

// Pi is the pi coding agent adapter (earendil-works/pi). JSON mode emits
// documented JSONL session events on stdout (packages/coding-agent/docs/
// json.md). Tool selection maps the skill's declared tools onto pi's builtin
// set; pi has no subagents or interactive-question tool, so skills relying on
// them degrade — the notes say so.
type Pi struct{}

func (p *Pi) ID() string          { return "pi" }
func (p *Pi) DisplayName() string { return "Pi" }
func (p *Pi) Binary() string      { return binaryEnv("pi", "pi") }

func (p *Pi) Capabilities() Caps {
	return Caps{StreamJSON: true, ModelSelect: true, PermissionScoping: true, UsageReporting: true}
}

func (p *Pi) Detect(ctx context.Context) Detection { return detect(ctx, p.Binary()) }

// mapTools translates Claude-style frontmatter tool names onto pi's builtin
// tools. Unmappable tools (Task subagents, AskUserQuestion) are dropped with
// a note.
func mapTools(allowed []string) string {
	if len(allowed) == 0 {
		return "read,bash,edit,write,grep,find,ls"
	}
	var out []string
	add := func(t string) {
		for _, x := range out {
			if x == t {
				return
			}
		}
		out = append(out, t)
	}
	for _, grant := range allowed {
		name := grant
		if i := strings.IndexByte(name, '('); i > 0 {
			name = name[:i]
		}
		switch name {
		case "Read":
			add("read")
		case "Bash":
			add("bash")
		case "Write":
			add("write")
		case "Edit":
			add("edit")
		case "Grep":
			add("grep")
		case "Glob", "LS":
			add("find")
		}
	}
	if len(out) == 0 {
		return "read,bash,edit,write,grep,find,ls"
	}
	return strings.Join(out, ",")
}

func (p *Pi) Command(ctx context.Context, spec *RunSpec) *exec.Cmd {
	args := []string{"--mode", "json"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.UnsafeFullPermission {
		// Full-permission mode: pi gates by tool declaration, not per-call
		// approval, so "all tools auto-approved" is the full builtin set.
		args = append(args, "--tools", "read,bash,edit,write,grep,find,ls")
	} else {
		args = append(args, "--tools", mapTools(spec.AllowedTools))
	}
	args = append(args, "--", spec.Prompt)
	cmd := exec.CommandContext(ctx, p.Binary(), args...)
	cmd.Dir = spec.Cwd
	cmd.Env = spec.Env
	return cmd
}

func (p *Pi) Notes() []string {
	return []string{
		"pi has no subagent fan-out or interactive-question tool: skills that rely on them run degraded",
		"tool grants are mapped onto pi's builtin read/bash/edit/write/grep/find/ls",
	}
}

// ParseEvent implements pi's documented JSON event stream.
func (p *Pi) ParseEvent(line []byte) (Event, bool) {
	var m struct {
		Type  string `json:"type"`
		Usage *struct {
			Input  int64 `json:"input"`
			Output int64 `json:"output"`
		} `json:"usage"`
		AssistantMessageEvent *struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			ToolName string `json:"toolName"`
			Reason   string `json:"reason"`
			Error    string `json:"error"`
		} `json:"assistantMessageEvent"`
		Message *struct {
			Role  string `json:"role"`
			Usage *struct {
				Input  int64 `json:"input"`
				Output int64 `json:"output"`
			} `json:"usage"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
		ToolName string `json:"toolName"`
		Aborted  bool   `json:"aborted"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return Event{}, false
	}
	switch m.Type {
	case "message_update":
		if m.AssistantMessageEvent == nil {
			return Event{}, false
		}
		ev := Event{}
		if m.Usage != nil && (m.Usage.Input > 0 || m.Usage.Output > 0) {
			ev.TokensIn, ev.TokensOut = m.Usage.Input, m.Usage.Output
		}
		switch m.AssistantMessageEvent.Type {
		case "text_delta":
			ev.Kind = EventText
			ev.Text = m.AssistantMessageEvent.Delta
		case "toolcall_start", "toolcall_end":
			ev.Kind = EventToolCall
			ev.Tool = m.AssistantMessageEvent.ToolName
		case "error":
			ev.Kind = EventError
			ev.Text = m.AssistantMessageEvent.Error
			ev.IsError = true
		default:
			if ev.TokensIn == 0 && ev.TokensOut == 0 {
				return Event{}, false
			}
			ev.Kind = EventUsage
		}
		return ev, true
	case "tool_execution_start":
		return Event{Kind: EventToolCall, Tool: m.ToolName}, true
	case "message_end":
		if m.Message == nil || m.Message.Role != "assistant" {
			// System/user message bookkeeping: recognized, ignored.
			return Event{}, m.Message != nil
		}
		var sb strings.Builder
		for _, block := range m.Message.Content {
			if block.Type == "text" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(block.Text)
			}
		}
		if sb.Len() == 0 {
			return Event{}, false
		}
		return Event{Kind: EventText, Text: sb.String()}, true
	case "turn_end":
		// pi >= 1.4 carries the final assistant message (content AND
		// usage) on turn_end rather than message_end.
		if m.Message == nil || m.Message.Role != "assistant" {
			return Event{}, false
		}
		ev := Event{Kind: EventResult}
		var sb strings.Builder
		for _, block := range m.Message.Content {
			if block.Type == "text" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(block.Text)
			}
		}
		ev.Text = sb.String()
		if m.Message.Usage != nil {
			ev.TokensIn, ev.TokensOut = m.Message.Usage.Input, m.Message.Usage.Output
		} else if m.Usage != nil {
			ev.TokensIn, ev.TokensOut = m.Usage.Input, m.Usage.Output
		}
		return ev, true
	case "agent_settled":
		return Event{Kind: EventResult, IsError: m.Aborted}, true
	case "session", "agent_start", "agent_end", "turn_start":
		// Protocol bookkeeping: recognized, ignored — printing them as raw
		// lines was pure noise.
		return Event{}, true
	case "message_start":
		return Event{}, true
	}
	return Event{}, false
}
