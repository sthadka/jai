package metrics

import (
	"testing"

	"github.com/sthadka/jai/internal/db"
)

// clSprint inserts a Sprint-membership changelog transition. from/to carry the full id/name
// sets (comma-separated) before/after the change — the set-valued shape M14 replays.
func clSprint(t *testing.T, database *db.DB, id, key, fromID, fromName, toID, toName, at string) {
	t.Helper()
	clField(t, database, id, key, sprintField, fromID, fromName, toID, toName, at)
}

func findSprint(sprints []SprintResolution, key string) *SprintResolution {
	for i := range sprints {
		if sprints[i].Key == key {
			return &sprints[i]
		}
	}
	return nil
}

// Eventual resolution of ever-members: distinct same-name/different-id sprints stay
// distinct, add/remove/re-add counts an issue once, genuine-done policy gates the
// numerator, unknown-id membership is surfaced (never merged), and resolution is the
// current snapshot state — an issue resolved "after close" still counts as eventual
// resolution, never as completion at close.
func TestSprintResolution_EverMemberResolution(t *testing.T) {
	database, store := newFactStore(t)
	pol := factPolicy() // done_resolutions = [Done]
	svc := NewService(database, store, pol, nil)

	// Sprint 101 "Sprint A": P-1 (resolved Done), P-2 (add/remove/re-add, unresolved),
	// P-6 (resolved Won't Do -> non-delivery, not counted).
	upIssue(t, database, "P-1", "PS", "Story", "Closed", "Done", day(1), day(30), "Done", true)
	clSprint(t, database, "p1s", "P-1", "", "", "101", "Sprint A", day(2))

	upIssue(t, database, "P-2", "PS", "Story", "In Progress", "In Progress", day(1), "", "", true)
	clSprint(t, database, "p2s1", "P-2", "", "", "101", "Sprint A", day(2))
	clSprint(t, database, "p2s2", "P-2", "101", "Sprint A", "", "", day(3))
	clSprint(t, database, "p2s3", "P-2", "", "", "101", "Sprint A", day(4))

	upIssue(t, database, "P-6", "PS", "Bug", "Closed", "Done", day(1), day(25), "Won't Do", true)
	clSprint(t, database, "p6s", "P-6", "", "", "101", "Sprint A", day(2))

	// Sprint 102 — SAME name "Sprint A", different id: P-3 (resolved Done). Must stay
	// distinct from 101.
	upIssue(t, database, "P-3", "PS", "Story", "Closed", "Done", day(1), day(10), "Done", true)
	clSprint(t, database, "p3s", "P-3", "", "", "102", "Sprint A", day(2))

	// Unknown-id sprint (only a name): P-4.
	upIssue(t, database, "P-4", "PS", "Story", "In Progress", "In Progress", day(1), "", "", true)
	clSprint(t, database, "p4s", "P-4", "", "", "", "Phantom Sprint", day(2))

	// Never a sprint member: P-5.
	upIssue(t, database, "P-5", "PS", "Story", "In Progress", "In Progress", day(1), "", "", true)

	if _, err := NewBuilder(store, pol).BuildAll(asOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}

	sprints, diag, err := svc.SprintResolutions(Query{Metric: MetricSprintResolution})
	if err != nil {
		t.Fatalf("SprintResolutions: %v", err)
	}

	cases := []struct {
		name          string
		key           string
		wantResolved  bool
		wantEver      int
		wantNumerator int
	}{
		{"sprint 101 counts distinct issues, genuine-done numerator", "id:101", true, 3, 1},
		{"sprint 102 stays distinct from same-name 101", "id:102", true, 1, 1},
		{"unknown-id sprint surfaced, never merged", "name?:Phantom Sprint", false, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sr := findSprint(sprints, tc.key)
			if sr == nil {
				t.Fatalf("sprint %q missing from results %+v", tc.key, sprints)
			}
			if sr.Resolved != tc.wantResolved {
				t.Errorf("Resolved = %v, want %v", sr.Resolved, tc.wantResolved)
			}
			if sr.EverMembers != tc.wantEver {
				t.Errorf("EverMembers = %d, want %d", sr.EverMembers, tc.wantEver)
			}
			if sr.ResolvedMembers != tc.wantNumerator {
				t.Errorf("ResolvedMembers = %d, want %d", sr.ResolvedMembers, tc.wantNumerator)
			}
		})
	}

	// Two distinct sprints share the name "Sprint A" but remain two rows.
	if n := len(sprints); n != 3 {
		t.Fatalf("distinct sprints = %d, want 3 (101, 102, unknown): %+v", n, sprints)
	}
	if diag.NeverMemberIssues != 1 {
		t.Errorf("NeverMemberIssues = %d, want 1 (P-5)", diag.NeverMemberIssues)
	}
	if diag.UnresolvedMemberships != 1 {
		t.Errorf("UnresolvedMemberships = %d, want 1 (P-4)", diag.UnresolvedMemberships)
	}
	// Denominator = 5 ever-memberships (P-1,P-2,P-6 -> 101; P-3 -> 102; P-4 -> unknown).
	if diag.TotalEverMembers != 5 {
		t.Errorf("TotalEverMembers = %d, want 5", diag.TotalEverMembers)
	}
	// Numerator = 2 resolved ever-members (P-1 on 101, P-3 on 102).
	if diag.TotalResolvedMembers != 2 {
		t.Errorf("TotalResolvedMembers = %d, want 2", diag.TotalResolvedMembers)
	}
	if !diag.GenuineDone {
		t.Errorf("GenuineDone = false, want true (done_resolutions configured)")
	}
}

