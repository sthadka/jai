package metrics

// no additional imports

// Assignee workload metrics. These attribute load and throughput to owners using event-time
// assignee intervals (reconstructed by scalar replay into fact_field_interval) joined to the
// flow/issue facts — never by copying today's assignee onto old intervals. They report item
// load, not effort, capacity, or individual productivity, and keep unknown/unassigned owners
// and unknown status categories explicitly visible.
//
// The current-owner open load lives in snapshot.go (MetricWorkload). Here we add the
// event-time attributions M15 is responsible for.
const (
	// MetricWorkloadThroughput counts completed items per owner-at-completion: the owner in
	// effect (event-time) at each issue's completion instant. A later reassignment does NOT
	// move the completion between owners.
	MetricWorkloadThroughput = "workload_throughput"
	// MetricWorkloadThroughputCurrent counts completed items per CURRENT owner (snapshot). A
	// reassignment after completion DOES move the completion between owners here. Offered
	// alongside owner-at-completion so the attribution choice is explicit, never implicit.
	MetricWorkloadThroughputCurrent = "workload_throughput_current"
	// MetricWorkloadTimeOnOwner sums event-time days each owner has held issues, aging open
	// intervals to the query as_of (advances without any source activity).
	MetricWorkloadTimeOnOwner = "workload_time_on_owner"
	// MetricReassignmentThrash is the per-issue distribution of assignee changes (event-time).
	MetricReassignmentThrash = "reassignment_thrash"
)

func init() {
	Register(workloadThroughputProvider{})
	Register(workloadThroughputCurrentProvider{})
	Register(timeOnOwnerProvider{})
	Register(reassignmentThrashProvider{})
}

// assigneeField is the changelog/tracked-field name whose intervals carry owner-over-time.
const assigneeField = "assignee"

// workloadThroughputProvider attributes each completed issue to its owner-at-completion,
// read from the assignee interval in effect at the completion instant (event-time). Issues
// whose owner at completion cannot be recovered (no covering/known assignee interval) are
// bucketed as unknown, never back-filled from the current owner.
type workloadThroughputProvider struct{}

func (workloadThroughputProvider) Metric() string { return MetricWorkloadThroughput }

func (workloadThroughputProvider) Compute(s *Service, q Query) (*Result, error) {
	rows, err := s.flowRows(q)
	if err != nil {
		return nil, err
	}
	intervals, err := s.store.Intervals(q.Projects, assigneeField)
	if err != nil {
		return nil, err
	}
	ix := NewIntervalIndex(intervals)

	r := s.newResult(q)
	if err := s.attachBuild(r); err != nil {
		return nil, err
	}
	delivered, nonDelivery, notCompleted, genuine, cohort := s.deliveredCohort(rows)
	r.GenuineDone = genuine
	r.Cohort = cohort + "; by owner-at-completion (event-time)"
	r.exclude("not completed", notCompleted)
	r.exclude("non-delivery resolution", nonDelivery)
	kept, outOfRange := rangeFilter(delivered, q)
	r.exclude("resolved out of range", outOfRange)

	r.Counts = map[string]int{}
	for _, fr := range kept {
		r.Eligible++
		owner, _ := ix.OwnerAt(fr.IssueKey, *fr.Resolved)
		r.Counts[owner]++
	}
	if r.Counts[unknownOwnerBucket] > 0 {
		r.warn("owner-at-completion unknown for some completions (no assignee history covering completion); surfaced, never current-copied")
	}
	return r, nil
}

// workloadThroughputCurrentProvider attributes each completed issue to its CURRENT owner
// (issues snapshot). This runs issues-only (no fact generation needed) and is the explicit
// current-owner counterpart to owner-at-completion attribution.
type workloadThroughputCurrentProvider struct{}

func (workloadThroughputCurrentProvider) Metric() string { return MetricWorkloadThroughputCurrent }

func (workloadThroughputCurrentProvider) Compute(s *Service, q Query) (*Result, error) {
	issues, err := s.loadIssues(q.Projects)
	if err != nil {
		return nil, err
	}
	r := s.newResult(q)
	genuine := s.pol.DoneResolutionsConfigured()
	r.GenuineDone = genuine
	if genuine {
		r.Cohort = "completed (genuine-done); by current owner (snapshot)"
	} else {
		r.Cohort = "completed (includes non-delivery resolutions); by current owner (snapshot)"
	}
	r.Counts = map[string]int{}
	var notCompleted, nonDelivery, outOfRange int
	for _, it := range issues {
		if it.resolved == nil {
			notCompleted++
			continue
		}
		if genuine && !s.pol.IsDoneResolution(it.resolution) {
			nonDelivery++
			continue
		}
		if q.Range != nil && !q.Range.contains(*it.resolved) {
			outOfRange++
			continue
		}
		r.Counts[NormalizeOwner("", it.assignee)]++
		r.Eligible++
	}
	r.exclude("not completed", notCompleted)
	r.exclude("non-delivery resolution", nonDelivery)
	r.exclude("resolved out of range", outOfRange)
	return r, nil
}

// timeOnOwnerProvider sums the event-time days each owner has held issues across the
// assignee intervals, aging open intervals to the query as_of. Unknown-history gaps are
// bucketed explicitly. Counts are days held, not item counts — stated in the cohort label.
type timeOnOwnerProvider struct{}

func (timeOnOwnerProvider) Metric() string { return MetricWorkloadTimeOnOwner }

func (timeOnOwnerProvider) Compute(s *Service, q Query) (*Result, error) {
	intervals, err := s.store.Intervals(q.Projects, assigneeField)
	if err != nil {
		return nil, err
	}
	r := s.newResult(q)
	if err := s.attachBuild(r); err != nil {
		return nil, err
	}
	asOf := s.asOf(q)
	r.Cohort = "time-on-owner (event-time days held; open intervals aged to as_of)"
	r.Counts = map[string]int{}
	for _, iv := range intervals {
		end := asOf
		if iv.ExitedAt != nil {
			end = *iv.ExitedAt
		}
		days := int(end.Sub(iv.EnteredAt).Hours() / 24)
		if days < 0 {
			days = 0
		}
		if iv.Provenance == ProvUnknown {
			r.Counts[unknownOwnerBucket] += days
			continue
		}
		r.Counts[NormalizeOwner(iv.ValueID, iv.Value)] += days
		r.Eligible++
	}
	return r, nil
}

// reassignmentThrashProvider reports the per-issue distribution of assignee changes. An
// issue's change count is the number of assignee intervals beyond the first (each recorded
// boundary is one change); a never-reassigned issue contributes 0.
type reassignmentThrashProvider struct{}

func (reassignmentThrashProvider) Metric() string { return MetricReassignmentThrash }

func (reassignmentThrashProvider) Compute(s *Service, q Query) (*Result, error) {
	intervals, err := s.store.Intervals(q.Projects, assigneeField)
	if err != nil {
		return nil, err
	}
	ix := NewIntervalIndex(intervals)
	r := s.newResult(q)
	if err := s.attachBuild(r); err != nil {
		return nil, err
	}
	r.Cohort = "reassignment thrash (assignee changes per issue, event-time)"
	var samples []float64
	for _, key := range ix.Keys() {
		changes := len(ix.Intervals(key)) - 1
		if changes < 0 {
			changes = 0
		}
		samples = append(samples, float64(changes))
		r.Eligible++
	}
	r.Distribution = distribution(samples)
	r.Distribution.Unit = "changes"
	return r, nil
}
