package live

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEngineLockdownBlocksFilesystem proves the session hardening applied in
// setup() seals the free-form SQL surface: enable_external_access=false (made
// permanent by lock_configuration) makes any filesystem read fail, while the
// already-ATTACHed jai.db stays fully queryable. This is the regression guard
// that the /explore/* path cannot escape to the host, yet still serves live data.
//
// A blocked statement errors inside DuckDB (to its stderr) and emits no result on
// stdout, so Engine.Query returns zero rows for it; a successful count(*) always
// returns exactly one row. That asymmetry is the deterministic proof the read was
// refused, and the engine's stderr confirms the reason is the filesystem lockdown.
func TestEngineLockdownBlocksFilesystem(t *testing.T) {
	dbPath := buildFixtureDB(t) // self-skips when duckdb is absent
	engine, err := NewEngine(EngineConfig{DBPath: dbPath, DoneResolutions: []string{"Done"}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })

	// A filesystem read of a path OUTSIDE the attached database is refused. (The
	// ATTACH whitelists jai.db's own path, so a non-attached file is the honest
	// probe.) count(*) over a successful read always yields one row, so zero rows
	// proves the whole statement errored before producing any output — i.e. the
	// hardened session blocked the file access.
	outside := filepath.Join(t.TempDir(), "outside.csv")
	if err := os.WriteFile(outside, []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatalf("write probe csv: %v", err)
	}
	rows, err := engine.Query("SELECT count(*) AS n FROM read_csv_auto('" + outside + "')")
	if err != nil {
		t.Fatalf("Query returned transport error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected read_csv_auto to be blocked (zero rows), got %+v", rows)
	}
	// Confirm the failure reason is the filesystem lockdown, not some unrelated
	// error. DuckDB writes the diagnostic to the engine's stderr just before the
	// end-of-query marker lands on stdout; poll briefly for the copy to land.
	if !waitForStderr(engine, "disabled by configuration") {
		t.Fatalf("expected a filesystem-disabled error in stderr, got: %q", engine.stderr.String())
	}

	// The attached jai.db is still readable: the lockdown happens AFTER ATTACH, so
	// legitimate dashboard queries keep working.
	rows, err = engine.Query("SELECT key FROM jai.issues LIMIT 1")
	if err != nil {
		t.Fatalf("attached-DB query failed after lockdown: %v", err)
	}
	if len(rows) == 0 || rows[0]["key"] == nil {
		t.Fatalf("expected a row from jai.issues, got %+v", rows)
	}
}

// waitForStderr polls the engine's stderr buffer for substr, giving the exec
// stderr-copy goroutine a moment to deliver DuckDB's diagnostic after a blocked
// statement.
func waitForStderr(engine *Engine, substr string) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(engine.stderr.String(), substr) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return strings.Contains(engine.stderr.String(), substr)
}
