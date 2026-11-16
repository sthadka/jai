package store

import "testing"

// TestUpsertIssueMovePreservesIdentity verifies that re-syncing an issue under a
// new key (a cross-project move) updates the existing id-keyed row in place,
// cascades the key change to child relations, records a historical alias, and
// leaves no duplicate or orphan.
func TestUpsertIssueMovePreservesIdentity(t *testing.T) {
	db := openTestDB(t)

	// Original issue in project RFE with a comment and a changelog entry.
	orig := &Issue{ID: "100", Key: "RFE-1", Project: "RFE", Summary: "Move me", RawJSON: `{"id":"100"}`}
	if err := db.UpsertIssue(orig, nil); err != nil {
		t.Fatalf("UpsertIssue orig: %v", err)
	}
	if err := db.UpsertComment(&Comment{ID: "c1", IssueKey: "RFE-1", Body: "hi"}); err != nil {
		t.Fatalf("UpsertComment: %v", err)
	}
	if err := db.InsertChangelog(&ChangelogEntry{ID: "g1", IssueKey: "RFE-1", Field: "status", ToString: "New"}); err != nil {
		t.Fatalf("InsertChangelog: %v", err)
	}

	// Same id, new key: the issue moved RFE-1 -> ROX-9.
	moved := &Issue{ID: "100", Key: "ROX-9", Project: "ROX", Summary: "Move me", RawJSON: `{"id":"100"}`}
	if err := db.UpsertIssue(moved, nil); err != nil {
		t.Fatalf("UpsertIssue moved: %v", err)
	}

	// Exactly one issues row, now under the new key.
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM issues WHERE id = '100'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row for id 100, got %d (duplicate not collapsed)", count)
	}
	var key, project string
	if err := db.QueryRow(`SELECT key, project FROM issues WHERE id = '100'`).Scan(&key, &project); err != nil {
		t.Fatal(err)
	}
	if key != "ROX-9" || project != "ROX" {
		t.Fatalf("expected ROX-9/ROX, got %s/%s", key, project)
	}

	// The old key must no longer have a live row.
	var staleRows int
	if err := db.QueryRow(`SELECT count(*) FROM issues WHERE key = 'RFE-1'`).Scan(&staleRows); err != nil {
		t.Fatal(err)
	}
	if staleRows != 0 {
		t.Fatalf("stale row under old key RFE-1 not removed")
	}

	// Child relations cascaded to the new key, none left under the old key.
	comments, err := db.GetComments("ROX-9")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected comment to follow the move, got %d under ROX-9", len(comments))
	}
	var orphanComments, orphanChangelog int
	db.QueryRow(`SELECT count(*) FROM comments WHERE issue_key = 'RFE-1'`).Scan(&orphanComments)
	db.QueryRow(`SELECT count(*) FROM changelog WHERE issue_key = 'RFE-1'`).Scan(&orphanChangelog)
	if orphanComments != 0 || orphanChangelog != 0 {
		t.Fatalf("orphaned children under old key: comments=%d changelog=%d", orphanComments, orphanChangelog)
	}
	var movedChangelog int
	db.QueryRow(`SELECT count(*) FROM changelog WHERE issue_key = 'ROX-9'`).Scan(&movedChangelog)
	if movedChangelog != 1 {
		t.Fatalf("expected changelog to follow the move, got %d under ROX-9", movedChangelog)
	}

	// The old key is aliased to the stable id and resolves via GetIssue.
	var aliasID string
	if err := db.QueryRow(`SELECT id FROM issue_key_alias WHERE old_key = 'RFE-1'`).Scan(&aliasID); err != nil {
		t.Fatalf("alias for RFE-1 not recorded: %v", err)
	}
	if aliasID != "100" {
		t.Fatalf("alias points to id %s, want 100", aliasID)
	}
	got, err := db.GetIssue("RFE-1")
	if err != nil {
		t.Fatalf("GetIssue(old key): %v", err)
	}
	if got == nil || got["key"] != "ROX-9" {
		t.Fatalf("GetIssue(RFE-1) did not resolve to ROX-9 via alias: %v", got)
	}
}

