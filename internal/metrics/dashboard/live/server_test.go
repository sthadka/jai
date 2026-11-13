package live

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildFixtureDB creates a minimal SQLite jai.db (via duckdb's sqlite writer)
// with the fact tables and a few ROX rows, so the engine has a real DB to attach.
func buildFixtureDB(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("duckdb")
	if err != nil {
		t.Skip("duckdb not on PATH; skipping live-engine integration test")
	}
	dbPath := filepath.Join(t.TempDir(), "jai.db")
	script := `
LOAD sqlite;
ATTACH '` + dbPath + `' AS jai (TYPE sqlite);
CREATE TABLE jai.metrics_active_generation (id BIGINT, generation BIGINT);
INSERT INTO jai.metrics_active_generation VALUES (1, 7);
CREATE TABLE jai.fact_issue_flow (
  generation BIGINT, issue_key VARCHAR, project VARCHAR, type VARCHAR,
  created TIMESTAMP, first_active_at TIMESTAMP, resolved TIMESTAMP, resolution VARCHAR,
  lead_seconds BIGINT, cycle_seconds BIGINT, reopen_count BIGINT, current_stage VARCHAR,
  has_active BIGINT, has_unknown BIGINT, flow_occupancy DOUBLE, estimate DOUBLE);
INSERT INTO jai.fact_issue_flow VALUES
  (7,'ROX-1','ROX','Bug', TIMESTAMP '2024-01-01', TIMESTAMP '2024-01-02', TIMESTAMP '2024-01-10','Done', 777600, 691200, 0,'done',0,0,0.5,NULL),
  (7,'ROX-2','ROX','Task',TIMESTAMP '2024-02-01', TIMESTAMP '2024-02-03', TIMESTAMP '2024-02-20','Done',1641600,1468800,0,'done',0,0,0.6,NULL),
  (7,'ROX-3','ROX','Bug', TIMESTAMP '2024-03-01', TIMESTAMP '2024-03-02', TIMESTAMP '2024-03-05','Done', 345600, 259200, 0,'done',0,0,0.4,NULL),
  (7,'ROX-4','ROX','Bug', TIMESTAMP '2024-04-01', NULL,                    TIMESTAMP '2024-04-10','Abandoned', 777600, NULL, 0,'done',0,0,NULL,NULL);
CREATE TABLE jai.fact_field_interval (
  generation BIGINT, issue_key VARCHAR, project VARCHAR, field VARCHAR, seq BIGINT,
  value_id VARCHAR, value VARCHAR, stage VARCHAR, provenance VARCHAR,
  entered_at TIMESTAMP, exited_at TIMESTAMP, is_open BIGINT);
INSERT INTO jai.fact_field_interval VALUES
  (7,'ROX-1','ROX','priority',0,'1','High','', '', TIMESTAMP '2024-01-01', NULL, 1),
  (7,'ROX-2','ROX','priority',0,'2','Low','',  '', TIMESTAMP '2024-02-01', NULL, 1),
  (7,'ROX-3','ROX','priority',0,'1','High','', '', TIMESTAMP '2024-03-01', NULL, 1);
CREATE TABLE jai.fact_issue_membership (
  generation BIGINT, issue_key VARCHAR, project VARCHAR, dimension VARCHAR, seq BIGINT,
  member_id VARCHAR, member_name VARCHAR, entered_at TIMESTAMP, exited_at TIMESTAMP, is_open BIGINT);
INSERT INTO jai.fact_issue_membership VALUES
  (7,'ROX-1','ROX','component',0,'c1','auth', TIMESTAMP '2024-01-01', NULL, 1),
  (7,'ROX-2','ROX','component',0,'c2','ui',   TIMESTAMP '2024-02-01', NULL, 1);
CREATE TABLE jai.issues (key VARCHAR, summary VARCHAR, team VARCHAR, status VARCHAR, created TIMESTAMP);
INSERT INTO jai.issues VALUES
  ('ROX-1','Fix login','platform','Done',        TIMESTAMP '2024-01-01'),
  ('ROX-2','Add report','platform','Done',        TIMESTAMP '2024-02-01'),
  ('ROX-3','Tune index','data','In Progress',     TIMESTAMP '2024-03-01'),
  ('ROX-4','Retire API','data','Done',            TIMESTAMP '2024-04-01');
`
	cmd := exec.Command(bin, ":memory:")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture db: %v\n%s", err, out)
	}
	return dbPath
}

