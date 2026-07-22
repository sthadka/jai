package db

import (
	"testing"
)

func TestInsertChangelogBatch(t *testing.T) {
	db := openTestDB(t)

	entries := []*ChangelogEntry{
		{ID: "1_0", IssueKey: "TEST-1", Author: "Alice", Field: "status", FieldType: "jira", FromString: "New", ToString: "In Progress", ChangedAt: "2026-06-01T10:00:00Z"},
		{ID: "1_1", IssueKey: "TEST-1", Author: "Alice", Field: "assignee", FieldType: "jira", ToString: "Bob", ChangedAt: "2026-06-01T10:00:00Z"},
		{ID: "2_0", IssueKey: "TEST-2", Author: "Bob", Field: "status", FieldType: "jira", FromString: "New", ToString: "Done", ChangedAt: "2026-06-02T12:00:00Z"},
	}

	if err := db.InsertChangelogBatch(entries); err != nil {
		t.Fatalf("InsertChangelogBatch: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM changelog").Scan(&count); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 rows, got %d", count)
	}
}

func TestInsertChangelogBatch_Duplicates(t *testing.T) {
	db := openTestDB(t)

	entries := []*ChangelogEntry{
		{ID: "1_0", IssueKey: "TEST-1", Author: "Alice", Field: "status", ToString: "Done", ChangedAt: "2026-06-01T10:00:00Z"},
		{ID: "2_0", IssueKey: "TEST-2", Author: "Bob", Field: "status", ToString: "Done", ChangedAt: "2026-06-02T12:00:00Z"},
	}
	if err := db.InsertChangelogBatch(entries); err != nil {
		t.Fatalf("first batch: %v", err)
	}

	overlap := []*ChangelogEntry{
		{ID: "1_0", IssueKey: "TEST-1", Author: "Alice", Field: "status", ToString: "Done", ChangedAt: "2026-06-01T10:00:00Z"},
		{ID: "3_0", IssueKey: "TEST-3", Author: "Carol", Field: "priority", ToString: "High", ChangedAt: "2026-06-03T08:00:00Z"},
	}
	if err := db.InsertChangelogBatch(overlap); err != nil {
		t.Fatalf("overlapping batch: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM changelog").Scan(&count); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 rows (duplicate ignored), got %d", count)
	}
}

func TestInsertChangelogBatch_Empty(t *testing.T) {
	db := openTestDB(t)
	if err := db.InsertChangelogBatch(nil); err != nil {
		t.Fatalf("empty batch should succeed: %v", err)
	}
}

func TestGetIssueIDToKeyMap(t *testing.T) {
	db := openTestDB(t)

	// Insert issues with raw_json containing numeric IDs.
	_, err := db.Exec(`INSERT INTO issues (key, project, summary, raw_json) VALUES (?, ?, '', ?)`,
		"TEST-1", "TEST", `{"id":"10042","key":"TEST-1","fields":{}}`)
	if err != nil {
		t.Fatalf("inserting issue: %v", err)
	}
	_, err = db.Exec(`INSERT INTO issues (key, project, summary, raw_json) VALUES (?, ?, '', ?)`,
		"TEST-2", "TEST", `{"id":"10043","key":"TEST-2","fields":{}}`)
	if err != nil {
		t.Fatalf("inserting issue: %v", err)
	}
	// Issue without ID in raw_json (old sync data).
	_, err = db.Exec(`INSERT INTO issues (key, project, summary, raw_json) VALUES (?, ?, '', ?)`,
		"TEST-3", "TEST", `{"key":"TEST-3","fields":{}}`)
	if err != nil {
		t.Fatalf("inserting issue: %v", err)
	}

	m, err := db.GetIssueIDToKeyMap([]string{"TEST-1", "TEST-2", "TEST-3"})
	if err != nil {
		t.Fatalf("GetIssueIDToKeyMap: %v", err)
	}

	if m["10042"] != "TEST-1" {
		t.Errorf("expected 10042->TEST-1, got %q", m["10042"])
	}
	if m["10043"] != "TEST-2" {
		t.Errorf("expected 10043->TEST-2, got %q", m["10043"])
	}
	if _, ok := m["TEST-3"]; ok {
		t.Error("TEST-3 should not be in map (no ID in raw_json)")
	}
	if len(m) != 2 {
		t.Errorf("expected 2 entries, got %d", len(m))
	}
}
