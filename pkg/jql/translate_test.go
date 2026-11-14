package jql

import (
	"strings"
	"testing"
	"time"
)

// fixedNow is the deterministic clock used by emitter tests so that resolved
// now()/startOf* anchors produce stable timestamps.
var fixedNow = time.Date(2026, 11, 13, 15, 30, 0, 0, time.UTC)

// translateOpts builds Options mirroring the §8.9 resolver fixture (custom
// numeric field + a fake id resolver) for a given dialect.
func translateOpts(d Dialect) Options {
	return Options{
		Dialect: d,
		FieldMap: FieldMap{
			"px_impact_score": {Column: "px_impact_score", Kind: KindNumber},
			"PX_Impact_Score": {Column: "px_impact_score", Kind: KindNumber},
		},
		CurrentUser: "alice@example.com",
		Now:         fixedNow,
		IDResolver: func(field, id string) (string, bool, error) {
			if field == "Team" && id == "ec74d716-af36-4b3c-950f-f79213d08f71-1819" {
				return "ACS Docs", true, nil
			}
			return "", false, nil
		},
	}
}

// norm collapses runs of whitespace to a single space for whitespace-insensitive
// fragment comparison.
func norm(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// mustTranslate runs Translate and fails the test on error.
func mustTranslate(t *testing.T, jql string, opts Options) Result {
	t.Helper()
	r, err := Translate(jql, opts)
	if err != nil {
		t.Fatalf("Translate(%q): %v", jql, err)
	}
	return r
}

// wantFragments asserts every fragment appears in the whitespace-normalized SQL.
func wantFragments(t *testing.T, sql string, fragments ...string) {
	t.Helper()
	got := norm(sql)
	for _, f := range fragments {
		if !strings.Contains(got, norm(f)) {
			t.Errorf("SQL missing fragment %q\n  in: %s", f, got)
		}
	}
}

func TestTranslateExample89DuckDB(t *testing.T) {
	r := mustTranslate(t, example89, translateOpts(DuckDB))

	wantFragments(t, r.SQL,
		"FROM jai.issues",
		"lower(project) = lower('ROX')",
		"lower(type) = lower('Bug')",
		"lower(COALESCE(team,'')) NOT IN ('', lower('ACS Docs'))",
		"(lower(priority) IN (lower('Critical'),lower('Blocker')) OR TRY_CAST(px_impact_score AS DOUBLE) >= 10000)",
		"TRY_CAST(created AS TIMESTAMP) < (CURRENT_TIMESTAMP - INTERVAL 90 DAY)",
		"(resolved IS NULL OR resolved = '')",
		"lower(status) IN (lower('New'),lower('Backlog'),lower('Refinement'),lower('To Do'))",
	)

	// standardIssueTypes() was redundant and dropped: it never reaches the SQL.
	if strings.Contains(r.SQL, "standardIssueTypes") {
		t.Errorf("standardIssueTypes() should be dropped, got: %s", r.SQL)
	}

	// The referenced custom column is folded into the projection.
	if !containsStr(r.Columns, "px_impact_score") {
		t.Errorf("columns missing px_impact_score: %v", r.Columns)
	}
}

func TestTranslateExample89SQLite(t *testing.T) {
	r := mustTranslate(t, example89, translateOpts(SQLite))

	wantFragments(t, r.SQL,
		"FROM issues",
		"lower(project) = lower('ROX')",
		"lower(type) = lower('Bug')",
		"lower(COALESCE(team,'')) NOT IN ('', lower('ACS Docs'))",
		"(lower(priority) IN (lower('Critical'),lower('Blocker')) OR CAST(px_impact_score AS REAL) >= 10000)",
		"created < datetime('now','-90 day')",
		"(resolved IS NULL OR resolved = '')",
		"lower(status) IN (lower('New'),lower('Backlog'),lower('Refinement'),lower('To Do'))",
	)

	if strings.Contains(r.SQL, "INTERVAL") {
		t.Errorf("SQLite emit should not use INTERVAL: %s", r.SQL)
	}
}

func TestTranslateOrderBy(t *testing.T) {
	for _, d := range []Dialect{SQLite, DuckDB} {
		r := mustTranslate(t, `project = ROX ORDER BY created DESC, key`, translateOpts(d))
		wantFragments(t, r.SQL, "ORDER BY created DESC, key")
	}
}

func TestTranslateNoWhere(t *testing.T) {
	r := mustTranslate(t, `ORDER BY key`, translateOpts(SQLite))
	if strings.Contains(r.SQL, "WHERE") {
		t.Errorf("expected no WHERE clause: %s", r.SQL)
	}
	wantFragments(t, r.SQL, "SELECT key, type, priority, status, team, created, summary FROM issues", "ORDER BY key")
}

func TestTranslateSelectColumnsOverride(t *testing.T) {
	opts := translateOpts(DuckDB)
	opts.SelectColumns = []string{"key", "summary"}
	r := mustTranslate(t, `project = ROX`, opts)
	if len(r.Columns) != 2 || r.Columns[0] != "key" || r.Columns[1] != "summary" {
		t.Fatalf("columns = %v, want [key summary]", r.Columns)
	}
	wantFragments(t, r.SQL, "SELECT key, summary FROM jai.issues")
}

// TestTranslateBareMultiWordValue verifies an unquoted multi-word value is
// emitted as a single quoted SQL string literal.
func TestTranslateBareMultiWordValue(t *testing.T) {
	for _, d := range []Dialect{SQLite, DuckDB} {
		r := mustTranslate(t, `status = To Do`, translateOpts(d))
		wantFragments(t, r.SQL, "lower(status) = lower('To Do')")
	}
}

// TestTranslateBareGUIDStillResolves pins that a single-token unquoted GUID
// value still runs through the IDResolver (display-name substitution).
func TestTranslateBareGUIDStillResolves(t *testing.T) {
	r := mustTranslate(t, `Team = ec74d716-af36-4b3c-950f-f79213d08f71-1819`, translateOpts(DuckDB))
	wantFragments(t, r.SQL, "lower(team) = lower('ACS Docs')")
}

// TestTranslateCaseInsensitiveStrings pins that equality/IN over textual fields
// is case-insensitive (lower()-wrapped on both sides) so lowercase user input
// matches mixed-case stored values (design §8.3).
func TestTranslateCaseInsensitiveStrings(t *testing.T) {
	for _, d := range []Dialect{SQLite, DuckDB} {
		r := mustTranslate(t, `status = new`, translateOpts(d))
		wantFragments(t, r.SQL, "lower(status) = lower('new')")

		r = mustTranslate(t, `priority in (blocker, critical)`, translateOpts(d))
		wantFragments(t, r.SQL, "lower(priority) IN (lower('blocker'),lower('critical'))")
	}
}

// TestTranslateNonStringKindsStayExact pins that numeric, date, and JSON-array
// predicates are NOT wrapped in lower(): numbers/dates compare exactly and Jira
// labels/components remain case-sensitive (design §8.3).
func TestTranslateNonStringKindsStayExact(t *testing.T) {
	// Numeric: bare comparison, no lower().
	r := mustTranslate(t, `px_impact_score >= 10000`, translateOpts(SQLite))
	wantFragments(t, r.SQL, "CAST(px_impact_score AS REAL) >= 10000")
	if strings.Contains(norm(r.SQL), "lower(") {
		t.Errorf("numeric clause should not use lower(): %s", r.SQL)
	}

	// Date: relative-date comparison, no lower().
	r = mustTranslate(t, `created < -90d`, translateOpts(SQLite))
	wantFragments(t, r.SQL, "created < datetime('now','-90 day')")
	if strings.Contains(norm(r.SQL), "lower(") {
		t.Errorf("date clause should not use lower(): %s", r.SQL)
	}

	// Array: containment test, case-sensitive, no lower().
	r = mustTranslate(t, `labels in (x)`, translateOpts(SQLite))
	wantFragments(t, r.SQL, "EXISTS (SELECT 1 FROM json_each(labels) WHERE value = 'x')")
	if strings.Contains(norm(r.SQL), "lower(") {
		t.Errorf("array clause should not use lower(): %s", r.SQL)
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
