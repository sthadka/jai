package metrics

import (
	"sort"
	"strconv"
	"time"
)

// Cumulative flow (CFD), reopen, and flow-occupancy metrics. CFD spans ALL workflow stages
// for a consistent, documented population; reopen reads fact_issue_flow.reopen_count against
// a named denominator; flow occupancy is an active-status occupancy proxy (active ÷ lead),
// never called flow efficiency unless configured statuses truly separate hands-on work from
// queueing (which this package cannot assert, so the proxy label stands).
const (
	// MetricCFD is the cumulative flow diagram data: per-day, per-stage issue counts across
	// all stages. It is NOT a cycle/lead-time distribution.
	MetricCFD = "cfd"
	// MetricReopen is the reopen count distribution and reopen rate.
	MetricReopen = "reopen"
	// MetricFlowOccupancy is the active-status occupancy proxy (active ÷ lead) distribution.
	MetricFlowOccupancy = "flow_occupancy"
)

// cfdSep separates the date and stage parts of a CFD Counts key.
const cfdSep = "|"

func init() {
	Register(cfdProvider{})
	Register(reopenProvider{})
	Register(flowOccupancyProvider{})
}

// CFDKey builds the Counts key for a (date, stage) CFD cell: "YYYY-MM-DD|stage".
func CFDKey(date, stage string) string { return date + cfdSep + stage }

// StageOrder returns stages in a canonical flow order (todo, active, waiting, done, unknown),
// with any extra tenant stages appended alphabetically. Consumers use it to stack CFD cells
// deterministically.
func StageOrder(stages []string) []string {
	rank := map[string]int{StageTodo: 0, StageActive: 1, StageWaiting: 2, StageDone: 3, StageUnknown: 4}
	seen := map[string]bool{}
	uniq := make([]string, 0, len(stages))
	for _, s := range stages {
		if !seen[s] {
			seen[s] = true
			uniq = append(uniq, s)
		}
	}
	sort.SliceStable(uniq, func(i, j int) bool {
		ri, iok := rank[uniq[i]]
		rj, jok := rank[uniq[j]]
		if iok && jok {
			return ri < rj
		}
		if iok != jok {
			return iok // ranked stages before unranked
		}
		return uniq[i] < uniq[j]
	})
	return uniq
}

// cfdProvider builds the cumulative flow diagram over a consistent population: every issue in
// scope with reconstructed status history, counted into exactly one stage per day across ALL
// stages (done is cumulative; unknown-history gaps surface as the unknown stage). The
// population grows as issues are created — a documented scope change, not a silent one.
type cfdProvider struct{}

func (cfdProvider) Metric() string { return MetricCFD }

func (cfdProvider) Compute(s *Service, q Query) (*Result, error) {
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
	r.Cohort = "cumulative flow (all stages; consistent population grows as issues are created)"
	r.Counts = map[string]int{}

	from, to := seriesRange(q, asOf, intervals)
	population := map[string]bool{}
	for _, day := range DailySamples(from, to) {
		dayKey := DayKey(day)
		total := 0
		for _, key := range ix.Keys() {
			stage, covered := ix.StageAt(key, day)
			if !covered {
				continue // not yet created on this day
			}
			r.Counts[CFDKey(dayKey, stage)]++
			population[key] = true
			total++
		}
		r.Series = append(r.Series, PeriodPoint{Period: dayKey, Count: total})
	}
	r.Eligible = len(population)

	r.Policies["cfd_key_format"] = "YYYY-MM-DD" + cfdSep + "stage"
	r.Policies["cfd_population"] = "all in-scope issues with reconstructed status history; population grows as issues are created (documented scope change); done is cumulative"
	r.warn("CFD shows flow patterns across all stages; it does NOT recover the cycle/lead-time distribution — use cycle_time/lead_time for percentiles")
	return r, nil
}

// reopenProvider reports the reopen-count distribution (histogram) and the reopen rate with a
// named denominator. Default denominator is all in-scope issues; cohort "completed" scopes it
// to the delivered cohort. A zero denominator yields an undefined (nil) rate, never zero.
type reopenProvider struct{}

func (reopenProvider) Metric() string { return MetricReopen }

