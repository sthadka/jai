package specs

import (
	"os"
	"path/filepath"
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
