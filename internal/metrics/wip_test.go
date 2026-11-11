package metrics

import (
	"testing"
	"time"
)

func seriesCount(s []PeriodPoint, date string) int {
	for _, p := range s {
		if p.Period == date {
			return p.Count
		}
	}
	return -1
}

func dayKey(d int) string { return time.Date(2024, 1, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02") }

// WIP counts a transition on exactly one day (half-open), excludes backlog returns, and
// re-enters WIP for reopened/second active episodes.
func TestWIP_DailyBoundaryAndBacklogReturn(t *testing.T) {
	pol := factPolicy()
	store, svc := buildService(t, pol)
	database := store.db

	// A: Backlog -> In Progress (d3) -> Closed (d8).
	upIssue(t, database, "A", "PW", "Story", "Closed", "Done", day(1), day(8), "Done", true)
	cl(t, database, "A-s1", "A", "Backlog", "In Progress", day(3))
	cl(t, database, "A-s2", "A", "In Progress", "Closed", day(8))
	// B: In Progress (d2) -> Backlog (d4, return) -> In Progress (d6) -> Closed (d9).
	upIssue(t, database, "B", "PW", "Story", "Closed", "Done", day(1), day(9), "Done", true)
	cl(t, database, "B-s1", "B", "Backlog", "In Progress", day(2))
	cl(t, database, "B-s2", "B", "In Progress", "Backlog", day(4))
	cl(t, database, "B-s3", "B", "Backlog", "In Progress", day(6))
	cl(t, database, "B-s4", "B", "In Progress", "Closed", day(9))

	rebuild(t, store, pol)

	r, err := svc.Compute(Query{Metric: MetricWIP, AsOf: asOf, Range: &TimeRange{From: timeOf(1), To: timeOf(10)}})
	if err != nil {
		t.Fatalf("wip: %v", err)
	}
	want := map[int]int{1: 0, 2: 1, 3: 2, 4: 1, 5: 1, 6: 2, 7: 2, 8: 1, 9: 0, 10: 0}
	for d, w := range want {
		if got := seriesCount(r.Series, dayKey(d)); got != w {
			t.Errorf("WIP day %d = %d, want %d", d, got, w)
		}
	}
}

// WIP series advances to a later as_of with no source activity: an open active issue stays
// counted and the series extends.
func TestWIP_AsOfAdvances(t *testing.T) {
	pol := factPolicy()
	store, svc := buildService(t, pol)
	database := store.db
	upIssue(t, database, "O", "PW", "Story", "In Progress", "In Progress", day(1), "", "", true)
	cl(t, database, "O-s1", "O", "Backlog", "In Progress", day(2))

	// Build at day 5, query at day 5 and day 20.
	if _, err := NewBuilder(store, pol).BuildAll(timeOf(5), nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	early, err := svc.Compute(Query{Metric: MetricWIP, AsOf: timeOf(5)})
	if err != nil {
		t.Fatalf("wip early: %v", err)
	}
	late, err := svc.Compute(Query{Metric: MetricWIP, AsOf: timeOf(20)})
	if err != nil {
		t.Fatalf("wip late: %v", err)
	}
	if len(late.Series) <= len(early.Series) {
		t.Errorf("series must extend with as_of: early=%d late=%d", len(early.Series), len(late.Series))
	}
	if late.Series[len(late.Series)-1].Period != dayKey(20) {
		t.Errorf("last sample = %s, want %s", late.Series[len(late.Series)-1].Period, dayKey(20))
	}
	// Still exactly one open WIP issue at both as_ofs.
	if early.Eligible != 1 || late.Eligible != 1 {
		t.Errorf("reconstructed WIP = early %d / late %d, want 1/1", early.Eligible, late.Eligible)
	}
}

// Stage dwell distributes wip-stage time and advances open-interval dwell with as_of, while
// surfacing per-stage interval counts for all stages.
func TestStageDwell_AsOfAdvancesAndAllStages(t *testing.T) {
	pol := factPolicy()
	store, svc := buildService(t, pol)
	database := store.db
	// Open issue currently In Progress since day 3 (todo d1-d3, active d3-open).
	upIssue(t, database, "D", "PW", "Story", "In Progress", "In Progress", day(1), "", "", true)
	cl(t, database, "D-s1", "D", "Backlog", "In Progress", day(3))

	if _, err := NewBuilder(store, pol).BuildAll(timeOf(10), nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	early, err := svc.Compute(Query{Metric: MetricStageDwell, AsOf: timeOf(10)})
	if err != nil {
		t.Fatalf("stage_dwell early: %v", err)
	}
	late, err := svc.Compute(Query{Metric: MetricStageDwell, AsOf: timeOf(30)})
	if err != nil {
		t.Fatalf("stage_dwell late: %v", err)
	}
	if !(late.Distribution.Max > early.Distribution.Max) {
		t.Errorf("open wip dwell must advance with as_of: early max=%v late max=%v", early.Distribution.Max, late.Distribution.Max)
	}
	// All stages surfaced in Counts (todo + active).
	if early.Counts[StageTodo] != 1 || early.Counts[StageActive] != 1 {
		t.Errorf("per-stage counts = %v, want todo=1 active=1", early.Counts)
	}
	// active dwell day3->day10 = 7 days.
	if early.Distribution.Max != 7 {
		t.Errorf("active dwell at day10 = %v, want 7", early.Distribution.Max)
	}
}

// timeOf returns midnight UTC for 2024-01-<d> (d may exceed 31, rolling over).
func timeOf(d int) time.Time {
	return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, d-1)
}
