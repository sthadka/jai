//go:build fts5

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/metrics"
)

// callMetrics invokes the jai_metrics handler and returns the single text
// payload plus whether the result was flagged as an error.
func callMetrics(t *testing.T, srv *Server, args map[string]interface{}) (string, bool) {
	t.Helper()
	res, err := handleJaiMetrics(srv, context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: args},
	})
	if err != nil {
		t.Fatalf("handleJaiMetrics returned transport error: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("handleJaiMetrics returned no content")
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("handleJaiMetrics content was not text: %T", res.Content[0])
	}
	return tc.Text, res.IsError
}

// seedMetricsServer builds a test server, installs a metrics config and seeds
// the issues the metric reads.
func seedMetricsServer(t *testing.T, mc *config.MetricsConfig, issues []*db.Issue) *Server {
	t.Helper()
	srv := testServer(t, false, nil)
	srv.cfg.Metrics = mc
	for _, is := range issues {
		if err := srv.db.UpsertIssue(is, nil); err != nil {
			t.Fatalf("seed issue %s: %v", is.Key, err)
		}
	}
	return srv
}

// TestHandleMetricsList verifies the no-metric call enumerates the registered
// metrics from the shared registry.
func TestHandleMetricsList(t *testing.T) {
	srv := testServer(t, false, nil)

	text, isErr := callMetrics(t, srv, map[string]interface{}{})
	if isErr {
		t.Fatalf("list returned error: %s", text)
	}

	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Metrics []string `json:"metrics"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("unmarshal list: %v\n%s", err, text)
	}
	if !env.OK {
		t.Fatalf("list not ok: %s", text)
	}
	// The returned set must be exactly the shared registry (no MCP-local fork).
	want := metrics.Metrics()
	if len(env.Data.Metrics) != len(want) {
		t.Fatalf("metric count = %d, want %d (%v)", len(env.Data.Metrics), len(want), want)
	}
	found := map[string]bool{}
	for _, m := range env.Data.Metrics {
		found[m] = true
	}
	for _, m := range want {
		if !found[m] {
			t.Fatalf("list missing registered metric %q: %v", m, env.Data.Metrics)
		}
	}
}

// TestHandleMetricsRunMatchesService runs aged_open through the MCP tool and
// asserts the envelope matches a direct shared-service computation — i.e. the
// MCP consumer returns the same values/taxonomy as the CLI, never a fork.
func TestHandleMetricsRunMatchesService(t *testing.T) {
	now := time.Now().UTC()
	issues := []*db.Issue{
		{Key: "P-1", Project: "P", Type: "Bug", Status: "In Progress", StatusCategory: "In Progress",
			Created: now.AddDate(0, 0, -3).Format(time.RFC3339)},
		{Key: "P-2", Project: "P", Type: "Bug", Status: "To Do", StatusCategory: "To Do",
			Created: now.AddDate(0, 0, -40).Format(time.RFC3339)},
		{Key: "P-3", Project: "P", Type: "Story", Status: "Done", StatusCategory: "Done",
			Created:  now.AddDate(0, 0, -100).Format(time.RFC3339),
			Resolved: now.AddDate(0, 0, -1).Format(time.RFC3339), Resolution: "Done"},
	}
	srv := seedMetricsServer(t, &config.MetricsConfig{}, issues)

	text, isErr := callMetrics(t, srv, map[string]interface{}{
		"metric":  "aged_open",
		"project": "P",
	})
	if isErr {
		t.Fatalf("run returned error: %s", text)
	}

	var env struct {
		OK   bool            `json:"ok"`
		Data *metrics.Result `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("unmarshal run: %v\n%s", err, text)
	}
	if !env.OK || env.Data == nil {
		t.Fatalf("run not ok: %s", text)
	}

	// Compute directly through the shared service and compare the counted data.
	svc, _ := srv.metricsService()
	want, err := svc.Compute(metrics.Query{Metric: "aged_open", Projects: []string{"P"}})
	if err != nil {
		t.Fatalf("direct compute: %v", err)
	}

	if env.Data.Metric != "aged_open" {
		t.Fatalf("metric = %q, want aged_open", env.Data.Metric)
	}
	if env.Data.TaxonomyVersion != want.TaxonomyVersion {
		t.Fatalf("taxonomy version %q != service %q", env.Data.TaxonomyVersion, want.TaxonomyVersion)
	}
	if len(env.Data.Counts) != len(want.Counts) {
		t.Fatalf("bucket count mismatch: mcp=%v service=%v", env.Data.Counts, want.Counts)
	}
	for k, v := range want.Counts {
		if env.Data.Counts[k] != v {
			t.Fatalf("bucket %q = %d, want %d (mcp=%v)", k, env.Data.Counts[k], v, env.Data.Counts)
		}
	}
	// The two open issues land in distinct age buckets; the delivered P-3 is not open.
	if env.Data.Counts["0-7d"] != 1 || env.Data.Counts["31-90d"] != 1 {
		t.Fatalf("unexpected age buckets: %v", env.Data.Counts)
	}
}

