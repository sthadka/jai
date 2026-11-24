package jql

import (
	"strings"
	"testing"
)

// TestEmitOperatorTable exercises each operator-mapping row (design §8.3) for
// both dialects, asserting the dialect-specific leaf spellings.
func TestEmitOperatorTable(t *testing.T) {
	cases := []struct {
		name   string
		jql    string
		duck   string
		sqlite string
	}{
		{
			name:   "eq",
			jql:    `project = ROX`,
			duck:   "lower(project) = lower('ROX')",
			sqlite: "lower(project) = lower('ROX')",
		},
		{
			name:   "neq excludes empty",
			jql:    `project != ROX`,
			duck:   "lower(COALESCE(project,'')) NOT IN ('', lower('ROX'))",
			sqlite: "lower(COALESCE(project,'')) NOT IN ('', lower('ROX'))",
		},
		{
			name:   "in",
			jql:    `status in (New, "To Do")`,
			duck:   "lower(status) IN (lower('New'),lower('To Do'))",
			sqlite: "lower(status) IN (lower('New'),lower('To Do'))",
		},
		{
			name:   "not in excludes empty",
			jql:    `status not in (New, Done)`,
			duck:   "lower(COALESCE(status,'')) NOT IN ('', lower('New'),lower('Done'))",
			sqlite: "lower(COALESCE(status,'')) NOT IN ('', lower('New'),lower('Done'))",
		},
		{
			name:   "contains",
			jql:    `summary ~ "login"`,
			duck:   "summary ILIKE '%login%'",
			sqlite: "summary LIKE '%login%'",
		},
		{
			name:   "not contains",
			jql:    `summary !~ "login"`,
			duck:   "summary NOT ILIKE '%login%'",
			sqlite: "summary NOT LIKE '%login%'",
		},
		{
			name:   "is empty",
			jql:    `resolved is EMPTY`,
			duck:   "(resolved IS NULL OR resolved = '')",
			sqlite: "(resolved IS NULL OR resolved = '')",
		},
		{
			name:   "is not empty",
			jql:    `resolved is not EMPTY`,
			duck:   "NOT (resolved IS NULL OR resolved = '')",
			sqlite: "NOT (resolved IS NULL OR resolved = '')",
		},
		{
			name:   "numeric compare",
			jql:    `px_impact_score > 5`,
			duck:   "TRY_CAST(px_impact_score AS DOUBLE) > 5",
			sqlite: "px_impact_score > 5",
		},
		{
			name:   "canonical key equality",
			jql:    `key = eng-123`,
			duck:   "key = UPPER('eng-123')",
			sqlite: "key = UPPER('eng-123')",
		},
		{
			name:   "canonical parent key IN",
			jql:    `parent in (eng-1, eng-2)`,
			duck:   "parent_key IN (UPPER('eng-1'),UPPER('eng-2'))",
			sqlite: "parent_key IN (UPPER('eng-1'),UPPER('eng-2'))",
		},
		{
			name:   "canonical epic key eq",
			jql:    `epic = eng-9`,
			duck:   "epic_key = UPPER('eng-9')",
			sqlite: "epic_key = UPPER('eng-9')",
		},
		{
			name:   "relative date",
			jql:    `created < -7d`,
			duck:   "TRY_CAST(created AS TIMESTAMP) < (CURRENT_TIMESTAMP - INTERVAL 7 DAY)",
			sqlite: "created < datetime('now','-7 day')",
		},
		{
			name:   "array equals",
			jql:    `labels = foo`,
			duck:   "list_contains(CAST(labels AS JSON)::VARCHAR[], 'foo')",
			sqlite: "EXISTS (SELECT 1 FROM json_each(labels) WHERE value = 'foo')",
		},
		{
			name:   "array in",
			jql:    `labels in (a, b)`,
			duck:   "(list_contains(CAST(labels AS JSON)::VARCHAR[], 'a') OR list_contains(CAST(labels AS JSON)::VARCHAR[], 'b'))",
			sqlite: "(EXISTS (SELECT 1 FROM json_each(labels) WHERE value = 'a') OR EXISTS (SELECT 1 FROM json_each(labels) WHERE value = 'b'))",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := mustTranslate(t, tc.jql, translateOpts(DuckDB))
			wantFragments(t, r.SQL, tc.duck)
			r = mustTranslate(t, tc.jql, translateOpts(SQLite))
			wantFragments(t, r.SQL, tc.sqlite)
		})
	}
}

// TestEmitNeqEmptySemantics pins the JQL `!=` / NOT IN rule: an empty or NULL
// column must never match (design §8.3).
func TestEmitNeqEmptySemantics(t *testing.T) {
	r := mustTranslate(t, `priority != High`, translateOpts(SQLite))
	wantFragments(t, r.SQL, "lower(COALESCE(priority,'')) NOT IN ('', lower('High'))")

	r = mustTranslate(t, `priority not in (High, Low)`, translateOpts(DuckDB))
	wantFragments(t, r.SQL, "lower(COALESCE(priority,'')) NOT IN ('', lower('High'),lower('Low'))")
}

