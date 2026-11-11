package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/metrics"
	"github.com/sthadka/jai/internal/output"
)

// Metric-run flags. These are local to the `jai metrics <name>` run path; the
// subcommands (coverage, rebuild) share --project / --as-of via persistent flags.
var (
	metricProjects []string
	metricAsOf     string
	metricFrom     string
	metricTo       string
	metricPeriod   string
	metricCohort   string
	metricBasis    string
	metricGenuine  bool
	metricTimeout  time.Duration
)

var metricsCmd = &cobra.Command{
	Use:   "metrics [name]",
	Short: "Compute flow metrics from the local database",
	Long: `Compute flow metrics through the single shared metrics service.

With no arguments, lists the available metrics. With a metric name, runs that
metric and renders it with the shared output formatters (table, json, csv, tsv,
markdown). Every metric is routed through internal/metrics so the CLI, MCP and
TUI never fork the SQL or the policy.

Examples:
  jai metrics                       # list available metrics
  jai metrics cycle_time --format csv
  jai metrics throughput --period week --from 2024-01-01 --to 2024-07-01
  jai metrics ratio --cohort completed
  jai metrics coverage              # data-coverage diagnostics
  jai metrics rebuild               # full fact-store restatement`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return runMetricList()
		}
		return runMetric(cmd, args[0])
	},
}

var metricsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available metrics",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMetricList()
	},
}

var metricsCoverageCmd = &cobra.Command{
	Use:   "coverage",
	Short: "Report flow-metric data coverage diagnostics",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMetricCoverage()
	},
}

