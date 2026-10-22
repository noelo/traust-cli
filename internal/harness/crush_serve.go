package harness

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// crushServerURL, when set, points the crush adapter at an already-running
// server (unix:///path/to/socket) instead of spawning one — used by tests
// and reserved for a future persistent-daemon mode.
func crushServerURL() string { return os.Getenv("TRAUST_CLI_CRUSH_SERVER_URL") }

// RunSession implements SessionRunner: drive the skill through `crush
// server` instead of `crush run`. The server gives the monitor everything
// the one-shot process could not: live structured events (thinking, text,
// tool calls), cumulative token usage, a clean cancel endpoint, and a
// RunID-correlated run_complete terminal signal. Workspaces live exactly as
// long as an attached SSE client, so the stream stays open for the whole
// run; permission and question requests cannot be answered unattended and
// fail closed (needs-input, exit 2).
func (c *Crush) RunSession(ctx context.Context, spec *RunSpec, emit func(Event)) (SessionResult, error) {
	srv, err := startCrushServer(ctx, c.Binary(), spec)
	if err != nil {
		return SessionResult{}, fmt.Errorf("%w: %v", ErrServerUnsupported, err)
	}
	defer srv.shutdown()

	clientID := randomID()
	wsID, err := srv.createWorkspace(ctx, spec.Cwd, clientID)
	if err != nil {
		return SessionResult{}, fmt.Errorf("create workspace: %w", err)
	}

	// Full-permission consent (init): skip the workspace's permission
	// prompts — the server equivalent of `crush --yolo`. The traust
	// skills cannot run unattended without it; without consent, permission
	// requests fail closed (needs-input) instead.
	if spec.UnsafeFullPermission {
		code, err := srv.call(ctx, http.MethodPost, "/v1/workspaces/"+wsID+"/permissions/skip", map[string]bool{"skip": true}, nil)
		if err != nil || code != http.StatusOK {
			return SessionResult{}, fmt.Errorf("set permissions skip: %d %v", code, err)
		}
	}

	sessID, err := srv.createSession(ctx, wsID)
	if err != nil {
		return SessionResult{}, fmt.Errorf("create session: %w", err)
	}

	// The SSE stream attaches the client; without it the workspace is torn
	// down and current-session would 409 ("client not attached").
	body, cancelSSE, err := srv.openEvents(ctx, wsID, clientID)
	if err != nil {
		return SessionResult{}, fmt.Errorf("open event stream: %w", err)
	}
	defer cancelSSE()

	if err := srv.setCurrentSession(ctx, wsID, clientID, sessID); err != nil {
		return SessionResult{}, fmt.Errorf("attach to session: %w", err)
	}

	if spec.Model != "" {
		if err := srv.overrideModel(ctx, c.Binary(), spec.Env, wsID, spec.Model); err != nil {
			return SessionResult{}, fmt.Errorf("override model: %w", err)
		}
	}

	runID := randomID()
	if err := srv.sendMessage(ctx, wsID, sessID, runID, spec.Prompt); err != nil {
		return SessionResult{}, fmt.Errorf("send prompt: %w", err)
	}

	return srv.readEvents(ctx, body, wsID, sessID, runID, emit)
}

// crushServer owns one `crush server` process (or borrows an external one).
type crushServer struct {
	client   *http.Client
	base     string
	cmd      *exec.Cmd
	sockDir  string
	external bool
}

// startCrushServer spawns `crush server` on a private Unix socket and waits
// for /v1/health. Any spawn or readiness failure maps to ErrServerUnsupported
// upstream so the runner can fall back to `crush run`.
func startCrushServer(ctx context.Context, bin string, spec *RunSpec) (*crushServer, error) {
	if url := crushServerURL(); url != "" {
		client, base, err := unixHTTPClient(url)
		if err != nil {
			return nil, err
		}
		return &crushServer{client: client, base: base, external: true}, nil
	}

	sockDir, err := os.MkdirTemp("", "traust-cli-crush-*")
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(sockDir, "crush.sock")
	cmd := exec.CommandContext(context.WithoutCancel(ctx), bin, "server", "--host", "unix://"+sock)
	cmd.Dir = spec.Cwd
	cmd.Env = spec.Env
	if spec.OutDir != "" {
		log, lerr := os.Create(filepath.Join(spec.OutDir, "crush-server.log"))
		if lerr == nil {
			cmd.Stderr = log
			cmd.Stdout = log
			defer log.Close()
		}
	}
	if err := cmd.Start(); err != nil {
		os.RemoveAll(sockDir)
		return nil, fmt.Errorf("start %s server: %w", bin, err)
	}

	client, base, err := unixHTTPClient("unix://" + sock)
	if err != nil {
		cmd.Process.Kill()
		os.RemoveAll(sockDir)
		return nil, err
	}
	srv := &crushServer{client: client, base: base, cmd: cmd, sockDir: sockDir}

	// Readiness: poll /v1/health.
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			srv.shutdown()
			return nil, ctx.Err()
		default:
		}
		if srv.healthy(ctx) {
			return srv, nil
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			os.RemoveAll(sockDir)
			return nil, fmt.Errorf("crush server exited during startup")
		}
		time.Sleep(250 * time.Millisecond)
	}
	srv.shutdown()
	return nil, fmt.Errorf("crush server did not become healthy within 25s")
}

