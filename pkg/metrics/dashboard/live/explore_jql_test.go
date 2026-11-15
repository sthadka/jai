package live

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildJQLFixtureDB creates a SQLite jai.db (via duckdb's sqlite writer) with the
// generation/fact tables the engine needs, an `issues` table carrying every
// column the default JQL projection selects (key, type, priority, status, team,
// created, summary) plus project + raw_json, and a field_map table so
// buildFieldMap has a real source. The shared buildFixtureDB helper's issues
// table omits type/priority/project, so JQL (whose default projection names them)
// needs this richer fixture.
func buildJQLFixtureDB(t *testing.T) string {
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
CREATE TABLE jai.fact_issue_flow (generation BIGINT, issue_key VARCHAR, project VARCHAR);
INSERT INTO jai.fact_issue_flow VALUES (7,'ROX-1','ROX'),(7,'ROX-2','ROX');
CREATE TABLE jai.issues (
  key VARCHAR, project VARCHAR, type VARCHAR, priority VARCHAR, status VARCHAR,
  team VARCHAR, created TIMESTAMP, summary VARCHAR, labels VARCHAR, raw_json VARCHAR);
INSERT INTO jai.issues VALUES
  ('ROX-1','ROX','Bug','High','Done','platform', TIMESTAMP '2024-01-01','Fix login','["a"]','{"key":"ROX-1"}'),
  ('ROX-2','ROX','Task','Low','In Progress','data', TIMESTAMP '2024-02-01','Add report','["b"]','{"key":"ROX-2"}');
CREATE TABLE jai.field_map (jira_name VARCHAR, name VARCHAR, type VARCHAR, item_type VARCHAR, is_column BIGINT);
INSERT INTO jai.field_map VALUES
  ('Key','key','text','',1),
  ('Project','project','text','',1),
  ('Issue Type','type','text','',1),
  ('Priority','priority','option','',1),
  ('Status','status','option','',1),
  ('Team','team','user','',1),
  ('Created','created','datetime','',1),
  ('Summary','summary','text','',1),
  ('Labels','labels','array','label',1);
`
	cmd := exec.Command(bin, ":memory:")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build jql fixture db: %v\n%s", err, out)
	}
	return dbPath
}

// newJQLTestServer spins up a real engine over the JQL fixture DB behind httptest.
func newJQLTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dbPath := buildJQLFixtureDB(t)
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
	return ts
}

// postExploreJQL issues a POST /explore/jql with the given CSRF header and body.
func postExploreJQL(t *testing.T, url, token, jql string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(exploreJQLRequest{JQL: jql})
	req, err := http.NewRequest(http.MethodPost, url+"/explore/jql", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Jai-CSRF", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /explore/jql: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestExploreJQLHandler is the duckdb-gated end-to-end check: a simple JQL filter
// translates to DuckDB SQL, runs read-only, and returns compiled_sql + typed
// rows; a bad CSRF token is 403; and an unknown field is a 400 that names the
// offending field.
func TestExploreJQLHandler(t *testing.T) {
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Skip("duckdb not on PATH; skipping live-engine integration test")
	}
	ts := newJQLTestServer(t)

	// Discover the server's CSRF token from /meta (same source the SPA uses).
	_, meta := getJSON(t, ts.URL+"/meta")
	token, _ := meta["csrf_token"].(string)
	if token == "" {
		t.Fatalf("/meta did not surface csrf_token: %v", meta)
	}

	// Bad CSRF → 403.
	if code, _ := postExploreJQL(t, ts.URL, "wrong-token", "project = ROX"); code != http.StatusForbidden {
		t.Errorf("bad CSRF: status = %d; want 403", code)
	}

	// Valid JQL → 200 with compiled_sql + rows.
	code, out := postExploreJQL(t, ts.URL, token, "project = ROX AND status IS NOT EMPTY")
	if code != http.StatusOK {
		t.Fatalf("valid JQL: status = %d; body = %v", code, out)
	}
	compiled, _ := out["compiled_sql"].(string)
	if compiled == "" {
		t.Errorf("compiled_sql = %q; want non-empty translated SQL", compiled)
	}
	if !strings.Contains(compiled, "jai.issues") {
		t.Errorf("compiled_sql = %q; want it to query jai.issues", compiled)
	}
	rows, ok := out["rows"].([]any)
	if !ok || len(rows) == 0 {
		t.Errorf("rows = %v; want non-empty", out["rows"])
	}
	cols, ok := out["columns"].([]any)
	if !ok || len(cols) == 0 {
		t.Errorf("columns = %v; want the projected columns", out["columns"])
	}

	// Unknown field → 400 naming the field.
	code, out = postExploreJQL(t, ts.URL, token, "nonexistent_field = 5")
	if code != http.StatusBadRequest {
		t.Fatalf("unknown field: status = %d; want 400; body = %v", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "nonexistent_field") {
		t.Errorf("unknown-field error = %q; want it to name the field", msg)
	}
}
