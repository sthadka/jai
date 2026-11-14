package metrics

import (
	"errors"
	"sort"
	"time"
)

// Basis values. Phase 1 is snapshot-only: each issue is counted once by its current
// resolved/resolution. Event / first-completion bases are a later phase and MUST be
// rejected rather than silently served with snapshot semantics.
const (
	BasisSnapshot       = "snapshot"
	BasisEvent          = "event"
	BasisFirstCompleted = "first_completion"
)

// Shared service errors.
var (
	// ErrUnknownMetric is returned when no provider is registered for a metric name.
	ErrUnknownMetric = errors.New("metrics: unknown metric")
	// ErrUnsupportedBasis is returned when a caller requests a non-snapshot basis in
	// Phase 1. The service never downgrades it to snapshot silently.
	ErrUnsupportedBasis = errors.New("metrics: unsupported basis (Phase 1 is snapshot-only)")
	// ErrGenuineDoneUnavailable is returned when a caller requires the genuine-done
	// variant but done_resolutions is not configured (the variant is disabled, not guessed).
	ErrGenuineDoneUnavailable = errors.New("metrics: genuine-done variant unavailable (done_resolutions unset)")
)

// TimeRange is an inclusive-start, exclusive-end bound on a metric's time axis.
type TimeRange struct {
	From time.Time
	To   time.Time
}

func (r *TimeRange) contains(t time.Time) bool {
	if r == nil {
		return true
	}
	if !r.From.IsZero() && t.Before(r.From) {
		return false
	}
	if !r.To.IsZero() && !t.Before(r.To) {
		return false
	}
	return true
}

// Query selects a metric and its cohort/scope. Zero AsOf defaults to now (UTC) at compute
// time. Basis empty defaults to snapshot; any other value is rejected. RequireGenuine
// errors when done_resolutions is unset instead of returning an all-resolved number.
type Query struct {
	Metric         string
	Projects       []string
	AsOf           time.Time
	Range          *TimeRange
	Basis          string
	Period         string // throughput bucket: "month" (default) or "week"
	Cohort         string // "created" | "completed" (metric-specific default)
	RequireGenuine bool
}

// Distribution is a duration distribution with the Phase-1 reported percentiles.
type Distribution struct {
	Unit  string  // always "days" in Phase 1
	Count int     // sample size (eligible cohort)
	P50   float64 // median
	P85   float64
	P95   float64
	Min   float64
	Max   float64
	Mean  float64
}

// PeriodPoint is one throughput bucket.
type PeriodPoint struct {
	Period string // "2006-01" (month) or ISO "2006-Www" (week)
	Count  int
}

// Result is the uniform metric envelope. Every result carries its basis, cohort,
// policies, taxonomy version (policy fingerprint), as_of, and — as applicable — coverage
// and source/build freshness, so no consumer re-derives them or ships an unlabelled number.
type Result struct {
	Metric          string
	Basis           string
	Cohort          string
	Projects        []string
	AsOf            time.Time
	Range           *TimeRange
	TaxonomyVersion string            // policy fingerprint
	Policies        map[string]string // human-readable active policies (done_resolutions, wip_stages, ...)
	GenuineDone     bool              // whether the genuine-done completion filter was applied

	Distribution *Distribution
	Series       []PeriodPoint
	Counts       map[string]int
	Ratio        *RatioResult

	Eligible int            // size of the counted cohort
	Excluded map[string]int // reason -> count (published, never dropped)

	BuildGeneration int64                      // active fact generation (0 if facts unused)
	BuiltAt         *time.Time                 // build watermark (fact-based metrics)
	Freshness       map[string]SourceFreshness // per-project source freshness
	Coverage        *CoverageReport            // optional, attached by caller when requested
	Warnings        []string
}

// RatioResult is a named configured class ratio (numerator/denominator), with the class
// counts alongside. Value is nil when the denominator is zero (undefined, not zero).
type RatioResult struct {
	Numerator   string
	Denominator string
	NumCount    int
	DenCount    int
	Value       *float64
	Classes     map[string]int // every class count, including unknown/other
}

func (r *Result) exclude(reason string, n int) {
	if n == 0 {
		return
	}
	if r.Excluded == nil {
		r.Excluded = map[string]int{}
	}
	r.Excluded[reason] += n
}

func (r *Result) warn(msg string) { r.Warnings = append(r.Warnings, msg) }

// distribution computes the Phase-1 percentile summary over days samples. Percentiles use
// the inclusive linear-interpolation method (R-7 / Excel PERCENTILE.INC): rank = p*(n-1),
// interpolating between adjacent order statistics, so odd and even sample counts are
// handled uniformly and a single sample yields itself at every percentile. An empty cohort
// yields Count 0 and zero percentiles (the caller publishes the exclusion reasons).
func distribution(days []float64) *Distribution {
	d := &Distribution{Unit: "days", Count: len(days)}
	if len(days) == 0 {
		return d
	}
	s := append([]float64(nil), days...)
	sort.Float64s(s)
	var sum float64
	for _, v := range s {
		sum += v
	}
	d.Min = s[0]
	d.Max = s[len(s)-1]
	d.Mean = sum / float64(len(s))
	d.P50 = percentile(s, 0.50)
	d.P85 = percentile(s, 0.85)
	d.P95 = percentile(s, 0.95)
	return d
}

// percentile returns the p-quantile (0..1) of a pre-sorted slice via R-7 interpolation.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	rank := p * float64(n-1)
	lo := int(rank)
	frac := rank - float64(lo)
	if lo+1 >= n {
		return sorted[n-1]
	}
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}
