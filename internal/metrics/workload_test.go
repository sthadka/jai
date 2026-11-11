package metrics

import (
	"testing"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

func workloadPolicy() *Policy {
	return CompilePolicy(&config.MetricsConfig{
		StatusStages: map[string][]string{
			"todo":   {"Backlog"},
			"active": {"In Progress"},
			"done":   {"Closed"},
		},
		WIPStages:       []string{"active"},
		DoneResolutions: []string{"Done"},
		TrackedFields:   []string{"status", "assignee"},
	})
}

// upIssueFull inserts an issue carrying an assignee; synced marks changelog history complete.
func upIssueFull(t *testing.T, database *db.DB, key, project, itype, status, category, created, resolved, resolution, assignee string, synced bool) {
	t.Helper()
	iss := &db.Issue{
		Key: key, Project: project, Type: itype, Summary: key,
		Status: status, StatusCategory: category, Assignee: assignee,
		Created: created, Resolved: resolved, Resolution: resolution,
	}
	var extra map[string]interface{}
	if synced {
		extra = map[string]interface{}{"changelog_synced_at": "2030-01-01T00:00:00Z"}
	}
	if err := database.UpsertIssue(iss, extra); err != nil {
		t.Fatalf("UpsertIssue %s: %v", key, err)
	}
}

func workloadService(t *testing.T, pol *Policy) (*db.DB, *Store, *Service) {
	t.Helper()
	database, store := newFactStore(t)
	return database, store, NewService(database, store, pol, nil)
}

// Owner-at-completion attributes a completion to the owner in effect at the completion
// instant; a reassignment AFTER completion leaves owner-at-completion unchanged but moves
// the current-owner attribution. This is the core event-time-vs-current cohort invariant.
func TestWorkloadThroughput_OwnerAtCompletionVsCurrent(t *testing.T) {
	pol := workloadPolicy()
	database, store, svc := workloadService(t, pol)

	// Resolved day 10, owner "Alice" at that time; reassigned to "Bob" at day 20 (after
	// completion). Current snapshot assignee is "Bob".
	upIssueFull(t, database, "W-1", "PW", "Story", "Closed", "Done", day(1), day(10), "Done", "Bob", true)
	cl(t, database, "W-1-s1", "W-1", "Backlog", "In Progress", day(2))
	cl(t, database, "W-1-s2", "W-1", "In Progress", "Closed", day(10))
	clField(t, database, "W-1-a1", "W-1", "assignee", "accA", "Alice", "accB", "Bob", day(20))

	if _, err := NewBuilder(store, pol).BuildAll(asOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}

	evt, err := svc.Compute(Query{Metric: MetricWorkloadThroughput, AsOf: asOf})
	if err != nil {
		t.Fatalf("workload_throughput: %v", err)
	}
	if evt.Counts["Alice"] != 1 {
		t.Errorf("owner-at-completion Alice = %d, want 1 (%v)", evt.Counts["Alice"], evt.Counts)
	}
	if evt.Counts["Bob"] != 0 {
		t.Errorf("owner-at-completion must not credit later owner Bob, got %d", evt.Counts["Bob"])
	}

	cur, err := svc.Compute(Query{Metric: MetricWorkloadThroughputCurrent, AsOf: asOf})
	if err != nil {
		t.Fatalf("workload_throughput_current: %v", err)
	}
	if cur.Counts["Bob"] != 1 {
		t.Errorf("current-owner Bob = %d, want 1 (%v)", cur.Counts["Bob"], cur.Counts)
	}
	if cur.Counts["Alice"] != 0 {
		t.Errorf("current-owner must not credit Alice, got %d", cur.Counts["Alice"])
	}
}

// A completion whose owner-at-completion cannot be recovered (incomplete assignee history)
// is bucketed as unknown and NEVER back-filled from the current snapshot owner.
func TestWorkloadThroughput_UnknownOwnerNotCurrentCopied(t *testing.T) {
	pol := workloadPolicy()
	database, store, svc := workloadService(t, pol)

	// synced=false => history incomplete => assignee interval is an unknown period.
	upIssueFull(t, database, "W-2", "PW", "Story", "Closed", "Done", day(1), day(10), "Done", "Zoe", false)

	if _, err := NewBuilder(store, pol).BuildAll(asOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	r, err := svc.Compute(Query{Metric: MetricWorkloadThroughput, AsOf: asOf})
	if err != nil {
		t.Fatalf("workload_throughput: %v", err)
	}
	if r.Counts[unknownOwnerBucket] != 1 {
		t.Errorf("unknown owner bucket = %d, want 1 (%v)", r.Counts[unknownOwnerBucket], r.Counts)
	}
	if r.Counts["Zoe"] != 0 {
		t.Errorf("unknown owner must not be current-copied to Zoe, got %d", r.Counts["Zoe"])
	}
}

// Sentinel/empty assignees collapse to the explicit Unassigned bucket in current-owner
// attribution; neither is dropped.
func TestWorkloadThroughputCurrent_Unassigned(t *testing.T) {
	pol := workloadPolicy()
	database, _, svc := workloadService(t, pol)
	upIssueFull(t, database, "U-1", "PW", "Story", "Closed", "Done", day(1), day(5), "Done", "", true)
	upIssueFull(t, database, "U-2", "PW", "Story", "Closed", "Done", day(1), day(5), "Done", "unassigned", true)
	upIssueFull(t, database, "U-3", "PW", "Story", "Closed", "Done", day(1), day(5), "Done", "Carol", true)

	r, err := svc.Compute(Query{Metric: MetricWorkloadThroughputCurrent, AsOf: asOf})
	if err != nil {
		t.Fatalf("workload_throughput_current: %v", err)
	}
	if r.Counts[unassigned] != 2 {
		t.Errorf("(Unassigned) = %d, want 2 (%v)", r.Counts[unassigned], r.Counts)
	}
	if r.Counts["Carol"] != 1 {
		t.Errorf("Carol = %d, want 1", r.Counts["Carol"])
	}
}

// Time-on-owner ages an open assignee interval against the query as_of, advancing with no
// source activity.
func TestTimeOnOwner_AdvancesWithAsOf(t *testing.T) {
	pol := workloadPolicy()
	database, store, svc := workloadService(t, pol)
	// Alice owns the issue from creation (never reassigned, complete history, still open).
	upIssueFull(t, database, "T-1", "PW", "Story", "In Progress", "In Progress", day(1), "", "", "Alice", true)

	buildAsOf := time.Date(2024, 1, 11, 0, 0, 0, 0, time.UTC) // day 11
	if _, err := NewBuilder(store, pol).BuildAll(buildAsOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}

	early, err := svc.Compute(Query{Metric: MetricWorkloadTimeOnOwner, AsOf: time.Date(2024, 1, 11, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("time_on_owner early: %v", err)
	}
	late, err := svc.Compute(Query{Metric: MetricWorkloadTimeOnOwner, AsOf: time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("time_on_owner late: %v", err)
	}
	if !(late.Counts["Alice"] > early.Counts["Alice"]) {
		t.Errorf("time-on-owner must advance with as_of: early=%d late=%d", early.Counts["Alice"], late.Counts["Alice"])
	}
	if early.Counts["Alice"] != 10 { // day1 -> day11 = 10 days
		t.Errorf("early Alice days = %d, want 10", early.Counts["Alice"])
	}
	if late.Counts["Alice"] != 30 { // day1 -> day31 = 30 days
		t.Errorf("late Alice days = %d, want 30", late.Counts["Alice"])
	}
}

// Reassignment thrash counts assignee changes per issue (intervals beyond the first).
func TestReassignmentThrash_Distribution(t *testing.T) {
	pol := workloadPolicy()
	database, store, svc := workloadService(t, pol)
	// R-1: two reassignments (Alice -> Bob -> Carol).
	upIssueFull(t, database, "R-1", "PW", "Story", "In Progress", "In Progress", day(1), "", "", "Carol", true)
	clField(t, database, "R-1-a1", "R-1", "assignee", "accA", "Alice", "accB", "Bob", day(5))
	clField(t, database, "R-1-a2", "R-1", "assignee", "accB", "Bob", "accC", "Carol", day(9))
	// R-2: never reassigned (0 changes).
	upIssueFull(t, database, "R-2", "PW", "Story", "In Progress", "In Progress", day(1), "", "", "Dan", true)

	if _, err := NewBuilder(store, pol).BuildAll(asOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	r, err := svc.Compute(Query{Metric: MetricReassignmentThrash, AsOf: asOf})
	if err != nil {
		t.Fatalf("reassignment_thrash: %v", err)
	}
	if r.Distribution == nil || r.Distribution.Count != 2 {
		t.Fatalf("distribution count = %v, want 2", r.Distribution)
	}
	if r.Distribution.Max != 2 {
		t.Errorf("max changes = %v, want 2", r.Distribution.Max)
	}
	if r.Distribution.Min != 0 {
		t.Errorf("min changes = %v, want 0", r.Distribution.Min)
	}
}
