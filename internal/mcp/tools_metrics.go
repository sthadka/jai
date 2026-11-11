package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sthadka/jai/internal/metrics"
	"github.com/sthadka/jai/internal/output"
)

// metricsTimeoutCap bounds every metric computation so a heavy/unbounded query
// can never outlive the MCP tool call window (the client enforces a ~30s
// timeout). The computation runs in a goroutine and is abandoned on deadline or
// client cancellation.
const (
	metricsTimeoutCap     = 30 * time.Second
	metricsTimeoutDefault = 25 * time.Second
)

// registerMetricsTools registers the flow-metrics MCP tool. It is the read-only
// `metrics` toolset: it only reads the local fact store / issues through the
// single shared metrics service (the same definition the CLI `jai metrics`
// uses), so equivalent requests return identical values, denominators, time
// basis and taxonomy version. It never writes the metric DB or forks any SQL.
func registerMetricsTools(s *Server, srv *server.MCPServer) {
	if !s.toolsets.IsEnabled("metrics") {
		return
	}

	srv.AddTool(mcp.Tool{
		Name: "jai_metrics",
		Description: "Compute flow metrics through the shared metrics service (identical to `jai metrics`). " +
			"Omit `metric` to list available metrics. Supply `metric` to run one and get the full result " +
			"envelope (values plus basis, cohort, taxonomy version, policies, exclusions, warnings, build " +
			"generation and per-project source freshness). Read-only; bounded and cancellable within the tool timeout.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"metric": map[string]string{
					"type":        "string",
					"description": "Metric id to run (e.g. cycle_time, throughput, ratio). Omit to list all available metrics.",
				},
				"project": map[string]string{
					"type":        "string",
					"description": "Scope to one or more project keys (comma-separated). Empty = all projects.",
				},
				"as_of": map[string]string{
					"type":        "string",
					"description": "Point in time for open-interval/aged durations (RFC3339 or YYYY-MM-DD, UTC). Defaults to now.",
				},
				"from": map[string]string{
					"type":        "string",
					"description": "Inclusive start of the time range (RFC3339 or YYYY-MM-DD, UTC).",
				},
				"to": map[string]string{
					"type":        "string",
					"description": "Exclusive end of the time range (RFC3339 or YYYY-MM-DD, UTC).",
				},
				"period": map[string]string{
					"type":        "string",
					"description": "Bucket for series metrics (throughput): month (default) or week.",
				},
				"cohort": map[string]string{
					"type":        "string",
					"description": "Cohort selector where applicable (e.g. created or completed).",
				},
				"basis": map[string]string{
					"type":        "string",
					"description": "Counting basis. Only snapshot is supported; other values are rejected.",
				},
				"genuine": map[string]interface{}{
					"type":        "boolean",
					"description": "Require the genuine-done completion filter; errors if done_resolutions is unset instead of including non-delivery resolutions.",
					"default":     false,
				},
				"timeout_seconds": map[string]interface{}{
					"type":        "number",
					"description": "Computation deadline in seconds (default 25, capped at 30).",
				},
			},
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleJaiMetrics(s, ctx, req)
	})
}

// metricsService builds the shared service and compiled policy from the active
// config and DB. There is exactly one definition of each metric behind this —
// the same construction the CLI uses — so the MCP consumer never forks logic.
func (s *Server) metricsService() (*metrics.Service, *metrics.Policy) {
	pol := metrics.CompilePolicy(s.cfg.Metrics)
	store := metrics.NewStore(s.db, s.cfg.DB.Path)
	svc := metrics.NewService(s.db, store, pol, s.cfg)
	return svc, pol
}

// handleJaiMetrics lists metrics (no `metric`) or runs one through the shared
// service, returning the Result envelope. Invalid inputs, unknown metrics and
// cancellation yield structured errors rather than a fabricated success.
func handleJaiMetrics(s *Server, ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := strings.TrimSpace(req.GetString("metric", ""))
	if name == "" {
		return mcp.NewToolResultText(string(output.OK(map[string]interface{}{
			"metrics": metrics.Metrics(),
		}))), nil
	}

	asOf, err := parseMetricTime(req.GetString("as_of", ""))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid as_of: %v", err)), nil
	}
	from, err := parseMetricTime(req.GetString("from", ""))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid from: %v", err)), nil
	}
	to, err := parseMetricTime(req.GetString("to", ""))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid to: %v", err)), nil
	}
	if !from.IsZero() && !to.IsZero() && !to.After(from) {
		return mcp.NewToolResultError(fmt.Sprintf("invalid range: to (%s) must be after from (%s)",
			to.Format(time.RFC3339), from.Format(time.RFC3339))), nil
	}

	svc, pol := s.metricsService()

	q := metrics.Query{
		Metric:         name,
		Projects:       splitMetricProjects(req.GetString("project", "")),
		AsOf:           asOf,
		Basis:          strings.TrimSpace(req.GetString("basis", "")),
		Period:         strings.TrimSpace(req.GetString("period", "")),
		Cohort:         strings.TrimSpace(req.GetString("cohort", "")),
		RequireGenuine: req.GetBool("genuine", false),
	}
	if !from.IsZero() || !to.IsZero() {
		q.Range = &metrics.TimeRange{From: from, To: to}
	}

	r, err := computeMetricBounded(ctx, svc, q, metricsTimeoutFrom(req))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("metric %q failed: %v", name, err)), nil
	}

	// Surface fail-closed config rejections as warnings (the affected metric is
	// already excluded inside CompilePolicy; unrelated metrics keep working),
	// matching the CLI so both consumers report the same caveats.
	for _, m := range sortedPolicyRejections(pol) {
		r.Warnings = append(r.Warnings,
			fmt.Sprintf("config: metric %q rejected: %s", m, strings.Join(pol.Rejections()[m], "; ")))
	}

	return mcp.NewToolResultText(string(output.OK(r))), nil
}

// computeMetricBounded runs svc.Compute under a bounded, cancellable deadline so
// a heavy metric can never outlive the MCP tool window. Client cancellation
// (ctx) also aborts the wait. The goroutine result is buffered so Compute can
// finish without leaking even after a timeout.
func computeMetricBounded(ctx context.Context, svc *metrics.Service, q metrics.Query, d time.Duration) (*metrics.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	type outcome struct {
		r   *metrics.Result
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		r, err := svc.Compute(q)
		ch <- outcome{r, err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("computation cancelled after %s: %w", d, ctx.Err())
	case out := <-ch:
		return out.r, out.err
	}
}

// metricsTimeoutFrom clamps the requested timeout to (0, cap], defaulting when
// absent or non-positive, so no request can disable the bound.
func metricsTimeoutFrom(req mcp.CallToolRequest) time.Duration {
	secs := req.GetFloat("timeout_seconds", 0)
	if secs <= 0 {
		return metricsTimeoutDefault
	}
	d := time.Duration(secs * float64(time.Second))
	if d > metricsTimeoutCap {
		return metricsTimeoutCap
	}
	return d
}

// splitMetricProjects normalizes a comma-separated project list into a deduped,
// trimmed slice (mirrors the CLI --project handling).
func splitMetricProjects(in string) []string {
	var out []string
	for _, p := range strings.Split(in, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sortedPolicyRejections returns the rejected metric names in stable order.
func sortedPolicyRejections(pol *metrics.Policy) []string {
	rej := pol.Rejections()
	keys := make([]string, 0, len(rej))
	for k := range rej {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// parseMetricTime accepts RFC3339 or a bare YYYY-MM-DD date (interpreted UTC).
// Empty input yields the zero time (no bound).
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