// unixHTTPClient builds an HTTP client that dials a Unix socket (or a
// normal http(s) URL when the override is one).
func unixHTTPClient(url string) (*http.Client, string, error) {
	if !strings.HasPrefix(url, "unix://") {
		return &http.Client{Timeout: 60 * time.Second}, url, nil
	}
	sock := strings.TrimPrefix(url, "unix://")
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}
	return &http.Client{Transport: transport}, "http://crush.local", nil
}

func (s *crushServer) healthy(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, s.base+"/v1/health", nil)
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (s *crushServer) shutdown() {
	if s.external {
		return
	}
	// Ask nicely first so crush can flush its session store.
	cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodPost, s.base+"/v1/control", strings.NewReader(`{"command":"shutdown"}`))
	req.Header.Set("Content-Type", "application/json")
	if resp, err := s.client.Do(req); err == nil {
		resp.Body.Close()
	}
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	done := make(chan struct{})
	go func() { s.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		s.cmd.Process.Kill()
		<-done
	}
	os.RemoveAll(s.sockDir)
}

// call is a one-shot JSON request helper.
func (s *crushServer) call(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func (s *crushServer) createWorkspace(ctx context.Context, path, clientID string) (string, error) {
	var ws struct {
		ID string `json:"id"`
	}
	code, err := s.call(ctx, http.MethodPost, "/v1/workspaces", map[string]string{"path": path, "client_id": clientID}, &ws)
	if err != nil || code != http.StatusOK {
		return "", fmt.Errorf("POST /v1/workspaces: %d %v", code, err)
	}
	return ws.ID, nil
}

func (s *crushServer) createSession(ctx context.Context, wsID string) (string, error) {
	var sess struct {
		ID string `json:"id"`
	}
	code, err := s.call(ctx, http.MethodPost, "/v1/workspaces/"+wsID+"/sessions", map[string]string{}, &sess)
	if err != nil || code != http.StatusOK {
		return "", fmt.Errorf("POST sessions: %d %v", code, err)
	}
	return sess.ID, nil
}

// openEvents starts the SSE subscription. The returned closer must be
// called to detach (which releases the workspace).
func (s *crushServer) openEvents(ctx context.Context, wsID, clientID string) (io.ReadCloser, func(), error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.base+"/v1/workspaces/"+wsID+"/events?client_id="+clientID, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, nil, fmt.Errorf("GET events: %d", resp.StatusCode)
	}
	return resp.Body, func() { resp.Body.Close() }, nil
}

func (s *crushServer) setCurrentSession(ctx context.Context, wsID, clientID, sessID string) error {
	code, err := s.call(ctx, http.MethodPost,
		"/v1/workspaces/"+wsID+"/current-session?client_id="+clientID,
		map[string]string{"session_id": sessID}, nil)
	if err != nil || code != http.StatusOK && code != http.StatusConflict {
		return fmt.Errorf("POST current-session: %d %v", code, err)
	}
	return nil
}

// overrideModel maps a model id onto {provider, model} via `crush models`
// and sets it as the workspace's large model. Scope marshals as an int
// (ScopeWorkspace = 1); model_type as "large".
func (s *crushServer) overrideModel(ctx context.Context, bin string, env []string, wsID, model string) error {
	provider, modelID, err := resolveCrushProvider(ctx, bin, env, model)
	if err != nil {
		return err
	}
	body := map[string]any{
		"scope":      1,
		"model_type": "large",
		"model":      map[string]string{"provider": provider, "model": modelID},
	}
	if code, err := s.call(ctx, http.MethodPost, "/v1/workspaces/"+wsID+"/config/model", body, nil); err != nil || code != http.StatusOK {
		return fmt.Errorf("POST config/model: %d %v", code, err)
	}
	if code, err := s.call(ctx, http.MethodPost, "/v1/workspaces/"+wsID+"/agent/update", nil, nil); err != nil || code != http.StatusOK {
		return fmt.Errorf("POST agent/update: %d %v", code, err)
	}
	return nil
}

