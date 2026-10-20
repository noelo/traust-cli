package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Journal is the on-disk record of one run, written under
// <workspace>/cli/runs/<id>/:
//
//	spec.json    — the run specification, written before launch
//	stream.jsonl — every raw harness output line plus normalized events
//	result.json  — the outcome, written at the end
type Journal struct {
	Dir string

	stream *os.File
}

// Spec is the run specification recorded before launch.
type Spec struct {
	RunID                string   `json:"run_id"`
	Skill                string   `json:"skill"`
	Args                 []string `json:"args,omitempty"`
	Harness              string   `json:"harness"`
	Model                string   `json:"model"`
	RegistryRole         string   `json:"registry_role,omitempty"`
	FloorModel           string   `json:"floor_model,omitempty"`
	ModelWarning         string   `json:"model_warning,omitempty"`
	BudgetCap            int64    `json:"budget_cap,omitempty"`
	BudgetOverride       string   `json:"budget_override,omitempty"`
	UnsafeFullPermission bool     `json:"unsafe_full_permission,omitempty"`
	TraustRef            string   `json:"traust_ref"`
	TraustSHA            string   `json:"traust_sha"`
	StartedAt            string   `json:"started_at"`
}

// NewJournal creates the run directory and writes the spec.
func NewJournal(runsDir, runID string, spec Spec) (*Journal, error) {
	dir := filepath.Join(runsDir, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}
	spec.StartedAt = time.Now().UTC().Format(time.RFC3339)
	specData, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), specData, 0o644); err != nil {
		return nil, err
	}
	stream, err := os.OpenFile(filepath.Join(dir, "stream.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Journal{Dir: dir, stream: stream}, nil
}

// journalEntry is one stream record.
type journalEntry struct {
	TS    string `json:"ts"`
	Kind  string `json:"kind"` // "raw" | "event"
	Raw   string `json:"raw,omitempty"`
	Event *Event `json:"event,omitempty"`
}

// Event mirrors harness.Event for the journal; kept as a local type so the
// journal schema is stable against harness package refactors.
type Event struct {
	Kind      string  `json:"kind"`
	Text      string  `json:"text,omitempty"`
	Tool      string  `json:"tool,omitempty"`
	TokensIn  int64   `json:"tokens_in,omitempty"`
	TokensOut int64   `json:"tokens_out,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	IsError   bool    `json:"is_error,omitempty"`
}

// Raw records one harness output line verbatim.
func (j *Journal) Raw(line string) {
	j.write(journalEntry{TS: nowRFC(), Kind: "raw", Raw: line})
}

// Event records one normalized event.
func (j *Journal) Event(kind, text, tool string, tokensIn, tokensOut int64, cost float64, isError bool) {
	j.write(journalEntry{
		TS: nowRFC(), Kind: "event",
		Event: &Event{Kind: kind, Text: text, Tool: tool, TokensIn: tokensIn, TokensOut: tokensOut, CostUSD: cost, IsError: isError},
	})
}

// WriteResult records the run outcome.
func (j *Journal) WriteResult(v any) error {
	if j.stream != nil {
		j.stream.Close()
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(j.Dir, "result.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(j.Dir, "result.json"))
}

func (j *Journal) write(e journalEntry) {
	if j.stream == nil {
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	j.stream.Write(append(data, '\n'))
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339Nano) }