func (reopenProvider) Compute(s *Service, q Query) (*Result, error) {
	rows, err := s.flowRows(q)
	if err != nil {
		return nil, err
	}
	r := s.newResult(q)
	if err := s.attachBuild(r); err != nil {
		return nil, err
	}
	r.Counts = map[string]int{}

	var denomRows []FlowRow
	denomName := "all in-scope issues"
	if q.Cohort == string(CutCompleted) {
		delivered, nonDelivery, notCompleted, genuine, cohort := s.deliveredCohort(rows)
		r.GenuineDone = genuine
		r.exclude("not completed", notCompleted)
		r.exclude("non-delivery resolution", nonDelivery)
		denomRows = delivered
		denomName = cohort
	} else {
		denomRows = rows
	}
	r.Cohort = "reopen rate (Done->non-Done transitions per issue); denominator = " + denomName

	reopened := 0
	for _, fr := range denomRows {
		r.Counts[strconv.Itoa(fr.ReopenCount)]++
		if fr.ReopenCount > 0 {
			reopened++
		}
		r.Eligible++
	}
	ratio := &RatioResult{
		Numerator:   "reopened issues",
		Denominator: denomName,
		NumCount:    reopened,
		DenCount:    len(denomRows),
	}
	if len(denomRows) > 0 {
		v := float64(reopened) / float64(len(denomRows))
		ratio.Value = &v
	}
	r.Ratio = ratio
	return r, nil
}

// flowOccupancyProvider computes the active-status occupancy proxy per delivered issue: the
// time spent in wip_stages divided by total lead time (created -> resolved). Each ratio is
// bounded to [0,1]; an issue with undefined lead time (resolved <= created, or missing
// timestamps) is excluded, never assigned a fabricated occupancy.
type flowOccupancyProvider struct{}

func (flowOccupancyProvider) Metric() string { return MetricFlowOccupancy }

func (flowOccupancyProvider) Compute(s *Service, q Query) (*Result, error) {
	rows, err := s.flowRows(q)
	if err != nil {
		return nil, err
	}
	intervals, err := s.store.Intervals(q.Projects, statusField)
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
	r.Cohort = cohort + "; active-status occupancy proxy (active ÷ lead), NOT flow efficiency"
	r.exclude("not completed", notCompleted)
	r.exclude("non-delivery resolution", nonDelivery)

	var occ []float64
	var undefinedLead, clamped int
	for _, fr := range delivered {
		if fr.Created == nil || fr.Resolved == nil || !fr.Resolved.After(*fr.Created) {
			undefinedLead++
			continue
		}
		total := fr.Resolved.Sub(*fr.Created).Seconds()
		active := activeSeconds(ix, fr.IssueKey, *fr.Created, *fr.Resolved, s.pol)
		ratio, cl := occupancyRatio(active, total)
		if cl {
			clamped++
		}
		occ = append(occ, ratio)
		r.Eligible++
	}
	r.exclude("undefined lead time", undefinedLead)
	r.Distribution = distribution(occ)
	r.Distribution.Unit = "ratio"
	if clamped > 0 {
		r.warn("some issues had active time exceeding lead (overlapping/rounded intervals); occupancy clamped to 1.0")
	}
	return r, nil
}

// activeSeconds sums the seconds an issue spent in wip_stages within [from, to], clipping
// each status interval to that window. Open intervals are bounded by to (the resolved
// instant), so an occupancy computation never runs past completion.
func activeSeconds(ix *IntervalIndex, key string, from, to time.Time, pol *Policy) float64 {
	var secs float64
	for _, iv := range ix.Intervals(key) {
		secs += wipSeconds(iv.Stage, iv.EnteredAt, iv.ExitedAt, from, to, pol)
	}
	return secs
}

// wipSeconds returns the seconds a single status interval contributes to active WIP time
// within [from, to]: zero unless stage is a WIP stage, otherwise the clipped overlap of
// [EnteredAt, ExitedAt||to] with [from, to]. This is the single clipping definition shared
// by the flow_occupancy provider (live metric) and the persisted
// fact_issue_flow.flow_occupancy column (writeFacts), so the two never diverge.
func wipSeconds(stage string, entered time.Time, exited *time.Time, from, to time.Time, pol *Policy) float64 {
	if !pol.IsWIPStage(stage) {
		return 0
	}
	start := entered
	if start.Before(from) {
		start = from
	}
	end := to
	if exited != nil && exited.Before(end) {
		end = *exited
	}
	if end.After(start) {
		return end.Sub(start).Seconds()
	}
	return 0
}

// occupancyRatio bounds the active-status occupancy proxy (active ÷ lead) to [0,1] and
// reports whether it was clamped above 1 (overlapping/rounded intervals exceeding lead).
// Single definition shared by the provider and the persisted column.
func occupancyRatio(activeSecs, totalSecs float64) (ratio float64, clamped bool) {
	ratio = activeSecs / totalSecs
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
		clamped = true
	}
	return ratio, clamped
}
