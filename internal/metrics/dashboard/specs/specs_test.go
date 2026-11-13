package specs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// the24 is the full flow-metrics suite (plan §1). Every id MUST be covered by some
// default panel — either a live panel or an explicit unavailable/requires card.
var the24 = []string{
	"cycle_time", "lead_time", "flow_occupancy", "triage_latency", "stage_dwell",
	"blocker_ageing", "security_sla", "reopen", "priority_changes", "rank_volatility",
	"reassignment_count", "reassignment_thrash", "resolution_mix", "aged_open", "ratio",
	"sprint_resolution", "workload_by_assignee", "workload_throughput",
	"workload_throughput_current", "workload_time_on_owner", "throughput", "net_flow",
	"wip", "cfd",
}

// TestEmbeddedCovers24 proves the batteries-included defaults cover every metric of
// the 24-metric suite, so a fresh install is useful with zero config.
func TestEmbeddedCovers24(t *testing.T) {
	dbs, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded: %v", err)
	}
	covered := map[string]bool{}
	for _, d := range dbs {
		if d.Source != SourceEmbedded {
			t.Fatalf("embedded dashboard %q source = %q", d.ID, d.Source)
		}
		for _, p := range d.Panels {
			if p.Metric != "" {
				covered[p.Metric] = true
			}
		}
	}
	for _, m := range the24 {
		if !covered[m] {
			t.Errorf("metric %q not covered by any default panel", m)
		}
	}
}

// TestUnavailablePanelsAreExplicit proves shape-incompatible metrics carry a static
// unavailable reason (never fabricated data).
func TestUnavailablePanelsAreExplicit(t *testing.T) {
	dbs, _ := Embedded()
	want := map[string]bool{"cfd": true, "wip": true, "stage_dwell": true, "workload_time_on_owner": true, "security_sla": true}
	got := map[string]bool{}
	for _, d := range dbs {
		for _, p := range d.Panels {
			if p.Unavailable != "" && p.Metric != "" {
				got[p.Metric] = true
			}
		}
	}
	for m := range want {
		if !got[m] {
			t.Errorf("metric %q expected an explicit unavailable card", m)
		}
	}
}

