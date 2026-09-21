package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
)

func TestNormalizeRawAndEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical strings", `"1|hy:"`, `"1|hy:"`, true},
		{"different strings", `"1|hy:"`, `"1|zz:"`, false},
		{"missing vs null", ``, `null`, true},
		{"missing vs empty string", ``, `""`, false},
		{"whitespace-insensitive object", `{"a":1, "b":2}`, `{"a":1,"b":2}`, true},
		{"present vs missing", `"x"`, ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rawFieldEqual(json.RawMessage(tt.a), json.RawMessage(tt.b))
			if got != tt.want {
				t.Errorf("rawFieldEqual(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestParseIssueFields(t *testing.T) {
	raw := `{"key":"TEST-1","fields":{"customfield_10019":"1|abc:","summary":"hi"}}`
	fields := parseIssueFields(raw)
	if got := string(fields["customfield_10019"]); got != `"1|abc:"` {
		t.Errorf("rank field = %s, want \"1|abc:\"", got)
	}
	if parseIssueFields("") != nil {
		t.Error("empty input should return nil")
	}
	if parseIssueFields("not json") != nil {
		t.Error("invalid json should return nil")
	}
}

func TestReconcileFields_DetectsAndRefreshesRank(t *testing.T) {
	// Search server returns TEST-1 with a NEW rank; the DB holds the OLD rank.
	const newRank = "1|newrank:"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/search/jql"):
			issueFields := map[string]interface{}{
				"summary":           "An issue",
				"project":           map[string]string{"key": "TEST"},
				"updated":           "2026-06-01T00:00:00.000+0000",
				"customfield_10019": newRank,
			}
			fieldsBytes, _ := json.Marshal(issueFields)
			resp := jira.SearchResponse{
				Issues: []*jira.Issue{{ID: "10001", Key: "TEST-1", Fields: fieldsBytes}},
			}
			json.NewEncoder(w).Encode(resp)
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/changelog/bulkfetch"):
			json.NewEncoder(w).Encode(jira.BulkChangelogResponse{})
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// Register rank as a custom column and create it.
	if err := database.UpsertFieldMapping(&db.FieldMapping{
		JiraID: "customfield_10019", JiraName: "Rank", Name: "rank",
		Type: "text", IsCustom: true, IsColumn: false,
	}); err != nil {
		t.Fatalf("UpsertFieldMapping: %v", err)
	}
	if err := database.EnsureColumn("rank", "TEXT"); err != nil {
		t.Fatalf("EnsureColumn: %v", err)
	}
	if err := database.MarkFieldAsColumn("customfield_10019"); err != nil {
		t.Fatalf("MarkFieldAsColumn: %v", err)
	}

	// Seed the issue with the OLD rank in both the column and raw_json.
	oldRaw := `{"id":"10001","key":"TEST-1","fields":{"summary":"An issue","customfield_10019":"1|oldrank:"}}`
	issue := &db.Issue{ID: "10001", Key: "TEST-1", Project: "TEST", Summary: "An issue",
		Updated: "2026-06-01T00:00:00Z", RawJSON: oldRaw}
	if err := database.UpsertIssue(issue, map[string]interface{}{"rank": "1|oldrank:"}); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	cfg := &config.Config{SyncSources: []config.SyncSource{{Name: "TEST", Projects: []string{"TEST"}}}}
	client := jira.New(srv.URL, "test@test.com", "token", 100)
	e := New(database, client, cfg)

	ch, err := e.ReconcileFields(context.Background(), "", []string{"rank"})
	if err != nil {
		t.Fatalf("ReconcileFields: %v", err)
	}
	var refetched int
	for p := range ch {
		if p.Done {
			if p.Err != nil {
				t.Fatalf("reconcile error: %v", p.Err)
			}
			refetched += p.Refetched
		}
	}
	if refetched != 1 {
		t.Errorf("refetched = %d, want 1", refetched)
	}

	var gotRank string
	if err := database.QueryRow(`SELECT rank FROM issues WHERE key = 'TEST-1'`).Scan(&gotRank); err != nil {
		t.Fatalf("querying rank: %v", err)
	}
	if gotRank != newRank {
		t.Errorf("stored rank = %q, want %q", gotRank, newRank)
	}
}

func TestReconcileFields_NoChangeNoRefetch(t *testing.T) {
	const rank = "1|samerank:"
	var refetchRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/3/search/jql") {
			t.Errorf("unexpected request to %s", r.URL.Path)
			return
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		jql, _ := body["jql"].(string)
		// The refetch phase issues a "key in (...)" query; the scan uses the source JQL.
		if strings.Contains(jql, "key in") {
			refetchRequests++
		}
		issueFields := map[string]interface{}{
			"summary":           "An issue",
			"customfield_10019": rank,
		}
		fieldsBytes, _ := json.Marshal(issueFields)
		resp := jira.SearchResponse{
			Issues: []*jira.Issue{{ID: "10001", Key: "TEST-1", Fields: fieldsBytes}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	if err := database.UpsertFieldMapping(&db.FieldMapping{
		JiraID: "customfield_10019", JiraName: "Rank", Name: "rank",
		Type: "text", IsCustom: true, IsColumn: false,
	}); err != nil {
		t.Fatalf("UpsertFieldMapping: %v", err)
	}
	if err := database.EnsureColumn("rank", "TEXT"); err != nil {
		t.Fatalf("EnsureColumn: %v", err)
	}
	_ = database.MarkFieldAsColumn("customfield_10019")

	// Stored raw_json already has the SAME rank — no drift.
	raw := `{"id":"10001","key":"TEST-1","fields":{"summary":"An issue","customfield_10019":"1|samerank:"}}`
	issue := &db.Issue{ID: "10001", Key: "TEST-1", Project: "TEST", Summary: "An issue",
		Updated: "2026-06-01T00:00:00Z", RawJSON: raw}
	if err := database.UpsertIssue(issue, map[string]interface{}{"rank": rank}); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	cfg := &config.Config{SyncSources: []config.SyncSource{{Name: "TEST", Projects: []string{"TEST"}}}}
	client := jira.New(srv.URL, "test@test.com", "token", 100)
	e := New(database, client, cfg)

	ch, err := e.ReconcileFields(context.Background(), "", []string{"rank"})
	if err != nil {
		t.Fatalf("ReconcileFields: %v", err)
	}
	for p := range ch {
		if p.Done && p.Refetched != 0 {
			t.Errorf("refetched = %d, want 0 (no drift)", p.Refetched)
		}
	}
	if refetchRequests != 0 {
		t.Errorf("refetch requests = %d, want 0", refetchRequests)
	}
}

func TestReconcileFields_EmptyIsNoop(t *testing.T) {
	e := New(nil, nil, &config.Config{})
	ch, err := e.ReconcileFields(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("ReconcileFields: %v", err)
	}
	for range ch {
		t.Error("expected no progress events for empty field list")
	}
}
