package feeds

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/traust-security/traust-cli/internal/bootstrap"
)

// testState creates a minimal workspace with a bin directory for test scripts.
func testState(t *testing.T) (*bootstrap.State, string) {
	t.Helper()
	ws := t.TempDir()
	binDir := filepath.Join(ws, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	state := &bootstrap.State{
		Workspace: ws,
	}
	return state, binDir
}

// writeScript writes a bash stub that simulates grype or osv-scanner behavior.
func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "#!/usr/bin/env bash\n" + body
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

func hasBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

func TestUpdateGrype_Current(t *testing.T) {
	hasBash(t)
	state, binDir := testState(t)
	// grype db check exits 0 → current
	writeScript(t, binDir, "grype", `
case "$1 $2" in
	"db check") exit 0 ;;
	"db update") echo "should not be called" >&2; exit 1 ;;
esac
`)
	var logs []string
	log := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	result := Update(context.Background(), state, 120*time.Hour, log)
	if result.GrypeStatus != "current" {
		t.Errorf("expected 'current', got %q", result.GrypeStatus)
	}
}

func TestUpdateGrype_StaleSuccess(t *testing.T) {
	hasBash(t)
	state, binDir := testState(t)
	// grype db check exits 100 → stale; db update exits 0 → success
	writeScript(t, binDir, "grype", `
case "$1 $2" in
	"db check") exit 100 ;;
	"db update") exit 0 ;;
esac
`)
	var logs []string
	log := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	result := Update(context.Background(), state, 120*time.Hour, log)
	if result.GrypeStatus != "updated" {
		t.Errorf("expected 'updated', got %q", result.GrypeStatus)
	}
}

func TestUpdateGrype_StaleFailure(t *testing.T) {
	hasBash(t)
	state, binDir := testState(t)
	// grype db check exits 100 → stale; db update exits 1 → fail open
	writeScript(t, binDir, "grype", `
case "$1 $2" in
	"db check") exit 100 ;;
	"db update") exit 1 ;;
esac
`)
	var logs []string
	log := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	result := Update(context.Background(), state, 120*time.Hour, log)
	if result.GrypeStatus != "update failed; using baked DB" {
		t.Errorf("expected fail-open status, got %q", result.GrypeStatus)
	}
}

func TestUpdateGrype_NotInstalled(t *testing.T) {
	state, _ := testState(t)
	// No grype binary in bin dir
	var logs []string
	log := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	result := Update(context.Background(), state, 120*time.Hour, log)
	if result.GrypeStatus != "not installed" {
		t.Errorf("expected 'not installed', got %q", result.GrypeStatus)
	}
}

func TestUpdateOSV_NotInstalled(t *testing.T) {
	state, _ := testState(t)
	var logs []string
	log := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	result := Update(context.Background(), state, 120*time.Hour, log)
	if result.OsvStatus != "not installed" {
		t.Errorf("expected 'not installed', got %q", result.OsvStatus)
	}
}

func TestUpdateOSV_FailedWithBakedDBs(t *testing.T) {
	hasBash(t)
	state, binDir := testState(t)
	osvDBDir := filepath.Join(state.Workspace, "osv-db")

	// Bake a pre-existing DB with an old mtime.
	goDir := filepath.Join(osvDBDir, "osv-scanner", "Go")
	if err := os.MkdirAll(goDir, 0755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(goDir, "all.zip")
	if err := os.WriteFile(dbPath, []byte("fake-baked"), 0644); err != nil {
		t.Fatal(err)
	}
	// Set mtime to the past so it's clearly not refreshed.
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(dbPath, past, past)

	// osv-scanner that exits 1 and doesn't touch any files — simulates
	// a download failure with baked DBs already present.
	writeScript(t, binDir, "osv-scanner", `exit 1`)
	t.Setenv("OSV_SCANNER_LOCAL_DB_CACHE_DIRECTORY", osvDBDir)

	var logs []string
	log := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	result := Update(context.Background(), state, 120*time.Hour, log)

	// Must NOT report "refreshed" — the DB was not updated.
	if strings.Contains(result.OsvStatus, "refreshed") {
		t.Errorf("falsely reported refresh with unchanged baked DB, got %q", result.OsvStatus)
	}
	// Should report baked DBs available.
	if !strings.Contains(result.OsvStatus, "baked") && !strings.Contains(result.OsvStatus, "unchanged") {
		t.Errorf("expected baked/unchanged status, got %q", result.OsvStatus)
	}
}

func TestUpdateOSV_ActualRefresh(t *testing.T) {
	hasBash(t)
	state, binDir := testState(t)
	osvDBDir := filepath.Join(state.Workspace, "osv-db")

	// Bake a pre-existing DB with an old mtime.
	goDir := filepath.Join(osvDBDir, "osv-scanner", "Go")
	if err := os.MkdirAll(goDir, 0755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(goDir, "all.zip")
	if err := os.WriteFile(dbPath, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(dbPath, past, past)

	// osv-scanner that actually rewrites the DB file (simulates successful download).
	writeScript(t, binDir, "osv-scanner", `
db="$OSV_SCANNER_LOCAL_DB_CACHE_DIRECTORY/osv-scanner/Go/all.zip"
echo "refreshed-content" > "$db"
exit 1
`)
	t.Setenv("OSV_SCANNER_LOCAL_DB_CACHE_DIRECTORY", osvDBDir)

	var logs []string
	log := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	result := Update(context.Background(), state, 120*time.Hour, log)

	// Must report refreshed — the DB mtime changed.
	if !strings.Contains(result.OsvStatus, "refreshed") {
		t.Errorf("expected refreshed status, got %q", result.OsvStatus)
	}
}

func TestUpdateOSV_NoDatabasesAtAll(t *testing.T) {
	hasBash(t)
	state, binDir := testState(t)

	// osv-scanner that exits 1 and writes nothing
	writeScript(t, binDir, "osv-scanner", `exit 1`)
	// Point to empty dir
	osvDBDir := filepath.Join(state.Workspace, "osv-db-empty")
	os.MkdirAll(osvDBDir, 0755)
	t.Setenv("OSV_SCANNER_LOCAL_DB_CACHE_DIRECTORY", osvDBDir)

	var logs []string
	log := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	result := Update(context.Background(), state, 120*time.Hour, log)
	if !strings.Contains(result.OsvStatus, "no databases") {
		t.Errorf("expected no-databases status, got %q", result.OsvStatus)
	}
}

func TestUpdateGrype_ContextTimeout(t *testing.T) {
	hasBash(t)
	state, binDir := testState(t)
	// grype db check hangs forever — context deadline should kill it
	writeScript(t, binDir, "grype", `sleep 300`)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var logs []string
	log := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

	start := time.Now()
	result := Update(ctx, state, 120*time.Hour, log)
	elapsed := time.Since(start)

	// Should return quickly (context cancelled), not hang for 300s
	if elapsed > 5*time.Second {
		t.Errorf("update took %v; expected context to cancel it promptly", elapsed)
	}
	// Grype should report a failure, not success
	if result.GrypeStatus == "current" || result.GrypeStatus == "updated" {
		t.Errorf("expected non-success status for timed-out grype, got %q", result.GrypeStatus)
	}
}

func TestUpdateOSV_ContextTimeout(t *testing.T) {
	hasBash(t)
	state, binDir := testState(t)
	// osv-scanner hangs forever
	writeScript(t, binDir, "osv-scanner", `sleep 300`)
	osvDBDir := filepath.Join(state.Workspace, "osv-db-timeout")
	os.MkdirAll(osvDBDir, 0755)
	t.Setenv("OSV_SCANNER_LOCAL_DB_CACHE_DIRECTORY", osvDBDir)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var logs []string
	log := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

	start := time.Now()
	result := Update(ctx, state, 120*time.Hour, log)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("update took %v; expected context to cancel it promptly", elapsed)
	}
	if result.OsvStatus == "refreshed" {
		t.Errorf("expected non-success status for timed-out osv, got %q", result.OsvStatus)
	}
}
