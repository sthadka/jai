//go:build fts5

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
	"github.com/sthadka/jai/internal/query"
	synce "github.com/sthadka/jai/internal/sync"
)

// writeTestServer builds a Server whose Jira client points at the given base URL
// so write requests can be captured by an httptest server.
func writeTestServer(t *testing.T, baseURL string) *Server {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := &config.Config{
		Jira: config.JiraConfig{URL: baseURL, Email: "test@example.com", Token: "test-token"},
		Me:   "test@example.com",
	}
	jiraClient := jira.New(baseURL, cfg.Jira.Email, cfg.Jira.Token, 1000)
	queryEngine := query.New(database, cfg)
	syncEngine := synce.New(database, jiraClient, cfg)
	return New(cfg, database, jiraClient, queryEngine, syncEngine)
}

func setReq(args map[string]interface{}) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
}

// TestJaiSetContributorsWrapsUserObjects is the acceptance test for the
// contributors write-back fix: jai_set on a user array must PUT an array of
// {"accountId": ...} objects (resolved from emails), not bare strings.
func TestJaiSetContributorsWrapsUserObjects(t *testing.T) {
	var captured map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/api/3/user/search"):
			email := r.URL.Query().Get("query")
			id := "acc-" + strings.Split(email, "@")[0]
			_ = json.NewEncoder(w).Encode([]map[string]string{{"accountId": id}})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/"):
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &captured)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()

	srv := writeTestServer(t, ts.URL)
	if !srv.toolsets.CanWrite() {
		t.Fatal("write toolset not enabled by default")
	}
	if err := srv.db.UpsertFieldMapping(&db.FieldMapping{
		JiraID: "customfield_10466", JiraName: "Contributors", Name: "contributors",
		Type: "array", ItemType: "user", IsCustom: true, IsColumn: true,
	}); err != nil {
		t.Fatalf("UpsertFieldMapping: %v", err)
	}
	if err := srv.db.EnsureColumn("contributors", "TEXT"); err != nil {
		t.Fatalf("EnsureColumn: %v", err)
	}
	insertTestIssue(t, srv.db, "ROX-1", "ROX", "Some project", "Open")
	for _, value := range []string{
		"jvmartin@redhat.com,ksanchet@redhat.com",
		`["jvmartin@redhat.com","ksanchet@redhat.com"]`,
	} {
		captured = nil
		res, err := srv.handleJaiSet(context.Background(), setReq(map[string]interface{}{
			"keys": "ROX-1", "field": "contributors", "value": value, "operation": "set",
		}))
		if err != nil {
			t.Fatalf("handleJaiSet(%q): %v", value, err)
		}
		if res.IsError {
			t.Fatalf("handleJaiSet(%q) tool error: %s", value, toolText(res))
		}

		fields, ok := captured["fields"].(map[string]interface{})
		if !ok {
			t.Fatalf("no fields in PUT body: %#v", captured)
		}
		got := fields["customfield_10466"]
		want := []interface{}{
			map[string]interface{}{"accountId": "acc-jvmartin"},
			map[string]interface{}{"accountId": "acc-ksanchet"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("input %q → customfield_10466 = %#v, want %#v", value, got, want)
		}
	}

	// Round-trip: the local column stores the JSON array of emails the read path emits.
	var stored string
	if err := srv.db.QueryRow(`SELECT contributors FROM issues WHERE key = 'ROX-1'`).Scan(&stored); err != nil {
		t.Fatalf("read back contributors: %v", err)
	}
	var emails []string
	if err := json.Unmarshal([]byte(stored), &emails); err != nil {
		t.Fatalf("stored column not a JSON array: %q", stored)
	}
	wantEmails := []string{"jvmartin@redhat.com", "ksanchet@redhat.com"}
	if !reflect.DeepEqual(emails, wantEmails) {
		t.Fatalf("round-trip emails = %#v, want %#v", emails, wantEmails)
	}
}

func toolText(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}
