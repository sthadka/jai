package metrics

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Issues-only metric names. These run directly off the issues snapshot and need no fact
// generation, so they work on a database that has never built the fact store.
const (
	MetricNetFlow       = "net_flow"
	MetricAgedOpen      = "aged_open"
	MetricResolutionMix = "resolution_mix"
	MetricWorkload      = "workload_by_assignee"
	// MetricRatio ("ratio") is defined in config.go; the ratio provider reuses it.
)

// unassigned is the explicit bucket for empty/sentinel assignees — never silently dropped.
const unassigned = "(Unassigned)"

// ErrRatioNotConfigured is returned when the ratio metric is requested but no valid
// type-class ratio is configured (a ratio needs a named, configured taxonomy; there is no
// hard-coded feature/security classification).
var ErrRatioNotConfigured = errors.New("metrics: ratio not configured (set metrics.type_classes + metrics.ratio)")

func init() {
	Register(netFlowProvider{})
	Register(agedOpenProvider{})
	Register(resolutionMixProvider{})
	Register(ratioProvider{})
	Register(workloadProvider{})
}

// issueSnap is a current-issues snapshot row for issues-only metrics.
type issueSnap struct {
	key, project, itype  string
	status, category     string
	assignee, resolution string
	created, resolved    *time.Time
}

// loadIssues reads the current issues snapshot for the query scope (no fact tables).
func (s *Service) loadIssues(projects []string) ([]issueSnap, error) {
	q := `SELECT key, project, type, created, resolved, resolution, status, status_category, assignee FROM issues`
	var args []any
	if len(projects) > 0 {
		ph := make([]string, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			args = append(args, p)
		}
		q += " WHERE project IN (" + strings.Join(ph, ", ") + ")"
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("load issues: %w", err)
	}
	defer rows.Close()

	var out []issueSnap
	for rows.Next() {
		var (
			key, project                         string
			itype, created, resolved, resolution sql.NullString
			status, category, assignee           sql.NullString
		)
		if err := rows.Scan(&key, &project, &itype, &created, &resolved, &resolution, &status, &category, &assignee); err != nil {
			return nil, err
		}
		it := issueSnap{
			key: key, project: project, itype: itype.String,
			status: status.String, category: category.String,
			assignee: assignee.String, resolution: resolution.String,
		}
		it.created = nullTime(created)
		it.resolved = nullTime(resolved)
		out = append(out, it)
	}
	return out, rows.Err()
}

// issueDelivered applies the shared completion policy to a snapshot row: an issue is
// delivered when it is resolved and (when done_resolutions is configured) carries a
// genuine-done resolution. Issues-only completed cohorts reuse this so they agree with the
// fact-based delivery metrics.
func (s *Service) issueDelivered(it issueSnap) bool {
	if it.resolved == nil {
		return false
	}
	if s.pol.DoneResolutionsConfigured() {
		return s.pol.IsDoneResolution(it.resolution)
	}
	return true
}

// isOpen reports whether an issue is still open for workload/ageing. Unknown/empty status
// category is treated as open (surfaced), never dropped.
func isOpen(it issueSnap) bool {
	cat := strings.ToLower(strings.TrimSpace(it.category))
	return cat != "done"
}

// netFlowProvider: arrivals (created) versus departures (current resolved) per period. Each
// issue arrives once (by created) and departs at most once (by current resolved — snapshot
// semantics, not a historical completion log). Series carries the net per period.
type netFlowProvider struct{}

func (netFlowProvider) Metric() string { return MetricNetFlow }

func (netFlowProvider) Compute(s *Service, q Query) (*Result, error) {
	issues, err := s.loadIssues(q.Projects)
	if err != nil {
		return nil, err
	}
	period := q.Period
	if period == "" {
		period = "month"
	}
	created := map[string]int{}
	resolved := map[string]int{}
	seen := map[string]bool{}
	var createdTotal, resolvedTotal int
	addPeriod := func(k string) {
		if !seen[k] {
			seen[k] = true
		}
	}
	r := s.newResult(q)
	r.Cohort = "created vs current resolved"
	r.Policies["period"] = period
	for _, it := range issues {
		if it.created != nil && q.Range.contains(*it.created) {
			k := periodKey(*it.created, period)
			created[k]++
			createdTotal++
			addPeriod(k)
		}
		if it.resolved != nil && q.Range.contains(*it.resolved) {
			k := periodKey(*it.resolved, period)
			resolved[k]++
			resolvedTotal++
			addPeriod(k)
		}
	}
	periods := make([]string, 0, len(seen))
	for k := range seen {
		periods = append(periods, k)
	}
	sort.Strings(periods)
	for _, k := range periods {
		r.Series = append(r.Series, PeriodPoint{Period: k, Count: created[k] - resolved[k]})
	}
	r.Counts = map[string]int{
		"created":  createdTotal,
		"resolved": resolvedTotal,
		"net":      createdTotal - resolvedTotal,
	}
	r.Eligible = len(issues)
	return r, nil
}

// agedBuckets are the open-age buckets (days). Age is computed against the explicit query
// as_of, never a build-frozen now, so ages advance at query time without a rebuild.
var agedBuckets = []struct {
	label  string
	maxDay int // inclusive upper bound in days; -1 = unbounded
}{
	{"0-7d", 7},
	{"8-30d", 30},
	{"31-90d", 90},
	{"91-180d", 180},
	{">180d", -1},
}

// agedOpenProvider: age buckets for currently-open issues (status category != done),
// against as_of.
type agedOpenProvider struct{}