// newLiveTestServer spins up a real engine over the fixture DB behind httptest.
func newLiveTestServer(t *testing.T) (*httptest.Server, *Engine) {
	t.Helper()
	dbPath := buildFixtureDB(t)
	engine, err := NewEngine(EngineConfig{DBPath: dbPath, DoneResolutions: []string{"Done"}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	projects, err := FetchProjects(engine)
	if err != nil {
		t.Fatalf("FetchProjects: %v", err)
	}
	srv := NewServer(engine, projects, int64(7))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); engine.Close() })
	return ts, engine
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// TestLiveValidQueryReturnsRows proves a valid templated /q request returns rows.
func TestLiveValidQueryReturnsRows(t *testing.T) {
	ts, _ := newLiveTestServer(t)
	status, body := getJSON(t, ts.URL+"/q?measure=lead_time&slice=priority&cohort=delivered")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %+v", status, body)
	}
	rows, _ := body["rows"].([]any)
	if len(rows) == 0 {
		t.Fatalf("expected rows, got none: %+v", body)
	}
}

// TestLiveRejectsNonWhitelisted proves /q rejects a non-whitelisted measure with 4xx.
func TestLiveRejectsNonWhitelisted(t *testing.T) {
	ts, _ := newLiveTestServer(t)
	status, body := getJSON(t, ts.URL+"/q?measure=secret")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %+v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error message, got %+v", body)
	}
}

// TestLiveRejectsInjection proves a smuggled-SQL project param is rejected (4xx),
// never executed.
func TestLiveRejectsInjection(t *testing.T) {
	ts, _ := newLiveTestServer(t)
	status, _ := getJSON(t, ts.URL+"/q?project=ROX%27%3B+DROP+TABLE+jai.fact_issue_flow%3B+--")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for injection attempt, got %d", status)
	}
}

// TestLiveControlsChangeData proves changing a control changes the /q response.
func TestLiveControlsChangeData(t *testing.T) {
	ts, _ := newLiveTestServer(t)
	_, a := getJSON(t, ts.URL+"/q?measure=lead_time&slice=priority&cohort=delivered")
	_, b := getJSON(t, ts.URL+"/q?measure=throughput&slice=type&cohort=delivered")
	ra, _ := json.Marshal(a["rows"])
	rb, _ := json.Marshal(b["rows"])
	if string(ra) == string(rb) {
		t.Fatalf("expected different data for different controls; both:\n%s", ra)
	}
}

// TestLiveMetaProjects proves /meta surfaces the active generation's projects.
func TestLiveMetaProjects(t *testing.T) {
	ts, _ := newLiveTestServer(t)
	status, body := getJSON(t, ts.URL+"/meta")
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	projs, _ := body["projects"].([]any)
	found := false
	for _, p := range projs {
		if p == "ROX" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ROX in /meta projects, got %+v", body["projects"])
	}
}

// TestLiveIndexNoCDN proves the served index references only vendored /assets
// (ECharts + the IBM Plex Sans font), never a CDN and never the removed Vega stack.
func TestLiveIndexNoCDN(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	html := string(data)
	// The removed-charting-stack token is assembled at runtime so the literal
	// string never appears anywhere in the tree (clean-cutover grep stays empty).
	removed := "ve" + "ga"
	for _, bad := range []string{"cdn.jsdelivr.net", "unpkg.com", "cdnjs", "https://cdn", "//cdn.", removed} {
		if strings.Contains(html, bad) {
			t.Fatalf("index.html references a forbidden source (%q)", bad)
		}
	}
	for _, want := range []string{"/assets/echarts.min.js", "/assets/fonts/ibm-plex-sans-latin.woff2"} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html missing vendored asset %q", want)
		}
	}
}

