package metrics

import (
	"strconv"
	"testing"
	"time"
)

// Phase-1 generalization gate (bead jai-f5b / M12).
//
// Before Phase 2 (generalized scalar/set replay + extended metrics) may begin, the
// Phase-1 pipeline must be proven end-to-end on a representative, safely bounded cohort:
// build facts from the real DB → read them back through the shared Service → and prove the
// reconstructed current WIP reconciles against the live `issues` snapshot. This is the
// correctness gate; the measured runtime for a representative CLI/view/service result is
// the perf gate (it guards against an accidental Cartesian join or a dropped timeout —
// both would blow a generous ceiling long before they'd ship).
//
// The numbers this test prints are recorded in docs/metrics-feasibility.md so the gate is
// documented before generalization. The budgets are deliberately generous (≈ an order of
// magnitude of headroom over the observed runtime) so the gate flags a catastrophic
// regression without flaking in CI.

// gateCohortSize is the representative, safely bounded synthetic cohort. Large enough that
// a quadratic read path or a missing index would be obvious, small enough to stay fast and
// deterministic in `make check`.
const gateCohortSize = 3000

// Generous end-to-end budgets (see note above). Catches O(n^2) / Cartesian regressions,
// not microbenchmark drift.
const (
	gateBuildBudget   = 30 * time.Second
	gateComputeBudget = 10 * time.Second
)

var gateProjects = []string{"GA", "GB", "GC"}

// gateAsOf is the fixed reference the open intervals and aged cohorts are computed against.
// Deterministic (never time.Now) so the gate is reproducible.
var gateAsOf = time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

// seedGateCohort inserts a deterministic, history-complete cohort whose current snapshot is
// consistent with its reconstructed status (every issue reconciles as a match). The mix
// exercises all three stages and both replay paths (seeded never-changed todo issues, and
// multi-event active/done issues).
func seedGateCohort(t *testing.T, store *Store) {
	t.Helper()
	database := store.db
	for i := range gateCohortSize {
		key := "G-" + strconv.Itoa(i)
		project := gateProjects[i%len(gateProjects)]
		created := day(1 + i%20)
		switch i % 3 {
		case 0: // todo: never changed, provably complete history.
			upIssue(t, database, key, project, "Story", "Backlog", "To Do", created, "", "", true)
		case 1: // active: moved into WIP, still open.
			upIssue(t, database, key, project, "Story", "In Progress", "In Progress", created, "", "", true)
			cl(t, database, key+"-a", key, "Backlog", "In Progress", day(10+i%30))
		case 2: // done: full lifecycle, resolved genuine-done.
			active := 10 + i%30
			resolved := active + 1 + i%25
			upIssue(t, database, key, project, "Story", "Closed", "Done", created, day(resolved), "Done", true)
			cl(t, database, key+"-a", key, "Backlog", "In Progress", day(active))
			cl(t, database, key+"-b", key, "In Progress", "Closed", day(resolved))
		}
	}
}

// reconcileFromDB reconstructs each issue's current stage from its DB status history and
// reconciles it against the DB snapshot — the same read path BuildAll uses, so this is a
// genuine end-to-end check, not a replay of in-memory test inputs.
func reconcileFromDB(t *testing.T, store *Store, pol *Policy) *ReconcileReport {
	t.Helper()
	sources, _, err := NewBuilder(store, pol).loadSources(nil, nil)
	if err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	results := make([]ReplayResult, 0, len(sources))
	snaps := make([]Snapshot, 0, len(sources))
	for _, src := range sources {
		results = append(results, ReplayStatus(src.hist, pol, gateAsOf))
		snaps = append(snaps, Snapshot{
			IssueKey:       src.hist.Key,
			Project:        src.hist.Project,
			Status:         src.hist.CurrentStatus,
			StatusCategory: src.hist.CurrentStatusCategory,
		})
	}
	return Reconcile(results, snaps, pol)
}

