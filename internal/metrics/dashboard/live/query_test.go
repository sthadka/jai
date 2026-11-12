package live

import (
	"regexp"
	"strings"
	"testing"
)

// newTestWhitelist mirrors a single-project active generation.
func newTestWhitelist() *Whitelist { return NewWhitelist([]string{"ROX"}) }

// TestBuildRejectsNonWhitelistedMeasure proves an unknown measure is rejected
// (never reaches SQL).
func TestBuildRejectsNonWhitelistedMeasure(t *testing.T) {
	w := newTestWhitelist()
	if _, err := w.Build(QueryParams{Measure: "cycle_seconds; DROP TABLE x"}); err == nil {
		t.Fatal("expected rejection for non-whitelisted measure")
	}
	if _, err := w.Build(QueryParams{Measure: "secret_salary"}); err == nil {
		t.Fatal("expected rejection for unknown measure")
	}
}

// TestBuildRejectsNonWhitelistedColumn proves an unknown slice/segment dimension
// (i.e. an arbitrary column name) is rejected.
func TestBuildRejectsNonWhitelistedColumn(t *testing.T) {
	w := newTestWhitelist()
	if _, err := w.Build(QueryParams{Slice: "f.resolution) UNION SELECT 1 --"}); err == nil {
		t.Fatal("expected rejection for non-whitelisted slice")
	}
	if _, err := w.Build(QueryParams{Segment: "assignee_salary"}); err == nil {
		t.Fatal("expected rejection for non-whitelisted segment")
	}
}

// TestBuildRejectsSQLSmuggledViaParams proves injection attempts smuggled through
// value params (project, dates, grain, cohort) are rejected, not interpolated.
func TestBuildRejectsSQLSmuggledViaParams(t *testing.T) {
	w := newTestWhitelist()
	cases := []QueryParams{
		{Project: "ROX'; DROP TABLE jai.fact_issue_flow; --"},
		{Project: "' OR '1'='1"},
		{From: "2024-01-01'); DELETE FROM x; --"},
		{To: "not-a-date"},
		{Grain: "month); ATTACH 'evil.db' AS e; --"},
		{Cohort: "all' OR 1=1 --"},
	}
	for i, c := range cases {
		if _, err := w.Build(c); err == nil {
			t.Fatalf("case %d: expected rejection for smuggled SQL %+v", i, c)
		}
	}
}

// TestBuildOnlyEmitsSelect proves every produced query is a single read-only
// SELECT with no write/DDL/attach verbs reachable — there is no non-SELECT path.
func TestBuildOnlyEmitsSelect(t *testing.T) {
	w := newTestWhitelist()
	// Exercise the full whitelist cross-product.
	for measure := range measures {
		for slice := range dimensions {
			for seg := range dimensions {
				sql, err := w.Build(QueryParams{
					Measure: measure, Slice: slice, Segment: seg,
					Grain: "week", Project: "ROX", Cohort: "resolved",
					From: "2024-01-01", To: "2025-01-01",
				})
				if err != nil {
					t.Fatalf("valid combo rejected (%s/%s/%s): %v", measure, slice, seg, err)
				}
				if !strings.HasPrefix(strings.TrimSpace(sql), "SELECT") {
					t.Fatalf("query does not start with SELECT: %s", sql)
				}
				up := strings.ToUpper(sql)
				for _, forbidden := range []string{"INSERT", "UPDATE", "DELETE", "DROP", "ATTACH", "PRAGMA", "COPY", "CREATE", "ALTER"} {
					// Word-boundary match so a column like "created" is not a false hit.
					if regexp.MustCompile(`\b` + forbidden + `\b`).MatchString(up) {
						t.Fatalf("query contains forbidden verb %q: %s", forbidden, sql)
					}
				}
				if strings.Contains(sql, ";") {
					t.Fatalf("query contains a statement separator: %s", sql)
				}
			}
		}
	}
}

// TestBuildAcceptsValidTemplate proves a valid request yields a well-formed
// templated SELECT carrying the expected generation filter and aggregations.
func TestBuildAcceptsValidTemplate(t *testing.T) {
	w := newTestWhitelist()
	sql, err := w.Build(QueryParams{Measure: "lead_time", Slice: "priority", Cohort: "delivered"})
	if err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for _, want := range []string{
		"f.generation = getvariable('g')",
		"list_contains(getvariable('done_res')",
		"fact_field_interval",
		"quantile_cont(f.lead_seconds / 86400.0, 0.95)",
		"GROUP BY slice, segment",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("template missing %q:\n%s", want, sql)
		}
	}
}

// TestBuildUnknownProjectRejected proves projects outside the active generation
// are rejected even though project is a value param.
func TestBuildUnknownProjectRejected(t *testing.T) {
	w := newTestWhitelist()
	if _, err := w.Build(QueryParams{Project: "ENG"}); err == nil {
		t.Fatal("expected rejection for project not in active generation")
	}
	if _, err := w.Build(QueryParams{Project: "all"}); err != nil {
		t.Fatalf("'all' project should be accepted: %v", err)
	}
}

