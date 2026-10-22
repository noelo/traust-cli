package cmd

import (
	"strings"
	"testing"

	"github.com/traust-security/traust-cli/internal/harness"
)

// TestKeyedRendererCollapsesStreams verifies plain rendering collapses a
// part's streaming updates to its final content — one line per part, not
// one per fragment.
func TestKeyedRendererCollapsesStreams(t *testing.T) {
	var b strings.Builder
	k := newKeyedRenderer(&b)
	k.Event(harness.Event{Kind: harness.EventText, Text: "The tar", Key: "m1:0"})
	k.Event(harness.Event{Kind: harness.EventText, Text: "The target is a Go monorepo", Key: "m1:0"})
	k.Event(harness.Event{Kind: harness.EventToolCall, Tool: "bash", Text: `{"command":"rg"}`, Key: "m1:1"})
	k.Event(harness.Event{Kind: harness.EventToolCall, Tool: "bash", Text: `{"command":"rg -n r.GET"}`, Key: "m1:1"})
	k.Event(harness.Event{Kind: harness.EventText, Text: "unkeyed appends", Key: ""})
	k.Flush()

	out := b.String()
	if strings.Contains(out, "The tar\n") { // the standalone fragment line
		t.Errorf("fragment leaked into plain output: %q", out)
	}
	if !strings.Contains(out, "The target is a Go monorepo") {
		t.Errorf("final text missing: %q", out)
	}
	if !strings.Contains(out, "rg -n r.GET") || strings.Contains(out, `{"command":"rg"}`+"\n") {
		t.Errorf("tool input not collapsed to final: %q", out)
	}
	if strings.Count(out, "→ bash") != 1 {
		t.Errorf("tool call rendered more than once: %q", out)
	}
	if !strings.Contains(out, "unkeyed appends") {
		t.Errorf("unkeyed event lost: %q", out)
	}
}
