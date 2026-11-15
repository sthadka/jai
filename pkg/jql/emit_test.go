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
			sqlite: "CAST(px_impact_score AS REAL) > 5",
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
