package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// DB file should exist.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db file not created: %v", err)
	}
}

func TestMigrations(t *testing.T) {
	db := openTestDB(t)

	version, err := db.currentVersion()
	if err != nil {
		t.Fatalf("currentVersion: %v", err)
	}
	if version != 4 {
		t.Errorf("expected version 4, got %d", version)
	}
}

func TestUpsertIssue(t *testing.T) {
	db := openTestDB(t)

	issue := &Issue{
		Key:     "TEST-1",
		Project: "TEST",
		Summary: "Test issue",
		RawJSON: `{"key":"TEST-1"}`,
	}

	if err := db.UpsertIssue(issue, nil); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	got, err := db.GetIssue("TEST-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got == nil {
		t.Fatal("GetIssue returned nil")
	}
	if got["key"] != "TEST-1" {
		t.Errorf("expected key TEST-1, got %v", got["key"])
	}
}

func TestUpsertComment(t *testing.T) {
	db := openTestDB(t)

	// Insert parent issue first (FK constraint).
	issue := &Issue{Key: "TEST-1", Project: "TEST", Summary: "Issue", RawJSON: "{}"}
	_ = db.UpsertIssue(issue, nil)

	c := &Comment{
		ID:       "c1",
		IssueKey: "TEST-1",
		Author:   "Jane",
		Body:     "Hello",
		Created:  "2026-01-01T00:00:00Z",
		Updated:  "2026-01-01T00:00:00Z",
	}
	if err := db.UpsertComment(c); err != nil {
		t.Fatalf("UpsertComment: %v", err)
	}

	comments, err := db.GetComments("TEST-1")
	if err != nil {
		t.Fatalf("GetComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Author != "Jane" {
		t.Errorf("expected author Jane, got %s", comments[0].Author)
	}
}

func TestSyncMeta(t *testing.T) {
	db := openTestDB(t)

	if err := db.UpdateSyncMeta("TEST", 1.5, 10, 10, ""); err != nil {
		t.Fatalf("UpdateSyncMeta: %v", err)
	}

	meta, err := db.GetSyncMeta("TEST")
	if err != nil {
		t.Fatalf("GetSyncMeta: %v", err)
	}
	if !meta.LastSyncTime.Valid {
		t.Error("expected LastSyncTime to be set")
	}
}

func TestEnsureColumn(t *testing.T) {
	db := openTestDB(t)

	if err := db.EnsureColumn("custom_field", "TEXT"); err != nil {
		t.Fatalf("EnsureColumn: %v", err)
	}
	// Second call should be idempotent.
	if err := db.EnsureColumn("custom_field", "TEXT"); err != nil {
		t.Fatalf("EnsureColumn (idempotent): %v", err)
	}
}

func TestFieldMap(t *testing.T) {
	db := openTestDB(t)

	f := &FieldMapping{
		JiraID:   "customfield_12345",
		JiraName: "Custom Team Field",
		Name:     "team",
		Type:     "option",
		IsCustom: true,
	}
	if err := db.UpsertFieldMapping(f); err != nil {
		t.Fatalf("UpsertFieldMapping: %v", err)
	}

	m, err := db.FieldMapByJiraID()
	if err != nil {
		t.Fatalf("FieldMapByJiraID: %v", err)
	}
	got, ok := m["customfield_12345"]
	if !ok {
		t.Fatal("field not found in map")
	}
	if got.Name != "team" {
		t.Errorf("expected name 'team', got %s", got.Name)
	}
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
