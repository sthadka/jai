package metrics

import (
	"fmt"
	"sort"
	"time"
)

// Delivery metric names.
const (
	MetricCycleTime  = "cycle_time"
	MetricLeadTime   = "lead_time"
	MetricThroughput = "throughput"
)

func init() {
	Register(cycleTimeProvider{})
	Register(leadTimeProvider{})
	Register(throughputProvider{})
}

// requireGenuine enforces RequireGenuine against the policy: when a caller demands the
// genuine-done variant but done_resolutions is unset, the variant is disabled, not guessed.
func requireGenuine(s *Service, q Query) error {
	if q.RequireGenuine && !s.pol.DoneResolutionsConfigured() {
		return ErrGenuineDoneUnavailable
	}
	return nil
}

// rangeFilter keeps only delivered rows whose Resolved falls in q.Range, returning the
// kept rows and the out-of-range count (published as an exclusion). A nil range keeps all.
func rangeFilter(delivered []FlowRow, q Query) (kept []FlowRow, outOfRange int) {
	if q.Range == nil {
		return delivered, 0
	}
	for i := range delivered {
		if delivered[i].Resolved != nil && q.Range.contains(*delivered[i].Resolved) {
			kept = append(kept, delivered[i])
		} else {
			outOfRange++
		}
	}
	return kept, outOfRange
}

// deliveredResult builds the common delivery envelope: applies the shared completion
// policy, publishes the not-completed / non-delivery / out-of-range exclusions, and returns
// the in-range delivered cohort. All three delivery metrics share this so their cohorts and
// genuine-done policy are identical.
func deliveredResult(s *Service, q Query) (*Result, []FlowRow, error) {
	if err := requireGenuine(s, q); err != nil {
		return nil, nil, err
	}
	rows, err := s.flowRows(q)
	if err != nil {
		return nil, nil, err
	}
	r := s.newResult(q)
	if err := s.attachBuild(r); err != nil {
		return nil, nil, err
	}
	delivered, nonDelivery, notCompleted, genuine, cohort := s.deliveredCohort(rows)
	r.GenuineDone = genuine
	r.Cohort = cohort
	r.exclude("not_completed", notCompleted)
	if genuine {
		r.exclude("non_delivery_resolution", nonDelivery)
	}
	kept, outOfRange := rangeFilter(delivered, q)
	r.exclude("out_of_range", outOfRange)
	return r, kept, nil
}

// cycleTimeProvider computes the cycle-time distribution: first active-stage entry ->
// current resolved, over the genuine-done cohort. Issues resolved without any active-stage
// entry stay unknown (excluded, never folded in via a created->resolved fallback, which
// would be lead time and silently inflate percentiles).
type cycleTimeProvider struct{}

func (cycleTimeProvider) Metric() string { return MetricCycleTime }

func (cycleTimeProvider) Compute(s *Service, q Query) (*Result, error) {
	r, delivered, err := deliveredResult(s, q)
	if err != nil {
		return nil, err
	}
	var samples []float64
	var noActive, reversed int
	for i := range delivered {
		fr := delivered[i]
		if fr.FirstActiveAt == nil {
			noActive++ // unknown cycle time; reported as a count, never created->resolved
			continue
		}
		if fr.CycleSeconds == nil {
			reversed++ // resolved precedes first active entry
			continue
		}
		samples = append(samples, float64(*fr.CycleSeconds)/86400)
	}
	r.exclude("no_active_start", noActive)
	r.exclude("reversed_timestamps", reversed)
	r.Distribution = distribution(samples)
	r.Eligible = len(samples)
	if r.Eligible == 0 {
		r.warn("empty cohort: no eligible cycle-time samples")
	}
	return r, nil
}

// leadTimeProvider computes the lead-time distribution: created -> current resolved, over
// the genuine-done cohort, requiring resolved >= created.
type leadTimeProvider struct{}

func (leadTimeProvider) Metric() string { return MetricLeadTime }

func (leadTimeProvider) Compute(s *Service, q Query) (*Result, error) {
	r, delivered, err := deliveredResult(s, q)
	if err != nil {
		return nil, err
	}
	var samples []float64
	var missingCreated, reversed int
	for i := range delivered {
		fr := delivered[i]
		if fr.Created == nil {
			missingCreated++
			continue
		}
		if fr.LeadSeconds == nil {
			reversed++ // resolved precedes created
			continue
		}
		samples = append(samples, float64(*fr.LeadSeconds)/86400)
	}
	r.exclude("missing_created", missingCreated)
	r.exclude("reversed_timestamps", reversed)
	r.Distribution = distribution(samples)
	r.Eligible = len(samples)
	if r.Eligible == 0 {
		r.warn("empty cohort: no eligible lead-time samples")
	}
	return r, nil
}

// throughputProvider counts completed issues per period, each issue once by its current
// resolved/resolution (snapshot semantics). A later current-resolution change restates the
// snapshot; it never fabricates a historical completion event.
type throughputProvider struct{}

func (throughputProvider) Metric() string { return MetricThroughput }

func (throughputProvider) Compute(s *Service, q Query) (*Result, error) {
	r, delivered, err := deliveredResult(s, q)
	if err != nil {
		return nil, err
	}
	period := q.Period
	if period == "" {
		period = "month"
	}
	buckets := map[string]int{}
	order := []string{}
	for i := range delivered {
		key := periodKey(*delivered[i].Resolved, period)
		if _, seen := buckets[key]; !seen {
			order = append(order, key)
		}
		buckets[key]++
		r.Eligible++
	}
	sort.Strings(order)
	for _, k := range order {
		r.Series = append(r.Series, PeriodPoint{Period: k, Count: buckets[k]})
	}
	r.Policies["period"] = period
	if r.Eligible == 0 {
		r.warn("empty cohort: no completed issues in range")
	}
	return r, nil
}

// periodKey buckets a timestamp by period. month -> "2006-01"; week -> ISO "2006-Www".
func periodKey(t time.Time, period string) string {
	t = t.UTC()
	if period == "week" {
		y, w := t.ISOWeek()
		return isoWeekKey(y, w)
	}
	return t.Format("2006-01")
}

func isoWeekKey(year, week int) string {
	return fmt.Sprintf("%04d-W%02d", year, week)
}
