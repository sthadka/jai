package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
	"github.com/sthadka/jai/internal/query"
)

// TestJQLIssueToRow_DescriptionPreservesLinks guards the JQL path against the
// smart-link drop bug: a description column requested via --fields must render
// the ADF to markdown (preserving URLs), not return null.
func TestJQLIssueToRow_DescriptionPreservesLinks(t *testing.T) {
	const docURL = "https://docs.google.com/document/d/1gXyX/edit"
	fields := json.RawMessage(`{
		"summary": "Project with linked doc",
		"description": {"type":"doc","version":1,"content":[
			{"type":"paragraph","content":[{"type":"text","text":"See project document:"}]},
			{"type":"paragraph","content":[{"type":"inlineCard","attrs":{"url":"` + docURL + `"}}]}
		]}
	}`)
	issue := &jira.Issue{Key: "ROX-36990", Fields: fields}

	row, err := jqlIssueToRow(issue, []string{"key", "description"})
	if err != nil {
		t.Fatalf("jqlIssueToRow: %v", err)
	}
	desc, _ := row[1].(string)
	if !strings.Contains(desc, docURL) {
		t.Errorf("expected description column to contain %q, got %q", docURL, desc)
	}
}

// TestJQLColumnsToAPIFields verifies requested columns map to the API field IDs
// needed to populate them (so description/reporter aren't silently dropped).
func TestJQLColumnsToAPIFields(t *testing.T) {
	got := jqlColumnsToAPIFields([]string{"key", "description", "reporter", "type"})
	want := map[string]bool{"description": true, "reporter": true, "issuetype": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d fields, got %v", len(want), got)
	}
	for _, f := range got {
		if !want[f] {
			t.Errorf("unexpected API field %q in %v", f, got)
		}
	}
}

// setupLocalJQL opens a fresh jai.db, wires the package globals, and seeds a
// small issue set so the local-JQL translate → SQL path can be exercised
// end-to-end. The DB handle is returned for additional seeding.
func setupLocalJQL(t *testing.T) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jai.db")
	cfg := &config.Config{DB: config.DBConfig{Path: path}}
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	g.db = database
	g.cfg = cfg
	g.query = query.New(database, cfg)
	g.jsonOut = false
	g.format = "table"
	g.fields = ""
	t.Cleanup(func() {
		g.db = nil
		g.cfg = nil
		g.query = nil
		g.jsonOut = false
		g.format = ""
		g.fields = ""
		querySQL = false
		queryLocalJQL = false
		queryJQL = ""
	})

	seed := func(key, project, status, priority string) {
		iss := &db.Issue{
			Key: key, Project: project, Type: "Bug", Summary: key, RawJSON: "{}",
			Status: status, StatusCategory: "In Progress", Priority: priority,
			Created: "2024-01-01T00:00:00Z", Updated: "2024-01-02T00:00:00Z",
		}
		if err := database.UpsertIssue(iss, nil); err != nil {
			t.Fatalf("UpsertIssue %s: %v", key, err)
		}
	}
	seed("ROX-1", "ROX", "In Progress", "High")
	seed("ROX-2", "ROX", "Done", "Low")
	seed("ACS-1", "ACS", "In Progress", "High")
	return database
}

