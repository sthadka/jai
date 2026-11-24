package store

import (
	"strings"
	"testing"
)

// TestJQLHotPathIndexes verifies that migration 23's case-insensitive and
// composite expression indexes are selected by SQLite for the exact LOWER(...)
// and ORDER BY updated DESC shapes emitted by pkg/jql.
func TestJQLHotPathIndexes(t *testing.T) {
	db := openTestDB(t)

	// Seed a few rows so the planner has realistic table contents.
	for _, iss := range []*Issue{
		{ID: "1", Key: "ENG-1", Project: "ENG", Summary: "a", Assignee: "Alice", Reporter: "Bob", Status: "Open", StatusCategory: "To Do", Updated: "2026-01-01T00:00:00Z", RawJSON: "{}"},
		{ID: "2", Key: "ENG-2", Project: "ENG", Summary: "b", Assignee: "Bob", Reporter: "Alice", Status: "In Progress", StatusCategory: "In Progress", Updated: "2026-01-02T00:00:00Z", RawJSON: "{}"},
	} {
		if err := db.UpsertIssue(iss, nil); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name           string
		query          string
		wantIndex      string
		forbidTempSort bool
	}{
		{
			name:           "project lower + updated desc",
			query:          `SELECT key FROM issues WHERE lower(project) = lower('ENG') ORDER BY updated DESC`,
			wantIndex:      "idx_issues_project_lower_updated",
			forbidTempSort: true,
		},
		{
			name:           "assignee lower + updated desc",
			query:          `SELECT key FROM issues WHERE lower(assignee) = lower('Alice') ORDER BY updated DESC`,
			wantIndex:      "idx_issues_assignee_lower_updated",
			forbidTempSort: true,
		},
		{
			name:      "reporter lower",
			query:     `SELECT key FROM issues WHERE lower(reporter) = lower('Bob')`,
			wantIndex: "idx_issues_reporter_lower",
		},
		{
			name:      "status lower",
			query:     `SELECT key FROM issues WHERE lower(status) = lower('In Progress')`,
			wantIndex: "idx_issues_status_lower",
		},
		{
			name:      "status lower IN",
			query:     `SELECT key FROM issues WHERE lower(status) IN (lower('Open'), lower('In Progress'))`,
			wantIndex: "idx_issues_status_lower",
		},
		{
			name:      "status_category lower",
			query:     `SELECT key FROM issues WHERE lower(status_category) = lower('In Progress')`,
			wantIndex: "idx_issues_status_cat_lower",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := explainQueryPlan(t, db, tc.query)
			if !strings.Contains(plan, tc.wantIndex) {
				t.Errorf("query %q did not use %s; plan:\n%s", tc.query, tc.wantIndex, plan)
			}
			if tc.forbidTempSort && strings.Contains(plan, "TEMP B-TREE") {
				t.Errorf("query %q unexpectedly used TEMP B-TREE sort; plan:\n%s", tc.query, plan)
			}
		})
	}
}

