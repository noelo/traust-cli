package harness

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// streamingScript replays the REAL shapes observed from a live crush
// server: message snapshots with parts growing in place (text fragments
// fill out, tool inputs stream in), and tool results in a separate
// role=tool message.
func streamingScript(w io.Writer, r *http.Request, sid, runID string) {
	sse(w, `{"type":"message","payload":{"type":"updated","payload":{"id":"m1","role":"assistant","parts":[%s]}}}`,
		`{"type":"text","data":{"text":"The tar"}}`)
	sse(w, `{"type":"message","payload":{"type":"updated","payload":{"id":"m1","role":"assistant","parts":[%s]}}}`,
		`{"type":"text","data":{"text":"The target is a Go monorepo"}}`)
	sse(w, `{"type":"message","payload":{"type":"updated","payload":{"id":"m1","role":"assistant","parts":[%s,%s]}}}`,
		`{"type":"text","data":{"text":"The target is a Go monorepo"}}`,
		`{"type":"tool_call","data":{"id":"c1","name":"bash","input":"","finished":false}}`)
	sse(w, `{"type":"message","payload":{"type":"updated","payload":{"id":"m1","role":"assistant","parts":[%s,%s]}}}`,
		`{"type":"text","data":{"text":"The target is a Go monorepo"}}`,
		`{"type":"tool_call","data":{"id":"c1","name":"bash","input":"{\"command\":\"rg -n r.GET\"}","finished":true}}`)
	sse(w, `{"type":"message","payload":{"type":"created","payload":{"id":"m2","role":"tool","parts":[%s]}}}`,
		`{"type":"tool_result","data":{"tool_call_id":"c1","name":"bash","content":"router.go:112: r.GET(\"/admin/export\")","is_error":false}}`)
	sse(w, `{"type":"run_complete","payload":{"type":"updated","payload":{"session_id":"%s","run_id":"%s","text":"done"}}}`, sid, runID)
}

// TestCrushServeStreamingParts is the regression test for the monitor
// showing only fragments: text and tool inputs stream by replacement, and
// tool results arrive in role=tool messages that were previously dropped.
func TestCrushServeStreamingParts(t *testing.T) {
	f := startFakeCrush(t, streamingScript)

	var events []Event
	res, err := (&Crush{}).RunSession(context.Background(), &RunSpec{
		Prompt: "p", Cwd: t.TempDir(), Env: os.Environ(),
	}, func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" {
		t.Fatalf("result = %+v", res)
	}

	var finalText, toolCall, toolResult *Event
	for i := range events {
		switch events[i].Kind {
		case EventText:
			finalText = &events[i]
		case EventToolCall:
			toolCall = &events[i]
		case EventToolResult:
			toolResult = &events[i]
		}
	}
	if finalText == nil || finalText.Text != "The target is a Go monorepo" {
		t.Errorf("text = %+v (want the FINAL streamed content, not the first fragment)", finalText)
	}
	if finalText.Key == "" {
		t.Errorf("streamed text must carry a key for in-place rendering")
	}
	if toolCall == nil || toolCall.Tool != "bash" || !strings.Contains(toolCall.Text, "rg -n r.GET") {
		t.Errorf("tool call = %+v (want the streamed input, not the empty first frame)", toolCall)
	}
	if toolCall.Key == "" {
		t.Errorf("streamed tool call must carry a key")
	}
	if toolResult == nil || toolResult.Tool != "bash" || !strings.Contains(toolResult.Text, "router.go:112") {
		t.Errorf("tool result = %+v (role=tool messages must not be dropped)", toolResult)
	}

	// Content-change dedup: the unchanged text part repeated across
	// snapshots must not re-emit.
	textCount := 0
	for _, ev := range events {
		if ev.Kind == EventText {
			textCount++
		}
	}
	if textCount != 2 {
		t.Errorf("text emissions = %d (fragment + final only; unchanged snapshots must not re-emit)", textCount)
	}
	_ = f
}
