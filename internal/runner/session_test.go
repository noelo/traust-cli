package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/traust-security/traust-cli/internal/catalog"
	"github.com/traust-security/traust-cli/internal/harness"
)

// fakeServeCrush runs a minimal crush-server v1 API on a Unix socket and
// points the crush adapter at it via TRAUST_CLI_CRUSH_SERVER_URL.
func fakeServeCrush(t *testing.T, script string) {
	t.Helper()
	mux := http.NewServeMux()
	var posted atomic.Bool
	var runID atomic.Value

	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"id": "ws-1"})
	})
	mux.HandleFunc("POST /v1/workspaces/{id}/sessions", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"id": "sess-1"})
	})
	mux.HandleFunc("POST /v1/workspaces/{id}/current-session", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /v1/workspaces/{id}/config/model", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /v1/workspaces/{id}/agent/update", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /v1/workspaces/{id}/agent", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RunID string `json:"run_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		runID.Store(body.RunID)
		posted.Store(true)
		w.WriteHeader(202)
	})
	mux.HandleFunc("POST /v1/workspaces/{id}/agent/sessions/{sid}/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	mux.HandleFunc("GET /v1/workspaces/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		for !posted.Load() {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(script, "{RUN_ID}", runID.Load().(string)))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})

	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	t.Setenv("TRAUST_CLI_CRUSH_SERVER_URL", "unix://"+ln.Addr().String())
}

const happyServeScript = `{"type":"message","payload":{"type":"updated","payload":{"id":"m1","role":"assistant","parts":[{"type":"tool_call","data":{"name":"bash","input":"{\"command\":\"ls\"}"}}]}}}
data: {"type":"session","payload":{"type":"updated","payload":{"id":"sess-1","prompt_tokens":120,"completion_tokens":60,"cost":1.5}}}
data: {"type":"message","payload":{"type":"updated","payload":{"id":"m1","role":"assistant","parts":[{"type":"tool_call","data":{"name":"bash","input":"{\"command\":\"ls\"}"}},{"type":"text","data":{"text":"audit complete"}}]}}}
data: {"type":"run_complete","payload":{"type":"updated","payload":{"session_id":"sess-1","run_id":"{RUN_ID}","text":"audit complete"}}}`

func TestRunSessionModeHappyPath(t *testing.T) {
	fakeServeCrush(t, happyServeScript)
	// A fake `crush models` so the registry floor model resolves and the
	// model-check pre-flight passes.
	bin := filepath.Join(t.TempDir(), "crush")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'PriceTag/floor-model-x\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRAUST_CLI_CRUSH_BIN", bin)

	state := testState(t)
	client := &fakeAPI{}
	sink := &recSink{}

	// A registry role maps the skill to the floor model.
	skill := testSkill()
	skill.Name = "vuln-scan"
	skill.Standalone = &catalog.StandaloneInfo{Role: "vuln-scan", MinModelClass: "opus"}

	res, err := Run(context.Background(), Options{
		State: state, API: client, Harness: &harness.Crush{},
		Skill: skill, Args: []string{"https://github.com/org/repo"},
		DeclareSpend: true,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != ExitOK {
		t.Fatalf("exit = %d (%+v)", res.ExitCode, res)
	}
	if res.ResultText != "audit complete" {
		t.Errorf("result text = %q", res.ResultText)
	}
	if res.Usage.TokensIn != 120 || res.Usage.TokensOut != 60 || res.Usage.CostUSD != 1.5 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if len(client.spendRows) != 1 || client.spendRows[0].TokensOut != 60 {
		t.Errorf("spend rows = %+v (session usage should be declared)", client.spendRows)
	}
	var sawTool, sawText bool
	for _, ev := range sink.events {
		if ev.Kind == harness.EventToolCall && ev.Tool == "bash" {
			sawTool = true
		}
		if ev.Kind == harness.EventText {
			sawText = true
		}
	}
	if !sawTool || !sawText {
		t.Errorf("live events missing: tool=%v text=%v", sawTool, sawText)
	}
	if res.HarnessExit != 0 {
		t.Errorf("harness exit = %d", res.HarnessExit)
	}
}

func TestRunSessionModeNeedsInput(t *testing.T) {
	fakeServeCrush(t, `{"type":"permission_request","payload":{"type":"updated","payload":{"session_id":"sess-1","tool_name":"write","description":"/etc/shadow"}}}
data: {"type":"run_complete","payload":{"type":"updated","payload":{"session_id":"sess-1","run_id":"{RUN_ID}","cancelled":true}}}`)
	state := testState(t)

	skill := testSkill()
	skill.Standalone = &catalog.StandaloneInfo{MinModelClass: "opus"}

	res, err := Run(context.Background(), Options{
		State: state, API: &fakeAPI{}, Harness: &harness.Crush{},
		Skill: skill, DeclareSpend: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != ExitNeedsInput || !res.NeedsInput {
		t.Fatalf("exit = %d, needsInput = %v", res.ExitCode, res.NeedsInput)
	}
}

func TestRunSessionModeRunError(t *testing.T) {
	fakeServeCrush(t, `{"type":"run_complete","payload":{"type":"updated","payload":{"session_id":"sess-1","run_id":"{RUN_ID}","error":"provider 401"}}}`)
	state := testState(t)

	skill := testSkill()
	skill.Standalone = &catalog.StandaloneInfo{MinModelClass: "opus"}

	res, err := Run(context.Background(), Options{
		State: state, API: &fakeAPI{}, Harness: &harness.Crush{},
		Skill: skill, DeclareSpend: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != ExitError || res.HarnessError != "provider 401" {
		t.Fatalf("exit = %d, harnessError = %q", res.ExitCode, res.HarnessError)
	}
}
