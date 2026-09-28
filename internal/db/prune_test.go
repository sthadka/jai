package db

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
