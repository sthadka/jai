package metrics

import (
	"testing"
	"time"

	"github.com/sthadka/jai/internal/config"
)

func buildService(t *testing.T, pol *Policy) (*Store, *Service) {
	t.Helper()
	database, store := newFactStore(t)
	return store, NewService(database, store, pol, nil)
}

// day returns an RFC3339 timestamp at midnight UTC for 2024-01-<d> (d may exceed 31; it
// rolls over via AddDate on Jan 1).
func day(d int) string {
	return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, d-1).Format(time.RFC3339)
}

// seedCycle creates a resolved, genuine-done issue whose active entry is at activeDay and
// whose resolved is at resolvedDay (both 2024-01-based), plus the backing changelog.
func seedCycle(t *testing.T, store *Store, key string, activeDay, resolvedDay int) {
	t.Helper()
	database := store.db
	upIssue(t, database, key, "PC", "Story", "Closed", "Done", day(1), day(resolvedDay), "Done", true)
	cl(t, database, key+"-a", key, "Backlog", "In Progress", day(activeDay))
	cl(t, database, key+"-b", key, "In Progress", "Closed", day(resolvedDay))
}

func rebuild(t *testing.T, store *Store, pol *Policy) {
	t.Helper()
	if _, err := NewBuilder(store, pol).BuildAll(asOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
}

// Cycle distribution over a 5-sample cohort checks the R-7 percentile boundaries on an odd
// count, and that each issue's envelope is populated.
func TestCycle_DistributionOddBoundaries(t *testing.T) {
	store, svc := buildService(t, factPolicy())
	// cycle days 1..5: active at day 10, resolved at day 10+k.
	for k := 1; k <= 5; k++ {
		seedCycle(t, store, "C-"+itoa(k), 10, 10+k)
	}
	rebuild(t, store, factPolicy())

	r, err := svc.Compute(Query{Metric: MetricCycleTime})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if r.Eligible != 5 || r.Distribution.Count != 5 {
		t.Fatalf("eligible/count = %d/%d, want 5/5", r.Eligible, r.Distribution.Count)
	}
	// sorted samples [1,2,3,4,5]: p50=3, p85=4.4, p95=4.8.
	assertFloat(t, "p50", r.Distribution.P50, 3)
	assertFloat(t, "p85", r.Distribution.P85, 4.4)
	assertFloat(t, "p95", r.Distribution.P95, 4.8)
	assertFloat(t, "min", r.Distribution.Min, 1)
	assertFloat(t, "max", r.Distribution.Max, 5)
	assertFloat(t, "mean", r.Distribution.Mean, 3)

	// Envelope.
	if r.Basis != BasisSnapshot {
		t.Errorf("basis = %q", r.Basis)
	}
	if r.Cohort != "completed (genuine-done)" {
		t.Errorf("cohort = %q", r.Cohort)
	}
	if !r.GenuineDone {
		t.Errorf("GenuineDone should be true")
	}
	if r.TaxonomyVersion != factPolicy().Fingerprint() {
		t.Errorf("taxonomy version mismatch")
	}
	if r.BuildGeneration == 0 || r.BuiltAt == nil {
		t.Errorf("build metadata not attached: gen=%d builtAt=%v", r.BuildGeneration, r.BuiltAt)
	}
}

// Lead distribution over an even count checks even-boundary interpolation.
func TestLead_EvenBoundary(t *testing.T) {
	store, svc := buildService(t, factPolicy())
	// created day1; resolved days 2,3,4,5 -> lead 1,2,3,4 days.
	for k := 2; k <= 5; k++ {
		seedCycle(t, store, "L-"+itoa(k), 2, k) // active day 2 (ensures first_active present)
	}
	rebuild(t, store, factPolicy())

	r, err := svc.Compute(Query{Metric: MetricLeadTime})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if r.Distribution.Count != 4 {
		t.Fatalf("count = %d, want 4", r.Distribution.Count)
	}
	// sorted lead [1,2,3,4]: p50 = 2.5.
	assertFloat(t, "p50", r.Distribution.P50, 2.5)
}

// An issue resolved genuine-done but with no active-stage entry is excluded as unknown and
// never folded into the distribution via a created->resolved fallback.
func TestCycle_NoActiveStartExcluded(t *testing.T) {
	store, svc := buildService(t, factPolicy())
	seedCycle(t, store, "OK-1", 10, 20) // valid, cycle 10 days

	// NA-1: Backlog straight to Closed, no In Progress.
	upIssue(t, store.db, "NA-1", "PC", "Story", "Closed", "Done", day(1), day(15), "Done", true)
	cl(t, store.db, "na-a", "NA-1", "Backlog", "Closed", day(15))
	rebuild(t, store, factPolicy())

	r, err := svc.Compute(Query{Metric: MetricCycleTime})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if r.Eligible != 1 {
		t.Fatalf("eligible = %d, want 1 (NA-1 excluded)", r.Eligible)
	}
	if r.Excluded["no_active_start"] != 1 {
		t.Errorf("no_active_start exclusion = %d, want 1", r.Excluded["no_active_start"])
	}
	// The unknown issue must not inflate the distribution (its lead would be 14 days).
	if r.Distribution.Max != 10 {
		t.Errorf("max = %v, want 10 (no created->resolved fallback)", r.Distribution.Max)
	}
}

// A resolved timestamp that precedes the first active entry is excluded as reversed.
func TestCycle_ReversedTimestampExcluded(t *testing.T) {
	store, svc := buildService(t, factPolicy())
	// first active at day 20, but resolved recorded at day 10 (reversed).
	upIssue(t, store.db, "RV-1", "PC", "Story", "Closed", "Done", day(1), day(10), "Done", true)
	cl(t, store.db, "rv-a", "RV-1", "Backlog", "In Progress", day(20))
	cl(t, store.db, "rv-b", "RV-1", "In Progress", "Closed", day(25))
	rebuild(t, store, factPolicy())

	r, err := svc.Compute(Query{Metric: MetricCycleTime})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if r.Eligible != 0 {
		t.Errorf("eligible = %d, want 0", r.Eligible)
	}
	if r.Excluded["reversed_timestamps"] != 1 {
		t.Errorf("reversed exclusion = %d, want 1", r.Excluded["reversed_timestamps"])
	}
	if len(r.Warnings) == 0 {
		t.Errorf("expected empty-cohort warning")
	}
}

// Omitted done_resolutions yields an all-resolved, labelled cohort and disables the
// genuine-only variant (RequireGenuine errors).
func TestDelivery_OmittedDoneResolutions(t *testing.T) {
	pol := CompilePolicy(&config.MetricsConfig{
		StatusStages: map[string][]string{"active": {"In Progress"}, "done": {"Closed"}},
		WIPStages:    []string{"active"},
	})
	store, svc := buildService(t, pol)
	// Two resolved issues with different resolutions; both count when done_resolutions unset.
	upIssue(t, store.db, "D-1", "PD", "Story", "Closed", "Done", day(1), day(5), "Done", true)
	cl(t, store.db, "d1a", "D-1", "Backlog", "In Progress", day(2))
	cl(t, store.db, "d1b", "D-1", "In Progress", "Closed", day(5))
	upIssue(t, store.db, "D-2", "PD", "Story", "Closed", "WontDo", day(1), day(6), "Won't Do", true)
	cl(t, store.db, "d2a", "D-2", "Backlog", "In Progress", day(2))
	cl(t, store.db, "d2b", "D-2", "In Progress", "Closed", day(6))
	rebuild(t, store, pol)

	r, err := svc.Compute(Query{Metric: MetricThroughput})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if r.GenuineDone {
		t.Errorf("GenuineDone should be false when done_resolutions unset")
	}
	if r.Cohort != "completed (includes non-delivery resolutions)" {
		t.Errorf("cohort label = %q", r.Cohort)
	}
	if r.Eligible != 2 {
		t.Errorf("eligible = %d, want 2 (all resolved)", r.Eligible)
	}

	_, err = svc.Compute(Query{Metric: MetricThroughput, RequireGenuine: true})
	if err != ErrGenuineDoneUnavailable {
		t.Errorf("RequireGenuine should error, got %v", err)
	}
}

// Throughput counts each issue once by current resolved; a later current-resolution change
// restates the snapshot rather than fabricating a second completion event.
func TestThroughput_SnapshotRestatement(t *testing.T) {
	store, svc := buildService(t, factPolicy())
	upIssue(t, store.db, "T-1", "PT", "Story", "Closed", "Done", day(1), "2024-01-15T00:00:00Z", "Done", true)
	cl(t, store.db, "t1a", "T-1", "Backlog", "In Progress", day(2))
	cl(t, store.db, "t1b", "T-1", "In Progress", "Closed", "2024-01-15T00:00:00Z")
	rebuild(t, store, factPolicy())

	r, _ := svc.Compute(Query{Metric: MetricThroughput})
	if len(r.Series) != 1 || r.Series[0].Period != "2024-01" || r.Series[0].Count != 1 {
		t.Fatalf("initial throughput = %+v, want 2024-01:1", r.Series)
	}

	// Current resolution changes to a non-delivery resolution and resolved moves to Feb.
	upIssue(t, store.db, "T-1", "PT", "Story", "Closed", "WontDo", day(1), "2024-02-15T00:00:00Z", "Won't Do", true)
	rebuild(t, store, factPolicy())

	r2, _ := svc.Compute(Query{Metric: MetricThroughput})
	if r2.Eligible != 0 {
		t.Errorf("after restatement eligible = %d, want 0 (now non-delivery, not a new event)", r2.Eligible)
	}
	if r2.Excluded["non_delivery_resolution"] != 1 {
		t.Errorf("expected 1 non_delivery exclusion, got %d", r2.Excluded["non_delivery_resolution"])
	}
}

// Range bounds throughput by resolved date; out-of-range completions are published as an
// exclusion, not silently dropped.
func TestThroughput_Range(t *testing.T) {
	store, svc := buildService(t, factPolicy())
	upIssue(t, store.db, "R-1", "PR", "Story", "Closed", "Done", day(1), "2024-01-15T00:00:00Z", "Done", true)
	cl(t, store.db, "r1a", "R-1", "Backlog", "In Progress", day(2))
	cl(t, store.db, "r1b", "R-1", "In Progress", "Closed", "2024-01-15T00:00:00Z")
	upIssue(t, store.db, "R-2", "PR", "Story", "Closed", "Done", day(1), "2024-03-15T00:00:00Z", "Done", true)
	cl(t, store.db, "r2a", "R-2", "Backlog", "In Progress", day(2))
	cl(t, store.db, "r2b", "R-2", "In Progress", "Closed", "2024-03-15T00:00:00Z")
	rebuild(t, store, factPolicy())

	rng := &TimeRange{From: tm("2024-01-01T00:00:00Z"), To: tm("2024-02-01T00:00:00Z")}
	r, _ := svc.Compute(Query{Metric: MetricThroughput, Range: rng})
	if r.Eligible != 1 {
		t.Errorf("eligible = %d, want 1 (Jan only)", r.Eligible)
	}
	if r.Excluded["out_of_range"] != 1 {
		t.Errorf("out_of_range = %d, want 1", r.Excluded["out_of_range"])
	}
}

// A non-snapshot basis is rejected, never silently downgraded.
func TestService_UnsupportedBasis(t *testing.T) {
	_, svc := buildService(t, factPolicy())
	if _, err := svc.Compute(Query{Metric: MetricCycleTime, Basis: BasisEvent}); err != ErrUnsupportedBasis {
		t.Errorf("expected ErrUnsupportedBasis, got %v", err)
	}
}

func TestService_UnknownMetric(t *testing.T) {
	_, svc := buildService(t, factPolicy())
	if _, err := svc.Compute(Query{Metric: "nope"}); err != ErrUnknownMetric {
		t.Errorf("expected ErrUnknownMetric, got %v", err)
	}
}

func assertFloat(t *testing.T, name string, got, want float64) {
	t.Helper()
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