// Provider surfaces per-sprint stacked ever-members-vs-resolved counts, the overall
// resolved share as a numerator/denominator ratio, and the limitation diagnostics.
func TestSprintResolution_ProviderEnvelope(t *testing.T) {
	database, store := newFactStore(t)
	pol := factPolicy()
	svc := NewService(database, store, pol, nil)

	upIssue(t, database, "Q-1", "PS", "Story", "Closed", "Done", day(1), day(10), "Done", true)
	clSprint(t, database, "q1s", "Q-1", "", "", "201", "Beta", day(2))
	upIssue(t, database, "Q-2", "PS", "Story", "In Progress", "In Progress", day(1), "", "", true)
	clSprint(t, database, "q2s", "Q-2", "", "", "201", "Beta", day(2))

	if _, err := NewBuilder(store, pol).BuildAll(asOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}

	r, err := svc.Compute(Query{Metric: MetricSprintResolution})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if r.Eligible != 1 {
		t.Errorf("Eligible sprints = %d, want 1", r.Eligible)
	}
	label := "Beta [201]"
	if got := r.Counts[label+" \u2014 ever-members"]; got != 2 {
		t.Errorf("ever-members count = %d, want 2 (%v)", got, r.Counts)
	}
	if got := r.Counts[label+" \u2014 resolved"]; got != 1 {
		t.Errorf("resolved count = %d, want 1 (%v)", got, r.Counts)
	}
	if r.Ratio == nil || r.Ratio.NumCount != 1 || r.Ratio.DenCount != 2 {
		t.Fatalf("ratio = %+v, want 1/2", r.Ratio)
	}
	if r.Ratio.Value == nil || *r.Ratio.Value != 0.5 {
		t.Errorf("ratio value = %v, want 0.5", r.Ratio.Value)
	}
	// Limitation must be surfaced, not silently converted to a completion claim.
	foundLimit := false
	for _, w := range r.Warnings {
		if w == "sprint start/close dates and story points are unavailable; commitment-at-close, carry-over, and velocity are not computed" {
			foundLimit = true
		}
	}
	if !foundLimit {
		t.Errorf("missing sprint-dates limitation warning: %v", r.Warnings)
	}
}

// Zero-denominator (no sprint members at all) is undefined — a nil ratio value — never
// reported as success, and empty sprint metadata must not prevent the metric.
func TestSprintResolution_ZeroDenominatorUndefined(t *testing.T) {
	database, store := newFactStore(t)
	pol := factPolicy()
	svc := NewService(database, store, pol, nil)

	upIssue(t, database, "Z-1", "PS", "Story", "In Progress", "In Progress", day(1), "", "", true)

	if _, err := NewBuilder(store, pol).BuildAll(asOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}

	r, err := svc.Compute(Query{Metric: MetricSprintResolution})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if r.Eligible != 0 {
		t.Errorf("Eligible = %d, want 0", r.Eligible)
	}
	if r.Ratio == nil {
		t.Fatalf("ratio must be present even when empty")
	}
	if r.Ratio.Value != nil {
		t.Errorf("zero-denominator ratio value = %v, want nil (undefined)", *r.Ratio.Value)
	}
	if r.Excluded["never a sprint member"] != 1 {
		t.Errorf("never-member exclusion = %d, want 1", r.Excluded["never a sprint member"])
	}
}