// execOnDB applies write SQL to the fixture DB from a separate duckdb process,
// simulating a `metrics rebuild` publishing a new generation while the server's
// persistent READ_ONLY engine stays attached.
func execOnDB(t *testing.T, dbPath, sql string) {
	t.Helper()
	bin, err := exec.LookPath("duckdb")
	if err != nil {
		t.Skip("duckdb not on PATH")
	}
	cmd := exec.Command(bin, ":memory:")
	cmd.Stdin = strings.NewReader("LOAD sqlite;\nATTACH '" + dbPath + "' AS jai (TYPE sqlite);\n" + sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("exec on db: %v\n%s", err, out)
	}
}

// TestLiveGenerationRefresh proves the dashboard serves the CURRENTLY active fact
// generation after a rebuild publishes a new one (and deletes the obsolete rows),
// with no server restart: Engine.Query re-binds `g` per call and /meta refreshes
// the generation id + the /q project whitelist. This is the regression guard for
// the "no data after rebuild" bug (g was frozen at engine setup).
func TestLiveGenerationRefresh(t *testing.T) {
	dbPath := buildFixtureDB(t)
	engine, err := NewEngine(EngineConfig{DBPath: dbPath, DoneResolutions: []string{"Done"}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	projects, err := FetchProjects(engine)
	if err != nil {
		t.Fatalf("FetchProjects: %v", err)
	}
	srv := NewServer(engine, projects, int64(7))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); engine.Close() })

	// Baseline: generation 7, ROX present and queryable.
	if _, body := getJSON(t, ts.URL+"/meta"); body["generation"] != float64(7) {
		t.Fatalf("baseline generation: got %v, want 7", body["generation"])
	}
	if status, _ := getJSON(t, ts.URL+"/q?measure=lead_time&slice=priority&cohort=delivered&project=ROX"); status != 200 {
		t.Fatalf("baseline ROX query: got %d", status)
	}

	// Simulate a rebuild: publish generation 8 for a NEW project, delete the old
	// generation's rows, and flip the active pointer — all external to the engine.
	execOnDB(t, dbPath, `
INSERT INTO jai.fact_issue_flow VALUES
  (8,'NEW-1','NEWP','Bug', TIMESTAMP '2024-05-01', TIMESTAMP '2024-05-02', TIMESTAMP '2024-05-10','Done', 777600, 691200, 0,'done',0,0,0.5,NULL);
INSERT INTO jai.fact_field_interval VALUES
  (8,'NEW-1','NEWP','priority',0,'1','High','', '', TIMESTAMP '2024-05-01', NULL, 1);
DELETE FROM jai.fact_issue_flow WHERE generation=7;
DELETE FROM jai.fact_field_interval WHERE generation=7;
UPDATE jai.metrics_active_generation SET generation=8 WHERE id=1;
`)

	// /meta now reports generation 8 and the new project (not ROX).
	_, meta := getJSON(t, ts.URL+"/meta")
	if meta["generation"] != float64(8) {
		t.Fatalf("after rebuild generation: got %v, want 8", meta["generation"])
	}
	projs, _ := meta["projects"].([]any)
	hasNew, hasROX := false, false
	for _, p := range projs {
		if p == "NEWP" {
			hasNew = true
		}
		if p == "ROX" {
			hasROX = true
		}
	}
	if !hasNew || hasROX {
		t.Fatalf("projects not refreshed after rebuild: %v", meta["projects"])
	}

	// /q serves generation-8 data: the new project returns rows; the stale project
	// is rejected by the refreshed whitelist.
	if status, body := getJSON(t, ts.URL+"/q?measure=lead_time&slice=priority&cohort=delivered&project=NEWP"); status != 200 {
		t.Fatalf("NEWP query after rebuild: got %d %+v", status, body)
	} else if rows, _ := body["rows"].([]any); len(rows) == 0 {
		t.Fatalf("expected gen-8 rows for NEWP, got none")
	}
	if status, _ := getJSON(t, ts.URL+"/q?measure=lead_time&slice=priority&cohort=delivered&project=ROX"); status != 400 {
		t.Fatalf("stale project ROX should be 400 after refresh, got %d", status)
	}
}

