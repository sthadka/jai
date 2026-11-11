package metrics

import (
	"strconv"
	"time"
)

// Work-in-progress and stage-dwell metrics, reconstructed on demand from status interval
// facts (no fact_wip_daily table — M12 found a materialized daily cube unjustified until
// profiling names the dimensions). Both sample the event-time status intervals through an
// IntervalIndex (the M15 temporal join): WIP counts issues in a wip_stage on each daily
// UTC-midnight boundary; stage dwell distributes time-in-stage with open intervals aged to
// the query as_of. Boundary transitions are counted on exactly one day (half-open
// membership), backlog returns leave WIP, reopened episodes re-enter, and unknown-history
// gaps are surfaced, never counted as zero WIP.
const (
	// MetricWIP is the daily work-in-progress count series.
	MetricWIP = "wip"
	// MetricStageDwell is the time-in-stage distribution (WIP stages), with per-stage
	// interval counts across all stages. Open intervals age to the query as_of.
	MetricStageDwell = "stage_dwell"
)

func init() {
	Register(wipProvider{})
	Register(stageDwellProvider{})
}

const statusField = "status"

// seriesRange resolves the [from, to] daily sample bounds. to is q.Range.To when set, else
// the query as_of; from is q.Range.From when set, else the earliest interval entry. Open
// series always advance to as_of without any source activity.
func seriesRange(q Query, asOf time.Time, intervals []IntervalRow) (from, to time.Time) {
	to = asOf
	if q.Range != nil && !q.Range.To.IsZero() {
		to = q.Range.To.UTC()
	}
	if q.Range != nil && !q.Range.From.IsZero() {
		from = q.Range.From.UTC()
		return from, to
	}
	for i, iv := range intervals {
		if i == 0 || iv.EnteredAt.Before(from) {
			from = iv.EnteredAt
		}
	}
	return from, to
}

// wipProvider reconstructs the daily WIP count. Each daily sample counts issues whose status
// at that instant maps to a configured wip_stage. The reconstructed current WIP (sample at
// as_of) is reconciled against the live issues snapshot; a mismatch is surfaced as a warning
// rather than silently trusting the series.
type wipProvider struct{}

func (wipProvider) Metric() string { return MetricWIP }

func (wipProvider) Compute(s *Service, q Query) (*Result, error) {
	intervals, err := s.store.Intervals(q.Projects, statusField)
	if err != nil {
		return nil, err
	}
	r := s.newResult(q)
	if err := s.attachBuild(r); err != nil {
		return nil, err
	}
	asOf := s.asOf(q)
	ix := NewIntervalIndex(intervals)
	r.Cohort = "daily work-in-progress (issues in a wip_stage; UTC-midnight sampling)"

	from, to := seriesRange(q, asOf, intervals)
	for _, day := range DailySamples(from, to) {
		count := 0
		for _, key := range ix.Keys() {
			stage, covered := ix.StageAt(key, day)
			if covered && s.pol.IsWIPStage(stage) {
				count++
			}
		}
		r.Series = append(r.Series, PeriodPoint{Period: DayKey(day), Count: count})
	}

	// Reconcile reconstructed current WIP (at as_of) against the issues snapshot.
	reconstructed := 0
	for _, key := range ix.Keys() {
		if stage, covered := ix.StageAt(key, asOf); covered && s.pol.IsWIPStage(stage) {
			reconstructed++
		}
	}
	r.Eligible = reconstructed
	if snap, err := s.snapshotWIP(q.Projects); err == nil && snap != reconstructed {
		r.warn("reconstructed current WIP (" + strconv.Itoa(reconstructed) + ") differs from issues snapshot (" + strconv.Itoa(snap) + "); verify stage mapping/history completeness before trusting the series")
	}
	return r, nil
}

// snapshotWIP counts currently-open issues whose current status maps to a wip_stage, from
// the live issues snapshot (the reconciliation target).
func (s *Service) snapshotWIP(projects []string) (int, error) {
	issues, err := s.loadIssues(projects)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range issues {
		stage := s.pol.StageFor("", it.status, it.category).Stage
		if s.pol.IsWIPStage(stage) {
			n++
		}
	}
	return n, nil
}

// stageDwellProvider distributes time-in-stage across status intervals. The distribution is
// over WIP-stage dwell (the bottleneck signal) with open intervals aged to the query as_of,
// so changing as_of advances the dwell with no source activity. Counts report the interval
// tally per stage across ALL stages (unknown included), never dropped.
type stageDwellProvider struct{}

func (stageDwellProvider) Metric() string { return MetricStageDwell }

func (stageDwellProvider) Compute(s *Service, q Query) (*Result, error) {
	intervals, err := s.store.Intervals(q.Projects, statusField)
	if err != nil {
		return nil, err
	}
	r := s.newResult(q)
	if err := s.attachBuild(r); err != nil {
		return nil, err
	}
	asOf := s.asOf(q)
	r.Cohort = "time-in-stage (wip-stage dwell distribution; open intervals aged to as_of)"
	r.Counts = map[string]int{}

	var wipDwellDays []float64
	for _, iv := range intervals {
		stage := iv.Stage
		if stage == "" {
			stage = StageUnknown
		}
		r.Counts[stage]++
		end := asOf
		if iv.ExitedAt != nil {
			end = *iv.ExitedAt
		}
		days := end.Sub(iv.EnteredAt).Hours() / 24
		if days < 0 {
			days = 0
		}
		if s.pol.IsWIPStage(iv.Stage) {
			wipDwellDays = append(wipDwellDays, days)
			r.Eligible++
		}
	}
	r.Distribution = distribution(wipDwellDays)
	return r, nil
}
