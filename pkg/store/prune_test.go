package store

import "testing"

func TestPruneProjects(t *testing.T) {
	db := openTestDB(t)

	// Two issues in DROP, one in KEEP, each with a changelog entry and comment.
	for _, i := range []*Issue{
		{Key: "DROP-1", Project: "DROP", Summary: "a", RawJSON: "{}"},
		{Key: "DROP-2", Project: "DROP", Summary: "b", RawJSON: "{}"},
		{Key: "KEEP-1", Project: "KEEP", Summary: "c", RawJSON: "{}"},
	} {
		if err := db.UpsertIssue(i, nil); err != nil {
			t.Fatalf("UpsertIssue %s: %v", i.Key, err)
		}
	}
	for _, e := range []*ChangelogEntry{
		{ID: "d1", IssueKey: "DROP-1", Field: "status", ToString: "Done"},
		{ID: "k1", IssueKey: "KEEP-1", Field: "status", ToString: "Done"},
	} {
		if err := db.InsertChangelog(e); err != nil {
			t.Fatalf("InsertChangelog %s: %v", e.ID, err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO comments (id, issue_key, body) VALUES ('cd', 'DROP-2', 'x'), ('ck', 'KEEP-1', 'y')`,
	); err != nil {
		t.Fatalf("seeding comments: %v", err)
	}

	removed, err := db.PruneProjects([]string{"DROP"})
	if err != nil {
		t.Fatalf("PruneProjects: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	assertCount(t, db, "SELECT count(*) FROM issues", 1)
	assertCount(t, db, "SELECT count(*) FROM issues WHERE project = 'DROP'", 0)
	assertCount(t, db, "SELECT count(*) FROM changelog", 1)
	assertCount(t, db, "SELECT count(*) FROM changelog WHERE issue_key = 'DROP-1'", 0)
	assertCount(t, db, "SELECT count(*) FROM comments", 1)
	assertCount(t, db, "SELECT count(*) FROM comments WHERE issue_key = 'DROP-2'", 0)
	// FTS is maintained by the delete trigger — the pruned issue must be gone.
	assertCount(t, db, "SELECT count(*) FROM issues_fts WHERE key = 'DROP-1'", 0)
	assertCount(t, db, "SELECT count(*) FROM issues_fts WHERE key = 'KEEP-1'", 1)
}

func TestDeleteIssuesTx_CleansAllChildAndFactTables(t *testing.T) {
	db := openTestDB(t)

	for _, i := range []*Issue{
		{ID: "1001", Key: "DEL-1", Project: "DEL", Summary: "gone", RawJSON: "{}"},
		{ID: "1002", Key: "KEEP-1", Project: "KEEP", Summary: "stay", RawJSON: "{}"},
	} {
		if err := db.UpsertIssue(i, nil); err != nil {
			t.Fatalf("UpsertIssue %s: %v", i.Key, err)
		}
	}

	seedStmts := []string{
		`INSERT INTO changelog (id, issue_key, field, to_string) VALUES ('c1', 'DEL-1', 'status', 'Done'), ('c2', 'KEEP-1', 'status', 'Done')`,
		`INSERT INTO comments (id, issue_key, body) VALUES ('cm1', 'DEL-1', 'bye'), ('cm2', 'KEEP-1', 'hi')`,
		`INSERT INTO issue_links (issue_key, link_type, direction, linked_key) VALUES ('DEL-1', 'blocks', 'outward', 'KEEP-1'), ('KEEP-1', 'relates', 'inward', 'DEL-1')`,
		`INSERT INTO dev_links (issue_key, type, url) VALUES ('DEL-1', 'pr', 'https://x/1'), ('KEEP-1', 'pr', 'https://x/2')`,
		`INSERT INTO attachments (id, issue_key, filename) VALUES (1, 'DEL-1', 'a.txt'), (2, 'KEEP-1', 'b.txt')`,
		`INSERT INTO pending_changes (issue_key, operation, payload) VALUES ('DEL-1', 'update', '{}'), ('KEEP-1', 'update', '{}')`,
		`INSERT INTO issue_key_alias (old_key, id, changed_at) VALUES ('OLD-1', '1001', '2026-01-01T00:00:00Z'), ('OLD-2', '1002', '2026-01-01T00:00:00Z')`,
		`INSERT INTO fact_field_interval (generation, issue_key, project, field, seq, entered_at) VALUES (1, 'DEL-1', 'DEL', 'status', 0, '2026-01-01T00:00:00Z'), (1, 'KEEP-1', 'KEEP', 'status', 0, '2026-01-01T00:00:00Z')`,
		`INSERT INTO fact_issue_flow (generation, issue_key, project) VALUES (1, 'DEL-1', 'DEL'), (1, 'KEEP-1', 'KEEP')`,
		`INSERT INTO fact_issue_membership (generation, issue_key, project, dimension, seq, entered_at) VALUES (1, 'DEL-1', 'DEL', 'sprint', 0, '2026-01-01T00:00:00Z'), (1, 'KEEP-1', 'KEEP', 'sprint', 0, '2026-01-01T00:00:00Z')`,
	}
	for _, stmt := range seedStmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seeding (%s): %v", stmt, err)
		}
	}

	n, err := db.DeleteIssuesTx([]string{"DEL-1"})
	if err != nil {
		t.Fatalf("DeleteIssuesTx: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted = %d, want 1", n)
	}

	tables := []string{
		"issues", "changelog", "comments", "issue_links", "dev_links",
		"attachments", "pending_changes", "issue_key_alias",
		"fact_field_interval", "fact_issue_flow", "fact_issue_membership",
	}
	for _, tbl := range tables {
		assertCount(t, db, "SELECT count(*) FROM "+tbl, 1)
	}
	assertCount(t, db, "SELECT count(*) FROM issues_fts WHERE key = 'DEL-1'", 0)
	assertCount(t, db, "SELECT count(*) FROM issues_fts WHERE key = 'KEEP-1'", 1)
}

func TestPruneProjectsEmpty(t *testing.T) {
	db := openTestDB(t)
	removed, err := db.PruneProjects(nil)
	if err != nil {
		t.Fatalf("PruneProjects(nil): %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

func TestDeleteSyncMetadata(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(
		`INSERT INTO sync_metadata (project) VALUES ('keep'), ('gone1'), ('gone2')`,
	); err != nil {
		t.Fatalf("seeding sync_metadata: %v", err)
	}

	n, err := db.DeleteSyncMetadata([]string{"gone1", "gone2"})
	if err != nil {
		t.Fatalf("DeleteSyncMetadata: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2", n)
	}
	assertCount(t, db, "SELECT count(*) FROM sync_metadata", 1)
	assertCount(t, db, "SELECT count(*) FROM sync_metadata WHERE project = 'keep'", 1)
}

func assertCount(t *testing.T, db *DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if got != want {
		t.Errorf("%s = %d, want %d", query, got, want)
	}
}
