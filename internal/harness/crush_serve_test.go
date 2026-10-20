package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCrush is an in-process stand-in for `crush server`, speaking the same
// v1 API surface the adapter uses: health, workspaces, sessions, the SSE
// event stream, agent prompt, cancel and control.
type fakeCrush struct {
	listener net.Listener
	server   *http.Server

	mu          sync.Mutex
	agentPrompt string
	agentRunID  string
	cancelCalls []string
	modelReqs   []map[string]any
	script      func(w io.Writer, r *http.Request, sid, runID string)
	agentPosted chan struct{}
	postOnce    sync.Once
}

func startFakeCrush(t *testing.T, script func(w io.Writer, r *http.Request, sid, runID string)) *fakeCrush {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "crush.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeCrush{
		listener:    ln,
		script:      script,
		agentPosted: make(chan struct{}),
	}
	mux := http.NewServeMux()

	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["client_id"] == nil || body["path"] == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid client_id"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "ws-1", "path": body["path"]})
	})

	mux.HandleFunc("POST /v1/workspaces/{id}/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "sess-1"})
	})

	mux.HandleFunc("POST /v1/workspaces/{id}/current-session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{})
	})

	mux.HandleFunc("POST /v1/workspaces/{id}/config/model", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.modelReqs = append(f.modelReqs, body)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /v1/workspaces/{id}/agent/update", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /v1/workspaces/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("client_id") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Flush headers immediately, exactly like the real server: the
		// client's HTTP Do must return before it posts the prompt.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-f.agentPosted:
		case <-r.Context().Done():
			return
		}
		f.mu.Lock()
		runID := f.agentRunID
		f.mu.Unlock()
		f.script(w, r, "sess-1", runID)
	})

	mux.HandleFunc("POST /v1/workspaces/{id}/agent", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SessionID string `json:"session_id"`
			RunID     string `json:"run_id"`
			Prompt    string `json:"prompt"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.agentPrompt, f.agentRunID = body.Prompt, body.RunID
		f.mu.Unlock()
		f.postOnce.Do(func() { close(f.agentPosted) })
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("POST /v1/workspaces/{id}/agent/sessions/{sid}/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.cancelCalls = append(f.cancelCalls, r.PathValue("sid"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /v1/control", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})

	f.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go f.server.Serve(ln)
	t.Cleanup(func() { f.server.Close() })
	t.Setenv("TRAUST_CI_CRUSH_SERVER_URL_UNUSED", "") // keep env hermetic intent
	t.Setenv("TRAUST_CLI_CRUSH_SERVER_URL", "unix://"+sock)
	return f
}

func sse(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(format, args...))
	w.(http.Flusher).Flush()
}

// happyScript streams the shapes a real crush turn produces.
func happyScript(w io.Writer, r *http.Request, sid, runID string) {
	sse(w, `{"type":"message","payload":{"type":"updated","payload":{"id":"m1","role":"assistant","parts":[%s]}}}`,
		`{"type":"reasoning","data":{"thinking":"pondering the target","finished_at":1792525268}}`)
	sse(w, `{"type":"message","payload":{"type":"updated","payload":{"id":"m1","role":"assistant","parts":[%s,%s]}}}`,
		`{"type":"reasoning","data":{"thinking":"pondering the target","finished_at":1792525268}}`,
		`{"type":"tool_call","data":{"name":"bash","input":"{\"command\":\"git clone\"}"}}`)
	sse(w, `{"type":"session","payload":{"type":"updated","payload":{"id":"%s","prompt_tokens":100,"completion_tokens":50,"cost":0.42}}}`, sid)
	sse(w, `{"type":"message","payload":{"type":"updated","payload":{"id":"m1","role":"assistant","parts":[%s,%s,%s]}}}`,
		`{"type":"reasoning","data":{"thinking":"pondering the target","finished_at":1792525268}}`,
		`{"type":"tool_call","data":{"name":"bash","input":"{\"command\":\"git clone\"}"}}`,
		`{"type":"text","data":{"text":"audit complete"}}`)
	sse(w, `{"type":"run_complete","payload":{"type":"updated","payload":{"session_id":"%s","run_id":"%s","message_id":"m1","text":"audit complete"}}}`, sid, runID)
}

func TestCrushServeSessionRun(t *testing.T) {
	f := startFakeCrush(t, happyScript)

	var events []Event
	res, err := (&Crush{}).RunSession(context.Background(), &RunSpec{
		Prompt: "do the audit", Cwd: t.TempDir(), Env: os.Environ(),
	}, func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "audit complete" || res.Err != "" || res.Cancelled {
		t.Fatalf("result = %+v", res)
	}

	var sawText, sawThinking, sawTool, sawUsage bool
	for _, ev := range events {
		switch ev.Kind {
		case EventText:
			sawText = ev.Text == "audit complete"
		case EventThinking:
			sawThinking = ev.Text == "pondering the target"
		case EventToolCall:
			sawTool = ev.Tool == "bash" && strings.Contains(ev.Text, "git clone")
		case EventUsage:
			sawUsage = ev.TokensIn == 100 && ev.TokensOut == 50 && ev.CostUSD == 0.42
		}
	}
	if !sawText || !sawThinking || !sawTool || !sawUsage {
		t.Errorf("events incomplete: text=%v thinking=%v tool=%v usage=%v (%d events)", sawText, sawThinking, sawTool, sawUsage, len(events))
	}
	if len(f.cancelCalls) != 0 {
		t.Errorf("unexpected cancel calls: %v", f.cancelCalls)
	}
}

func TestCrushServePermissionFailsClosed(t *testing.T) {
	f := startFakeCrush(t, func(w io.Writer, r *http.Request, sid, runID string) {
		sse(w, `{"type":"permission_request","payload":{"type":"updated","payload":{"session_id":"%s","tool_name":"write","description":"/etc/hosts"}}}`, sid)
		// Hold the stream open until the client disconnects.
		<-r.Context().Done()
	})

	var events []Event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := (&Crush{}).RunSession(ctx, &RunSpec{Prompt: "p", Cwd: t.TempDir(), Env: os.Environ()},
		func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	needsInput := false
	for _, ev := range events {
		if ev.Kind == EventNeedsInput && ev.Tool == "write" && ev.Text == "/etc/hosts" {
			needsInput = true
		}
	}
	if !needsInput {
		t.Errorf("permission request should surface as needs-input: %+v", events)
	}
	if !res.Cancelled {
		t.Errorf("cancelled run should report Cancelled: %+v", res)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		calls := len(f.cancelCalls)
		f.mu.Unlock()
		if calls > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the remote cancel endpoint was never called")
}

func TestCrushServeRunCompleteError(t *testing.T) {
	startFakeCrush(t, func(w io.Writer, r *http.Request, sid, runID string) {
		sse(w, `{"type":"run_complete","payload":{"type":"updated","payload":{"session_id":"%s","run_id":"%s","error":"provider 401"}}}`, sid, runID)
	})
	res, err := (&Crush{}).RunSession(context.Background(), &RunSpec{Prompt: "p", Cwd: t.TempDir(), Env: os.Environ()}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Err != "provider 401" {
		t.Fatalf("result = %+v", res)
	}
}

func TestCrushServeIgnoresForeignRunComplete(t *testing.T) {
	startFakeCrush(t, func(w io.Writer, r *http.Request, sid, runID string) {
		// A run_complete for a different run must not terminate ours.
		sse(w, `{"type":"run_complete","payload":{"type":"updated","payload":{"session_id":"other","run_id":"not-mine","text":"nope"}}}`)
		sse(w, `{"type":"run_complete","payload":{"type":"updated","payload":{"session_id":"%s","run_id":"%s","text":"mine"}}}`, sid, runID)
	})
	res, err := (&Crush{}).RunSession(context.Background(), &RunSpec{Prompt: "p", Cwd: t.TempDir(), Env: os.Environ()}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "mine" {
		t.Fatalf("result = %+v", res)
	}
}

func TestCrushServeModelOverride(t *testing.T) {
	f := startFakeCrush(t, happyScript)
	// Fake `crush models` listing; the runner resolves the floor model here.
	dir := t.TempDir()
	bin := filepath.Join(dir, "crush-models")
	script := "#!/bin/sh\nprintf 'PriceTag/floor-model-x\\nProvider2/other-model\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRAUST_CLI_CRUSH_BIN", bin)

	_, err := (&Crush{}).RunSession(context.Background(), &RunSpec{
		Prompt: "p", Cwd: t.TempDir(), Env: os.Environ(), Model: "floor-model-x",
	}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.modelReqs) != 1 {
		t.Fatalf("model requests = %+v", f.modelReqs)
	}
	req := f.modelReqs[0]
	if req["model_type"] != "large" || req["scope"] != float64(1) {
		t.Errorf("model request = %+v", req)
	}
	model := req["model"].(map[string]any)
	if model["provider"] != "PriceTag" || model["model"] != "floor-model-x" {
		t.Errorf("resolved model = %+v", model)
	}
}
