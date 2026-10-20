package harness

import (
	"context"
	"os"
	"strings"
	"testing"
)

func spec() *RunSpec {
	return &RunSpec{
		Prompt:       "PROMPT",
		Model:        "model-x",
		Cwd:          "/harness/root",
		Env:          []string{"PATH=/x"},
		AllowedTools: []string{"Read", "Bash(git clone:*)"},
		MaxTurns:     10,
		OutDir:       "/run/dir",
	}
}

func argsOf(t *testing.T, h Harness, s *RunSpec) []string {
	t.Helper()
	cmd := h.Command(context.Background(), s)
	return cmd.Args
}

func TestClaudeCommand(t *testing.T) {
	args := argsOf(t, &Claude{}, spec())
	for _, want := range []string{"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--model", "model-x", "--max-turns", "10", "--permission-mode", "acceptEdits",
		"--allowedTools", "Read,Bash(git clone:*)"} {
		if !contains(args, want) {
			t.Errorf("claude args missing %q: %v", want, args)
		}
	}
	unsafe := spec()
	unsafe.UnsafeFullPermission = true
	args = argsOf(t, &Claude{}, unsafe)
	if !contains(args, "--dangerously-skip-permissions") || contains(args, "--permission-mode") {
		t.Errorf("unsafe claude args wrong: %v", args)
	}
}

func TestCodexCommand(t *testing.T) {
	args := argsOf(t, &Codex{}, spec())
	for _, want := range []string{"codex", "exec", "-", "--json",
		"--output-last-message", "/run/dir/final-message.md",
		"--cd", "/harness/root", "-m", "model-x",
		"-s", "workspace-write", "-a", "never"} {
		if !contains(args, want) {
			t.Errorf("codex args missing %q: %v", want, args)
		}
	}
	unsafe := spec()
	unsafe.UnsafeFullPermission = true
	args = argsOf(t, &Codex{}, unsafe)
	if !contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("unsafe codex args wrong: %v", args)
	}
}

func TestPiCommand(t *testing.T) {
	// The spec declares Read + Bash(git clone:*): mapped onto pi's read/bash.
	args := argsOf(t, &Pi{}, spec())
	for _, want := range []string{"pi", "--mode", "json", "--model", "model-x",
		"--tools", "read,bash", "--", "PROMPT"} {
		if !contains(args, want) {
			t.Errorf("pi args missing %q: %v", want, args)
		}
	}
	// With no declared tools, the full safe default applies.
	empty := spec()
	empty.AllowedTools = nil
	args = argsOf(t, &Pi{}, empty)
	if !contains(args, "--tools") || !contains(args, "read,bash,edit,write,grep,find,ls") {
		t.Errorf("pi default tools wrong: %v", args)
	}
	// Task and AskUserQuestion are unmappable and dropped.
	mapped := mapTools([]string{"Read", "Task", "AskUserQuestion", "Grep"})
	if mapped != "read,grep" {
		t.Errorf("mapTools = %q, want read,grep", mapped)
	}
}

func TestOpencodeCommand(t *testing.T) {
	args := argsOf(t, &Opencode{}, spec())
	for _, want := range []string{"opencode", "run", "--model", "model-x",
		"--format", "json", "--auto", "--dir", "/harness/root", "PROMPT"} {
		if !contains(args, want) {
			t.Errorf("opencode args missing %q: %v", want, args)
		}
	}
}

func TestCrushCommand(t *testing.T) {
	s := spec()
	cmd := (&Crush{}).Command(context.Background(), s)
	for _, want := range []string{"crush", "run", "--quiet", "--model", "model-x"} {
		if !contains(cmd.Args, want) {
			t.Errorf("crush args missing %q: %v", want, cmd.Args)
		}
	}
	if cmd.Stdin == nil {
		t.Error("crush should take the prompt on stdin")
	}
	if cmd.Dir != "/harness/root" {
		t.Errorf("cwd = %q", cmd.Dir)
	}
}

func TestGooseCommandTemplate(t *testing.T) {
	t.Setenv("TRAUST_CLI_GOOSE_ARGS", "run --name x --text {prompt}")
	args := argsOf(t, &Goose{}, spec())
	want := []string{"goose", "run", "--name", "x", "--text", "PROMPT"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("goose args = %v, want %v", args, want)
	}
}

