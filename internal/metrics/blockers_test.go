package metrics

import (
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

// blockerPolicy maps a waiting status and configures blocker link types.
func blockerPolicy() *Policy {
	return CompilePolicy(&config.MetricsConfig{
		StatusStages: map[string][]string{
			"todo":    {"Backlog"},
			"active":  {"In Progress"},
			"waiting": {"Waiting"},
			"done":    {"Closed"},
		},
		WIPStages:        []string{"active"},
		DoneResolutions:  []string{"Done"},
		BlockerLinkTypes: []string{"is blocked by", "depends on"},
	})
}

func links(t *testing.T, database *db.DB, blockedKey string, ls ...db.IssueLink) {
	t.Helper()
	if err := database.UpsertIssueLinks(blockedKey, ls); err != nil {
		t.Fatalf("UpsertIssueLinks %s: %v", blockedKey, err)
	}
}

// Multiple blockers plus an inverse-direction link count the blocked issue once in the
// age histogram and Eligible, while blocker_links_total reflects every matched relationship.
func TestBlockerAgeing_DedupAndDirection(t *testing.T) {
	database, svc := snapshotService(t, blockerPolicy())
	// Blocked issue created 2024-01-01; as_of 2024-02-01 => age 31d => "31-90d".
	upIssue(t, database, "B-1", "PB", "Story", "In Progress", "In Progress", day(1), "", "", false)
	links(t, database, "B-1",
		db.IssueLink{ID: "l1", IssueKey: "B-1", LinkType: "is blocked by", Direction: "inward", LinkedKey: "B-2"},
		db.IssueLink{ID: "l2", IssueKey: "B-1", LinkType: "depends on", Direction: "inward", LinkedKey: "B-3"},
		// Inverse direction: B-1 blocks B-4. "blocks" is not a configured blocker type and
		// must NOT turn B-1 into a (duplicate) blocked issue.
		db.IssueLink{ID: "l3", IssueKey: "B-1", LinkType: "blocks", Direction: "outward", LinkedKey: "B-4"},
	)

	r, err := svc.Compute(Query{Metric: MetricBlockerAgeing, AsOf: tm("2024-02-01T00:00:00Z")})
	if err != nil {
		t.Fatalf("blocker_ageing: %v", err)
	}
	if r.Eligible != 1 {
		t.Errorf("Eligible = %d, want 1 (one distinct blocked issue)", r.Eligible)
	}
	if got := r.Counts["blocker_links_total"]; got != 2 {
		t.Errorf("blocker_links_total = %d, want 2 (inverse 'blocks' excluded)", got)
	}
	total := 0
	for _, b := range agedBuckets {
		total += r.Counts["age:"+b.label]
	}
	if total != 1 {
		t.Errorf("age histogram total = %d, want 1 (blocked issue counted once)", total)
	}
	if r.Counts["age:31-90d"] != 1 {
		t.Errorf("age:31-90d = %d, want 1", r.Counts["age:31-90d"])
	}
}

// The blocker's fresh local state is used when the linked issue exists; a resolved local
// blocker is surfaced as a likely-stale link. Snapshot-only and missing states stay visible.
func TestBlockerAgeing_FreshOverridesSnapshotAndMissing(t *testing.T) {
	database, svc := snapshotService(t, blockerPolicy())
	upIssue(t, database, "B-1", "PB", "Story", "In Progress", "In Progress", day(1), "", "", false)
	// Fresh blocker: exists locally and is resolved (done) -> likely-stale link.
	upIssue(t, database, "B-2", "PB", "Story", "Closed", "Done", day(1), day(3), "Done", false)
	links(t, database, "B-1",
		// Fresh: linked issue B-2 present locally; snapshot says "In Progress" but the live
		// row is Closed/Done, so fresh state must override the stale snapshot.
		db.IssueLink{ID: "l1", IssueKey: "B-1", LinkType: "is blocked by", Direction: "inward", LinkedKey: "B-2", LinkedStatus: "In Progress"},
		// Snapshot-only: linked issue absent locally, but a captured status snapshot exists.
		db.IssueLink{ID: "l2", IssueKey: "B-1", LinkType: "is blocked by", Direction: "inward", LinkedKey: "EXT-9", LinkedStatus: "Open"},
		// Missing: linked issue absent locally and no snapshot.
		db.IssueLink{ID: "l3", IssueKey: "B-1", LinkType: "depends on", Direction: "inward", LinkedKey: "EXT-0", LinkedStatus: ""},
	)

	r, err := svc.Compute(Query{Metric: MetricBlockerAgeing, AsOf: tm("2024-02-01T00:00:00Z")})
	if err != nil {
		t.Fatalf("blocker_ageing: %v", err)
	}
	if got := r.Counts["blocker_state:fresh"]; got != 1 {
		t.Errorf("blocker_state:fresh = %d, want 1", got)
	}
	if got := r.Counts["blocker_state:stale_snapshot"]; got != 1 {
		t.Errorf("blocker_state:stale_snapshot = %d, want 1", got)
	}
	if got := r.Counts["blocker_state:missing"]; got != 1 {
		t.Errorf("blocker_state:missing = %d, want 1", got)
	}
	if got := r.Counts["blocker_resolved"]; got != 1 {
		t.Errorf("blocker_resolved = %d, want 1 (fresh blocker is done => stale link)", got)
	}
}

// Waiting-stage dwell is summed per issue against as_of and reported as a distribution,
// labelled a proxy — independent of the relationship-age signal.
func TestBlockerAgeing_WaitingDwell(t *testing.T) {
	store, svc := buildService(t, blockerPolicy())
	database := store.db
	// W-1 enters Waiting on day 5 and leaves on day 15 => 10 days of dwell.
	upIssue(t, database, "W-1", "PW", "Story", "Closed", "Done", day(1), day(15), "Done", true)
	cl(t, database, "w1a", "W-1", "Backlog", "Waiting", day(5))
	cl(t, database, "w1b", "W-1", "Waiting", "Closed", day(15))
	rebuild(t, store, blockerPolicy())

	r, err := svc.Compute(Query{Metric: MetricBlockerAgeing, AsOf: asOf})
	if err != nil {
		t.Fatalf("blocker_ageing: %v", err)
	}
	if r.Distribution == nil || r.Distribution.Count != 1 {
		t.Fatalf("waiting dwell distribution = %+v, want Count 1", r.Distribution)
	}
	if got := r.Distribution.P50; got < 9.99 || got > 10.01 {
		t.Errorf("waiting dwell P50 = %v days, want ~10", got)
	}
	if got := r.Counts["waiting_dwell_issues"]; got != 1 {
		t.Errorf("waiting_dwell_issues = %d, want 1", got)
	}
}

// Open waiting intervals age against the query as_of, not a build-frozen now.
func TestBlockerAgeing_OpenWaitingAgainstAsOf(t *testing.T) {
	store, svc := buildService(t, blockerPolicy())
	database := store.db
	// W-2 enters Waiting on day 5 and never leaves (open interval).
	upIssue(t, database, "W-2", "PW", "Story", "Waiting", "In Progress", day(1), "", "", true)
	cl(t, database, "w2a", "W-2", "Backlog", "Waiting", day(5))
	rebuild(t, store, blockerPolicy())

	// as_of day 25 => open waiting interval dwell = 20 days.
	r, err := svc.Compute(Query{Metric: MetricBlockerAgeing, AsOf: tm("2024-01-25T00:00:00Z")})
	if err != nil {
		t.Fatalf("blocker_ageing: %v", err)
	}
	if r.Distribution == nil || r.Distribution.Count != 1 {
		t.Fatalf("waiting dwell distribution = %+v, want Count 1", r.Distribution)
	}
	if got := r.Distribution.P50; got < 19.99 || got > 20.01 {
		t.Errorf("open waiting dwell P50 = %v days, want ~20 (aged against as_of)", got)
	}
}

// With blocker_link_types unset the relationship-age signal is unavailable (never guessed),
// but the metric still runs and the waiting-dwell signal is still produced.
func TestBlockerAgeing_UnconfiguredLinkTypes(t *testing.T) {
	database, svc := snapshotService(t, factPolicy()) // factPolicy has no blocker_link_types
	upIssue(t, database, "B-1", "PB", "Story", "In Progress", "In Progress", day(1), "", "", false)
	links(t, database, "B-1",
		db.IssueLink{ID: "l1", IssueKey: "B-1", LinkType: "is blocked by", Direction: "inward", LinkedKey: "B-2"},
	)

	r, err := svc.Compute(Query{Metric: MetricBlockerAgeing, AsOf: tm("2024-02-01T00:00:00Z")})
	if err != nil {
		t.Fatalf("blocker_ageing: %v", err)
	}
	// No blocker links were consulted: no relationship-age cohort.
	if r.Eligible != 0 {
		t.Errorf("Eligible = %d, want 0 (relationship-age unavailable)", r.Eligible)
	}
	if _, ok := r.Counts["blocker_links_total"]; ok {
		t.Errorf("blocker_links_total should be absent when link types are unconfigured")
	}
	var found bool
	for _, w := range r.Warnings {
		if strings.Contains(w, "blocker_link_types is unset") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an unavailable-signal warning for unset blocker_link_types, got %v", r.Warnings)
	}
}