// TestEmitLiteralEscaping verifies single quotes in literals are doubled and an
// injection attempt stays quoted data, never breaking out of the string.
func TestEmitLiteralEscaping(t *testing.T) {
	r := mustTranslate(t, `project = "O'Brien"`, translateOpts(SQLite))
	wantFragments(t, r.SQL, "lower(project) = lower('O''Brien')")

	inj := `summary ~ "x'; DROP TABLE issues; --"`
	r = mustTranslate(t, inj, translateOpts(DuckDB))
	wantFragments(t, r.SQL, "summary ILIKE '%x''; DROP TABLE issues; --%'")
	// The dangerous payload remains inside the quoted literal (doubled quote).
	if strings.Contains(r.SQL, "x';") {
		t.Errorf("single quote not escaped, injection possible: %s", r.SQL)
	}
}

// TestEmitDateAnchors checks now()/startOf* anchors resolve to concrete
// timestamps from the injected clock, deterministically.
func TestEmitDateAnchors(t *testing.T) {
	// 2026-11-13 is a Friday; start of its ISO week is Monday 2026-11-09.
	r := mustTranslate(t, `created < startOfDay()`, translateOpts(DuckDB))
	wantFragments(t, r.SQL, "TRY_CAST(created AS TIMESTAMP) < TIMESTAMP '2026-11-13 00:00:00'")

	r = mustTranslate(t, `created < startOfWeek()`, translateOpts(SQLite))
	wantFragments(t, r.SQL, "created < '2026-11-09 00:00:00'")

	r = mustTranslate(t, `created < startOfMonth()`, translateOpts(SQLite))
	wantFragments(t, r.SQL, "created < '2026-11-01 00:00:00'")
}

func TestEmitUseFTS5AndCustomRewrite(t *testing.T) {
	opts := translateOpts(SQLite)
	opts.UseFTS5 = true

	r := mustTranslate(t, `text ~ "login bug"`, opts)
	wantFragments(t, r.SQL, `rowid IN (SELECT rowid FROM issues_fts WHERE issues_fts MATCH '"login bug"')`)
	for _, c := range r.Columns {
		if c == "text" {
			t.Errorf("virtual 'text' alias should not be projected in SELECT columns: %v", r.Columns)
		}
	}

	r = mustTranslate(t, `summary ~ "auth"`, opts)
	wantFragments(t, r.SQL, `rowid IN (SELECT rowid FROM issues_fts WHERE issues_fts MATCH 'summary : "auth"')`)

	r = mustTranslate(t, `description !~ "legacy"`, opts)
	wantFragments(t, r.SQL, `rowid NOT IN (SELECT rowid FROM issues_fts WHERE issues_fts MATCH 'description : "legacy"')`)

	// Non-SQLite dialect ignores UseFTS5 and emits DuckDB ILIKE.
	dopts := translateOpts(DuckDB)
	dopts.UseFTS5 = true
	r = mustTranslate(t, `summary ~ "auth"`, dopts)
	wantFragments(t, r.SQL, `summary ILIKE '%auth%'`)

	// Custom FTSRewrite hook overrides default emission.
	custom := translateOpts(SQLite)
	custom.FTSRewrite = func(field, pat string) (string, bool) {
		return "custom_fts(" + field + "," + quote(pat) + ")", true
	}
	r = mustTranslate(t, `summary ~ "hello"`, custom)
	wantFragments(t, r.SQL, `custom_fts(summary,'hello')`)
	r = mustTranslate(t, `summary !~ "hello"`, custom)
	wantFragments(t, r.SQL, `NOT (custom_fts(summary,'hello'))`)
}

func TestEmitCommentsTextFallbackUsesIssueBlobs(t *testing.T) {
	opts := translateOpts(SQLite)
	opts.FieldMap["comment"] = FieldDef{Column: "comments_text", Kind: KindString}

	// UseFTS5: false must query issue_blobs instead of dropped issues.comments_text.
	r := mustTranslate(t, `comment ~ "needle"`, opts)
	wantFragments(t, r.SQL, `EXISTS (SELECT 1 FROM issue_blobs b WHERE b.issue_key = issues.key AND b.comments_text LIKE '%needle%')`)
	for _, c := range r.Columns {
		if c == "comments_text" {
			t.Errorf("comments_text should not be projected from issues: %v", r.Columns)
		}
	}

	r = mustTranslate(t, `comment !~ "needle"`, opts)
	wantFragments(t, r.SQL, `NOT EXISTS (SELECT 1 FROM issue_blobs b WHERE b.issue_key = issues.key AND b.comments_text LIKE '%needle%')`)

	// DuckDB dialect uses jai.issue_blobs + ILIKE.
	dopts := translateOpts(DuckDB)
	dopts.FieldMap["comment"] = FieldDef{Column: "comments_text", Kind: KindString}
	r = mustTranslate(t, `comment ~ "needle"`, dopts)
	wantFragments(t, r.SQL, `EXISTS (SELECT 1 FROM jai.issue_blobs b WHERE b.issue_key = jai.issues.key AND b.comments_text ILIKE '%needle%')`)

	// UseFTS5: true still uses issues_fts directly.
	opts.UseFTS5 = true
	r = mustTranslate(t, `comment ~ "needle"`, opts)
	wantFragments(t, r.SQL, `rowid IN (SELECT rowid FROM issues_fts WHERE issues_fts MATCH 'comments_text : "needle"')`)
}
