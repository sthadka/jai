package metrics

import (
	"testing"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

// snapshotService builds a service over an issues-only DB with NO fact generation, proving
// these metrics need no fact store.
func snapshotService(t *testing.T, pol *Policy) (*db.DB, *Service) {
	t.Helper()
	database, store := newFactStore(t)
	return database, NewService(database, store, pol, nil)
}

// upIssueA inserts an issue including an assignee (upIssue omits it).
func upIssueA(t *testing.T, database *db.DB, key, project, itype, status, category, created, resolved, resolution, assignee string) {
	t.Helper()
	iss := &db.Issue{
		Key: key, Project: project, Type: itype, Summary: key,
		Status: status, StatusCategory: category, Assignee: assignee,
		Created: created, Resolved: resolved, Resolution: resolution,
	}
	if err := database.UpsertIssue(iss, nil); err != nil {
		t.Fatalf("UpsertIssue %s: %v", key, err)
	}
}

func ratioConfig(cohort string) *Policy {
	return CompilePolicy(&config.MetricsConfig{
		StatusStages:    map[string][]string{"active": {"In Progress"}, "done": {"Closed"}},
		WIPStages:       []string{"active"},
		DoneResolutions: []string{"Done"},
		TypeClasses: map[string][]string{
			"bug":     {"Bug"},
			"feature": {"Story", "Feature"},
		},
		Ratio: &config.RatioConfig{Numerator: "bug", Denominator: "feature", Cohort: cohort},
	})
}

// All issues-only metrics return correct results on a database with no fact generation.
func TestSnapshot_NoFactsRequired(t *testing.T) {
	database, svc := snapshotService(t, factPolicy())
	upIssue(t, database, "N-1", "PN", "Story", "In Progress", "In Progress", day(5), "", "", false)
	upIssue(t, database, "N-2", "PN", "Story", "Closed", "Done", day(1), "2024-01-20T00:00:00Z", "Done", false)

	// No build was run: active generation must be absent.
	active, _ := NewStore(database, "").ActiveGeneration()
	if active != nil {
		t.Fatalf("test setup: expected no active generation")
	}

	for _, m := range []string{MetricNetFlow, MetricAgedOpen, MetricResolutionMix, MetricWorkload} {
		r, err := svc.Compute(Query{Metric: m, AsOf: tm("2024-02-01T00:00:00Z")})
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		if r.BuildGeneration != 0 {
			t.Errorf("%s should not use a fact generation, got gen %d", m, r.BuildGeneration)
		}
		if r.TaxonomyVersion == "" {
			t.Errorf("%s missing taxonomy version", m)
		}
	}
}

// Net-flow buckets arrivals by created and departures by current resolved; the per-period
// net reflects the boundaries correctly.
func TestNetFlow_Boundaries(t *testing.T) {
	database, svc := snapshotService(t, factPolicy())
	// Created in Jan, resolved in Feb.
	upIssue(t, database, "F-1", "PF", "Story", "Closed", "Done", "2024-01-05T00:00:00Z", "2024-02-10T00:00:00Z", "Done", false)
	// Created in Jan, still open.
	upIssue(t, database, "F-2", "PF", "Story", "In Progress", "In Progress", "2024-01-20T00:00:00Z", "", "", false)

	r, err := svc.Compute(Query{Metric: MetricNetFlow})
	if err != nil {
		t.Fatalf("net_flow: %v", err)
	}
	got := map[string]int{}
	for _, p := range r.Series {
		got[p.Period] = p.Count
	}
	if got["2024-01"] != 2 { // two arrivals, no departures
		t.Errorf("Jan net = %d, want 2", got["2024-01"])
	}
	if got["2024-02"] != -1 { // one departure
		t.Errorf("Feb net = %d, want -1", got["2024-02"])
	}
	if r.Counts["created"] != 2 || r.Counts["resolved"] != 1 || r.Counts["net"] != 1 {
		t.Errorf("totals = %+v", r.Counts)
	}
}

// Aged-open buckets are computed against the explicit as_of and open issues only.
func TestAgedOpen_Buckets(t *testing.T) {
	database, svc := snapshotService(t, factPolicy())
	asOfT := tm("2024-06-01T00:00:00Z")
	// 100 days old -> 91-180d.
	upIssue(t, database, "O-1", "PO", "Story", "In Progress", "In Progress", asOfT.AddDate(0, 0, -100).Format(time.RFC3339), "", "", false)
	// 3 days old -> 0-7d.
	upIssue(t, database, "O-2", "PO", "Story", "Backlog", "To Do", asOfT.AddDate(0, 0, -3).Format(time.RFC3339), "", "", false)
	// Resolved -> excluded (status category done).
	upIssue(t, database, "O-3", "PO", "Story", "Closed", "Done", day(1), "2024-02-01T00:00:00Z", "Done", false)

	r, err := svc.Compute(Query{Metric: MetricAgedOpen, AsOf: asOfT})
	if err != nil {
		t.Fatalf("aged_open: %v", err)
	}
	if r.Eligible != 2 {
		t.Errorf("eligible open = %d, want 2", r.Eligible)
	}
	if r.Counts["91-180d"] != 1 {
		t.Errorf("91-180d = %d, want 1", r.Counts["91-180d"])
	}
	if r.Counts["0-7d"] != 1 {
		t.Errorf("0-7d = %d, want 1", r.Counts["0-7d"])
	}

	// Ages advance with a later as_of, without any rebuild.
	r2, _ := svc.Compute(Query{Metric: MetricAgedOpen, AsOf: asOfT.AddDate(0, 0, 200)})
	if r2.Counts[">180d"] != 2 { // both O-1 and O-2 are now older than 180 days
		t.Errorf("after advancing as_of, >180d = %d, want 2", r2.Counts[">180d"])
	}
}

// Resolution mix counts resolved issues by resolution, with an explicit (none) bucket.
func TestResolutionMix(t *testing.T) {
	database, svc := snapshotService(t, factPolicy())
	upIssue(t, database, "M-1", "PM", "Story", "Closed", "Done", day(1), "2024-01-10T00:00:00Z", "Done", false)
	upIssue(t, database, "M-2", "PM", "Bug", "Closed", "Done", day(1), "2024-01-11T00:00:00Z", "Won't Do", false)
	upIssue(t, database, "M-3", "PM", "Bug", "Closed", "Done", day(1), "2024-01-12T00:00:00Z", "", false) // resolved, no resolution
	upIssue(t, database, "M-4", "PM", "Bug", "In Progress", "In Progress", day(1), "", "", false)         // unresolved

	r, err := svc.Compute(Query{Metric: MetricResolutionMix})
	if err != nil {
		t.Fatalf("resolution_mix: %v", err)
	}
	if r.Eligible != 3 {
		t.Errorf("eligible = %d, want 3 resolved", r.Eligible)
	}
	if r.Counts["Done"] != 1 || r.Counts["Won't Do"] != 1 || r.Counts["(none)"] != 1 {
		t.Errorf("counts = %+v", r.Counts)
	}
}

// Ratio reports class counts, a defined value, and keeps unknown/other classes visible with
// no hard-coded classification.
func TestRatio_AllCohort(t *testing.T) {
	database, svc := snapshotService(t, ratioConfig("created"))
	upIssueA(t, database, "R-1", "PR", "Bug", "Open", "To Do", day(1), "", "", "")
	upIssueA(t, database, "R-2", "PR", "Bug", "Open", "To Do", day(1), "", "", "")
	upIssueA(t, database, "R-3", "PR", "Story", "Open", "To Do", day(1), "", "", "")
	upIssueA(t, database, "R-4", "PR", "Task", "Open", "To Do", day(1), "", "", "")          // -> other
	upIssueA(t, database, "R-5", "PR", "Vulnerability", "Open", "To Do", day(1), "", "", "") // -> other, never auto-security

	r, err := svc.Compute(Query{Metric: MetricRatio, Cohort: "all"})
	if err != nil {
		t.Fatalf("ratio: %v", err)
	}
	if r.Ratio.NumCount != 2 || r.Ratio.DenCount != 1 {
		t.Errorf("num/den = %d/%d, want 2/1", r.Ratio.NumCount, r.Ratio.DenCount)
	}
	if r.Ratio.Value == nil || *r.Ratio.Value != 2 {
		t.Errorf("value = %v, want 2", r.Ratio.Value)
	}
	if r.Ratio.Classes["other"] != 2 {
		t.Errorf("other class = %d, want 2 (Task + Vulnerability, no hard-coded security)", r.Ratio.Classes["other"])
	}
	if r.Cohort != "all" {
		t.Errorf("cohort = %q", r.Cohort)
	}
}

// A zero denominator yields a null (undefined) ratio, not zero.
func TestRatio_ZeroDenominatorUndefined(t *testing.T) {
	database, svc := snapshotService(t, ratioConfig("created"))
	upIssueA(t, database, "Z-1", "PZ", "Bug", "Open", "To Do", day(1), "", "", "")
	// No feature-class issues at all.

	r, err := svc.Compute(Query{Metric: MetricRatio, Cohort: "all"})
	if err != nil {
		t.Fatalf("ratio: %v", err)
	}
	if r.Ratio.Value != nil {
		t.Errorf("value = %v, want nil (undefined)", *r.Ratio.Value)
	}
	if r.Ratio.DenCount != 0 {
		t.Errorf("den = %d, want 0", r.Ratio.DenCount)
	}
	if len(r.Warnings) == 0 {
		t.Errorf("expected undefined-ratio warning")
	}
}

// The completed ratio cohort reuses the shared completion policy.
func TestRatio_CompletedCohort(t *testing.T) {
	database, svc := snapshotService(t, ratioConfig("completed"))
	// Delivered bug, delivered feature, and a non-delivery bug that must be excluded.
	upIssueA(t, database, "C-1", "PC", "Bug", "Closed", "Done", day(1), "2024-01-10T00:00:00Z", "Done", "")
	upIssueA(t, database, "C-2", "PC", "Story", "Closed", "Done", day(1), "2024-01-11T00:00:00Z", "Done", "")
	upIssueA(t, database, "C-3", "PC", "Bug", "Closed", "Done", day(1), "2024-01-12T00:00:00Z", "Won't Do", "")

	r, err := svc.Compute(Query{Metric: MetricRatio})
	if err != nil {
		t.Fatalf("ratio: %v", err)
	}
	if r.Ratio.NumCount != 1 || r.Ratio.DenCount != 1 {
		t.Errorf("completed num/den = %d/%d, want 1/1 (C-3 excluded as non-delivery)", r.Ratio.NumCount, r.Ratio.DenCount)
	}
	if !r.GenuineDone || r.Cohort != "completed (genuine-done)" {
		t.Errorf("cohort = %q genuine=%v", r.Cohort, r.GenuineDone)
	}
}

// Ratio without configured taxonomy errors (no hard-coded classification).
func TestRatio_NotConfigured(t *testing.T) {
	_, svc := snapshotService(t, factPolicy()) // factPolicy has no type_classes/ratio
	if _, err := svc.Compute(Query{Metric: MetricRatio}); err != ErrRatioNotConfigured {
		t.Errorf("expected ErrRatioNotConfigured, got %v", err)
	}
}

// Workload buckets empty/sentinel assignees as (Unassigned) and counts unknown status
// categories explicitly; neither is dropped.
func TestWorkload_UnassignedAndUnknownCategory(t *testing.T) {
	database, svc := snapshotService(t, factPolicy())
	upIssueA(t, database, "W-1", "PW", "Story", "In Progress", "In Progress", day(1), "", "", "Alice")
	upIssueA(t, database, "W-2", "PW", "Story", "In Progress", "In Progress", day(1), "", "", "")                // unassigned
	upIssueA(t, database, "W-3", "PW", "Story", "Escalated", "", day(1), "", "", "unassigned")                   // sentinel + unknown category
	upIssueA(t, database, "W-4", "PW", "Story", "Closed", "Done", day(1), "2024-01-10T00:00:00Z", "Done", "Bob") // closed -> excluded

	r, err := svc.Compute(Query{Metric: MetricWorkload})
	if err != nil {
		t.Fatalf("workload: %v", err)
	}
	if r.Eligible != 3 {
		t.Errorf("open items = %d, want 3", r.Eligible)
	}
	if r.Counts["Alice"] != 1 {
		t.Errorf("Alice = %d, want 1", r.Counts["Alice"])
	}
	if r.Counts[unassigned] != 2 {
		t.Errorf("(Unassigned) = %d, want 2 (empty + sentinel)", r.Counts[unassigned])
	}
	if r.Counts["(unknown status category)"] != 1 {
		t.Errorf("unknown category = %d, want 1", r.Counts["(unknown status category)"])
	}
}
