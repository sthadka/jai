package live

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestDashboardsEndpointEmbedded proves /dashboards serves the embedded defaults
// without needing the DuckDB engine (the handler reads only the spec FS + dirs).
func TestDashboardsEndpointEmbedded(t *testing.T) {
	srv := NewServer(nil, nil, int64(1))
	// Point at no custom dirs so only embedded defaults are returned.
	srv.SetDashboards(map[string]bool{}, []string{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/dashboards")
	if err != nil {
		t.Fatalf("GET /dashboards: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Dashboards []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
			Panels []struct {
				Metric      string `json:"metric"`
				Unavailable string `json:"unavailable"`
			} `json:"panels"`
		} `json:"dashboards"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Dashboards) < 5 {
		t.Fatalf("expected >=5 embedded dashboards, got %d", len(body.Dashboards))
	}
	// blocker_ageing has requires:blocker_link_types; with caps off it must be an
	// explicit unavailable card (never fabricated).
	for _, d := range body.Dashboards {
		for _, p := range d.Panels {
			if p.Metric == "blocker_ageing" && p.Unavailable == "" {
				t.Fatal("blocker_ageing should be unavailable when capability off")
			}
		}
	}
}

// TestDashboardsEndpointCustomNoRebuild proves a spec dropped into a configured dir
// appears in the /dashboards output on the next request — no rebuild/restart.
func TestDashboardsEndpointCustomNoRebuild(t *testing.T) {
	dir := t.TempDir()
	srv := NewServer(nil, nil, int64(1))
	srv.SetDashboards(map[string]bool{}, []string{dir})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	spec := "id: live-custom\ntitle: Live Custom\npanels:\n  - title: t\n    metric: throughput\n    measure: throughput\n    slice: period\n    chart: line\n"
	if err := os.WriteFile(filepath.Join(dir, "c.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := ts.Client().Get(ts.URL + "/dashboards")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Dashboards []struct {
			ID string `json:"id"`
		} `json:"dashboards"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	for _, d := range body.Dashboards {
		if d.ID == "live-custom" {
			return
		}
	}
	t.Fatal("custom spec did not appear in /dashboards without rebuild")
}