// TestBuildDateWindowCohortBasis proves the date window filters on the cohort's
// time basis: resolution time for delivered/resolved, but CREATION time for the
// open cohort (open issues have resolved IS NULL, so a resolved-based window would
// wipe them). Regression guard for empty open-cohort panels under a date range.
func TestBuildDateWindowCohortBasis(t *testing.T) {
	w := newTestWhitelist()
	delivered, err := w.Build(QueryParams{Measure: "cycle_time", Cohort: "delivered", From: "2025-01-01", To: "2025-04-01"})
	if err != nil {
		t.Fatalf("delivered build: %v", err)
	}
	if !strings.Contains(delivered, "f.resolved >= DATE '2025-01-01'") || !strings.Contains(delivered, "f.resolved < DATE '2025-04-01'") {
		t.Fatalf("delivered cohort should window on f.resolved:\n%s", delivered)
	}
	open, err := w.Build(QueryParams{Measure: "aged_open", Cohort: "open", From: "2025-01-01", To: "2025-04-01"})
	if err != nil {
		t.Fatalf("open build: %v", err)
	}
	if !strings.Contains(open, "f.created >= DATE '2025-01-01'") || !strings.Contains(open, "f.created < DATE '2025-04-01'") {
		t.Fatalf("open cohort should window on f.created:\n%s", open)
	}
	if strings.Contains(open, "f.resolved >= DATE") {
		t.Fatalf("open cohort must not window on f.resolved:\n%s", open)
	}
}

// TestBuildEventTimeDimOpenFallback proves event-time interval/membership dims
// fall back to the currently-open interval for open issues (resolved IS NULL), so
// the open cohort gets current values instead of an empty join. Delivered path
// (entered_at <= f.resolved) is preserved, so reconciliation is unaffected.
func TestBuildEventTimeDimOpenFallback(t *testing.T) {
	w := newTestWhitelist()
	sql, err := w.Build(QueryParams{Measure: "aged_open", Slice: "priority", Cohort: "open"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "entered_at <= f.resolved") {
		t.Fatalf("delivered interval path missing (reconciliation guard):\n%s", sql)
	}
	if !strings.Contains(sql, "f.resolved IS NULL AND") || !strings.Contains(sql, ".exited_at IS NULL))") {
		t.Fatalf("open-issue current-interval fallback missing:\n%s", sql)
	}
}

// TestTeamDimensionJoinsIssues proves the Team slice/segment is served from a
// current-snapshot join on jai.issues (Team is not event-time tracked).
func TestTeamDimensionJoinsIssues(t *testing.T) {
	w := newTestWhitelist()
	sql, err := w.Build(QueryParams{Measure: "throughput", Slice: "team", Cohort: "delivered"})
	if err != nil {
		t.Fatalf("team slice build: %v", err)
	}
	if !strings.Contains(sql, "JOIN jai.issues") || !strings.Contains(sql, ".team") {
		t.Fatalf("team dimension should join jai.issues on the team column:\n%s", sql)
	}
}

// TestBuildIssuesDrill proves the issue drill narrows to the clicked slice value
// (as an escaped literal), joins jai.issues for the summary, selects issue_key,
// and stays a single SELECT — the same whitelist guarantees as the aggregate.
func TestBuildIssuesDrill(t *testing.T) {
	w := newTestWhitelist()
	sql, err := w.BuildIssues(QueryParams{Measure: "cycle_time", Slice: "priority", Cohort: "delivered"}, "P2-Medium", "", 500)
	if err != nil {
		t.Fatalf("drill build: %v", err)
	}
	for _, want := range []string{
		"f.issue_key AS issue_key",
		"JOIN jai.issues iss ON iss.key = f.issue_key",
		"= 'P2-Medium'",
		"LIMIT 500",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("drill SQL missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "INSERT") || strings.Contains(strings.ToUpper(sql), "UPDATE") || strings.Contains(strings.ToUpper(sql), "DELETE") {
		t.Fatalf("drill must be read-only SELECT:\n%s", sql)
	}
}

// TestBuildIssuesEscapesSliceValue proves a slice value carrying a quote is
// escaped into a literal, never breaking out of the string (no injection).
func TestBuildIssuesEscapesSliceValue(t *testing.T) {
	w := newTestWhitelist()
	sql, err := w.BuildIssues(QueryParams{Measure: "throughput", Slice: "team", Cohort: "all"}, "Ops' OR 1=1 --", "", 10)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "= 'Ops'' OR 1=1 --'") {
		t.Fatalf("slice value not escaped as a literal:\n%s", sql)
	}
}

// TestBuildIssuesRejectsUnknownMeasure proves drill tokens are whitelisted too.
func TestBuildIssuesRejectsUnknownMeasure(t *testing.T) {
	w := newTestWhitelist()
	if _, err := w.BuildIssues(QueryParams{Measure: "evil", Slice: "team"}, "x", "", 10); err == nil {
		t.Fatal("expected rejection for unknown measure in drill")
	}
}
