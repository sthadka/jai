package metrics

import (
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

func eventsService(t *testing.T, tracked []string) (*db.DB, *Service) {
	t.Helper()
	database, store := newFactStore(t)
	pol := CompilePolicy(&config.MetricsConfig{
		StatusStages: map[string][]string{
			"active": {"In Progress"},
			"done":   {"Closed"},
		},
		WIPStages:     []string{"active"},
		TrackedFields: tracked,
	})
	return database, NewService(database, store, pol, nil)
}

func upComment(t *testing.T, database *db.DB, id, key, created string) {
	t.Helper()
	if err := database.UpsertComment(&db.Comment{ID: id, IssueKey: key, Body: "c", Created: created}); err != nil {
		t.Fatalf("UpsertComment %s: %v", id, err)
	}
}

// Migration 15 creates the partial index idx_cl_tracked on a fresh DB.
func TestMigration_PartialChangelogIndex(t *testing.T) {
	database, _ := eventsService(t, nil)
	var name, sqlText string
	err := database.QueryRow(
		`SELECT name, sql FROM sqlite_master WHERE type='index' AND name='idx_cl_tracked'`,
	).Scan(&name, &sqlText)
	if err != nil {
		t.Fatalf("idx_cl_tracked not found on fresh DB: %v", err)
	}
	if want := "WHERE field IN"; !strings.Contains(sqlText, want) {
		t.Errorf("index is not partial: %q", sqlText)
	}
}

// Change counts are index-backed, exclude no-op/display-only churn via the stable id, and
// count never-changed issues as 0 (not dropped).
func TestPriorityChanges_StableIDCountsAndNoOps(t *testing.T) {
	database, svc := eventsService(t, []string{"status", "priority"})

	upIssue(t, database, "E-1", "PE", "Story", "In Progress", "In Progress", day(1), "", "", true)
	clField(t, database, "p1", "E-1", "priority", "3", "Low", "2", "Medium", day(2))
	clField(t, database, "p2", "E-1", "priority", "2", "Medium", "1", "High", day(3))

	upIssue(t, database, "E-2", "PE", "Story", "In Progress", "In Progress", day(1), "", "", true)
	clField(t, database, "p3", "E-2", "priority", "3", "Low", "2", "Medium", day(2))
	// Display-only: same priority id, renamed display -> NOT a change.
	clField(t, database, "p4", "E-2", "priority", "2", "Medium", "2", "Med", day(3))

	// Never changed -> contributes 0, not dropped.
	upIssue(t, database, "E-3", "PE", "Story", "In Progress", "In Progress", day(1), "", "", true)

	counts, err := svc.fieldChangeCounts(nil, priorityField)
	if err != nil {
		t.Fatalf("fieldChangeCounts: %v", err)
	}
	cases := []struct {
		key  string
		want int
	}{
		{"E-1", 2},
		{"E-2", 1}, // display-only excluded
		{"E-3", 0}, // absent -> 0
	}
	for _, c := range cases {
		if counts[c.key] != c.want {
			t.Errorf("%s change count = %d, want %d", c.key, counts[c.key], c.want)
		}
	}

	r, err := svc.Compute(Query{Metric: MetricPriorityChanges})
	if err != nil {
		t.Fatalf("Compute priority_changes: %v", err)
	}
	if r.Eligible != 3 {
		t.Errorf("Eligible = %d, want 3 (population includes never-changed)", r.Eligible)
	}
	if r.Distribution.Max != 2 || r.Distribution.Min != 0 {
		t.Errorf("distribution min/max = %v/%v, want 0/2", r.Distribution.Min, r.Distribution.Max)
	}
	if r.Policies["priority_escalation"] != "unavailable (no priority ordering configured)" {
		t.Errorf("escalation must be marked unavailable, got %q", r.Policies["priority_escalation"])
	}
}

// Reassignment count reads the changelog by stable account id; a display-name-only change is
// not a reassignment.
func TestReassignmentCount_StableAccountID(t *testing.T) {
	database, svc := eventsService(t, []string{"status", "assignee"})

	upIssue(t, database, "R-1", "PE", "Story", "In Progress", "In Progress", day(1), "", "", true)
	clField(t, database, "r1", "R-1", "assignee", "", "", "accB", "Bob", day(2))
	clField(t, database, "r2", "R-1", "assignee", "accB", "Bob", "accC", "Carol", day(3))

	upIssue(t, database, "R-2", "PE", "Story", "In Progress", "In Progress", day(1), "", "", true)
	clField(t, database, "r3", "R-2", "assignee", "", "", "accA", "Alice", day(2))
	// Same account, renamed display -> not a reassignment.
	clField(t, database, "r4", "R-2", "assignee", "accA", "Alice", "accA", "Alice Smith", day(3))

	counts, err := svc.fieldChangeCounts(nil, assigneeField)
	if err != nil {
		t.Fatalf("fieldChangeCounts: %v", err)
	}
	if counts["R-1"] != 2 {
		t.Errorf("R-1 reassignments = %d, want 2", counts["R-1"])
	}
	if counts["R-2"] != 1 {
		t.Errorf("R-2 reassignments = %d, want 1 (display-only excluded)", counts["R-2"])
	}
}

// Rank volatility is unavailable (not zero) until Rank is tracked; once tracked it counts
// Rank changelog rows (which carry no stable id, so realness falls back to the token).
func TestRankVolatility_GatedOnTracked(t *testing.T) {
	// Untracked -> unavailable.
	database, svc := eventsService(t, []string{"status"})
	upIssue(t, database, "K-1", "PE", "Story", "In Progress", "In Progress", day(1), "", "", true)
	r, err := svc.Compute(Query{Metric: MetricRankVolatility})
	if err != nil {
		t.Fatalf("Compute rank_volatility (untracked): %v", err)
	}
	if r.Policies["rank"] != "untracked" {
		t.Errorf("rank must be marked untracked, got %q", r.Policies["rank"])
	}
	if r.Distribution.Count != 0 {
		t.Errorf("untracked rank distribution Count = %d, want 0", r.Distribution.Count)
	}

	// Tracked -> counts token-based Rank changes, excluding no-ops.
	database2, svc2 := eventsService(t, []string{"status", "Rank"})
	upIssue(t, database2, "K-2", "PE", "Story", "In Progress", "In Progress", day(1), "", "", true)
	clField(t, database2, "rk1", "K-2", "Rank", "", "0|aaaaa:", "", "0|bbbbb:", day(2))
	clField(t, database2, "rk2", "K-2", "Rank", "", "0|bbbbb:", "", "0|ccccc:", day(3))
	// No-op reorder (same token) -> not counted.
	clField(t, database2, "rk3", "K-2", "Rank", "", "0|ccccc:", "", "0|ccccc:", day(4))
	counts, err := svc2.fieldChangeCounts(nil, rankField)
	if err != nil {
		t.Fatalf("fieldChangeCounts Rank: %v", err)
	}
	if counts["K-2"] != 2 {
		t.Errorf("K-2 rank changes = %d, want 2 (no-op excluded)", counts["K-2"])
	}
}

// Triage latency uses first-assignment / first-comment semantics with the MIN of the two,
// credits assignment at creation (latency 0), and excludes never-responded and
// unknown-creation issues from the denominator.
func TestTriageLatency_FirstResponseSemantics(t *testing.T) {
	database, svc := eventsService(t, []string{"status", "assignee"})

	// TA: assigned at creation (current assignee, no assignee events) -> latency 0.
	upIssueFull(t, database, "TA", "PE", "Story", "In Progress", "In Progress", day(1), "", "", "Bob", true)

	// TB: first assignment at day 6 -> latency 5.
	upIssue(t, database, "TB", "PE", "Story", "In Progress", "In Progress", day(1), "", "", true)
	clField(t, database, "tb1", "TB", "assignee", "", "", "accB", "Bob", day(6))

	// TC: comment (day 3) earlier than first assignment (day 10) -> latency 2.
	upIssue(t, database, "TC", "PE", "Story", "In Progress", "In Progress", day(1), "", "", true)
	clField(t, database, "tc1", "TC", "assignee", "", "", "accB", "Bob", day(10))
	upComment(t, database, "tcc1", "TC", day(3))

	// TD: never assigned, no comment -> excluded (no first response).
	upIssue(t, database, "TD", "PE", "Story", "In Progress", "In Progress", day(1), "", "", true)

	// TE: unknown creation time -> excluded.
	upIssue(t, database, "TE", "PE", "Story", "In Progress", "In Progress", "", "", "", true)
	clField(t, database, "te1", "TE", "assignee", "", "", "accB", "Bob", day(5))

	r, err := svc.Compute(Query{Metric: MetricTriageLatency})
	if err != nil {
		t.Fatalf("Compute triage_latency: %v", err)
	}
	if r.Eligible != 3 {
		t.Errorf("Eligible = %d, want 3 (TA,TB,TC)", r.Eligible)
	}
	if r.Distribution.Min != 0 {
		t.Errorf("MIN latency = %v, want 0 (assigned at creation)", r.Distribution.Min)
	}
	if r.Distribution.Max != 5 {
		t.Errorf("MAX latency = %v, want 5 (TB)", r.Distribution.Max)
	}
	if r.Excluded["no first response (never assigned, no comment)"] != 1 {
		t.Errorf("no-response exclusion = %d, want 1 (TD)", r.Excluded["no first response (never assigned, no comment)"])
	}
	if r.Excluded["unknown creation time"] != 1 {
		t.Errorf("unknown-creation exclusion = %d, want 1 (TE)", r.Excluded["unknown creation time"])
	}
}