// TestLoadCustomWithoutRebuild proves a spec dropped into a runtime dir appears in
// the merged set (the server calls Load per request, so no rebuild is needed).
func TestLoadCustomWithoutRebuild(t *testing.T) {
	dir := t.TempDir()
	custom := `id: my-custom
title: My Custom
panels:
  - title: Lead by project
    metric: lead_time
    measure: lead_time
    slice: project
    chart: scatter
    defaults: { cohort: delivered }
`
	if err := os.WriteFile(filepath.Join(dir, "foo.yaml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	dbs, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var found *Dashboard
	for i := range dbs {
		if dbs[i].ID == "my-custom" {
			found = &dbs[i]
		}
	}
	if found == nil {
		t.Fatalf("custom dashboard not loaded; got %d dashboards", len(dbs))
	}
	if found.Source != SourceUser {
		t.Errorf("custom source = %q, want %q", found.Source, SourceUser)
	}
}

// TestLoadJSONCustom proves JSON custom specs load too (foo.json).
func TestLoadJSONCustom(t *testing.T) {
	dir := t.TempDir()
	js := `{"id":"j","title":"J","panels":[{"title":"t","metric":"throughput","measure":"throughput","slice":"period","chart":"line"}]}`
	if err := os.WriteFile(filepath.Join(dir, "j.json"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	dbs, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, d := range dbs {
		if d.ID == "j" && len(d.Panels) == 1 {
			return
		}
	}
	t.Fatal("JSON custom spec not loaded")
}

// TestCustomOverridesEmbedded proves a later dir overrides an embedded dashboard by
// id (project-local > user > embedded precedence).
func TestCustomOverridesEmbedded(t *testing.T) {
	dir := t.TempDir()
	override := "id: delivery\ntitle: Overridden Delivery\npanels: []\n"
	if err := os.WriteFile(filepath.Join(dir, "delivery.yaml"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	dbs, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, d := range dbs {
		if d.ID == "delivery" {
			if d.Title != "Overridden Delivery" {
				t.Fatalf("delivery not overridden: title=%q", d.Title)
			}
			return
		}
	}
	t.Fatal("delivery dashboard missing")
}

// TestResolveAvailability proves a requires-gated panel becomes an explicit
// unavailable card when the capability is unsatisfied, and stays live when it is.
func TestResolveAvailability(t *testing.T) {
	dbs := []Dashboard{{ID: "x", Panels: []Panel{
		{Title: "blocker", Metric: "blocker_ageing", Measure: "blocker_ageing", Requires: "blocker_link_types"},
	}}}

	off := ResolveAvailability(dbs, map[string]bool{})
	if off[0].Panels[0].Unavailable == "" {
		t.Fatal("expected unavailable when capability off")
	}
	on := ResolveAvailability(dbs, map[string]bool{"blocker_link_types": true})
	if on[0].Panels[0].Unavailable != "" {
		t.Fatalf("expected live panel when capability on, got %q", on[0].Panels[0].Unavailable)
	}
}

// TestLoadQueryPanel proves a kind:query panel round-trips through Load/parse into
// the expected Panel fields (source/query + client-side shaping).
func TestLoadQueryPanel(t *testing.T) {
	dir := t.TempDir()
	custom := `id: my-explore
title: My Explore
panels:
  - title: Open by status
    kind: query
    source: jql
    query: project = JAI AND statusCategory != Done
    chart: bar
    x: status
    y: "**count**"
    agg: count
    segment: priority
    filters:
      status: [New, Backlog]
`
	if err := os.WriteFile(filepath.Join(dir, "explore.yaml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	dbs, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var p *Panel
	for i := range dbs {
		if dbs[i].ID == "my-explore" && len(dbs[i].Panels) == 1 {
			p = &dbs[i].Panels[0]
		}
	}
	if p == nil {
		t.Fatalf("kind:query dashboard not loaded; got %d dashboards", len(dbs))
	}
	if p.Kind != "query" {
		t.Errorf("Kind = %q, want %q", p.Kind, "query")
	}
	if p.Source != "jql" {
		t.Errorf("Source = %q, want %q", p.Source, "jql")
	}
	if p.Query != "project = JAI AND statusCategory != Done" {
		t.Errorf("Query = %q", p.Query)
	}
	if p.Chart != "bar" {
		t.Errorf("Chart = %q, want %q", p.Chart, "bar")
	}
	if p.X != "status" {
		t.Errorf("X = %q, want %q", p.X, "status")
	}
	if p.Y != "**count**" {
		t.Errorf("Y = %q, want %q", p.Y, "**count**")
	}
	if p.Agg != "count" {
		t.Errorf("Agg = %q, want %q", p.Agg, "count")
	}
	if p.Segment != "priority" {
		t.Errorf("Segment = %q, want %q", p.Segment, "priority")
	}
	if got := p.Filters["status"]; len(got) != 2 || got[0] != "New" || got[1] != "Backlog" {
		t.Errorf("Filters[status] = %v, want [New Backlog]", got)
	}
}

// TestLoadMetricPanelUnaffected proves a plain metric panel still parses with its
// metric fields and no stray kind:query fields (regression for the new fields).
func TestLoadMetricPanelUnaffected(t *testing.T) {
	dir := t.TempDir()
	custom := `id: my-metric
title: My Metric
panels:
  - title: Lead by project
    metric: lead_time
    measure: lead_time
    slice: project
    chart: scatter
    defaults: { cohort: delivered }
`
	if err := os.WriteFile(filepath.Join(dir, "metric.yaml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	dbs, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var p *Panel
	for i := range dbs {
		if dbs[i].ID == "my-metric" && len(dbs[i].Panels) == 1 {
			p = &dbs[i].Panels[0]
		}
	}
	if p == nil {
		t.Fatalf("metric dashboard not loaded; got %d dashboards", len(dbs))
	}
	if p.Kind != "" {
		t.Errorf("Kind = %q, want empty", p.Kind)
	}
	if p.Measure != "lead_time" || p.Slice != "project" || p.Chart != "scatter" {
		t.Errorf("metric fields mangled: measure=%q slice=%q chart=%q", p.Measure, p.Slice, p.Chart)
	}
	if p.Defaults.Cohort != "delivered" {
		t.Errorf("Defaults.Cohort = %q, want %q", p.Defaults.Cohort, "delivered")
	}
	if p.Source != "" || p.Query != "" || p.X != "" || p.Y != "" || p.Agg != "" || p.Filters != nil {
		t.Errorf("unexpected kind:query fields set on metric panel: %+v", p)
	}
}

// TestUserDashboardDir proves the save-target dir resolves under ~/.config/jai.
func TestUserDashboardDir(t *testing.T) {
	dir, err := UserDashboardDir()
	if err != nil {
		t.Fatalf("UserDashboardDir: %v", err)
	}
	want := filepath.Join(".config", "jai", "dashboards")
	if !strings.HasSuffix(dir, want) {
		t.Errorf("UserDashboardDir = %q, want suffix %q", dir, want)
	}
}
