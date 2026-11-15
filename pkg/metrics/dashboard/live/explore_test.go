package live

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os/exec"
	"testing"
)

// TestValidateSelectSQLAccepts proves the guard accepts plain SELECT and WITH …
// SELECT queries (case-insensitive, with a tolerated trailing semicolon).
func TestValidateSelectSQLAccepts(t *testing.T) {
	ok := []string{
		"SELECT 1",
		"select key, status from jai.issues",
		"  SELECT key FROM jai.issues LIMIT 5 ;  ",
		"WITH t AS (SELECT 1 AS x) SELECT x FROM t",
		"with recursive r(n) as (select 1) select n from r",
		"SELECT ';' AS semi, '-- not a comment' AS c",
	}
	for _, q := range ok {
		if err := validateSelectSQL(q); err != nil {
			t.Errorf("validateSelectSQL(%q) = %v; want nil", q, err)
		}
	}
}

// TestValidateSelectSQLRejectsVerbs proves every disallowed statement keyword is
// rejected when used as a leading or embedded bare word.
func TestValidateSelectSQLRejectsVerbs(t *testing.T) {
	// Leading-verb statements: each must be rejected (not a SELECT/WITH start).
	leading := map[string]string{
		"INSERT":  "INSERT INTO jai.issues VALUES ('x')",
		"UPDATE":  "UPDATE jai.issues SET status = 'x'",
		"DELETE":  "DELETE FROM jai.issues",
		"MERGE":   "MERGE INTO jai.issues USING t ON false",
		"CREATE":  "CREATE TABLE t (a INT)",
		"ALTER":   "ALTER TABLE jai.issues ADD COLUMN c INT",
		"DROP":    "DROP TABLE jai.issues",
		"ATTACH":  "ATTACH 'x.db' AS x",
		"COPY":    "COPY jai.issues TO 'x.csv'",
		"PRAGMA":  "PRAGMA database_list",
		"INSTALL": "INSTALL httpfs",
		"LOAD":    "LOAD httpfs",
		"SET":     "SET memory_limit = '1GB'",
		"EXPORT":  "EXPORT DATABASE 'x'",
		"CALL":    "CALL pragma_version()",
	}
	for verb, q := range leading {
		if err := validateSelectSQL(q); err == nil {
			t.Errorf("validateSelectSQL(%q) = nil; want rejection of %s", q, verb)
		}
	}

	// Embedded verbs inside an otherwise-SELECT statement must also be rejected
	// (defense in depth against smuggled DDL/DML after the leading SELECT token).
	for verb := range disallowedVerbs {
		q := "SELECT 1; " + verb + " something"
		if err := validateSelectSQL(q); err == nil {
			t.Errorf("validateSelectSQL(%q) = nil; want rejection (embedded %s)", q, verb)
		}
	}
}

// TestValidateSelectSQLRejectsMultiStatement proves a second statement is
// rejected while a lone trailing semicolon is tolerated.
func TestValidateSelectSQLRejectsMultiStatement(t *testing.T) {
	bad := []string{
		"SELECT 1; SELECT 2",
		"SELECT 1;SELECT 2",
		"SELECT key FROM jai.issues; DROP TABLE jai.issues",
		"SELECT 1;;",
	}
	for _, q := range bad {
		if err := validateSelectSQL(q); err == nil {
			t.Errorf("validateSelectSQL(%q) = nil; want multi-statement rejection", q)
		}
	}
	for _, q := range []string{"SELECT 1;", "SELECT 1 ;  "} {
		if err := validateSelectSQL(q); err != nil {
			t.Errorf("validateSelectSQL(%q) = %v; want nil (trailing ';' allowed)", q, err)
		}
	}
}

// TestValidateSelectSQLWordBoundary proves column names like created/updated do
// NOT trip the CREATE/UPDATE token checks (the check is word-level, not
// substring), while the real keywords still fail.
func TestValidateSelectSQLWordBoundary(t *testing.T) {
	ok := []string{
		"SELECT created, updated FROM jai.issues",
		"SELECT created_at, last_updated, insertion_order, dropped FROM t",
		`SELECT "update" FROM t`, // quoted identifier, not a keyword
	}
	for _, q := range ok {
		if err := validateSelectSQL(q); err != nil {
			t.Errorf("validateSelectSQL(%q) = %v; want nil (word boundary)", q, err)
		}
	}
	for _, q := range []string{"SELECT 1; CREATE TABLE t (a INT)", "SELECT 1; UPDATE t SET a=1"} {
		if err := validateSelectSQL(q); err == nil {
			t.Errorf("validateSelectSQL(%q) = nil; want rejection", q)
		}
	}
}

