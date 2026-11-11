package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/metrics"
	"github.com/sthadka/jai/internal/output"
)

// Dashboard-local flags. --project / --as-of are inherited (persistent) from metricsCmd.
var (
	dashboardOutput string
	dashboardTitle  string
	dashboardFrom   string
	dashboardTo     string
)

var metricsDashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Export a self-contained offline flow-metrics dashboard (HTML)",
	Long: `Export ONE self-contained dashboard.html, fed by the shared metrics service.

The exported file embeds both the computed metric data (as JSON) and the
visualization assets (a pure-SVG renderer, inlined). It has no CDN references, no
<script src>, and makes no network or file-fetch requests at runtime: open the
file from disk, with networking disabled, and every chart renders.

Charts follow the spec chart-mapping: cycle/lead-time distributions and monthly
percentile trends, an all-stage cumulative-flow diagram (WIP), throughput,
net-flow, aged-open, resolution mix, type-class ratio, workload by assignee,
sprint eventual-resolution, and the blocker-age proxy. Metrics that are
unconfigured or have no data render as explicit "unavailable" cards rather than
fabricated charts.

The artifact contains the selected Jira issue data: share it only with viewers
authorized to see those issues.

Examples:
  jai metrics dashboard
  jai metrics dashboard --output flow.html --project ENG
  jai metrics dashboard --from 2024-01-01 --to 2024-07-01 --as-of 2024-07-01`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMetricsDashboard()
	},
}

func runMetricsDashboard() error {
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
	if path == "" {
		path = "dashboard.html"
	}
	if err := os.WriteFile(path, html, 0o644); err != nil {
		return metricErr("DashboardError", fmt.Errorf("write %s: %w", path, err))
	}

	if g.jsonOut || g.format == "json" {
		fmt.Println(string(output.OK(map[string]interface{}{
			"path":            path,
			"bytes":           len(html),
			"self_contained":  true,
			"metric_warnings": rejections,
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
	metricsDashboardCmd.Flags().StringVarP(&dashboardOutput, "output", "o", "dashboard.html",
		"output HTML file path")
	metricsDashboardCmd.Flags().StringVar(&dashboardTitle, "title", "Flow Metrics",
		"dashboard heading")
	metricsDashboardCmd.Flags().StringVar(&dashboardFrom, "from", "",
		"trend window start (inclusive; RFC3339 or YYYY-MM-DD; default as_of − 6 months)")
	metricsDashboardCmd.Flags().StringVar(&dashboardTo, "to", "",
		"trend window end (exclusive; RFC3339 or YYYY-MM-DD; default as_of)")

	metricsCmd.AddCommand(metricsDashboardCmd)
}