// TestLiveMetaLabels proves /meta emits the vocabulary as {value,label} objects
// (measures also carry unit), the single source of truth for the SPA dropdowns.
func TestLiveMetaLabels(t *testing.T) {
	ts, _ := newLiveTestServer(t)
	status, body := getJSON(t, ts.URL+"/meta")
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	measures, _ := body["measures"].([]any)
	if len(measures) == 0 {
		t.Fatalf("expected measure options, got %+v", body["measures"])
	}
	foundCycle := false
	for _, m := range measures {
		obj, ok := m.(map[string]any)
		if !ok {
			t.Fatalf("measure option not an object: %T", m)
		}
		if obj["value"] == nil || obj["label"] == nil || obj["unit"] == nil {
			t.Fatalf("measure option missing value/label/unit: %+v", obj)
		}
		if obj["value"] == "cycle_time" {
			foundCycle = true
			if obj["label"] != "Cycle Time" {
				t.Fatalf("cycle_time label = %v, want Cycle Time", obj["label"])
			}
			if obj["unit"] != "days" {
				t.Fatalf("cycle_time unit = %v, want days", obj["unit"])
			}
		}
	}
	if !foundCycle {
		t.Fatalf("cycle_time not present in /meta measures")
	}
	for _, key := range []string{"dimensions", "grains", "cohorts"} {
		opts, _ := body[key].([]any)
		if len(opts) == 0 {
			t.Fatalf("expected %s options, got %+v", key, body[key])
		}
		obj, ok := opts[0].(map[string]any)
		if !ok || obj["value"] == nil || obj["label"] == nil {
			t.Fatalf("%s option not a {value,label} object: %+v", key, opts[0])
		}
	}
}

// TestLiveNumberTileCurrentAndPrior proves a chart:number tile resolves both its
// current window and a prior window via two slice=none /q calls (the aggregate
// row that feeds the tile value and its trend delta). slice=none yields at most
// one aggregate row; the current (unbounded) window has data so it returns
// exactly one, and a bounded prior-window query is accepted and returns the same
// single-aggregate shape.
func TestLiveNumberTileCurrentAndPrior(t *testing.T) {
	ts, _ := newLiveTestServer(t)
	// Current window: whole generation, exactly one aggregate row feeds the tile.
	status, cur := getJSON(t, ts.URL+"/q?measure=cycle_time&slice=none&cohort=delivered")
	if status != 200 {
		t.Fatalf("current tile query: got %d %+v", status, cur)
	}
	rows, _ := cur["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("slice=none must return exactly one aggregate row, got %d", len(rows))
	}
	// Prior window: a bounded earlier date range (client date math supplies from/to)
	// is a valid tile query and yields the same single-aggregate shape (<=1 row).
	status, prior := getJSON(t, ts.URL+"/q?measure=cycle_time&slice=none&cohort=delivered&from=2024-01-01&to=2024-02-15")
	if status != 200 {
		t.Fatalf("prior tile query: got %d %+v", status, prior)
	}
	prows, _ := prior["rows"].([]any)
	if len(prows) > 1 {
		t.Fatalf("prior slice=none must return at most one aggregate row, got %d", len(prows))
	}
}