var metricsRebuildCmd = &cobra.Command{
	Use:   "rebuild",
	Short: "Rebuild the flow-metric fact store (full restatement)",
	Long: `Stage and atomically publish a fresh fact generation (BuildAll).

The rebuild takes the same cross-process advisory writer lock as incremental
sync, so a rebuild invoked from a second process never publishes stale facts
over a newer incremental update.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMetricRebuild()
	},
}

// metricsService builds the shared service, compiled policy and store from the
// active config and DB. There is one definition of each metric behind this.
func metricsService() (*metrics.Service, *metrics.Policy, *metrics.Store) {
	pol := metrics.CompilePolicy(g.cfg.Metrics)
	store := metrics.NewStore(g.db, g.cfg.DB.Path)
	svc := metrics.NewService(g.db, store, pol, g.cfg)
	return svc, pol, store
}

func runMetricList() error {
	names := metrics.Metrics()
	if g.jsonOut || g.format == "json" {
		fmt.Println(string(output.OK(map[string]interface{}{"metrics": names})))
		return nil
	}
	if len(names) == 0 {
		fmt.Println("No metrics registered.")
		return nil
	}
	fmt.Println("Available metrics:")
	for _, n := range names {
		fmt.Printf("  %s\n", n)
	}
	return nil
}

func runMetric(cmd *cobra.Command, name string) error {
	svc, pol, _ := metricsService()

	asOf, err := parseMetricTime(metricAsOf)
	if err != nil {
		return metricErr("InvalidArgument", err)
	}
	from, err := parseMetricTime(metricFrom)
	if err != nil {
		return metricErr("InvalidArgument", err)
	}
	to, err := parseMetricTime(metricTo)
	if err != nil {
		return metricErr("InvalidArgument", err)
	}

	q := metrics.Query{
		Metric:         name,
		Projects:       splitProjects(metricProjects),
		AsOf:           asOf,
		Basis:          metricBasis,
		Period:         metricPeriod,
		Cohort:         metricCohort,
		RequireGenuine: metricGenuine,
	}
	if !from.IsZero() || !to.IsZero() {
		q.Range = &metrics.TimeRange{From: from, To: to}
	}

	r, err := computeWithTimeout(cmd.Context(), svc, q, metricTimeout)
	if err != nil {
		return metricErr("MetricError", err)
	}

	// Surface fail-closed config rejections as warnings (the affected metric
	// is already excluded inside CompilePolicy; unrelated metrics keep working).
	for _, m := range sortedRejectionKeys(pol.Rejections()) {
		r.Warnings = append(r.Warnings,
			fmt.Sprintf("config: metric %q rejected: %s", m, strings.Join(pol.Rejections()[m], "; ")))
	}

	renderResult(r)
	return nil
}

// computeWithTimeout bounds and cancels a metric computation. A zero timeout
// disables the deadline; the command's context (Ctrl-C) always cancels.
func computeWithTimeout(ctx context.Context, svc *metrics.Service, q metrics.Query, d time.Duration) (*metrics.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	type result struct {
		r   *metrics.Result
		err error
	}
	ch := make(chan result, 1)
	go func() {
		r, err := svc.Compute(q)
		ch <- result{r, err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("computation cancelled: %w", ctx.Err())
	case out := <-ch:
		return out.r, out.err
	}
}

func renderResult(r *metrics.Result) {
	if g.jsonOut || g.format == "json" {
		fmt.Println(string(output.OK(r)))
		return
	}
	cols, rows := resultToTable(r)
	switch g.format {
	case "csv":
		fmt.Print(output.CSV(cols, rows))
		printDiagnostics(os.Stderr, r)
	case "tsv":
		fmt.Print(output.TSV(cols, rows))
		printDiagnostics(os.Stderr, r)
	case "markdown":
		fmt.Print(output.Markdown(cols, rows))
		printDiagnostics(os.Stderr, r)
	default: // table
		fmt.Printf("=== %s (basis=%s, cohort=%s) ===\n", r.Metric, r.Basis, r.Cohort)
		fmt.Print(output.Table(cols, rows))
		printDiagnostics(os.Stdout, r)
	}
}

// resultToTable flattens a metric Result into the primary data table for the
// row-oriented formatters. The full envelope (policies, freshness, every
// exclusion and warning) is preserved in the JSON format and summarized by
// printDiagnostics for the row formats so CSV/TSV data stays clean.
func resultToTable(r *metrics.Result) ([]string, [][]interface{}) {
	switch {
	case r.Distribution != nil:
		d := r.Distribution
		cols := []string{"metric", "unit", "count", "p50", "p85", "p95", "min", "max", "mean"}
		rows := [][]interface{}{{r.Metric, d.Unit, d.Count,
			round2(d.P50), round2(d.P85), round2(d.P95), round2(d.Min), round2(d.Max), round2(d.Mean)}}
		return cols, rows
	case r.Ratio != nil:
		rr := r.Ratio
		val := "undefined"
		if rr.Value != nil {
			val = fmt.Sprintf("%.4f", *rr.Value)
		}
		cols := []string{"numerator", "denominator", "num_count", "den_count", "ratio"}
		rows := [][]interface{}{{rr.Numerator, rr.Denominator, rr.NumCount, rr.DenCount, val}}
		return cols, rows
	case r.Series != nil:
		cols := []string{"period", "count"}
		rows := make([][]interface{}, 0, len(r.Series))
		for _, p := range r.Series {
			rows = append(rows, []interface{}{p.Period, p.Count})
		}
		return cols, rows
	case r.Counts != nil:
		cols := []string{"bucket", "count"}
		keys := make([]string, 0, len(r.Counts))
		for k := range r.Counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rows := make([][]interface{}, 0, len(keys))
		for _, k := range keys {
			rows = append(rows, []interface{}{k, r.Counts[k]})
		}
		return cols, rows
	default:
		return []string{"metric", "eligible"}, [][]interface{}{{r.Metric, r.Eligible}}
	}
}

// printDiagnostics writes the machine-readable caveats and published exclusions
// that accompany a result (cohort, genuine-done state, build/freshness,
// exclusions, warnings). It never corrupts the primary data stream: row-format
// callers pass os.Stderr so the CSV/TSV on stdout stays parseable.
func printDiagnostics(w io.Writer, r *metrics.Result) {
	fmt.Fprintf(w, "cohort: %s\n", r.Cohort)
	fmt.Fprintf(w, "basis: %s | as_of: %s | taxonomy: %s\n",
		r.Basis, r.AsOf.UTC().Format(time.RFC3339), r.TaxonomyVersion)
	if r.GenuineDone {
		fmt.Fprintln(w, "genuine-done: applied (delivery resolutions only)")
	} else {
		fmt.Fprintln(w, "genuine-done: not applied (includes non-delivery resolutions or N/A)")
	}
	if r.BuildGeneration != 0 {
		fmt.Fprintf(w, "build generation: %d", r.BuildGeneration)
		if r.BuiltAt != nil {
			fmt.Fprintf(w, " (built %s)", r.BuiltAt.UTC().Format(time.RFC3339))
		}
		fmt.Fprintln(w)
	}
	if r.Ratio != nil && len(r.Ratio.Classes) > 0 {
		fmt.Fprintln(w, "class counts:")
		for _, k := range sortedCountKeys(r.Ratio.Classes) {
			fmt.Fprintf(w, "  %s: %d\n", k, r.Ratio.Classes[k])
		}
	}
	if len(r.Excluded) > 0 {
		fmt.Fprintln(w, "excluded:")
		for _, k := range sortedCountKeys(r.Excluded) {
			fmt.Fprintf(w, "  %s: %d\n", k, r.Excluded[k])
		}
	}
	for _, msg := range r.Warnings {
		fmt.Fprintf(w, "warning: %s\n", msg)
	}
}

func runMetricCoverage() error {
	pol := metrics.CompilePolicy(g.cfg.Metrics)
	report, err := metrics.ReportCoverage(g.db, splitProjects(metricProjects), pol, g.cfg)
	if err != nil {
		return metricErr("CoverageError", err)
	}
	if g.jsonOut || g.format == "json" {
		fmt.Println(string(output.OK(report)))
		return nil
	}

	cols := []string{"project", "issues", "with_changelog", "any_history_pct",
		"pending_backlog", "never_checked", "updated_after_check", "unmapped_statuses"}
	rows := make([][]interface{}, 0, len(report.Projects))
	for _, p := range report.Projects {
		rows = append(rows, []interface{}{
			p.Project, p.Issues, p.WithChangelog, round2(p.AnyHistoryCoveragePct),
			p.PendingHistoryBacklog, p.NeverChecked, p.UpdatedAfterCheck,
			strings.Join(p.UnmappedStatuses, ", "),
		})
	}

	switch g.format {
	case "csv":
		fmt.Print(output.CSV(cols, rows))
	case "tsv":
		fmt.Print(output.TSV(cols, rows))
	case "markdown":
		fmt.Print(output.Markdown(cols, rows))
	default:
		fmt.Println("=== metrics coverage ===")
		fmt.Print(output.Table(cols, rows))
		printCoverageCaveats(os.Stdout, report)
		return nil
	}
	printCoverageCaveats(os.Stderr, report)
	return nil
}

func printCoverageCaveats(w io.Writer, report *metrics.CoverageReport) {
	if len(report.AbsentTrackedFields) > 0 {
		fmt.Fprintf(w, "absent tracked fields: %s\n", strings.Join(report.AbsentTrackedFields, ", "))
	}
	if report.Sprint.Reason != "" {
		fmt.Fprintf(w, "sprint data: %s\n", report.Sprint.Reason)
	}
	if report.DevInfo.Reason != "" {
		fmt.Fprintf(w, "dev info: %s\n", report.DevInfo.Reason)
	}
	if report.StoryPointsGap.Reason != "" {
		fmt.Fprintf(w, "story points: %s\n", report.StoryPointsGap.Reason)
	}
	for _, c := range report.Caveats {
		fmt.Fprintf(w, "caveat: %s\n", c)
	}
}

func runMetricRebuild() error {
	pol := metrics.CompilePolicy(g.cfg.Metrics)
	store := metrics.NewStore(g.db, g.cfg.DB.Path)
	builder := metrics.NewBuilder(store, pol)

	asOf := time.Now().UTC()
	if metricAsOf != "" {
		t, err := parseMetricTime(metricAsOf)
		if err != nil {
			return metricErr("InvalidArgument", err)
		}
		asOf = t
	}

	res, err := builder.BuildAll(asOf, splitProjects(metricProjects))
	if err != nil {
		return metricErr("RebuildError", err)
	}

	if g.jsonOut || g.format == "json" {
		fmt.Println(string(output.OK(res)))
		return nil
	}
	fmt.Printf("Rebuilt fact store: generation %d, %d issues, %d intervals (built %s)\n",
		res.Generation, res.IssueCount, res.IntervalCount, res.BuiltAt.UTC().Format(time.RFC3339))
	return nil
}

// metricErr renders an error on the JSON path as a structured envelope (so
// JSON consumers never see a bare stderr line) and otherwise returns it.
func metricErr(kind string, err error) error {
	if g.jsonOut || g.format == "json" {
		fmt.Println(string(output.Err(kind, err.Error())))
		return nil
	}
	return err
}

// parseMetricTime accepts RFC3339 or a bare YYYY-MM-DD date (interpreted UTC).
func parseMetricTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (use RFC3339 or YYYY-MM-DD)", s)
}

// splitProjects normalizes repeated and comma-separated --project values.
func splitProjects(in []string) []string {
	var out []string
	for _, v := range in {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func sortedRejectionKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedCountKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

func init() {
	metricsCmd.PersistentFlags().StringSliceVarP(&metricProjects, "project", "p", nil,
		"limit to project(s); repeat or comma-separate")
	metricsCmd.PersistentFlags().StringVar(&metricAsOf, "as-of", "",
		"compute open intervals/ages against this time (RFC3339 or YYYY-MM-DD; default now UTC)")

	metricsCmd.Flags().StringVar(&metricFrom, "from", "", "range start (inclusive) for time-bounded metrics")
	metricsCmd.Flags().StringVar(&metricTo, "to", "", "range end (exclusive) for time-bounded metrics")
	metricsCmd.Flags().StringVar(&metricPeriod, "period", "", "throughput/net-flow bucket: month (default) or week")
	metricsCmd.Flags().StringVar(&metricCohort, "cohort", "", "cohort selector (metric-specific: created|completed|all)")
	metricsCmd.Flags().StringVar(&metricBasis, "basis", "", "counting basis (Phase 1: snapshot only)")
	metricsCmd.Flags().BoolVar(&metricGenuine, "genuine", false, "require the genuine-done variant (errors if done_resolutions unset)")
	metricsCmd.Flags().DurationVar(&metricTimeout, "timeout", 60*time.Second, "bound the computation; 0 disables")

	metricsCmd.AddCommand(metricsListCmd, metricsCoverageCmd, metricsRebuildCmd)
	rootCmd.AddCommand(metricsCmd)
}