func (agedOpenProvider) Metric() string { return MetricAgedOpen }

func (agedOpenProvider) Compute(s *Service, q Query) (*Result, error) {
	issues, err := s.loadIssues(q.Projects)
	if err != nil {
		return nil, err
	}
	asOf := s.asOf(q)
	r := s.newResult(q)
	r.Cohort = "open issues (status category != done)"
	r.Counts = map[string]int{}
	for _, b := range agedBuckets {
		r.Counts[b.label] = 0
	}
	var unknownAge int
	for _, it := range issues {
		if !isOpen(it) {
			continue
		}
		r.Eligible++
		if it.created == nil {
			unknownAge++
			continue
		}
		ageDays := int(asOf.Sub(*it.created).Hours() / 24)
		if ageDays < 0 {
			ageDays = 0
		}
		r.Counts[bucketFor(ageDays)]++
	}
	r.exclude("unknown_age", unknownAge)
	return r, nil
}

func bucketFor(ageDays int) string {
	for _, b := range agedBuckets {
		if b.maxDay == -1 || ageDays <= b.maxDay {
			return b.label
		}
	}
	return agedBuckets[len(agedBuckets)-1].label
}

// resolutionMixProvider: distribution of resolutions among resolved issues. Empty
// resolution maps to an explicit "(none)" bucket (never dropped). Optionally bounded by
// resolved date.
type resolutionMixProvider struct{}

func (resolutionMixProvider) Metric() string { return MetricResolutionMix }

func (resolutionMixProvider) Compute(s *Service, q Query) (*Result, error) {
	issues, err := s.loadIssues(q.Projects)
	if err != nil {
		return nil, err
	}
	r := s.newResult(q)
	r.Cohort = "resolved issues by resolution"
	r.Counts = map[string]int{}
	for _, it := range issues {
		if it.resolved == nil || !q.Range.contains(*it.resolved) {
			continue
		}
		res := strings.TrimSpace(it.resolution)
		if res == "" {
			res = "(none)"
		}
		r.Counts[res]++
		r.Eligible++
	}
	return r, nil
}

// ratioProvider: a named configured class ratio (e.g. bug/feature). It reports the class
// counts alongside the ratio, a null (undefined) value on a zero denominator, keeps
// unknown/other classes visible, and has no hard-coded feature/security classification.
type ratioProvider struct{}

func (ratioProvider) Metric() string { return MetricRatio }

func (ratioProvider) Compute(s *Service, q Query) (*Result, error) {
	ratioPol, ok := s.pol.Ratio()
	if !ok {
		return nil, ErrRatioNotConfigured
	}
	issues, err := s.loadIssues(q.Projects)
	if err != nil {
		return nil, err
	}

	cohort := strings.TrimSpace(q.Cohort)
	if cohort == "" {
		cohort = ratioPol.Cohort
	}
	// The all-current cohort is illustrative and offered only when explicitly named.
	if cohort != "created" && cohort != "completed" && cohort != "all" {
		return nil, fmt.Errorf("metrics: ratio cohort %q must be created, completed, or all", cohort)
	}

	r := s.newResult(q)
	classes := map[string]int{}
	var eligible int
	for _, it := range issues {
		switch cohort {
		case "created":
			if !q.Range.contains(timeOrZero(it.created)) {
				continue
			}
		case "completed":
			if !s.issueDelivered(it) || !q.Range.contains(timeOrZero(it.resolved)) {
				continue
			}
		case "all":
			// every current issue
		}
		classes[s.pol.ClassForType(it.itype)]++
		eligible++
	}

	num := classes[ratioPol.Numerator]
	den := classes[ratioPol.Denominator]
	rr := &RatioResult{
		Numerator:   ratioPol.Numerator,
		Denominator: ratioPol.Denominator,
		NumCount:    num,
		DenCount:    den,
		Classes:     classes,
	}
	if den > 0 {
		v := float64(num) / float64(den)
		rr.Value = &v
	} else {
		r.warn("ratio denominator is zero; ratio is undefined")
	}
	r.Ratio = rr
	r.Eligible = eligible
	r.Cohort = cohort
	if cohort == "completed" {
		r.GenuineDone = s.pol.DoneResolutionsConfigured()
		if r.GenuineDone {
			r.Cohort = "completed (genuine-done)"
		} else {
			r.Cohort = "completed (includes non-delivery resolutions)"
		}
	}
	return r, nil
}

// workloadProvider: current-assignee open item load. Empty/sentinel assignees are bucketed
// as (Unassigned) and unknown status categories are counted explicitly — neither is
// silently dropped. This is item load, not effort or productivity, and current-owner
// attribution only (historical attribution arrives in M15).
type workloadProvider struct{}

func (workloadProvider) Metric() string { return MetricWorkload }

func (workloadProvider) Compute(s *Service, q Query) (*Result, error) {
	issues, err := s.loadIssues(q.Projects)
	if err != nil {
		return nil, err
	}
	r := s.newResult(q)
	r.Cohort = "open items by current assignee"
	r.Counts = map[string]int{}
	var unknownCategory int
	for _, it := range issues {
		if !isOpen(it) {
			continue
		}
		owner := strings.TrimSpace(it.assignee)
		if owner == "" || strings.EqualFold(owner, "unassigned") {
			owner = unassigned
		}
		r.Counts[owner]++
		r.Eligible++
		if strings.TrimSpace(it.category) == "" {
			unknownCategory++
		}
	}
	if unknownCategory > 0 {
		r.Counts["(unknown status category)"] = unknownCategory
	}
	return r, nil
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
