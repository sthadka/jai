package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/metrics"
	"github.com/sthadka/jai/internal/query"
)

// setupMetricsGlobals opens a fresh DB, seeds the given issues and installs a
// metrics config, wiring g so the `jai metrics` run paths work in-process.
func setupMetricsGlobals(t *testing.T, issues []*db.Issue, mc *config.MetricsConfig) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	for _, is := range issues {
		if is.RawJSON == "" {
			is.RawJSON = "{}"
		}
		if err := database.UpsertIssue(is, nil); err != nil {
			t.Fatalf("upsert %s: %v", is.Key, err)
		}
	}

	cfg := &config.Config{Metrics: mc}
	cfg.DB.Path = dbPath

	g.db = database
	g.cfg = cfg
	g.query = query.New(database, cfg)
	g.jsonOut = false
	g.fields = ""
	g.format = ""

	resetMetricFlags()
	metricTimeout = 10 * time.Second

	t.Cleanup(func() {
		g.db = nil
		g.cfg = nil
		g.query = nil
		g.jsonOut = false
		g.format = ""
		resetMetricFlags()
	})
}

func resetMetricFlags() {
	metricProjects = nil
	metricAsOf, metricFrom, metricTo = "", "", ""
	metricPeriod, metricCohort, metricBasis = "", "", ""
	metricGenuine = false
}

// TestResultToTable covers the flattening of each result shape and the
// preservation of an undefined ratio (null value -> "undefined").
func TestResultToTable(t *testing.T) {
	f := 1.5
	tests := []struct {
		name     string
		result   *metrics.Result
		wantCols []string
		wantCell string // a value that must appear in the first data row
	}{
		{
			name:     "distribution",
			result:   &metrics.Result{Metric: "cycle_time", Distribution: &metrics.Distribution{Unit: "days", Count: 3, P50: 2}},
			wantCols: []string{"metric", "unit", "count", "p50", "p85", "p95", "min", "max", "mean"},
			wantCell: "cycle_time",
		},
		{
			name:     "series",
			result:   &metrics.Result{Metric: "throughput", Series: []metrics.PeriodPoint{{Period: "2024-01", Count: 5}}},
			wantCols: []string{"period", "count"},
			wantCell: "2024-01",
		},
		{
			name:     "counts",
			result:   &metrics.Result{Metric: "aged_open", Counts: map[string]int{"0-7d": 2}},
			wantCols: []string{"bucket", "count"},
			wantCell: "0-7d",
		},
		{
			name: "ratio_defined",
			result: &metrics.Result{Metric: "ratio", Ratio: &metrics.RatioResult{
				Numerator: "bug", Denominator: "feature", NumCount: 3, DenCount: 2, Value: &f}},
			wantCols: []string{"numerator", "denominator", "num_count", "den_count", "ratio"},
			wantCell: "1.5000",
		},
		{
			name: "ratio_undefined",
			result: &metrics.Result{Metric: "ratio", Ratio: &metrics.RatioResult{
				Numerator: "bug", Denominator: "feature", NumCount: 3, DenCount: 0, Value: nil}},
			wantCols: []string{"numerator", "denominator", "num_count", "den_count", "ratio"},
			wantCell: "undefined",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cols, rows := resultToTable(tc.result)
			if strings.Join(cols, ",") != strings.Join(tc.wantCols, ",") {
				t.Fatalf("cols = %v, want %v", cols, tc.wantCols)
			}
			if len(rows) == 0 {
				t.Fatalf("no rows")
			}
			var found bool
			for _, cell := range rows[0] {
				if fmt.Sprintf("%v", cell) == tc.wantCell {
					found = true
				}
			}
			if !found {
				t.Fatalf("row %v does not contain %q", rows[0], tc.wantCell)
			}
		})
	}
}

