package query

import (
	"path/filepath"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open DB: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestExecute_Basic(t *testing.T) {
	database := openTestDB(t)
	cfg := &config.Config{}

	// Insert test data.
	issue := &db.Issue{Key: "TEST-1", Project: "TEST", Summary: "Test issue", RawJSON: "{}"}
	if err := database.UpsertIssue(issue, nil); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	engine := New(database, cfg)
	results, err := engine.Execute("SELECT key, summary FROM issues")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if results.Count != 1 {
		t.Errorf("expected 1 row, got %d", results.Count)
	}
	if results.Columns[0] != "key" {
		t.Errorf("expected first column 'key', got %s", results.Columns[0])
	}
}

func TestExecute_TemplateVars(t *testing.T) {
	database := openTestDB(t)
	cfg := &config.Config{Me: "me@example.com"}

	engine := New(database, cfg)
	// Just verify template substitution doesn't crash with SQL using {{me}}.
	_, err := engine.Execute("SELECT '{{me}}' as me")
	if err != nil {
		t.Fatalf("Execute with template: %v", err)
	}
}

func TestTable_Empty(t *testing.T) {
	r := &Results{Columns: []string{"key"}, Rows: nil, Count: 0}
	out := r.Table()
	if out != "(no results)\n" {
		t.Errorf("expected '(no results)\\n', got %q", out)
	}
}

func TestTable_Format(t *testing.T) {
	r := &Results{
		Columns: []string{"key", "summary"},
		Rows:    [][]interface{}{{"TEST-1", "My issue"}},
		Count:   1,
	}
	out := r.Table()
	if out == "" {
		t.Error("expected non-empty table output")
	}
}

func TestJSONBytes(t *testing.T) {
	r := &Results{
		Columns: []string{"key"},
		Rows:    [][]interface{}{{"TEST-1"}},
		Count:   1,
	}
	data, err := r.JSONBytes()
	if err != nil {
		t.Fatalf("JSONBytes: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty JSON")
	}
}
