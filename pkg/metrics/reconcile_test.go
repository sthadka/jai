package metrics

import (
	"testing"
)

func snapshotFromCase(c fixtureCase) Snapshot {
	return Snapshot{
		IssueKey:       c.Name,
		Project:        c.Project,
		StatusID:       c.Current.StatusID,
		Status:         c.Current.Status,
		StatusCategory: c.Current.StatusCategory,
	}
}

func reconcileFixture(t *testing.T) (*fixtureDoc, *ReconcileReport) {
	t.Helper()
	doc := loadFlowCases(t)
	pol := policyFromFixture(doc.Config)
	asOf := ftime(t, doc.AsOf)

	var results []ReplayResult
	var snaps []Snapshot
	for _, c := range doc.Cases {
		results = append(results, ReplayStatus(issueFromCase(t, c), pol, asOf))
		snaps = append(snaps, snapshotFromCase(c))
	}
	return doc, Reconcile(results, snaps, pol)
}

// Exact aggregate and per-project counts against the M00 fixtures.
func TestReconcile_FixtureCounts(t *testing.T) {
	doc, rep := reconcileFixture(t)
	want := doc.ReconcileSummary

	if rep.Counts.Match != want.Match || rep.Counts.Mismatch != want.Mismatch || rep.Counts.Unknown != want.Unknown {
		t.Fatalf("counts = %+v, want match=%d mismatch=%d unknown=%d", rep.Counts, want.Match, want.Mismatch, want.Unknown)
	}
	if rep.Trusted != want.Trusted {
		t.Errorf("trusted = %v, want %v", rep.Trusted, want.Trusted)
	}
	for proj, wc := range want.ByProject {
		gc := rep.ByProject[proj]
		if gc.Match != wc.Match || gc.Mismatch != wc.Mismatch || gc.Unknown != wc.Unknown {
			t.Errorf("project %s counts = %+v, want %+v", proj, gc, wc)
		}
	}
}

// Each case's reconcile outcome and reason match the fixture.
func TestReconcile_PerCaseOutcomes(t *testing.T) {
	doc, rep := reconcileFixture(t)
	byKey := map[string]ReconcileRow{}
	for _, r := range rep.Rows {
		byKey[r.IssueKey] = r
	}
	for _, c := range doc.Cases {
		row, ok := byKey[c.Name]
		if !ok {
			t.Errorf("%s: no reconcile row", c.Name)
			continue
		}
		if row.Outcome != c.Expected.Reconcile.Outcome {
			t.Errorf("%s: outcome = %q, want %q (reason %q)", c.Name, row.Outcome, c.Expected.Reconcile.Outcome, row.Reason)
		}
		if c.Expected.Reconcile.Reason != "" && row.Reason != c.Expected.Reconcile.Reason {
			t.Errorf("%s: reason = %q, want %q", c.Name, row.Reason, c.Expected.Reconcile.Reason)
		}
	}
}

// The deliberate mismatch stays visible and forbids a trusted series.
func TestReconcile_MismatchVisibleAndBlocksTrust(t *testing.T) {
	_, rep := reconcileFixture(t)
	if rep.Trusted {
		t.Fatal("report must not be trusted when a mismatch exists")
	}
	var found bool
	for _, r := range rep.Rows {
		if r.Outcome == OutcomeMismatch {
			found = true
			if r.Reason == "" {
				t.Error("mismatch row must carry a reason")
			}
		}
	}
	if !found {
		t.Fatal("expected the deliberate mismatch to remain visible")
	}
	var mentionsMismatch bool
	for _, cv := range rep.Caveats {
		if contains(cv, "NOT trusted") {
			mentionsMismatch = true
		}
	}
	if !mentionsMismatch {
		t.Error("caveats must state the series is not trusted")
	}
}

// A clean, complete cohort reconciles exactly and is trusted.
func TestReconcile_CleanCohortTrusted(t *testing.T) {
	doc := loadFlowCases(t)
	pol := policyFromFixture(doc.Config)
	asOf := ftime(t, doc.AsOf)

	var results []ReplayResult
	var snaps []Snapshot
	for _, c := range doc.Cases {
		// only the matching cases
		if c.Expected.Reconcile.Outcome != OutcomeMatch {
			continue
		}
		results = append(results, ReplayStatus(issueFromCase(t, c), pol, asOf))
		snaps = append(snaps, snapshotFromCase(c))
	}
	rep := Reconcile(results, snaps, pol)
	if !rep.Trusted || rep.Counts.Mismatch != 0 || rep.Counts.Unknown != 0 {
		t.Fatalf("clean cohort should reconcile exactly: %+v trusted=%v", rep.Counts, rep.Trusted)
	}
	if rep.Counts.Match != len(results) {
		t.Errorf("match=%d, want %d", rep.Counts.Match, len(results))
	}
}

// Unknown cohorts are preserved (never reconciled away) with caveats.
func TestReconcile_UnknownPreserved(t *testing.T) {
	_, rep := reconcileFixture(t)
	if rep.Counts.Unknown == 0 {
		t.Fatal("expected at least one unknown cohort in fixtures")
	}
	var mentionsUnknown bool
	for _, cv := range rep.Caveats {
		if contains(cv, "unknown cohort") {
			mentionsUnknown = true
		}
	}
	if !mentionsUnknown {
		t.Error("caveats must preserve unknown-cohort note")
	}
}

// Missing status category on the snapshot surfaces as an unknown cohort, not a match.
func TestReconcile_SnapshotMissingCategoryUnknown(t *testing.T) {
	pol := CompilePolicy(nil) // category-only policy
	results := []ReplayResult{{IssueKey: "X-1", Project: "P", CurrentStage: StageActive}}
	snaps := []Snapshot{{IssueKey: "X-1", Project: "P", Status: "WeirdStatus", StatusCategory: ""}}
	rep := Reconcile(results, snaps, pol)
	if rep.Rows[0].Outcome != OutcomeUnknown {
		t.Fatalf("missing category -> unknown, got %q", rep.Rows[0].Outcome)
	}
}

// Snapshot without reconstructed history, and history without snapshot, stay visible.
func TestReconcile_UnmatchedSides(t *testing.T) {
	pol := policyFromFixture(loadFlowCases(t).Config)

	// reconstructed but no snapshot
	rep1 := Reconcile([]ReplayResult{{IssueKey: "A", Project: "P", CurrentStage: StageActive}}, nil, pol)
	if rep1.Rows[0].Outcome != OutcomeUnknown || !contains(rep1.Rows[0].Reason, "no current snapshot") {
		t.Errorf("reconstructed-without-snapshot: %+v", rep1.Rows[0])
	}
	// snapshot but no reconstructed history
	rep2 := Reconcile(nil, []Snapshot{{IssueKey: "B", Project: "P", Status: "In Progress"}}, pol)
	if rep2.Rows[0].Outcome != OutcomeUnknown || !contains(rep2.Rows[0].Reason, "no reconstructed history") {
		t.Errorf("snapshot-without-history: %+v", rep2.Rows[0])
	}
}
