package metrics

import (
	"fmt"
	"sort"
)

// Reconcile outcomes.
const (
	OutcomeMatch    = "match"
	OutcomeMismatch = "mismatch"
	OutcomeUnknown  = "unknown"
)

// Snapshot is an issue's current status in the local `issues` table (the live snapshot the
// reconstruction is checked against).
type Snapshot struct {
	IssueKey       string
	Project        string
	StatusID       string
	Status         string
	StatusCategory string
}

// ReconcileRow is the per-issue reconciliation result with an explicit reason.
type ReconcileRow struct {
	IssueKey           string
	Project            string
	Outcome            string
	ReconstructedStage string
	SnapshotStage      string
	Reason             string
}

// ReconcileCounts tallies outcomes.
type ReconcileCounts struct {
	Match    int
	Mismatch int
	Unknown  int
}

// ReconcileReport is the correctness gate: reconstructed current status/WIP vs the live
// snapshot, under one project scope and config. Mismatches are surfaced, never dropped to
// manufacture agreement; a reconciled endpoint still does not prove complete history.
type ReconcileReport struct {
	Rows      []ReconcileRow // sorted by (project, issue key)
	Counts    ReconcileCounts
	ByProject map[string]ReconcileCounts
	// Trusted is true only when there are zero mismatches. Unknown cohorts lower
	// confidence (caveats preserved) but a single mismatch forbids a trusted-series claim.
	Trusted  bool
	Caveats  []string
	Coverage *CoverageReport // optional: published alongside (M02), set by the caller
}

// Reconcile compares reconstructed current stages (from status replay) to the live issues
// snapshot under policy pol. Matching is by issue key; reconstructed/snapshot stages use
// the same precedence (ID > name > category > unknown). Either side being unknown yields
// an unknown cohort (not a forced match); differing known stages yield a visible mismatch.
func Reconcile(results []ReplayResult, snapshots []Snapshot, pol *Policy) *ReconcileReport {
	snapByKey := make(map[string]Snapshot, len(snapshots))
	for _, s := range snapshots {
		snapByKey[s.IssueKey] = s
	}

	report := &ReconcileReport{ByProject: map[string]ReconcileCounts{}}
	seen := make(map[string]bool, len(results))

	for _, res := range results {
		seen[res.IssueKey] = true
		recon := res.CurrentStage
		if recon == "" {
			recon = StageUnknown
		}
		snap, ok := snapByKey[res.IssueKey]
		row := ReconcileRow{IssueKey: res.IssueKey, Project: res.Project, ReconstructedStage: recon}
		if !ok {
			row.Outcome = OutcomeUnknown
			row.SnapshotStage = StageUnknown
			row.Reason = "no current snapshot for reconstructed issue"
			report.add(row)
			continue
		}
		if row.Project == "" {
			row.Project = snap.Project
		}
		snapStage := pol.StageFor(snap.StatusID, snap.Status, snap.StatusCategory).Stage
		row.SnapshotStage = snapStage

		switch {
		case recon == StageUnknown:
			row.Outcome = OutcomeUnknown
			row.Reason = "reconstructed stage unknown"
		case snapStage == StageUnknown:
			row.Outcome = OutcomeUnknown
			row.Reason = "snapshot stage unknown (missing status category / unmapped status)"
		case recon == snapStage:
			row.Outcome = OutcomeMatch
		default:
			row.Outcome = OutcomeMismatch
			row.Reason = fmt.Sprintf("reconstructed %s != snapshot %s", recon, snapStage)
		}
		report.add(row)
	}

	// Snapshots with no reconstructed history are unknown cohorts, kept visible.
	for _, s := range snapshots {
		if seen[s.IssueKey] {
			continue
		}
		report.add(ReconcileRow{
			IssueKey:           s.IssueKey,
			Project:            s.Project,
			Outcome:            OutcomeUnknown,
			ReconstructedStage: StageUnknown,
			SnapshotStage:      pol.StageFor(s.StatusID, s.Status, s.StatusCategory).Stage,
			Reason:             "no reconstructed history for snapshot issue",
		})
	}

	sort.SliceStable(report.Rows, func(i, j int) bool {
		if report.Rows[i].Project != report.Rows[j].Project {
			return report.Rows[i].Project < report.Rows[j].Project
		}
		return report.Rows[i].IssueKey < report.Rows[j].IssueKey
	})

	report.Trusted = report.Counts.Mismatch == 0
	report.Caveats = reconcileCaveats(report)
	return report
}

func (r *ReconcileReport) add(row ReconcileRow) {
	r.Rows = append(r.Rows, row)
	c := r.ByProject[row.Project]
	switch row.Outcome {
	case OutcomeMatch:
		r.Counts.Match++
		c.Match++
	case OutcomeMismatch:
		r.Counts.Mismatch++
		c.Mismatch++
	case OutcomeUnknown:
		r.Counts.Unknown++
		c.Unknown++
	}
	r.ByProject[row.Project] = c
}

func reconcileCaveats(r *ReconcileReport) []string {
	caveats := []string{
		"a reconciled current endpoint does not prove complete history (old or permission-filtered changes may be missing)",
		"snapshot synchronization freshness is local, not live-Jira freshness",
	}
	if r.Counts.Mismatch > 0 {
		caveats = append(caveats, fmt.Sprintf("%d mismatch(es) present: the reconstructed series is NOT trusted", r.Counts.Mismatch))
	}
	if r.Counts.Unknown > 0 {
		caveats = append(caveats, fmt.Sprintf("%d issue(s) in an unknown cohort: counts and caveats preserved, not reconciled away", r.Counts.Unknown))
	}
	return caveats
}
