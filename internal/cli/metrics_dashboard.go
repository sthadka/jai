package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/metrics"
	"github.com/sthadka/jai/internal/metrics/dashboard/live"
	"github.com/sthadka/jai/internal/output"
)

// Dashboard-local flags. --project / --as-of are inherited (persistent) from metricsCmd.
var (
	dashboardOutput string
	dashboardTitle  string
	dashboardFrom   string
	dashboardTo     string
	dashboardPort   int
)

var metricsDashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Serve a live, interactive flow-metrics dashboard (localhost)",
	Long: `Serve a LIVE, interactive flow-metrics dashboard.

By default this starts a localhost-only HTTP server backed by one persistent
DuckDB process (jai.db ATTACHed READ_ONLY) and opens your browser at it. Every
control (measure / slice / segment / time-grain / project / date-window / cohort)
re-queries the live fact store and re-renders with a vendored Vega-Lite SPA — no
CDN, no export step. The project dropdown lists the projects present in the active
fact generation. The one dependency is the ` + "`duckdb`" + ` binary on PATH.

The data endpoint (/q) is read-only, SELECT-only, and parameterized from a fixed
SQL template plus whitelisted identifiers — never raw client SQL, so there is no
injection or write surface.

Pass --output to instead write the self-contained offline snapshot (pure-SVG,
inlined JSON, no server, no deps) — a portable artifact for stakeholders without
jai/duckdb.

Examples:
  jai metrics dashboard
  jai metrics dashboard --port 8099
  jai metrics dashboard --output flow.html --project ENG`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if dashboardOutput != "" {
			return runMetricsDashboardStatic()
		}
		return runMetricsDashboardLive()
	},
}

// runMetricsDashboardLive starts the persistent DuckDB-backed localhost server.
func runMetricsDashboardLive() error {
	_, pol, store := metricsService()

	engine, err := live.NewEngine(live.EngineConfig{
		DBPath:          g.cfg.DB.Path,
		DoneResolutions: pol.DoneResolutions(),
	})
	if err != nil {
		if errors.Is(err, live.ErrDuckDBNotFound) {
			fmt.Fprintln(os.Stderr, "error: the live dashboard requires the `duckdb` binary on your PATH.")
			fmt.Fprintln(os.Stderr, "  Install it with:  brew install duckdb")
			fmt.Fprintln(os.Stderr, "  Or download from: https://duckdb.org/docs/installation/")
			fmt.Fprintln(os.Stderr, "  (Alternatively, `jai metrics dashboard --output dashboard.html` writes an offline snapshot with no duckdb dependency.)")
			return metricErr("DuckDBNotFound", err)
		}
		return metricErr("DashboardError", err)
	}
	defer engine.Close()

	projects, err := live.FetchProjects(engine)
	if err != nil {
		return metricErr("DashboardError", fmt.Errorf("list active-generation projects: %w", err))
	}

	var gen any
	if meta, gerr := store.ActiveGeneration(); gerr == nil && meta != nil {
		gen = meta.Generation
	}

	srv := live.NewServer(engine, projects, gen)
	url, httpSrv, err := srv.ListenAndServe(dashboardPort)
	if err != nil {
		return metricErr("DashboardError", fmt.Errorf("bind localhost server: %w", err))
	}

	fmt.Printf("Live flow-metrics dashboard: %s\n", url)
	fmt.Printf("Active generation %v · projects: %v\n", gen, projects)
	fmt.Println("Read-only, localhost-only. Press Ctrl-C to stop.")
	if err := browserOpener(url); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not open browser automatically (%v); open %s manually\n", err, url)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	<-ctx.Done()

	fmt.Println("\nShutting down…")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	return nil
}

// runMetricsDashboardStatic writes the self-contained offline snapshot. Reachable
// via --output; jai-2lc.5 promotes this to --snapshot.
func runMetricsDashboardStatic() error {
	svc, pol, _ := metricsService()

	asOf, err := parseMetricTime(metricAsOf)
	if err != nil {
		return metricErr("InvalidArgument", err)
	}
	from, err := parseMetricTime(dashboardFrom)
	if err != nil {
		return metricErr("InvalidArgument", err)
	}
	to, err := parseMetricTime(dashboardTo)
	if err != nil {
		return metricErr("InvalidArgument", err)
	}

	opts := metrics.DashboardOptions{
		Title:    dashboardTitle,
		Projects: splitProjects(metricProjects),
		AsOf:     asOf,
	}
	if !from.IsZero() || !to.IsZero() {
		opts.Range = &metrics.TimeRange{From: from, To: to}
	}

	html, err := metrics.ExportDashboard(svc, opts)
	if err != nil {
		return metricErr("DashboardError", err)
	}

	// Config rejections are already scoped to the affected metric (those sections render as
	// unavailable); surface them on the JSON path so the export is never silently partial.
	rejections := sortedRejectionKeys(pol.Rejections())

	path := dashboardOutput
	if err := os.WriteFile(path, html, 0o644); err != nil {
		return metricErr("DashboardError", fmt.Errorf("write %s: %w", path, err))
	}

	if g.jsonOut || g.format == "json" {
		fmt.Println(string(output.OK(map[string]interface{}{
			"path":       path,
			"bytes":      len(html),
			"rejections": rejections,
		})))
		return nil
	}
	fmt.Printf("Wrote self-contained offline dashboard: %s (%d bytes)\n", path, len(html))
	fmt.Println("No external/CDN references; open it from disk with networking disabled and every chart renders.")
	fmt.Println("Note: this artifact embeds the selected Jira issue data; share it only with authorized viewers.")
	for _, m := range rejections {
		fmt.Fprintf(os.Stderr, "warning: metric %q rejected by config (rendered as unavailable)\n", m)
	}
	return nil
}

func init() {
	metricsDashboardCmd.Flags().StringVarP(&dashboardOutput, "output", "o", "",
		"write the self-contained offline snapshot to this path instead of serving live")
	metricsDashboardCmd.Flags().IntVar(&dashboardPort, "port", 0,
		"localhost port for the live server (0 = ephemeral)")
	metricsDashboardCmd.Flags().StringVar(&dashboardTitle, "title", "Flow Metrics",
		"snapshot heading (--output only)")
	metricsDashboardCmd.Flags().StringVar(&dashboardFrom, "from", "",
		"snapshot trend window start (--output only; RFC3339 or YYYY-MM-DD)")
	metricsDashboardCmd.Flags().StringVar(&dashboardTo, "to", "",
		"snapshot trend window end (--output only; RFC3339 or YYYY-MM-DD)")

	metricsCmd.AddCommand(metricsDashboardCmd)
}