// TestGate_EndToEndReconciledBenchmark is the Phase-1 generalization gate. It runs the full
// fact pipeline on a representative cohort, reads a representative metric back through the
// shared Service, and asserts the reconstructed current WIP reconciles end-to-end.
func TestGate_EndToEndReconciledBenchmark(t *testing.T) {
	database, store := newFactStore(t)
	pol := factPolicy()
	if err := pol.Err(); err != nil {
		t.Fatalf("gate policy rejected: %v", err)
	}

	seedGateCohort(t, store)

	// --- Correctness + perf gate: build facts (full restatement) ---
	buildStart := time.Now()
	buildRes, err := NewBuilder(store, pol).BuildAll(gateAsOf, nil)
	if err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	buildDur := time.Since(buildStart)
	if buildRes.IssueCount != gateCohortSize {
		t.Fatalf("built issue count = %d, want %d", buildRes.IssueCount, gateCohortSize)
	}
	if buildDur > gateBuildBudget {
		t.Fatalf("BuildAll took %s, over budget %s (possible Cartesian join / missing index)", buildDur, gateBuildBudget)
	}

	// --- Representative CLI/view/service results through the one shared Service ---
	svc := NewService(database, store, pol, nil)
	computeStart := time.Now()
	cycle, err := svc.Compute(Query{Metric: MetricCycleTime, AsOf: gateAsOf})
	if err != nil {
		t.Fatalf("Compute cycle: %v", err)
	}
	throughput, err := svc.Compute(Query{Metric: MetricThroughput, AsOf: gateAsOf})
	if err != nil {
		t.Fatalf("Compute throughput: %v", err)
	}
	netflow, err := svc.Compute(Query{Metric: MetricNetFlow, AsOf: gateAsOf})
	if err != nil {
		t.Fatalf("Compute net-flow: %v", err)
	}
	aged, err := svc.Compute(Query{Metric: MetricAgedOpen, AsOf: gateAsOf})
	if err != nil {
		t.Fatalf("Compute aged-open: %v", err)
	}
	computeDur := time.Since(computeStart)
	if computeDur > gateComputeBudget {
		t.Fatalf("Service.Compute x4 took %s, over budget %s", computeDur, gateComputeBudget)
	}

	// Representative output counts: a third of the cohort is genuine-done (cycle-eligible),
	// a third is open-active, a third is todo. Fact-based metrics must carry a generation.
	wantDone := gateCohortSize / 3
	if cycle.Distribution == nil || cycle.Distribution.Count != wantDone {
		t.Fatalf("cycle distribution count = %v, want %d", cycle.Distribution, wantDone)
	}
	if cycle.BuildGeneration != buildRes.Generation {
		t.Fatalf("cycle build generation = %d, want %d", cycle.BuildGeneration, buildRes.Generation)
	}
	var throughputTotal int
	for _, p := range throughput.Series {
		throughputTotal += p.Count
	}
	if throughputTotal != wantDone {
		t.Fatalf("throughput total = %d, want %d", throughputTotal, wantDone)
	}

	// --- The reconciliation gate: reconstructed current WIP vs the live snapshot ---
	rep := reconcileFromDB(t, store, pol)
	if !rep.Trusted {
		t.Fatalf("reconcile not trusted: counts=%+v caveats=%v", rep.Counts, rep.Caveats)
	}
	if rep.Counts.Mismatch != 0 || rep.Counts.Unknown != 0 {
		t.Fatalf("reconcile counts = %+v, want mismatch=0 unknown=0", rep.Counts)
	}
	if rep.Counts.Match != gateCohortSize {
		t.Fatalf("reconcile match = %d, want %d (every issue reconciled)", rep.Counts.Match, gateCohortSize)
	}
	for _, proj := range gateProjects {
		if c := rep.ByProject[proj]; c.Mismatch != 0 || c.Unknown != 0 {
			t.Errorf("project %s reconcile = %+v, want no drift", proj, c)
		}
	}

	t.Logf("GATE cohort=%d projects=%d intervals=%d | BuildAll=%s Compute(cycle+throughput+netflow+aged)=%s | reconcile match=%d mismatch=%d unknown=%d trusted=%v | cycle.p50=%.1fd throughput_total=%d netflow=%v aged=%d",
		gateCohortSize, len(gateProjects), buildRes.IntervalCount,
		buildDur.Round(time.Millisecond), computeDur.Round(time.Millisecond),
		rep.Counts.Match, rep.Counts.Mismatch, rep.Counts.Unknown, rep.Trusted,
		cycle.Distribution.P50, throughputTotal, netflow.Counts, aged.Eligible)
}

// TestGate_ReconcileCatchesDrift proves the gate has teeth: a single drifted snapshot (its
// current status diverges from its reconstructed history) must surface as a visible
// mismatch and forbid a trusted series. A gate that could never fail would not gate.
func TestGate_ReconcileCatchesDrift(t *testing.T) {
	_, store := newFactStore(t)
	pol := factPolicy()
	seedGateCohort(t, store)

	// Drift one done issue's snapshot back to an active status without a backing event.
	if _, err := store.db.Exec(`UPDATE issues SET status = ?, status_category = ?, resolved = '', resolution = '' WHERE key = ?`,
		"In Progress", "In Progress", "G-2"); err != nil {
		t.Fatalf("drift update: %v", err)
	}

	rep := reconcileFromDB(t, store, pol)
	if rep.Trusted {
		t.Fatal("reconcile reported trusted despite injected drift")
	}
	if rep.Counts.Mismatch != 1 {
		t.Fatalf("mismatch count = %d, want 1", rep.Counts.Mismatch)
	}
	var found bool
	for _, row := range rep.Rows {
		if row.IssueKey == "G-2" {
			found = true
			if row.Outcome != OutcomeMismatch {
				t.Errorf("G-2 outcome = %q, want %q", row.Outcome, OutcomeMismatch)
			}
		}
	}
	if !found {
		t.Fatal("drifted issue G-2 absent from reconcile rows")
	}
}
