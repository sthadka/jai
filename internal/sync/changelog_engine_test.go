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
			IssueChangeLogs: []jira.IssueChangeLog{
				{
					IssueID: "10042",
					ChangeHistories: []jira.BulkChangeHistory{
						{
							ID:      "500",
							Created: 1781101800000, // 2026-06-10T14:30:00Z
							Items: []jira.ChangelogItem{
								{Field: "status", FieldType: "jira", FromString: "New", ToString: "Done"},
							},
						},
					},
				},
			},
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

func TestSyncChangelogs_Incremental(t *testing.T) {
	var bulkCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/3/changelog/bulkfetch") {
			t.Fatalf("unexpected request to %s", r.URL.Path)
		}
		bulkCalls++
		resp := jira.BulkChangelogResponse{
			IssueChangeLogs: []jira.IssueChangeLog{
				{
					IssueID: "10042",
					ChangeHistories: []jira.BulkChangeHistory{
						{
							ID:      "500",
							Created: 1781101800000, // 2026-06-10T14:30:00Z
							Items: []jira.ChangelogItem{
								{Field: "status", FieldType: "jira", FromString: "New", ToString: "Done"},
							},
						},
					},
				},
			},
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

	drain := func(force bool) ChangelogProgress {
		t.Helper()
		ch, err := e.SyncChangelogs(context.Background(), "", force)
		if err != nil {
			t.Fatalf("SyncChangelogs: %v", err)
		}
		var final ChangelogProgress
		for p := range ch {
			if p.Done {
				final = p
			}
		}
		return final
	}

	// First run: the changelog has never been synced -> 1 candidate, 1 fetch.
	first := drain(false)
	if first.Total != 1 || first.Synced != 1 {
		t.Fatalf("first run: expected Total=1 Synced=1, got Total=%d Synced=%d", first.Total, first.Synced)
	}
	if bulkCalls != 1 {
		t.Fatalf("first run: expected 1 bulk call, got %d", bulkCalls)
	}

	// Second run without force: incremental state means no candidates remain,
	// so nothing is re-fetched. This is the bug the fix addresses — previously
	// the CLI forced a reset every run and refetched all issues.
	bulkCalls = 0
	second := drain(false)
	if second.Total != 0 {
		t.Errorf("second run: expected 0 candidates (incremental), got Total=%d", second.Total)
	}
	if bulkCalls != 0 {
		t.Errorf("second run: expected 0 bulk calls, got %d", bulkCalls)
	}

	// force=true resets timestamps and re-fetches everything.
	bulkCalls = 0
	forced := drain(true)
	if forced.Total != 1 || forced.Synced != 1 {
		t.Errorf("forced run: expected Total=1 Synced=1, got Total=%d Synced=%d", forced.Total, forced.Synced)
	}
	if bulkCalls != 1 {
		t.Errorf("forced run: expected 1 bulk call after force, got %d", bulkCalls)
	}
}

func TestSyncSource_IncludesChangelogs(t *testing.T) {
	myselfCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/myself":
			myselfCalls++
			json.NewEncoder(w).Encode(jira.MySelf{
				DisplayName: "Test User",
				TimeZone:    "America/New_York",
			})
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
				IssueChangeLogs: []jira.IssueChangeLog{
					{
						IssueID: "10042",
						ChangeHistories: []jira.BulkChangeHistory{
							{
								ID:      "500",
								Created: 1781101800000, // 2026-06-10T14:30:00Z
								Items: []jira.ChangelogItem{
									{Field: "status", FieldType: "jira", FromString: "New", ToString: "Done"},
								},
							},
						},
					},
				},
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

	if err := e.VerifyAuth(context.Background()); err != nil {
		t.Fatalf("VerifyAuth: %v", err)
	}
	ch, err := e.Sync(context.Background(), false, false, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for range ch {
	}
	if myselfCalls != 1 {
		t.Errorf("expected one /myself request, got %d", myselfCalls)
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

// TestFullSync_ReupsertsUnchangedUpdated proves that "jai sync --full"
// re-upserts issues whose "updated" timestamp is unchanged, while an
// incremental sync skips them. This is the reconcile-of-last-resort: Jira does
// not bump "updated" on a rank/LexoRank rebalance, so a full sync must rewrite
// unchanged-"updated" issues or the drift (e.g. a whole-project rebalance that
// exceeds the incremental reconcile cap) is never corrected.
func TestFullSync_ReupsertsUnchangedUpdated(t *testing.T) {
	const sameUpdated = "2026-06-01T00:00:00.000+0000"
	const newRank = "1|newrank:"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/myself":
			json.NewEncoder(w).Encode(jira.MySelf{DisplayName: "Test User", TimeZone: "UTC"})
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/search/jql"):
			// Same "updated" as stored, but a NEW rank (rebalance).
			resp := jira.SearchResponse{
				Issues: []*jira.Issue{
					{ID: "10001", Key: "TEST-1", Fields: json.RawMessage(`{
						"summary": "An issue",
						"project": {"key": "TEST"},
						"updated": "` + sameUpdated + `",
						"customfield_10019": "` + newRank + `"
					}`)},
				},
			}
			json.NewEncoder(w).Encode(resp)
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/changelog/bulkfetch"):
			json.NewEncoder(w).Encode(jira.BulkChangelogResponse{})
		default:
			// project lookups, etc. — non-fatal side effects.
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// Register rank as a custom column.
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

	// Seed the issue with the OLD rank and the exact "updated" the API returns.
	oldRaw := `{"id":"10001","key":"TEST-1","fields":{"summary":"An issue","updated":"` + sameUpdated + `","customfield_10019":"1|oldrank:"}}`
	seed := &db.Issue{ID: "10001", Key: "TEST-1", Project: "TEST", Summary: "An issue",
		Updated: "2026-06-01T00:00:00Z", RawJSON: oldRaw}
	if err := database.UpsertIssue(seed, map[string]interface{}{"rank": "1|oldrank:"}); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	client := jira.New(srv.URL, "test@test.com", "token", 100)
	cfg := &config.Config{SyncSources: []config.SyncSource{{Name: "test", JQL: "project = TEST"}}}
	e := New(database, client, cfg)
	if err := e.VerifyAuth(context.Background()); err != nil {
		t.Fatalf("VerifyAuth: %v", err)
	}

	rankOf := func() string {
		var r string
		if err := database.QueryRow(`SELECT rank FROM issues WHERE key = 'TEST-1'`).Scan(&r); err != nil {
			t.Fatalf("querying rank: %v", err)
		}
		return r
	}

	// Incremental sync must SKIP the unchanged-"updated" issue: rank stays old.
	ch, err := e.Sync(context.Background(), false, false, "")
	if err != nil {
		t.Fatalf("incremental Sync: %v", err)
	}
	for range ch {
	}
	if got := rankOf(); got != "1|oldrank:" {
		t.Fatalf("after incremental sync rank = %q, want unchanged %q", got, "1|oldrank:")
	}

	// Full sync must RE-UPSERT it despite the unchanged "updated": rank refreshed.
	ch, err = e.Sync(context.Background(), true, false, "")
	if err != nil {
		t.Fatalf("full Sync: %v", err)
	}
	for range ch {
	}
	if got := rankOf(); got != newRank {
		t.Errorf("after full sync rank = %q, want %q", got, newRank)
	}
}
