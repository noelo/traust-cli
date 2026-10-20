package harness

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
)

// Codex is the OpenAI Codex CLI adapter (`codex exec`). The prompt is piped
// on stdin; events arrive as experimental JSONL on stdout. Safe mode runs in
// the workspace-write sandbox with approvals disabled (fail-closed: actions
// outside the sandbox fail rather than prompt). AGENTS.md at the harness root
// is auto-discovered by codex from the working directory.
type Codex struct{}

func (c *Codex) ID() string          { return "codex" }
func (c *Codex) DisplayName() string { return "Codex" }
func (c *Codex) Binary() string      { return binaryEnv("codex", "codex") }

func (c *Codex) Capabilities() Caps {
	return Caps{StreamJSON: true, ModelSelect: true, PermissionScoping: false, UsageReporting: true}
}

func (c *Codex) Detect(ctx context.Context) Detection { return detect(ctx, c.Binary()) }

func (c *Codex) Command(ctx context.Context, spec *RunSpec) *exec.Cmd {
	args := []string{"exec", "-", "--json"}
	if spec.OutDir != "" {
		args = append(args, "--output-last-message", filepath.Join(spec.OutDir, "final-message.md"))
	}
	args = append(args, "--cd", spec.Cwd)
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	if spec.UnsafeFullPermission {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	} else {
		args = append(args, "-s", "workspace-write", "-a", "never")
	}
	cmd := exec.CommandContext(ctx, c.Binary(), args...)
	cmd.Dir = spec.Cwd
	cmd.Env = spec.Env
	cmd.Stdin = strings.NewReader(spec.Prompt)
	return cmd
}

func (c *Codex) Notes() []string {
	return []string{
		"codex cannot express per-tool grants: safe mode is the workspace-write sandbox with approvals disabled (fail-closed)",
		"codex auto-reads AGENTS.md from the working directory (the harness root — trusted)",
	}
}

// ParseEvent handles both the current JSONL schema (thread.started /
// item.completed / turn.completed) and the older {"msg":{...}} envelopes.
func (c *Codex) ParseEvent(line []byte) (Event, bool) {
	var m struct {
		Type string `json:"type"`
		Item *struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Command string `json:"command"`
		} `json:"item"`
		Usage *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
		Message string `json:"message"`
		Msg     *struct {
			Type      string `json:"type"`
			Message   string `json:"message"`
			LastAgent string `json:"last_agent_message"`
			InputTok  int64  `json:"input_tokens"`
			OutputTok int64  `json:"output_tokens"`
		} `json:"msg"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return Event{}, false
	}
	switch m.Type {
	case "item.completed", "item.updated":
		if m.Item == nil {
			return Event{}, false
		}
		switch m.Item.Type {
		case "agent_message":
			return Event{Kind: EventText, Text: m.Item.Text}, true
		case "command_execution", "file_change", "mcp_tool_call", "web_search":
			return Event{Kind: EventToolCall, Tool: m.Item.Type}, true
		}
		return Event{}, false
	case "turn.completed":
		ev := Event{Kind: EventResult}
		if m.Usage != nil {
			ev.TokensIn = m.Usage.InputTokens
			ev.TokensOut = m.Usage.OutputTokens
		}
		return ev, true
	case "error":
		return Event{Kind: EventError, Text: m.Message, IsError: true}, true
	}
	if m.Msg != nil {
		switch m.Msg.Type {
		case "agent_message":
			return Event{Kind: EventText, Text: m.Msg.Message}, true
		case "token_count":
			return Event{Kind: EventUsage, TokensIn: m.Msg.InputTok, TokensOut: m.Msg.OutputTok}, true
		case "task_complete":
			return Event{Kind: EventResult, Text: m.Msg.LastAgent}, true
		}
	}
	return Event{}, false
}