// TestInferColumnTypes covers number/date/string, mixed→string, and all-null→
// string classification, preserving the given projection order.
func TestInferColumnTypes(t *testing.T) {
	rows := []map[string]any{
		{"n": "42", "f": 3.14, "d": "2024-01-01 12:30:00", "day": "2024-02-03", "s": "hello", "mixed": "7", "nul": nil},
		{"n": "7", "f": 2.0, "d": "2024-03-04 00:00:00", "day": "2024-02-04", "s": "world", "mixed": "nope", "nul": nil},
		{"n": nil, "f": nil, "d": nil, "day": nil, "s": "", "mixed": nil, "nul": nil},
	}
	order := []string{"n", "f", "d", "day", "s", "mixed", "nul"}
	cols := inferColumns(order, rows)

	if len(cols) != len(order) {
		t.Fatalf("inferColumns returned %d cols; want %d", len(cols), len(order))
	}
	want := map[string]string{
		"n":     "number",
		"f":     "number",
		"d":     "date",
		"day":   "date",
		"s":     "string",
		"mixed": "string", // "7" + "nope" → not all-number → string
		"nul":   "string", // all null → string
	}
	for i, c := range cols {
		if c.Name != order[i] {
			t.Errorf("column %d name = %q; want %q (order must be preserved)", i, c.Name, order[i])
		}
		if c.Type != want[c.Name] {
			t.Errorf("column %q type = %q; want %q", c.Name, c.Type, want[c.Name])
		}
	}
}

// postExplore issues a POST /explore/sql with the given CSRF header and body.
func postExplore(t *testing.T, url, token, sql string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(exploreRequest{SQL: sql})
	req, err := http.NewRequest(http.MethodPost, url+"/explore/sql", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Jai-CSRF", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /explore/sql: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestExploreSQLHandler is the duckdb-gated end-to-end check: a valid SELECT
// returns typed columns in order, a bad CSRF token is 403, and a non-SELECT body
// is 400. It reuses the shared fixture-DB engine helper.
func TestExploreSQLHandler(t *testing.T) {
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Skip("duckdb not on PATH; skipping live-engine integration test")
	}
	ts, engine := newLiveTestServer(t)
	_ = engine

	// Discover the server's CSRF token from /meta (same source the SPA uses).
	_, meta := getJSON(t, ts.URL+"/meta")
	token, _ := meta["csrf_token"].(string)
	if token == "" {
		t.Fatalf("/meta did not surface csrf_token: %v", meta)
	}

	// Bad CSRF → 403.
	if code, _ := postExplore(t, ts.URL, "wrong-token", "SELECT 1"); code != http.StatusForbidden {
		t.Errorf("bad CSRF: status = %d; want 403", code)
	}
	// Non-SELECT body → 400.
	if code, _ := postExplore(t, ts.URL, token, "DROP TABLE jai.issues"); code != http.StatusBadRequest {
		t.Errorf("non-SELECT: status = %d; want 400", code)
	}

	// Valid SELECT → 200 with typed, ordered columns.
	code, out := postExplore(t, ts.URL, token, "SELECT key, status, created FROM jai.issues LIMIT 5")
	if code != http.StatusOK {
		t.Fatalf("valid SELECT: status = %d; body = %v", code, out)
	}
	rawCols, ok := out["columns"].([]any)
	if !ok || len(rawCols) != 3 {
		t.Fatalf("columns = %v; want 3 entries", out["columns"])
	}
	gotTypes := map[string]string{}
	var gotOrder []string
	for _, rc := range rawCols {
		m := rc.(map[string]any)
		name := m["name"].(string)
		gotOrder = append(gotOrder, name)
		gotTypes[name] = m["type"].(string)
	}
	wantOrder := []string{"key", "status", "created"}
	for i, n := range wantOrder {
		if gotOrder[i] != n {
			t.Errorf("column order[%d] = %q; want %q", i, gotOrder[i], n)
		}
	}
	wantTypes := map[string]string{"key": "string", "status": "string", "created": "date"}
	for name, want := range wantTypes {
		if gotTypes[name] != want {
			t.Errorf("column %q type = %q; want %q", name, gotTypes[name], want)
		}
	}
	rows, ok := out["rows"].([]any)
	if !ok || len(rows) == 0 {
		t.Errorf("rows = %v; want non-empty", out["rows"])
	}
	if trunc, _ := out["truncated"].(bool); trunc {
		t.Errorf("truncated = true; want false for a 5-row result")
	}

	// A syntactically valid SELECT that fails at DuckDB (unknown column) must be
	// surfaced as a 500, not a silent empty 200.
	if code, _ := postExplore(t, ts.URL, token, "SELECT no_such_col FROM jai.issues"); code != http.StatusInternalServerError {
		t.Errorf("bad column: status = %d; want 500", code)
	}
}