// TestUpsertIssueProjectRename verifies the same id-identity logic handles a bulk
// project-key rename: every issue in the project re-syncs under a new key prefix,
// its row updates in place, and old keys alias to the stable ids.
func TestUpsertIssueProjectRename(t *testing.T) {
	db := openTestDB(t)

	for i, id := range []string{"201", "202"} {
		oldKey := []string{"OLD-1", "OLD-2"}[i]
		if err := db.UpsertIssue(&Issue{ID: id, Key: oldKey, Project: "OLD", Summary: "x", RawJSON: "{}"}, nil); err != nil {
			t.Fatalf("seed %s: %v", oldKey, err)
		}
	}
	// Project OLD renamed to NEW: same ids, new keys.
	for i, id := range []string{"201", "202"} {
		newKey := []string{"NEW-1", "NEW-2"}[i]
		if err := db.UpsertIssue(&Issue{ID: id, Key: newKey, Project: "NEW", Summary: "x", RawJSON: "{}"}, nil); err != nil {
			t.Fatalf("rename %s: %v", newKey, err)
		}
	}

	var total int
	db.QueryRow(`SELECT count(*) FROM issues`).Scan(&total)
	if total != 2 {
		t.Fatalf("expected 2 issues after rename, got %d", total)
	}
	for _, old := range []string{"OLD-1", "OLD-2"} {
		got, err := db.GetIssue(old)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got["project"] != "NEW" {
			t.Fatalf("old key %s did not resolve to renamed project: %v", old, got)
		}
	}
}

// TestUpsertIssueIDLessRowsStayDistinct guards the partial unique index: issues
// without a Jira id (e.g. local-only fixtures) must not collapse into one row.
func TestUpsertIssueIDLessRowsStayDistinct(t *testing.T) {
	db := openTestDB(t)
	for _, k := range []string{"A-1", "A-2"} {
		if err := db.UpsertIssue(&Issue{Key: k, Project: "A", Summary: "x", RawJSON: "{}"}, nil); err != nil {
			t.Fatalf("upsert %s: %v", k, err)
		}
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM issues`).Scan(&n)
	if n != 2 {
		t.Fatalf("id-less rows collapsed: got %d, want 2", n)
	}
}

// TestDedupIssuesByID verifies the migration helper collapses pre-existing
// duplicate-id rows (undetected moves), keeping the most recently synced key and
// folding the stale duplicate's relations into it.
func TestDedupIssuesByID(t *testing.T) {
	db := openTestDB(t)

	// openTestDB already created idx_issues_id (migration 18), which would reject
	// the duplicate we need to construct. Drop it, build the pre-migration state,
	// run the helper, then assert — mirroring what migration 18 does on upgrade.
	if _, err := db.Exec(`DROP INDEX idx_issues_id`); err != nil {
		t.Fatalf("drop index: %v", err)
	}

	// Two rows share id 300: stale OLD-1 (older sync) and live NEW-9 (newer sync).
	mustExec(t, db, `INSERT INTO issues (id, key, project, summary, synced_at) VALUES
		('300','OLD-1','OLD','x','2026-01-01T00:00:00Z'),
		('300','NEW-9','NEW','x','2026-06-01T00:00:00Z')`)
	if err := db.UpsertComment(&Comment{ID: "dc1", IssueKey: "OLD-1", Body: "stale"}); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := dedupIssuesByID(tx); err != nil {
		tx.Rollback()
		t.Fatalf("dedupIssuesByID: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var rows int
	db.QueryRow(`SELECT count(*) FROM issues WHERE id = '300'`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("expected 1 row after dedup, got %d", rows)
	}
	var survivor string
	db.QueryRow(`SELECT key FROM issues WHERE id = '300'`).Scan(&survivor)
	if survivor != "NEW-9" {
		t.Fatalf("expected survivor NEW-9 (most recent sync), got %s", survivor)
	}
	// Stale comment folded onto the survivor; old key aliased.
	comments, _ := db.GetComments("NEW-9")
	if len(comments) != 1 {
		t.Fatalf("expected stale comment folded onto survivor, got %d", len(comments))
	}
	var aliasID string
	if err := db.QueryRow(`SELECT id FROM issue_key_alias WHERE old_key = 'OLD-1'`).Scan(&aliasID); err != nil {
		t.Fatalf("stale key not aliased: %v", err)
	}
}

func mustExec(t *testing.T, db *DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