// TestHandleMetricsBoundedInput verifies malformed bounded inputs fail closed
// with an actionable structured error rather than pretending success.
func TestHandleMetricsBoundedInput(t *testing.T) {
	srv := seedMetricsServer(t, &config.MetricsConfig{}, nil)

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantSub string
	}{
		{
			name:    "bad as_of",
			args:    map[string]interface{}{"metric": "aged_open", "as_of": "last-tuesday"},
			wantSub: "invalid as_of",
		},
		{
			name:    "bad from",
			args:    map[string]interface{}{"metric": "aged_open", "from": "nope"},
			wantSub: "invalid from",
		},
		{
			name:    "inverted range",
			args:    map[string]interface{}{"metric": "throughput", "from": "2024-02-01", "to": "2024-01-01"},
			wantSub: "must be after",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := callMetrics(t, srv, tt.args)
			if !isErr {
				t.Fatalf("expected error result, got: %s", text)
			}
			if !strings.Contains(text, tt.wantSub) {
				t.Fatalf("error %q missing %q", text, tt.wantSub)
			}
		})
	}
}

// TestHandleMetricsTimeoutClamp verifies the requested timeout is bounded to the
// cap so no request can disable the MCP-window deadline, and defaults when
// absent/non-positive.
func TestHandleMetricsTimeoutClamp(t *testing.T) {
	cases := []struct {
		in   float64
		want time.Duration
	}{
		{in: 0, want: metricsTimeoutDefault},
		{in: -5, want: metricsTimeoutDefault},
		{in: 10, want: 10 * time.Second},
		{in: 999, want: metricsTimeoutCap},
	}
	for _, c := range cases {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{"timeout_seconds": c.in},
		}}
		if got := metricsTimeoutFrom(req); got != c.want {
			t.Fatalf("timeout(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestHandleMetricsUnknown verifies an unknown metric name returns a structured
// error (ErrUnknownMetric), not an empty success.
func TestHandleMetricsUnknown(t *testing.T) {
	srv := seedMetricsServer(t, &config.MetricsConfig{}, nil)

	text, isErr := callMetrics(t, srv, map[string]interface{}{"metric": "does_not_exist"})
	if !isErr {
		t.Fatalf("expected error for unknown metric, got: %s", text)
	}
	if !strings.Contains(text, "does_not_exist") {
		t.Fatalf("error missing metric name: %s", text)
	}
}

// TestHandleMetricsUnsupportedBasis verifies a non-snapshot basis is rejected
// (the shared service never silently downgrades the basis).
func TestHandleMetricsUnsupportedBasis(t *testing.T) {
	srv := seedMetricsServer(t, &config.MetricsConfig{}, nil)

	text, isErr := callMetrics(t, srv, map[string]interface{}{
		"metric": "aged_open",
		"basis":  "event",
	})
	if !isErr {
		t.Fatalf("expected error for unsupported basis, got: %s", text)
	}
}