// TestResolveQueryRoute covers the SQL-vs-JQL detector and the explicit override
// flags: SELECT/WITH → SQL, bare JQL → local JQL, --sql/--local-jql force a path,
// and --jql always routes live without consulting the local translator.
func TestResolveQueryRoute(t *testing.T) {
	tests := []struct {
		name                    string
		arg, live               string
		forceSQL, forceLocalJQL bool
		want                    queryRoute
	}{
		{name: "select is sql", arg: "SELECT * FROM issues", want: routeSQL},
		{name: "lowercase select is sql", arg: "select 1", want: routeSQL},
		{name: "with is sql", arg: "WITH t AS (SELECT 1) SELECT * FROM t", want: routeSQL},
		{name: "leading space select is sql", arg: "  SELECT 1", want: routeSQL},
		{name: "bare jql is local jql", arg: "project = ROX AND status IS NOT EMPTY", want: routeLocalJQL},
		{name: "status jql is local jql", arg: "status = Done", want: routeLocalJQL},
		{name: "force sql on bare jql", arg: "project = ROX", forceSQL: true, want: routeSQL},
		{name: "force local jql on select", arg: "SELECT 1", forceLocalJQL: true, want: routeLocalJQL},
		{name: "jql routes live", arg: "SELECT 1", live: "project = ROX", want: routeLiveJQL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveQueryRoute(tc.arg, tc.live, tc.forceSQL, tc.forceLocalJQL)
			if err != nil {
				t.Fatalf("resolveQueryRoute: %v", err)
			}
			if got != tc.want {
				t.Errorf("route = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestResolveQueryRoute_ConflictingOverrides rejects --sql with --local-jql.
func TestResolveQueryRoute_ConflictingOverrides(t *testing.T) {
	if _, err := resolveQueryRoute("project = ROX", "", true, true); err == nil {
		t.Fatal("expected error when both --sql and --local-jql are set")
	}
}

// TestRunLocalJQLQuery_Rows translates a bare JQL filter to local SQL and runs it
// against the seeded DB, asserting the expected issue keys come back.
func TestRunLocalJQLQuery_Rows(t *testing.T) {
	setupLocalJQL(t)
	g.jsonOut = true

	out := captureStdout(t, func() {
		if err := runLocalJQLQuery("project = ROX AND status = \"In Progress\""); err != nil {
			t.Fatalf("runLocalJQLQuery: %v", err)
		}
	})

	var env struct {
		OK      bool            `json:"ok"`
		Columns []string        `json:"columns"`
		Rows    [][]interface{} `json:"rows"`
		Count   int             `json:"count"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		t.Fatalf("unmarshal envelope %q: %v", out, err)
	}
	if !env.OK {
		t.Fatalf("expected ok envelope, got %q", out)
	}
	keyIdx := -1
	for i, c := range env.Columns {
		if c == "key" {
			keyIdx = i
		}
	}
	if keyIdx < 0 {
		t.Fatalf("no key column in %v", env.Columns)
	}
	var keys []string
	for _, row := range env.Rows {
		keys = append(keys, row[keyIdx].(string))
	}
	if len(keys) != 1 || keys[0] != "ROX-1" {
		t.Errorf("keys = %v, want [ROX-1]", keys)
	}
}

// TestRunLocalJQLQuery_FormatPassthrough verifies --format (csv here) flows
// through the shared SQL path unchanged for a translated JQL query.
func TestRunLocalJQLQuery_FormatPassthrough(t *testing.T) {
	setupLocalJQL(t)
	g.format = "csv"
	g.fields = "key"

	out := captureStdout(t, func() {
		if err := runLocalJQLQuery("project = ROX"); err != nil {
			t.Fatalf("runLocalJQLQuery: %v", err)
		}
	})

	if !strings.Contains(out, "key") {
		t.Errorf("expected csv header 'key' in %q", out)
	}
	if !strings.Contains(out, "ROX-1") || !strings.Contains(out, "ROX-2") {
		t.Errorf("expected ROX rows in csv output %q", out)
	}
	if strings.Contains(out, "ACS-1") {
		t.Errorf("csv output should not include ACS-1: %q", out)
	}
}

// TestRunLocalJQLQuery_UnsupportedConstruct asserts a v1-unsupported construct
// (a WAS history operator) is rejected with a clear local-JQL error message.
func TestRunLocalJQLQuery_UnsupportedConstruct(t *testing.T) {
	setupLocalJQL(t)
	g.jsonOut = false

	err := runLocalJQLQuery("status WAS Done")
	if err == nil {
		t.Fatal("expected error for history operator, got nil")
	}
	if !strings.Contains(err.Error(), "historical operator") {
		t.Errorf("error = %q, want it to mention the unsupported historical operator", err.Error())
	}
}

// TestRunLocalJQLQuery_UnsupportedFunction asserts membersOf() is rejected
// clearly (group membership is not synced locally).
func TestRunLocalJQLQuery_UnsupportedFunction(t *testing.T) {
	setupLocalJQL(t)
	g.jsonOut = false

	err := runLocalJQLQuery("assignee in membersOf(\"devs\")")
	if err == nil {
		t.Fatal("expected error for membersOf(), got nil")
	}
	if !strings.Contains(err.Error(), "membersOf") {
		t.Errorf("error = %q, want it to mention membersOf()", err.Error())
	}
}