// resolveCrushProvider resolves a bare or prefixed model id to its provider
// using the `crush models` listing ("PriceTag/rits/zai-org/glm-5-3" style:
// the provider is the FIRST segment; the model id may itself contain
// slashes).
func resolveCrushProvider(ctx context.Context, bin string, env []string, model string) (string, string, error) {
	if provider, rest, ok := strings.Cut(model, "/"); ok && provider != "" && rest != "" {
		// Only treat as provider/model when the whole id appears in the
		// listing under that provider; otherwise fall through to listing
		// resolution (model ids contain slashes more often than not).
		if _, _, err := matchCrushModelListing(ctx, bin, env, provider+"/"+rest, model); err == nil {
			return provider, rest, nil
		}
	}
	line, _, err := matchCrushModelListing(ctx, bin, env, "", model)
	if err != nil {
		return "", "", err
	}
	provider, rest, _ := strings.Cut(line, "/")
	return provider, rest, nil
}

// matchCrushModelListing finds the full listing line for a model (exact or
// provider-prefixed match), running `crush models` to get the listing.
func matchCrushModelListing(ctx context.Context, bin string, env []string, wantPrefix, model string) (string, string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "models")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("crush models: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == model || strings.HasSuffix(line, "/"+model) {
			if wantPrefix != "" && !strings.HasPrefix(line, wantPrefix+"/") {
				continue
			}
			return line, line, nil
		}
	}
	return "", "", fmt.Errorf("model %q is not in `crush models`", model)
}

func (s *crushServer) sendMessage(ctx context.Context, wsID, sessID, runID, prompt string) error {
	code, err := s.call(ctx, http.MethodPost, "/v1/workspaces/"+wsID+"/agent", map[string]any{
		"session_id": sessID,
		"run_id":     runID,
		"prompt":     prompt,
	}, nil)
	if err != nil || code != http.StatusAccepted {
		return fmt.Errorf("POST agent: %d %v", code, err)
	}
	return nil
}

func (s *crushServer) cancelSession(ctx context.Context, wsID, sessID string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.call(cctx, http.MethodPost, "/v1/workspaces/"+wsID+"/agent/sessions/"+sessID+"/cancel", nil, nil)
}

// readEvents consumes the SSE stream until run_complete for our run (or
// context cancellation, which cancels the remote run first).
func (s *crushServer) readEvents(ctx context.Context, body io.Reader, wsID, sessID, runID string, emit func(Event)) (SessionResult, error) {
	result := SessionResult{}
	partState := map[string]string{}

	reader := bufio.NewReaderSize(body, 64*1024)
	for {
		if ctx.Err() != nil {
			s.cancelSession(ctx, wsID, sessID)
			result.Cancelled = true
			return result, nil
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if ctx.Err() != nil {
				s.cancelSession(ctx, wsID, sessID)
				result.Cancelled = true
				return result, nil
			}
			if errors.Is(err, io.EOF) {
				return result, fmt.Errorf("event stream ended without run_complete")
			}
			return result, fmt.Errorf("event stream: %w", err)
		}
		data, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data: ")
		if !ok || data == "" {
			continue
		}
		if done := s.handleSSE(data, sessID, runID, partState, emit, &result); done {
			return result, nil
		}
	}
}

