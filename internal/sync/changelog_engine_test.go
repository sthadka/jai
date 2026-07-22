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

func TestSyncChangelogsForKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/3/changelog/bulkfetch") {
			t.Fatalf("unexpected request to %s", r.URL.Path)
		}
		resp := jira.BulkChangelogResponse{
			Values: []jira.BulkChangelogEntry{
				{
					ID:      "500",
					IssueID: "10042",
					Created: "2026-06-10T14:30:00.000+0000",
					Items: []jira.ChangelogItem{
						{Field: "status", FieldType: "jira", FromString: "New", ToString: "Done"},
					},
				},
			},
			Total: 1,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	issue := &db.Issue{ID: "10042", Key: "TEST-1", Project: "TEST", Summary: "Issue", Updated: "2026-06-01T00:00:00Z", RawJSON: "{}"}
	if err := database.UpsertIssue(issue, nil); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	client := jira.New(srv.URL, "test@test.com", "token", 100)
	e := New(database, client, &config.Config{})

	e.syncChangelogsForKeys(context.Background(), []string{"TEST-1"})

	var count int
	if err := database.QueryRow(`SELECT count(*) FROM changelog WHERE issue_key = 'TEST-1'`).Scan(&count); err != nil {
		t.Fatalf("counting changelog rows: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 changelog row, got %d", count)
	}

	var syncedAt *string
	if err := database.QueryRow(`SELECT changelog_synced_at FROM issues WHERE key = 'TEST-1'`).Scan(&syncedAt); err != nil {
		t.Fatalf("querying changelog_synced_at: %v", err)
	}
	if syncedAt == nil || *syncedAt == "" {
		t.Error("expected changelog_synced_at to be stamped")
	}
}

func TestSyncSource_IncludesChangelogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/search/jql"):
			resp := jira.SearchResponse{
				Issues: []*jira.Issue{
					{ID: "10042", Key: "TEST-1", Fields: json.RawMessage(`{
						"summary": "Test issue",
						"project": {"key": "TEST"},
						"updated": "2026-06-01T00:00:00.000+0000"
					}`)},
				},
			}
			json.NewEncoder(w).Encode(resp)
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/changelog/bulkfetch"):
			resp := jira.BulkChangelogResponse{
				Values: []jira.BulkChangelogEntry{
					{
						ID:      "500",
						IssueID: "10042",
						Created: "2026-06-10T14:30:00.000+0000",
						Items: []jira.ChangelogItem{
							{Field: "status", FieldType: "jira", FromString: "New", ToString: "Done"},
						},
					},
				},
				Total: 1,
			}
			json.NewEncoder(w).Encode(resp)
		default:
			t.Fatalf("unexpected request to %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	client := jira.New(srv.URL, "test@test.com", "token", 100)
	cfg := &config.Config{
		SyncSources: []config.SyncSource{
			{Name: "test", Projects: []string{"TEST"}},
		},
	}
	e := New(database, client, cfg)

	ch, err := e.Sync(context.Background(), false, false, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for range ch {
	}

	var count int
	if err := database.QueryRow(`SELECT count(*) FROM changelog WHERE issue_key = 'TEST-1'`).Scan(&count); err != nil {
		t.Fatalf("counting changelog rows: %v", err)
	}
	if count != 1 {
		t.Errorf("expected changelog to be synced inline for TEST-1, got %d rows", count)
	}

	var syncedAt *string
	if err := database.QueryRow(`SELECT changelog_synced_at FROM issues WHERE key = 'TEST-1'`).Scan(&syncedAt); err != nil {
		t.Fatalf("querying changelog_synced_at: %v", err)
	}
	if syncedAt == nil || *syncedAt == "" {
		t.Error("expected changelog_synced_at to be stamped after sync")
	}
}
