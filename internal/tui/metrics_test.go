package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sthadka/jai/internal/metrics"
)

func TestSparkline(t *testing.T) {
	tests := []struct {
		name string
		vals []float64
		want string
	}{
		{"empty", nil, ""},
		{"single", []float64{5}, "█"},
		{"all-zero flat renders low", []float64{0, 0, 0}, "▁▁▁"},
		{"all-equal positive renders high", []float64{3, 3, 3}, "███"},
		{"ascending spans full range", []float64{0, 1, 2, 3, 4, 5, 6, 7}, "▁▂▃▄▅▆▇█"},
		{"signed net-flow normalizes min to floor", []float64{-2, 0, 2}, "▁▅█"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sparkline(tc.vals)
			if got != tc.want {
				t.Fatalf("sparkline(%v) = %q, want %q", tc.vals, got, tc.want)
			}
		})
	}
}

func TestBar(t *testing.T) {
	tests := []struct {
		name          string
		value, max    float64
		width, wantLn int
	}{
		{"zero value empty", 0, 10, 10, 0},
		{"zero max empty (no divide by zero)", 5, 0, 10, 0},
		{"negative max empty", 5, -3, 10, 0},
		{"full", 10, 10, 10, 10},
		{"half rounds", 5, 10, 10, 5},
		{"over-max clamps to width", 20, 10, 10, 10},
		{"zero width empty", 5, 10, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bar(tc.value, tc.max, tc.width)
			if n := len([]rune(got)); n != tc.wantLn {
				t.Fatalf("bar(%v,%v,%d) len = %d, want %d (%q)", tc.value, tc.max, tc.width, n, tc.wantLn, got)
			}
		})
	}
}