func TestGetUnknownAndAgy(t *testing.T) {
	if _, err := Get("nope"); err == nil {
		t.Error("unknown harness should error")
	}
	_, err := Get("agy")
	if err == nil || !strings.Contains(err.Error(), "no headless execution path") {
		t.Errorf("agy error = %v", err)
	}
}

func TestBinaryEnvOverride(t *testing.T) {
	t.Setenv("TRAUST_CLI_CLAUDE_BIN", "/fake/claude")
	if got := (&Claude{}).Binary(); got != "/fake/claude" {
		t.Errorf("binary = %q", got)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestClaudeParseEvent(t *testing.T) {
	c := &Claude{}
	ev, ok := c.ParseEvent([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`))
	if !ok || ev.Kind != EventText || ev.Text != "hello" {
		t.Errorf("text event = %+v, %v", ev, ok)
	}
	ev, ok = c.ParseEvent([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`))
	if !ok || ev.Kind != EventToolCall || ev.Tool != "Bash" {
		t.Errorf("tool event = %+v, %v", ev, ok)
	}
	ev, ok = c.ParseEvent([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion"}]}}`))
	if !ok || ev.Kind != EventNeedsInput {
		t.Errorf("needs-input event = %+v, %v", ev, ok)
	}
	ev, ok = c.ParseEvent([]byte(`{"type":"result","subtype":"success","result":"done","usage":{"input_tokens":10,"output_tokens":5},"total_cost_usd":0.5}`))
	if !ok || ev.Kind != EventResult || ev.TokensIn != 10 || ev.TokensOut != 5 || ev.CostUSD != 0.5 {
		t.Errorf("result event = %+v, %v", ev, ok)
	}
	if _, ok := c.ParseEvent([]byte(`not json`)); ok {
		t.Error("garbage should not parse")
	}
}

func TestCodexParseEvent(t *testing.T) {
	c := &Codex{}
	ev, ok := c.ParseEvent([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`))
	if !ok || ev.Kind != EventText || ev.Text != "hi" {
		t.Errorf("text = %+v, %v", ev, ok)
	}
	ev, ok = c.ParseEvent([]byte(`{"type":"item.completed","item":{"type":"command_execution","command":"ls"}}`))
	if !ok || ev.Kind != EventToolCall {
		t.Errorf("tool = %+v, %v", ev, ok)
	}
	ev, ok = c.ParseEvent([]byte(`{"type":"turn.completed","usage":{"input_tokens":7,"output_tokens":3}}`))
	if !ok || ev.Kind != EventResult || ev.TokensIn != 7 {
		t.Errorf("result = %+v, %v", ev, ok)
	}
	// Old envelope.
	ev, ok = c.ParseEvent([]byte(`{"id":"1","msg":{"type":"token_count","input_tokens":4,"output_tokens":2}}`))
	if !ok || ev.Kind != EventUsage || ev.TokensOut != 2 {
		t.Errorf("usage = %+v, %v", ev, ok)
	}
}

func TestPiParseEvent(t *testing.T) {
	p := &Pi{}
	ev, ok := p.ParseEvent([]byte(`{"type":"message_update","usage":{"input":11,"output":6},"assistantMessageEvent":{"type":"text_delta","delta":"Hel"}}`))
	if !ok || ev.Kind != EventText || ev.Text != "Hel" || ev.TokensIn != 11 {
		t.Errorf("text = %+v, %v", ev, ok)
	}
	ev, ok = p.ParseEvent([]byte(`{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{}}`))
	if !ok || ev.Kind != EventToolCall || ev.Tool != "bash" {
		t.Errorf("tool = %+v, %v", ev, ok)
	}
	ev, ok = p.ParseEvent([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"final"}]}}`))
	if !ok || ev.Kind != EventText || ev.Text != "final" {
		t.Errorf("message_end = %+v, %v", ev, ok)
	}
	ev, ok = p.ParseEvent([]byte(`{"type":"agent_settled","aborted":false}`))
	if !ok || ev.Kind != EventResult {
		t.Errorf("settled = %+v, %v", ev, ok)
	}
}

func TestMain(m *testing.M) {
	// Keep the environment hermetic for golden argv construction.
	os.Unsetenv("TRAUST_CLI_CLAUDE_BIN")
	os.Unsetenv("TRAUST_CLI_GOOSE_ARGS")
	os.Exit(m.Run())
}
