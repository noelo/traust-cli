package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTailerReplayAndFollow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")

	if err := os.WriteFile(path, []byte("{\"kind\":\"raw\",\"raw\":\"one\"}\n{\"kind\":\"raw\",\"raw\":\"two\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tailer, err := NewTailer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer tailer.Close()

	lines, err := tailer.Read()
	if err != nil || len(lines) != 2 {
		t.Fatalf("replay = %v (%v), want 2 lines", lines, err)
	}

	// Nothing new.
	lines, err = tailer.Read()
	if err != nil || len(lines) != 0 {
		t.Fatalf("second read = %v (%v), want none", lines, err)
	}

	// Appends appear, including a trailing partial line.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{\"kind\":\"raw\",\"raw\":\"three\"}\n{\"kind\":\"raw\",\"raw\":\"par")
	f.Close()

	lines, err = tailer.Read()
	if err != nil || len(lines) != 1 || lines[0] != `{"kind":"raw","raw":"three"}` {
		t.Fatalf("follow = %v (%v)", lines, err)
	}

	// Completing the partial line makes it readable.
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("tial\"}\n")
	f.Close()
	lines, err = tailer.Read()
	if err != nil || len(lines) != 1 || lines[0] != `{"kind":"raw","raw":"partial"}` {
		t.Fatalf("partial follow = %v (%v)", lines, err)
	}
}

func TestParseEntry(t *testing.T) {
	e, err := ParseEntry([]byte(`{"ts":"2026","kind":"event","event":{"kind":"tool_result","tool":"bash","text":"out"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != "event" || e.Event == nil || e.Event.Kind != "tool_result" || e.Event.Tool != "bash" {
		t.Errorf("entry = %+v", e)
	}
	e, err = ParseEntry([]byte(`{"ts":"2026","kind":"raw","raw":"hello"}`))
	if err != nil || e.Raw != "hello" {
		t.Errorf("entry = %+v (%v)", e, err)
	}
	if _, err := ParseEntry([]byte(`not json`)); err == nil {
		t.Error("garbage should fail")
	}
}

// TestTailerSpin guards against a busy loop when the writer stalls: Read
// must return promptly with no data.
func TestTailerSpin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, nil, 0o644)
	tailer, err := NewTailer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer tailer.Close()
	start := time.Now()
	for i := 0; i < 5; i++ {
		lines, err := tailer.Read()
		if err != nil || len(lines) != 0 {
			t.Fatalf("read = %v (%v)", lines, err)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("empty reads took %s", elapsed)
	}
}
