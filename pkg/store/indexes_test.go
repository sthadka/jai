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