// TestMigration24IndexesAndFTSTrigger verifies migration 24's composite/covering
// indexes, removal of idx_cl_tracked, and scoped issues_fts_update trigger.
func TestMigration24IndexesAndFTSTrigger(t *testing.T) {
	db := openTestDB(t)

	for _, iss := range []*Issue{
		{ID: "1", Key: "ENG-1", Project: "ENG", Type: "Bug", Priority: "High", Summary: "alpha bug", Status: "Open", Updated: "2026-01-01T00:00:00Z", RawJSON: "{}"},
		{ID: "2", Key: "ENG-2", Project: "ENG", Type: "Story", Priority: "Low", Summary: "beta story", Status: "Done", Updated: "2026-01-02T00:00:00Z", RawJSON: "{}"},
	} {
		if err := db.UpsertIssue(iss, nil); err != nil {
			t.Fatal(err)
		}
	}

	// idx_cl_tracked must be dropped.
	var trackedCount int
	if err := db.ReadDB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_cl_tracked'`).Scan(&trackedCount); err != nil {
		t.Fatal(err)
	}
	if trackedCount != 0 {
		t.Errorf("idx_cl_tracked still exists after migration 24")
	}

	cases := []struct {
		name           string
		query          string
		wantIndex      string
		forbidTempSort bool
	}{
		{
			name:           "type+priority lower + updated desc",
			query:          `SELECT key FROM issues WHERE lower(type) = 'bug' AND lower(priority) = 'high' ORDER BY updated DESC`,
			wantIndex:      "idx_issues_type_lower_prio",
			forbidTempSort: true,
		},
		{
			name:           "project lower + status + updated desc",
			query:          `SELECT key FROM issues WHERE lower(project) = 'eng' AND status = 'Open' ORDER BY updated DESC`,
			wantIndex:      "idx_issues_project_status_updated",
			forbidTempSort: true,
		},
		{
			name:      "changelog field+issue_key+changed_at",
			query:     `SELECT issue_key, changed_at FROM changelog WHERE field = 'Sprint' ORDER BY issue_key, changed_at`,
			wantIndex: "idx_changelog_field_issue_time",
		},
		{
			name:           "changelog per-issue timeline desc",
			query:          `SELECT id FROM changelog WHERE issue_key = 'ENG-1' ORDER BY changed_at DESC, id DESC`,
			wantIndex:      "idx_changelog_issue_time_desc",
			forbidTempSort: true,
		},
		{
			name:           "comments per-issue timeline desc",
			query:          `SELECT id FROM comments WHERE issue_key = 'ENG-1' ORDER BY created DESC, id DESC`,
			wantIndex:      "idx_comments_issue_created_desc",
			forbidTempSort: true,
		},
		{
			name:      "fact_field_interval project scoped",
			query:     `SELECT issue_key FROM fact_field_interval WHERE generation = 1 AND field = 'status' AND project = 'ENG' ORDER BY issue_key, seq`,
			wantIndex: "idx_ffi_gen_field_proj",
		},
		{
			name:      "fact_issue_membership project scoped",
			query:     `SELECT issue_key FROM fact_issue_membership WHERE generation = 1 AND dimension = 'sprint' AND project = 'ENG' ORDER BY issue_key, seq`,
			wantIndex: "idx_fim_gen_dim_proj",
		},
		{
			name:      "issue_links by link_type",
			query:     `SELECT issue_key, linked_key FROM issue_links WHERE link_type = 'Blocks'`,
			wantIndex: "idx_issue_links_type",
		},
		{
			name:      "pending_changes by issue_key",
			query:     `SELECT id FROM pending_changes WHERE issue_key = 'ENG-1'`,
			wantIndex: "idx_pending_changes_issue_key",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := explainQueryPlan(t, db, tc.query)
			if !strings.Contains(plan, tc.wantIndex) {
				t.Errorf("query %q did not use %s; plan:\n%s", tc.query, tc.wantIndex, plan)
			}
			if tc.forbidTempSort && strings.Contains(plan, "TEMP B-TREE") {
				t.Errorf("query %q unexpectedly used TEMP B-TREE sort; plan:\n%s", tc.query, plan)
			}
		})
	}

	// Verify scoped FTS trigger still updates FTS when summary changes and remains intact after metadata update.
	if err := db.MarkChangelogSynced([]string{"ENG-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE issues SET summary = 'gamma renamed' WHERE key = 'ENG-1'`); err != nil {
		t.Fatal(err)
	}
	var matchKey string
	if err := db.ReadDB().QueryRow(`SELECT key FROM issues_fts WHERE issues_fts MATCH 'gamma'`).Scan(&matchKey); err != nil || matchKey != "ENG-1" {
		t.Fatalf("FTS lookup after summary update = (%q, %v), want ENG-1", matchKey, err)
	}
	var oldCount int
	if err := db.ReadDB().QueryRow(`SELECT COUNT(*) FROM issues_fts WHERE issues_fts MATCH 'alpha'`).Scan(&oldCount); err != nil || oldCount != 0 {
		t.Fatalf("stale FTS match count = (%d, %v), want 0", oldCount, err)
	}
}
