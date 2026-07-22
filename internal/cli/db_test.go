package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

// setupDBTestGlobals opens a temp DB, points g.cfg.DB.Path at it, and
// registers cleanup. Unlike setupTestGlobals (get_test.go), db.go's
// commands read g.cfg.DB.Path directly, so g.cfg must be populated.
func setupDBTestGlobals(t *testing.T) string {
	t.Helper()
	dbFile := filepath.Join(t.TempDir(), "test.db")

	database, err := db.Open(dbFile)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	g.db = database
	g.cfg = &config.Config{DB: config.DBConfig{Path: dbFile}}
	g.jsonOut = false
	t.Cleanup(func() {
		if g.db != nil {
			g.db.Close()
		}
		g.db = nil
		g.cfg = nil
		g.jsonOut = false
	})

	return dbFile
}

func TestDBPath(t *testing.T) {
	dbFile := setupDBTestGlobals(t)

	out := captureStdout(t, func() {
		if err := dbPathCmd.RunE(dbPathCmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	if strings.TrimSpace(out) != dbFile {
		t.Errorf("expected path %q, got %q", dbFile, strings.TrimSpace(out))
	}
}

func TestDBInfo(t *testing.T) {
	setupDBTestGlobals(t)

	for _, key := range []string{"TEST-1", "TEST-2"} {
		issue := &db.Issue{Key: key, Project: "TEST", Summary: "s", RawJSON: "{}"}
		if err := g.db.UpsertIssue(issue, nil); err != nil {
			t.Fatalf("upsert issue: %v", err)
		}
	}
	if err := g.db.InsertChangelog(&db.ChangelogEntry{
		ID: "1", IssueKey: "TEST-1", Field: "status", ToString: "Done",
	}); err != nil {
		t.Fatalf("insert changelog: %v", err)
	}

	g.jsonOut = true
	out := captureStdout(t, func() {
		if err := dbInfoCmd.RunE(dbInfoCmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	var result struct {
		OK   bool `json:"ok"`
		Data struct {
			Issues           int `json:"issues"`
			Changelogs       int `json:"changelogs"`
			MigrationVersion int `json:"migration_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if result.Data.Issues != 2 {
		t.Errorf("expected 2 issues, got %d", result.Data.Issues)
	}
	if result.Data.Changelogs != 1 {
		t.Errorf("expected 1 changelog, got %d", result.Data.Changelogs)
	}
	if result.Data.MigrationVersion <= 0 {
		t.Errorf("expected positive migration version, got %d", result.Data.MigrationVersion)
	}
}

func TestDBReset(t *testing.T) {
	dbFile := setupDBTestGlobals(t)

	issue := &db.Issue{Key: "TEST-1", Project: "TEST", Summary: "s", RawJSON: "{}"}
	if err := g.db.UpsertIssue(issue, nil); err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	dbResetForce = true
	t.Cleanup(func() { dbResetForce = false })

	// db reset closes and reopens the DB itself; root.go's PersistentPreRunE
	// never opened it for this command, so drop our handle first to match.
	g.db.Close()
	g.db = nil

	captureStdout(t, func() {
		if err := dbResetCmd.RunE(dbResetCmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	reopened, err := db.Open(dbFile)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer reopened.Close()

	count, err := reopened.TotalIssueCount()
	if err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 issues after reset, got %d", count)
	}
}

func TestDBResetNoConfirm(t *testing.T) {
	dbFile := setupDBTestGlobals(t)

	issue := &db.Issue{Key: "TEST-1", Project: "TEST", Summary: "s", RawJSON: "{}"}
	if err := g.db.UpsertIssue(issue, nil); err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	g.db.Close()
	g.db = nil

	dbResetForce = false

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString("n\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	captureStdout(t, func() {
		if err := dbResetCmd.RunE(dbResetCmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	reopened, err := db.Open(dbFile)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer reopened.Close()

	count, err := reopened.TotalIssueCount()
	if err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if count != 1 {
		t.Errorf("expected DB untouched (1 issue), got %d", count)
	}
}
