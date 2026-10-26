package harness

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
)

// Claude is the Claude Code adapter. Headless print mode with the stream-json
// output format; the skill's frontmatter allowed-tools are passed through
// verbatim (they are Claude Code grant syntax). Permission posture is
// fail-closed: --permission-mode acceptEdits plus the skill's declared
// grants; anything else is denied headlessly. --unsafe-full-permission adds
// --dangerously-skip-permissions.
type Claude struct{}

func (c *Claude) ID() string          { return "claude" }
func (c *Claude) DisplayName() string { return "Claude Code" }
func (c *Claude) Binary() string      { return binaryEnv("claude", "claude") }

func (c *Claude) Capabilities() Caps {
	return Caps{StreamJSON: true, ModelSelect: true, PermissionScoping: true, UsageReporting: true}
}

func (c *Claude) Detect(ctx context.Context) Detection { return detect(ctx, c.Binary()) }

func (c *Claude) Command(ctx context.Context, spec *RunSpec) *exec.Cmd {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(spec.MaxTurns))
	}
	if spec.UnsafeFullPermission {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		args = append(args, "--permission-mode", "acceptEdits")
		if len(spec.AllowedTools) > 0 {
			args = append(args, "--allowedTools", strings.Join(spec.AllowedTools, ","))
		}
	}
	cmd := exec.CommandContext(ctx, c.Binary(), args...)
	cmd.Dir = spec.Cwd
	cmd.Env = spec.Env
	cmd.Stdin = strings.NewReader(spec.Prompt)
	return cmd
}

func (c *Claude) Notes() []string {
	return []string{
		"permission posture: acceptEdits + the skill's declared allowed-tools; undeclared commands are denied headlessly (fail-closed)",
	}
}

// ParseEvent handles Claude Code stream-json lines: assistant messages with
// text/tool_use content blocks, and the final result message with usage.
func (c *Claude) ParseEvent(line []byte) (Event, bool) {
	var m struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Message *struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
				Name     string `json:"name"`
			} `json:"content"`
		} `json:"message"`
		Result       string  `json:"result"`
		IsError      bool    `json:"is_error"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		Usage        *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return Event{}, false
	}
	switch m.Type {
	case "assistant":
		if m.Message == nil {
			return Event{}, false
		}
		for _, block := range m.Message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					return Event{Kind: EventText, Text: block.Text}, true
				}
			case "thinking":
				if block.Thinking != "" {
					return Event{Kind: EventThinking, Text: block.Thinking}, true
				}
			case "tool_use":
				if block.Name == "AskUserQuestion" {
					return Event{Kind: EventNeedsInput, Tool: block.Name}, true
				}
				return Event{Kind: EventToolCall, Tool: block.Name}, true
			}
		}
		return Event{}, false
	case "system":
		// Protocol bookkeeping (init, thinking-token estimates): recognized,
		// ignored — printing them as raw lines was pure noise.
		return Event{}, true
	case "result":
		ev := Event{Kind: EventResult, Text: m.Result, IsError: m.IsError, CostUSD: m.TotalCostUSD}
		if m.Usage != nil {
			ev.TokensIn = m.Usage.InputTokens
			ev.TokensOut = m.Usage.OutputTokens
		}
		return ev, true
	}
	return Event{}, false
}