// handleSSE maps one SSE data frame onto normalized events. Returns true
// when the terminal run_complete for our run arrived.
//
// Crush streams message parts by REPLACEMENT: every "message updated"
// frame carries the full parts array with parts grown in place (text
// fragments fill out, tool inputs stream in), and tool results arrive in
// separate role=tool messages. Parts are therefore tracked by
// msgid:partIndex key with their last-emitted content, and every change
// re-emits with the key so renderers update the line in place — live
// streaming text instead of first-fragment artifacts.
func (s *crushServer) handleSSE(data, sessID, runID string, partState map[string]string, emit func(Event), result *SessionResult) bool {
	var envelope struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		return false
	}
	var inner struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(envelope.Payload, &inner); err != nil {
		return false
	}

	switch envelope.Type {
	case "message":
		var msg struct {
			ID    string `json:"id"`
			Role  string `json:"role"`
			Parts []struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			} `json:"parts"`
		}
		if err := json.Unmarshal(inner.Payload, &msg); err != nil {
			return false
		}
		// user messages carry the prompt (already known); assistant and
		// tool messages carry the streamed parts. Tool results live in
		// role=tool messages — skipping them is why tool output vanished.
		if msg.Role == "user" {
			return false
		}
		for i, part := range msg.Parts {
			key := msg.ID + ":" + strconv.Itoa(i)
			switch part.Type {
			case "text":
				var p struct {
					Text   string `json:"text"`
					Hidden bool   `json:"hidden"`
				}
				if json.Unmarshal(part.Data, &p) != nil || p.Hidden || p.Text == "" || p.Text == partState[key] {
					break
				}
				partState[key] = p.Text
				emit(Event{Kind: EventText, Text: p.Text, Key: key})
			case "reasoning":
				// Thinking streams too; keyed updates keep it one live
				// line per block instead of a fragment per frame.
				var p struct {
					Thinking string `json:"thinking"`
				}
				if json.Unmarshal(part.Data, &p) != nil || p.Thinking == "" || p.Thinking == partState[key] {
					break
				}
				partState[key] = p.Thinking
				emit(Event{Kind: EventThinking, Text: p.Thinking, Key: key})
			case "tool_call":
				var p struct {
					ID       string `json:"id"`
					Name     string `json:"name"`
					Input    string `json:"input"`
					Finished bool   `json:"finished"`
				}
				if json.Unmarshal(part.Data, &p) != nil || p.Name == "" {
					break
				}
				display := p.Name + "\x00" + firstLine(p.Input, 160)
				if display == partState[key] {
					break
				}
				partState[key] = display
				name, input, _ := strings.Cut(display, "\x00")
				emit(Event{Kind: EventToolCall, Tool: name, Text: input, Key: key})
			case "tool_result":
				// The actual command/scanner output — the bulk of a run's
				// output. Truncated to keep the transcript readable; the
				// journal keeps what the harness sent.
				var p struct {
					ToolCallID string `json:"tool_call_id"`
					Name       string `json:"name"`
					Content    string `json:"content"`
					IsError    bool   `json:"is_error"`
				}
				if json.Unmarshal(part.Data, &p) != nil || p.Content == "" {
					break
				}
				display := truncateBlock(p.Content, 6, 400)
				if display == partState[key] {
					break
				}
				partState[key] = display
				emit(Event{Kind: EventToolResult, Tool: p.Name, Text: display, IsError: p.IsError, Key: key})
			}
		}
		return false

	case "session":
		var sess struct {
			PromptTokens     int64   `json:"prompt_tokens"`
			CompletionTokens int64   `json:"completion_tokens"`
			Cost             float64 `json:"cost"`
		}
		if err := json.Unmarshal(inner.Payload, &sess); err == nil {
			if sess.PromptTokens > 0 || sess.CompletionTokens > 0 || sess.Cost > 0 {
				emit(Event{Kind: EventUsage, TokensIn: sess.PromptTokens, TokensOut: sess.CompletionTokens, CostUSD: sess.Cost})
			}
		}

	case "permission_request", "question_batch_request":
		var req struct {
			ToolName     string `json:"tool_name"`
			Description  string `json:"description"`
			ConfirmTitle string `json:"confirm_title"`
		}
		_ = json.Unmarshal(inner.Payload, &req)
		what := req.ToolName
		if what == "" {
			what = req.ConfirmTitle
		}
		if what == "" {
			what = envelope.Type
		}
		emit(Event{Kind: EventNeedsInput, Tool: what, Text: req.Description})

	case "agent_event":
		var ev struct {
			Type  string `json:"type"`
			Error string `json:"error"`
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(inner.Payload, &ev); err == nil && ev.Type == "error" {
			if ev.RunID == "" || ev.RunID == runID {
				emit(Event{Kind: EventError, Text: ev.Error, IsError: true})
			}
		}

	case "run_complete":
		var rc struct {
			SessionID string `json:"session_id"`
			RunID     string `json:"run_id"`
			Text      string `json:"text"`
			Error     string `json:"error"`
			Cancelled bool   `json:"cancelled"`
		}
		if err := json.Unmarshal(inner.Payload, &rc); err != nil {
			return false
		}
		if rc.RunID != runID && !(rc.RunID == "" && rc.SessionID == sessID) {
			return false
		}
		result.Text = rc.Text
		result.Err = rc.Error
		result.Cancelled = rc.Cancelled
		return true
	}
	return false
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n-1] + "…"
	}
	return s
}

// truncateBlock clips a multi-line tool output to at most maxLines lines
// and maxChars characters, marking the cut.
func truncateBlock(s string, maxLines, maxChars int) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	cut := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		cut = true
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxChars {
		out = out[:maxChars-1]
		cut = true
	}
	if cut {
		out += "\n…"
	}
	return out
}

func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}
