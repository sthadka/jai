package db

import (
	"fmt"
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

	// Insert issues with numeric IDs.
	_, err := db.Exec(`INSERT INTO issues (id, key, project, summary) VALUES (?, ?, ?, '')`,
		"10042", "TEST-1", "TEST")
	if err != nil {
		t.Fatalf("inserting issue: %v", err)
	}
	_, err = db.Exec(`INSERT INTO issues (id, key, project, summary) VALUES (?, ?, ?, '')`,
		"10043", "TEST-2", "TEST")
	if err != nil {
		t.Fatalf("inserting issue: %v", err)
	}
	// Issue without ID (old sync data).
	_, err = db.Exec(`INSERT INTO issues (key, project, summary) VALUES (?, ?, '')`,
		"TEST-3", "TEST")
	if err != nil {
		t.Fatalf("inserting issue: %v", err)
	}

	m, err := db.GetIssueIDToKeyMap()
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
		t.Error("TEST-3 should not be in map (no ID)")
	}
	if len(m) != 2 {
		t.Errorf("expected 2 entries, got %d", len(m))
	}
}

func TestMarkChangelogSynced(t *testing.T) {
	db := openTestDB(t)

	keys := []string{"TEST-1", "TEST-2", "TEST-3"}
	for _, k := range keys {
		if _, err := db.Exec(`INSERT INTO issues (key, project, summary) VALUES (?, 'TEST', '')`, k); err != nil {
			t.Fatalf("inserting issue %s: %v", k, err)
		}
	}

	if err := db.MarkChangelogSynced(keys); err != nil {
		t.Fatalf("MarkChangelogSynced: %v", err)
	}

	for _, k := range keys {
		var syncedAt *string
		if err := db.QueryRow(`SELECT changelog_synced_at FROM issues WHERE key = ?`, k).Scan(&syncedAt); err != nil {
			t.Fatalf("querying %s: %v", k, err)
		}
		if syncedAt == nil || *syncedAt == "" {
			t.Errorf("expected changelog_synced_at to be set for %s", k)
		}
	}
}

func TestMarkChangelogSynced_LargeBatch(t *testing.T) {
	db := openTestDB(t)

	const n = 600
	keys := make([]string, n)
	for i := range n {
		keys[i] = fmt.Sprintf("TEST-%d", i)
		if _, err := db.Exec(`INSERT INTO issues (key, project, summary) VALUES (?, 'TEST', '')`, keys[i]); err != nil {
			t.Fatalf("inserting issue %s: %v", keys[i], err)
		}
	}

	if err := db.MarkChangelogSynced(keys); err != nil {
		t.Fatalf("MarkChangelogSynced: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM issues WHERE changelog_synced_at IS NOT NULL`).Scan(&count); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != n {
		t.Errorf("expected %d issues stamped, got %d", n, count)
	}
}

func TestGetChangelogSyncCandidates_SkipsChecked(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.Exec(`INSERT INTO issues (key, project, summary, updated, changelog_synced_at)
		VALUES ('TEST-1', 'TEST', '', '2026-06-01T00:00:00Z', '2026-06-02T00:00:00Z')`); err != nil {
		t.Fatalf("inserting issue: %v", err)
	}

	keys, err := db.GetChangelogSyncCandidates(nil)
	if err != nil {
		t.Fatalf("GetChangelogSyncCandidates: %v", err)
	}
	for _, k := range keys {
		if k == "TEST-1" {
			t.Error("TEST-1 should not be a candidate (already synced after last update)")
		}
	}
}

func TestGetChangelogSyncCandidates_IncludesStale(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.Exec(`INSERT INTO issues (key, project, summary, updated, changelog_synced_at)
		VALUES ('TEST-1', 'TEST', '', '2026-06-02T00:00:00Z', '2026-06-01T00:00:00Z')`); err != nil {
		t.Fatalf("inserting issue: %v", err)
	}

	keys, err := db.GetChangelogSyncCandidates(nil)
	if err != nil {
		t.Fatalf("GetChangelogSyncCandidates: %v", err)
	}
	found := false
	for _, k := range keys {
		if k == "TEST-1" {
			found = true
		}
	}
	if !found {
		t.Error("TEST-1 should be a candidate (updated after last changelog sync)")
	}
}

func TestGetChangelogSyncCandidates_IncludesNeverSynced(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.Exec(`INSERT INTO issues (key, project, summary, updated)
		VALUES ('TEST-1', 'TEST', '', '2026-06-01T00:00:00Z')`); err != nil {
		t.Fatalf("inserting issue: %v", err)
	}

	keys, err := db.GetChangelogSyncCandidates(nil)
	if err != nil {
		t.Fatalf("GetChangelogSyncCandidates: %v", err)
	}
	found := false
	for _, k := range keys {
		if k == "TEST-1" {
			found = true
		}
	}
	if !found {
		t.Error("TEST-1 should be a candidate (never checked for changelogs)")
	}
}

func TestGetIssueIDToKeyMapForKeys(t *testing.T) {
	db := openTestDB(t)

	issues := []struct{ id, key string }{
		{"10042", "TEST-1"},
		{"10043", "TEST-2"},
		{"10044", "TEST-3"},
	}
	for _, iss := range issues {
		if _, err := db.Exec(`INSERT INTO issues (id, key, project, summary) VALUES (?, ?, 'TEST', '')`,
			iss.id, iss.key); err != nil {
			t.Fatalf("inserting issue %s: %v", iss.key, err)
		}
	}

	m, err := db.GetIssueIDToKeyMapForKeys([]string{"TEST-1", "TEST-2"})
	if err != nil {
		t.Fatalf("GetIssueIDToKeyMapForKeys: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m))
	}
	if m["10042"] != "TEST-1" {
		t.Errorf("expected 10042->TEST-1, got %q", m["10042"])
	}
	if m["10043"] != "TEST-2" {
		t.Errorf("expected 10043->TEST-2, got %q", m["10043"])
	}
	if _, ok := m["10044"]; ok {
		t.Error("TEST-3 should not be included (not requested)")
	}
}
