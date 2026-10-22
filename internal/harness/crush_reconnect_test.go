package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// reconnectServer is a fake crush server whose SSE connection drops
// mid-run, exercising the reconnect and run-recovery paths.
type reconnectServer struct {
	listener net.Listener
	server   *http.Server

	mu          sync.Mutex
	eventConns  int
	clientIDs   []string
	agentRunID  string
	agentPosted chan struct{}
	sessionBusy bool
	messages    []map[string]any
}

func startReconnectServer(t *testing.T) *reconnectServer {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &reconnectServer{listener: ln, agentPosted: make(chan struct{}), sessionBusy: true}
	mux := http.NewServeMux()

	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"id": "ws-1"})
	})
	mux.HandleFunc("POST /v1/workspaces/{id}/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"id": "sess-1"})
	})
	mux.HandleFunc("POST /v1/workspaces/{id}/current-session", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /v1/workspaces/{id}/agent", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RunID string `json:"run_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.agentRunID = body.RunID
		f.mu.Unlock()
		select {
		case <-f.agentPosted:
		default:
			close(f.agentPosted)
		}
		w.WriteHeader(202)
	})
	mux.HandleFunc("GET /v1/workspaces/{id}/agent/sessions/{sid}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		busy := f.sessionBusy
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"id": "sess-1", "is_busy": busy})
	})
	mux.HandleFunc("GET /v1/workspaces/{id}/sessions/{sid}/messages", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		msgs := f.messages
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"messages": msgs})
	})
	mux.HandleFunc("POST /v1/control", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	mux.HandleFunc("GET /v1/workspaces/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.eventConns++
		conn := f.eventConns
		f.clientIDs = append(f.clientIDs, r.URL.Query().Get("client_id"))
		runID := f.agentRunID
		f.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-f.agentPosted:
		case <-r.Context().Done():
			return
		}
		sse := func(format string, args ...any) {
			fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(format, args...))
			w.(http.Flusher).Flush()
		}
		if conn == 1 {
			// First connection: a fragment, then the connection DROPS
			// (no run_complete) — the run keeps going server-side.
			sse(`{"type":"message","payload":{"type":"updated","payload":{"id":"m1","role":"assistant","parts":[{"type":"text","data":{"text":"The tar"}}]}}}`)
			return
		}
		// Reconnect: replayed snapshots (deduped) and completion.
		sse(`{"type":"message","payload":{"type":"updated","payload":{"id":"m1","role":"assistant","parts":[{"type":"text","data":{"text":"The target is a Go monorepo"}}]}}}`)
		sse(`{"type":"run_complete","payload":{"type":"updated","payload":{"session_id":"sess-1","run_id":"%s","text":"audit complete"}}}`, runID)
	})

	f.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go f.server.Serve(ln)
	t.Cleanup(func() { f.server.Close() })
	t.Setenv("TRAUST_CLI_CRUSH_SERVER_URL", "unix://"+sock)
	return f
}

// TestCrushServeReconnectsOnDrop is the regression test for the
// 45-minute-run-killed-by-an-EOF failure: the SSE stream ending without
// run_complete must reconnect (same client id, state preserved) instead of
// failing the run.
func TestCrushServeReconnectsOnDrop(t *testing.T) {
	f := startReconnectServer(t)

	var events []Event
	res, err := (&Crush{}).RunSession(context.Background(), &RunSpec{
		Prompt: "p", Cwd: t.TempDir(), Env: os.Environ(),
	}, func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "audit complete" {
		t.Fatalf("result = %+v (the run should survive the dropped connection)", res)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.eventConns != 2 {
		t.Fatalf("event connections = %d, want 2 (drop + reconnect)", f.eventConns)
	}
	if len(f.clientIDs) != 2 || f.clientIDs[0] == "" || f.clientIDs[0] != f.clientIDs[1] {
		t.Errorf("reconnect must reuse the client id: %v", f.clientIDs)
	}
	var final *Event
	for i := range events {
		if events[i].Kind == EventText {
			final = &events[i]
		}
	}
	if final == nil || final.Text != "The target is a Go monorepo" {
		t.Errorf("final text = %+v", final)
	}
}

// TestCrushServeRecoversFinishedRun covers the run completing while the
// stream was down: a no-longer-busy session has its final state recovered
// from the session messages instead of hanging or failing.
func TestCrushServeRecoversFinishedRun(t *testing.T) {
	f := startReconnectServer(t)
	// The session finishes while the first connection is down.
	f.mu.Lock()
	f.sessionBusy = false
	f.messages = []map[string]any{
		{"id": "m0", "role": "user", "parts": []any{}},
		{"id": "m1", "role": "assistant", "parts": []any{
			map[string]any{"type": "text", "data": map[string]any{"text": "recovered result"}},
		}},
	}
	f.mu.Unlock()

	var events []Event
	res, err := (&Crush{}).RunSession(context.Background(), &RunSpec{
		Prompt: "p", Cwd: t.TempDir(), Env: os.Environ(),
	}, func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "recovered result" {
		t.Fatalf("result = %+v (final state must be recovered from the session)", res)
	}
	sawFinal := false
	for _, ev := range events {
		if ev.Kind == EventText && ev.Text == "recovered result" {
			sawFinal = true
		}
	}
	if !sawFinal {
		t.Error("recovered text was not emitted to the stream")
	}
}
