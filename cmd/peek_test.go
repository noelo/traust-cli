package cmd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPeekRunTail verifies a run snapshot shows the state and the LAST few
// journal entries (not the head).
func TestPeekRunTail(t *testing.T) {
	ws := t.TempDir()
	state := setupWorkerWorkspace(t, ws)

	runID := "20261021-180000-test-peeks"
	dir := filepath.Join(state.RunsDir(), runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var journal strings.Builder
	for i := 1; i <= 20; i++ {
		journal.WriteString(`{"ts":"t","kind":"event","event":{"kind":"text","text":"line ` + itoa(i) + `"}}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.jsonl"), []byte(journal.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// A result marks the run done.
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"run_id":"`+runID+`","skill":"secure-code-audit","exit_code":0,"usage":{"tokens_in":100,"tokens_out":50}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	info, err := loadRunInfo(dir)
	if err != nil || info == nil {
		t.Fatal(err)
	}
	out := peekRun(info)

	if !strings.Contains(out, "done") {
		t.Errorf("peek missing state: %q", out)
	}
	if !strings.Contains(out, "line 20") || !strings.Contains(out, "line 9") {
		t.Errorf("peek should show the journal tail: %q", out)
	}
	if strings.Contains(out, "line 8") {
		t.Errorf("peek shows more than the tail window: %q", out)
	}
}

// TestPeekFlowRecursesCurrentStage verifies a flow peek includes the
// current stage's snapshot.
func TestPeekFlowRecursesCurrentStage(t *testing.T) {
	ws := t.TempDir()
	state := setupWorkerWorkspace(t, ws)

	runID := "20261021-180100-test-peekf"
	runDir := filepath.Join(state.RunsDir(), runID)
	os.MkdirAll(runDir, 0o755)
	os.WriteFile(filepath.Join(runDir, "stream.jsonl"), []byte(`{"ts":"t","kind":"event","event":{"kind":"text","text":"stage activity"}}`+"\n"), 0o644)
	os.WriteFile(filepath.Join(runDir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)
	os.WriteFile(filepath.Join(runDir, "options.json"), []byte(`{"skill":"triage"}`), 0o644)

	flowID := "20261021-180200-flow-peek"
	flowDir := filepath.Join(flowsDir(ws), flowID)
	os.MkdirAll(flowDir, 0o755)
	os.WriteFile(filepath.Join(flowDir, "pipeline.json"), []byte(`{"id":"`+flowID+`","flow":"audit-triage","target":"https://github.com/example/repo","state":"running","stages":[`+
		`{"name":"audit","skill":"secure-code-audit","run_id":"x1","state":"done"},`+
		`{"name":"triage","skill":"triage","run_id":"`+runID+`","state":"running"}]}`), 0o644)

	m, err := loadFlowManifest(flowDir)
	if err != nil {
		t.Fatal(err)
	}
	out := peekFlow(m, state.RunsDir())

	for _, want := range []string{"✓ audit", "▸ triage", "stage activity"} {
		if !strings.Contains(out, want) {
			t.Errorf("flow peek missing %q: %q", want, out)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
