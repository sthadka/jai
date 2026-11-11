package metrics

import (
	"testing"
)

// CFD spans all stages for a consistent population, keeps done cumulative, and is explicitly
// labelled as not a cycle/lead distribution.
func TestCFD_AllStagesCumulative(t *testing.T) {
	pol := factPolicy()
	store, svc := buildService(t, pol)
	database := store.db
	// todo [d1,d4), active [d4,d7), done [d7,open).
	upIssue(t, database, "C", "PW", "Story", "Closed", "Done", day(1), day(7), "Done", true)
	cl(t, database, "C-s1", "C", "Backlog", "In Progress", day(4))
	cl(t, database, "C-s2", "C", "In Progress", "Closed", day(7))
	rebuild(t, store, pol)

	r, err := svc.Compute(Query{Metric: MetricCFD, AsOf: asOf, Range: &TimeRange{From: timeOf(1), To: timeOf(10)}})
	if err != nil {
		t.Fatalf("cfd: %v", err)
	}
	if r.Counts[CFDKey(dayKey(1), StageTodo)] != 1 {
		t.Errorf("day1 todo = %d, want 1", r.Counts[CFDKey(dayKey(1), StageTodo)])
	}
	if r.Counts[CFDKey(dayKey(5), StageActive)] != 1 {
		t.Errorf("day5 active = %d, want 1", r.Counts[CFDKey(dayKey(5), StageActive)])
	}
	// done is cumulative: present on every day from d7 onward.
	for _, d := range []int{7, 8, 9, 10} {
		if r.Counts[CFDKey(dayKey(d), StageDone)] != 1 {
			t.Errorf("day%d done = %d, want 1 (cumulative)", d, r.Counts[CFDKey(dayKey(d), StageDone)])
		}
	}
	if r.Eligible != 1 {
		t.Errorf("population = %d, want 1", r.Eligible)
	}
	if len(r.Warnings) == 0 {
		t.Errorf("CFD must warn it is not a cycle/lead-time distribution")
	}
	// Daily total equals the population present each day (1 after creation).
	if seriesCount(r.Series, dayKey(10)) != 1 {
		t.Errorf("day10 total = %d, want 1", seriesCount(r.Series, dayKey(10)))
	}
}

func TestStageOrder(t *testing.T) {
	got := StageOrder([]string{StageDone, "custom", StageActive, StageTodo, StageUnknown})
	want := []string{StageTodo, StageActive, StageDone, StageUnknown, "custom"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("StageOrder = %v, want %v", got, want)
		}
	}
}

// Reopen rate uses reopen_count with a named denominator; the histogram keeps every count.
func TestReopen_RateAndHistogram(t *testing.T) {
	pol := factPolicy()
	store, svc := buildService(t, pol)
	database := store.db
	// R1 reopened once: Closed (d4) -> In Progress (d6) -> Closed (d8).
	upIssue(t, database, "R1", "PW", "Story", "Closed", "Done", day(1), day(8), "Done", true)
	cl(t, database, "R1-s1", "R1", "Backlog", "In Progress", day(2))
	cl(t, database, "R1-s2", "R1", "In Progress", "Closed", day(4))
	cl(t, database, "R1-s3", "R1", "Closed", "In Progress", day(6))
	cl(t, database, "R1-s4", "R1", "In Progress", "Closed", day(8))
	// R2 never reopened.
	upIssue(t, database, "R2", "PW", "Story", "Closed", "Done", day(1), day(5), "Done", true)
	cl(t, database, "R2-s1", "R2", "Backlog", "In Progress", day(2))
	cl(t, database, "R2-s2", "R2", "In Progress", "Closed", day(5))
	rebuild(t, store, pol)

	r, err := svc.Compute(Query{Metric: MetricReopen, AsOf: asOf})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if r.Ratio == nil || r.Ratio.NumCount != 1 || r.Ratio.DenCount != 2 {
		t.Fatalf("reopen ratio = %+v, want num 1 / den 2", r.Ratio)
	}
	assertFloat(t, "reopen rate", *r.Ratio.Value, 0.5)
	if r.Counts["0"] != 1 || r.Counts["1"] != 1 {
		t.Errorf("reopen histogram = %v, want {0:1,1:1}", r.Counts)
	}
}

// A zero denominator yields an undefined (nil) reopen rate, never zero.
func TestReopen_ZeroDenominatorUndefined(t *testing.T) {
	pol := factPolicy()
	store, svc := buildService(t, pol)
	upIssue(t, store.db, "Z1", "PW", "Story", "Closed", "Done", day(1), day(5), "Done", true)
	rebuild(t, store, pol)

	r, err := svc.Compute(Query{Metric: MetricReopen, AsOf: asOf, Projects: []string{"NONE"}})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if r.Ratio.DenCount != 0 || r.Ratio.Value != nil {
		t.Errorf("empty denominator must be undefined, got %+v", r.Ratio)
	}
}

// Flow occupancy is bounded to [0,1]; an issue with undefined lead time is excluded.
func TestFlowOccupancy_BoundsAndUndefinedLead(t *testing.T) {
	pol := factPolicy()
	store, svc := buildService(t, pol)
	database := store.db
	// F1: active [d3,d7), lead [d1,d7) => occupancy 4/6.
	upIssue(t, database, "F1", "PW", "Story", "Closed", "Done", day(1), day(7), "Done", true)
	cl(t, database, "F1-s1", "F1", "Backlog", "In Progress", day(3))
	cl(t, database, "F1-s2", "F1", "In Progress", "Closed", day(7))
	// F2: resolved == created => undefined lead, excluded (still delivered).
	upIssue(t, database, "F2", "PW", "Story", "Closed", "Done", day(5), day(5), "Done", true)
	rebuild(t, store, pol)

	r, err := svc.Compute(Query{Metric: MetricFlowOccupancy, AsOf: asOf})
	if err != nil {
		t.Fatalf("flow_occupancy: %v", err)
	}
	if r.Distribution.Count != 1 {
		t.Fatalf("occupancy sample count = %d, want 1 (undefined-lead excluded)", r.Distribution.Count)
	}
	if r.Excluded["undefined lead time"] != 1 {
		t.Errorf("undefined lead exclusion = %d, want 1", r.Excluded["undefined lead time"])
	}
	assertFloat(t, "occupancy p50", r.Distribution.P50, 4.0/6.0)
	if r.Distribution.Min < 0 || r.Distribution.Max > 1 {
		t.Errorf("occupancy must be bounded [0,1], got min=%v max=%v", r.Distribution.Min, r.Distribution.Max)
	}
	if r.Distribution.Unit != "ratio" {
		t.Errorf("occupancy unit = %q, want ratio", r.Distribution.Unit)
	}
}