func TestFmtNum(t *testing.T) {
	cases := map[float64]string{0: "0.0", 4.25: "4.2", 4.26: "4.3", -1.5: "-1.5"}
	for in, want := range cases {
		if got := fmtNum(in); got != want {
			t.Errorf("fmtNum(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestDistributionLinesEmptyCohort(t *testing.T) {
	got := distributionLines(&metrics.Distribution{Unit: "days", Count: 0})
	if len(got) != 1 || !strings.Contains(got[0], "empty cohort") {
		t.Fatalf("empty distribution should report empty cohort, got %v", got)
	}
}

func TestDistributionLinesScalesToMax(t *testing.T) {
	d := &metrics.Distribution{Unit: "days", Count: 4, P50: 2, P85: 8, P95: 10, Max: 10, Mean: 5}
	got := distributionLines(d)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "n=4") || !strings.Contains(joined, "mean=5.0") {
		t.Fatalf("summary line missing stats: %q", joined)
	}
	// max row (value == scale) must render a full-width bar; p50 (20%) shorter.
	var p50, max string
	for _, l := range got {
		if strings.HasPrefix(strings.TrimSpace(l), "p50") {
			p50 = l
		}
		if strings.HasPrefix(strings.TrimSpace(l), "max") {
			max = l
		}
	}
	if strings.Count(max, "█") != metricBarWidth {
		t.Fatalf("max bar should be full width %d, got %d", metricBarWidth, strings.Count(max, "█"))
	}
	if strings.Count(p50, "█") >= strings.Count(max, "█") {
		t.Fatalf("p50 bar (%d) should be shorter than max bar (%d)", strings.Count(p50, "█"), strings.Count(max, "█"))
	}
}

func TestCountLinesSortedDescAndTruncated(t *testing.T) {
	counts := map[string]int{}
	for i := range 10 {
		counts["owner"+string(rune('A'+i))] = i + 1 // ownerA=1 .. ownerJ=10
	}
	got := countLines(counts)
	// Top bucket first (highest count), at most topN rows + truncation note.
	if !strings.Contains(got[0], "ownerJ") || !strings.Contains(got[0], "10") {
		t.Fatalf("first row should be the largest bucket ownerJ=10, got %q", got[0])
	}
	last := got[len(got)-1]
	if !strings.Contains(last, "and 2 more") {
		t.Fatalf("expected truncation note for 10 buckets, got %q", last)
	}
}

func TestCountLinesEmptyLabel(t *testing.T) {
	got := countLines(map[string]int{"": 3})
	if !strings.Contains(got[0], "(none)") {
		t.Fatalf("empty label should render as (none), got %q", got[0])
	}
}

func TestCaveatLinesSurfacesUncertainty(t *testing.T) {
	r := &metrics.Result{
		Cohort:      "completed",
		GenuineDone: false,
		Excluded:    map[string]int{"not_completed": 3, "no_cycle_data": 1},
		Warnings:    []string{"includes non-delivery resolutions"},
	}
	got := strings.Join(caveatLines(r), "\n")
	if !strings.Contains(got, "cohort: completed") {
		t.Errorf("missing cohort label: %q", got)
	}
	if !strings.Contains(got, "includes non-delivery resolutions") {
		t.Errorf("genuine-done uncertainty must be surfaced: %q", got)
	}
	// Exclusions are published, never dropped, and sorted.
	if !strings.Contains(got, "no_cycle_data=1") || !strings.Contains(got, "not_completed=3") {
		t.Errorf("exclusions must be published: %q", got)
	}
	if strings.Index(got, "no_cycle_data") > strings.Index(got, "not_completed") {
		t.Errorf("exclusions should be sorted: %q", got)
	}
}

func TestCaveatLinesGenuineApplied(t *testing.T) {
	r := &metrics.Result{Cohort: "completed", GenuineDone: true}
	got := strings.Join(caveatLines(r), "\n")
	if !strings.Contains(got, "delivery resolutions only") {
		t.Fatalf("genuine-done applied should be labeled: %q", got)
	}
}

func TestBuildPaneLinesUnconfigured(t *testing.T) {
	d := &metricsData{asOf: time.Unix(0, 0).UTC(), configured: false}
	got := strings.Join(buildPaneLines(d, 80), "\n")
	if !strings.Contains(got, "not configured") {
		t.Fatalf("unconfigured pane must say so: %q", got)
	}
	// No crash, no metric sections rendered.
	if strings.Contains(got, "▸") {
		t.Fatalf("unconfigured pane should render no metric sections: %q", got)
	}
}

func TestBuildPaneLinesNoGeneration(t *testing.T) {
	d := &metricsData{asOf: time.Unix(0, 0).UTC(), configured: true, generation: 0}
	got := strings.Join(buildPaneLines(d, 80), "\n")
	if !strings.Contains(got, "No fact generation built") {
		t.Fatalf("missing-generation hint expected: %q", got)
	}
	if !strings.Contains(got, "none built") {
		t.Fatalf("header should report no generation: %q", got)
	}
}

func TestBuildPaneLinesRendersSectionsAndDiagnostics(t *testing.T) {
	built := time.Unix(1700000000, 0).UTC()
	d := &metricsData{
		asOf:       time.Unix(1700000000, 0).UTC(),
		configured: true,
		generation: 7,
		projects:   []string{"ABC"},
		rejections: map[string][]string{"ratio": {"undefined class"}},
		freshness: map[string]metrics.SourceFreshness{
			"ABC": {LastSyncTime: &built},
		},
		outcomes: []metricOutcome{
			{
				spec: metricSpec{metric: metrics.MetricThroughput, title: "Throughput"},
				res:  &metrics.Result{Cohort: "completed", Series: []metrics.PeriodPoint{{Period: "2023-W40", Count: 3}, {Period: "2023-W41", Count: 5}}},
			},
			{
				spec: metricSpec{metric: metrics.MetricCycleTime, title: "Cycle time"},
				res:  &metrics.Result{Cohort: "completed", Distribution: &metrics.Distribution{Unit: "days", Count: 2, P50: 1, P85: 2, P95: 3, Max: 3, Mean: 1.5}},
			},
			{
				spec: metricSpec{metric: metrics.MetricCycleTime, title: "Unavailable one"},
				err:  errors.New("boom"),
			},
		},
	}
	got := strings.Join(buildPaneLines(d, 80), "\n")
	for _, want := range []string{
		"scope: ABC",
		"generation 7",
		"freshness: ABC",
		"config: metric ratio disabled: undefined class",
		"▸ Throughput",
		"▸ Cycle time",
		"total=8", // throughput series sum
		"unavailable: boom",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("pane output missing %q\n---\n%s", want, got)
		}
	}
}

// fakeComputer drives computeMetrics deterministically without a DB.
type fakeComputer struct {
	results map[string]*metrics.Result
	errs    map[string]error
	seen    []metrics.Query
}

func (f *fakeComputer) Compute(q metrics.Query) (*metrics.Result, error) {
	f.seen = append(f.seen, q)
	if err := f.errs[q.Metric]; err != nil {
		return nil, err
	}
	if r := f.results[q.Metric]; r != nil {
		return r, nil
	}
	return &metrics.Result{Metric: q.Metric}, nil
}

func TestComputeMetricsUnconfiguredSkipsCompute(t *testing.T) {
	f := &fakeComputer{}
	d := computeMetrics(context.Background(), f, nil, 0, false, nil, time.Now())
	if d.configured {
		t.Fatal("expected unconfigured")
	}
	if len(f.seen) != 0 {
		t.Fatalf("unconfigured must not compute, saw %d queries", len(f.seen))
	}
	if len(d.outcomes) != 0 {
		t.Fatalf("expected no outcomes, got %d", len(d.outcomes))
	}
}

func TestComputeMetricsPropagatesAsOfAndErrors(t *testing.T) {
	asOf := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	f := &fakeComputer{errs: map[string]error{metrics.MetricWorkload: errors.New("nope")}}
	d := computeMetrics(context.Background(), f, nil, 3, true, []string{"ABC"}, asOf)

	if len(d.outcomes) != len(paneMetrics) {
		t.Fatalf("expected %d outcomes, got %d", len(paneMetrics), len(d.outcomes))
	}
	for _, q := range f.seen {
		if !q.AsOf.Equal(asOf) {
			t.Fatalf("as_of must flow to every query: got %v want %v", q.AsOf, asOf)
		}
		if len(q.Projects) != 1 || q.Projects[0] != "ABC" {
			t.Fatalf("projects must flow through: %v", q.Projects)
		}
	}
	// The workload metric surfaced its error as an outcome, not a panic.
	var found bool
	for _, o := range d.outcomes {
		if o.spec.metric == metrics.MetricWorkload {
			found = true
			if o.err == nil {
				t.Fatal("expected workload error recorded")
			}
		}
	}
	if !found {
		t.Fatal("workload metric not attempted")
	}
}

func TestComputeMetricsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeComputer{}
	d := computeMetrics(ctx, f, nil, 1, true, nil, time.Now())
	if d.computeErr == nil {
		t.Fatal("cancelled context should set computeErr")
	}
	if len(f.seen) != 0 {
		t.Fatalf("cancelled compute should not run metrics, saw %d", len(f.seen))
	}
}

func TestComputeMetricsSetsPeriodForSeriesMetrics(t *testing.T) {
	f := &fakeComputer{}
	computeMetrics(context.Background(), f, nil, 1, true, nil, time.Now())
	got := map[string]string{}
	for _, q := range f.seen {
		got[q.Metric] = q.Period
	}
	if got[metrics.MetricThroughput] != "week" {
		t.Errorf("throughput should request weekly buckets, got %q", got[metrics.MetricThroughput])
	}
	if got[metrics.MetricCycleTime] != "" {
		t.Errorf("distribution metric should not force a period, got %q", got[metrics.MetricCycleTime])
	}
}