// TestMetricCSVSharedService runs an issues-only metric end to end through the
// CLI run path and asserts the CSV it emits matches the shared service result.
func TestMetricCSVSharedService(t *testing.T) {
	now := time.Now().UTC()
	issues := []*db.Issue{
		{Key: "P-1", Project: "P", Type: "Bug", Status: "In Progress", StatusCategory: "In Progress",
			Assignee: "alice", Created: now.AddDate(0, 0, -3).Format(time.RFC3339)},
		{Key: "P-2", Project: "P", Type: "Bug", Status: "To Do", StatusCategory: "To Do",
			Assignee: "bob", Created: now.AddDate(0, 0, -40).Format(time.RFC3339)},
		{Key: "P-3", Project: "P", Type: "Story", Status: "Done", StatusCategory: "Done",
			Assignee: "alice", Created: now.AddDate(0, 0, -100).Format(time.RFC3339),
			Resolved: now.AddDate(0, 0, -1).Format(time.RFC3339), Resolution: "Done"},
	}
	setupMetricsGlobals(t, issues, &config.MetricsConfig{})
	g.format = "csv"

	out := captureStdout(t, func() {
		if err := runMetric(metricsCmd, "aged_open"); err != nil {
			t.Fatalf("runMetric: %v", err)
		}
	})

	// CSV header is clean (diagnostics go to stderr, not stdout).
	if !strings.HasPrefix(out, "bucket,count") {
		t.Fatalf("csv did not start with header, got:\n%s", out)
	}
	// Two open issues: P-1 (0-7d) and P-2 (31-90d).
	if !strings.Contains(out, "0-7d,1") {
		t.Fatalf("expected 0-7d bucket count 1, got:\n%s", out)
	}
	if !strings.Contains(out, "31-90d,1") {
		t.Fatalf("expected 31-90d bucket count 1, got:\n%s", out)
	}
	// The delivered (done) issue P-3 must not be counted as open.
	if strings.Contains(out, ">180d,1") {
		t.Fatalf("done issue leaked into open ageing, got:\n%s", out)
	}
}

// TestMetricRatioUndefinedPreserved verifies a zero-denominator ratio surfaces
// as undefined (not zero) through the CLI shared policy.
func TestMetricRatioUndefinedPreserved(t *testing.T) {
	now := time.Now().UTC()
	issues := []*db.Issue{
		{Key: "P-1", Project: "P", Type: "Bug", Status: "Done", StatusCategory: "Done",
			Created:  now.AddDate(0, 0, -10).Format(time.RFC3339),
			Resolved: now.AddDate(0, 0, -1).Format(time.RFC3339), Resolution: "Done"},
	}
	mc := &config.MetricsConfig{
		TypeClasses:     map[string][]string{"bug": {"Bug"}, "feature": {"Story"}},
		Ratio:           &config.RatioConfig{Numerator: "bug", Denominator: "feature", Cohort: "completed"},
		DoneResolutions: []string{"Done"},
	}
	setupMetricsGlobals(t, issues, mc)
	g.format = "csv"

	out := captureStdout(t, func() {
		if err := runMetric(metricsCmd, "ratio"); err != nil {
			t.Fatalf("runMetric: %v", err)
		}
	})
	if !strings.Contains(out, "undefined") {
		t.Fatalf("expected undefined ratio (zero denominator), got:\n%s", out)
	}
}

// TestMetricRatioNotConfiguredFailsClosed verifies the ratio metric fails
// closed with a useful error when no ratio taxonomy is configured, rather than
// returning a fabricated number.
func TestMetricRatioNotConfiguredFailsClosed(t *testing.T) {
	setupMetricsGlobals(t, nil, &config.MetricsConfig{})
	g.format = "table"
	err := runMetric(metricsCmd, "ratio")
	if err == nil {
		t.Fatalf("expected error for unconfigured ratio")
	}
	if !strings.Contains(err.Error(), "ratio") {
		t.Fatalf("error not useful: %v", err)
	}
}

// TestMetricUnknownMetric verifies an unknown metric name errors.
func TestMetricUnknownMetric(t *testing.T) {
	setupMetricsGlobals(t, nil, &config.MetricsConfig{})
	g.format = "table"
	if err := runMetric(metricsCmd, "does_not_exist"); err == nil {
		t.Fatalf("expected error for unknown metric")
	}
}
