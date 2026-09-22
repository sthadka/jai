package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestNormalizeRaw_CanonicalizesObjectKeyOrder(t *testing.T) {
	// Same object, different key order → must compare equal (no false drift).
	if !rawFieldEqual(json.RawMessage(`{"id":"1","name":"x"}`), json.RawMessage(`{"name":"x","id":"1"}`)) {
		t.Error("object fields with reordered keys should compare equal")
	}
	// Genuinely different values still differ.
	if rawFieldEqual(json.RawMessage(`{"id":"1"}`), json.RawMessage(`{"id":"2"}`)) {
		t.Error("object fields with different values should differ")
	}
}

// seedRankIssue registers rank as a custom column and inserts an issue with the
// given stored rank in both the column and raw_json.
func seedRankIssue(t *testing.T, database *db.DB, key, storedRank string) {
	t.Helper()
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
	raw := `{"id":"` + key + `","key":"` + key + `","fields":{"summary":"An issue","customfield_10019":"` + storedRank + `"}}`
	issue := &db.Issue{ID: key, Key: key, Project: "TEST", Summary: "An issue",
		Updated: "2026-06-01T00:00:00Z", RawJSON: raw}
	if err := database.UpsertIssue(issue, map[string]interface{}{"rank": storedRank}); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}
}

// Above the full-refetch cap the pass no longer bails: it applies the fresh
// field values captured during the scan directly to each drifted issue, with no
// extra API round-trip. This is what lets a whole-project LexoRank rebalance
// self-heal on a plain incremental sync instead of demanding a full sync.
func TestReconcileFields_CapExceededAppliesDirectly(t *testing.T) {
	orig := maxReconcileRefetch
	maxReconcileRefetch = 1
	defer func() { maxReconcileRefetch = orig }()

	var refetchRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/3/search/jql") {
			t.Errorf("unexpected request to %s", r.URL.Path)
			return
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if jql, _ := body["jql"].(string); strings.Contains(jql, "key in") {
			refetchRequests++ // a full re-fetch would use "key in (...)"
		}
		// Two issues, both drifted (fresh rank differs from stored). With the cap
		// at 1, this 2-issue change set exceeds it and must be applied directly.
		mk := func(key string) *jira.Issue {
			f, _ := json.Marshal(map[string]interface{}{"summary": "x", "customfield_10019": "1|fresh:"})
			return &jira.Issue{ID: key, Key: key, Fields: f}
		}
		json.NewEncoder(w).Encode(jira.SearchResponse{Issues: []*jira.Issue{mk("TEST-1"), mk("TEST-2")}})
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	seedRankIssue(t, database, "TEST-1", "1|old1:")
	seedRankIssue(t, database, "TEST-2", "1|old2:")

	cfg := &config.Config{SyncSources: []config.SyncSource{{Name: "TEST", Projects: []string{"TEST"}}}}
	e := New(database, jira.New(srv.URL, "t@t.com", "tok", 100), cfg)

	ch, err := e.ReconcileFields(context.Background(), "", []string{"rank"})
	if err != nil {
		t.Fatalf("ReconcileFields: %v", err)
	}
	var changed, refetched int
	var skipped bool
	var doneErr error
	for p := range ch {
		if p.Done {
			changed, refetched, skipped, doneErr = p.Changed, p.Refetched, p.Skipped, p.Err
		}
	}
	if doneErr != nil {
		t.Fatalf("reconcile error: %v", doneErr)
	}
	if skipped {
		t.Error("Skipped must not be set: large change sets are now applied directly")
	}
	if changed != 2 || refetched != 2 {
		t.Errorf("changed=%d refetched=%d, want 2 and 2 (both applied directly)", changed, refetched)
	}
	if refetchRequests != 0 {
		t.Errorf("refetch API requests = %d, want 0 (direct apply must not re-fetch)", refetchRequests)
	}
	// Both the rank column and raw_json must carry the fresh scan value.
	for _, key := range []string{"TEST-1", "TEST-2"} {
		var col, raw string
		if err := database.QueryRow(`SELECT rank, raw_json FROM issues WHERE key = '`+key+`'`).Scan(&col, &raw); err != nil {
			t.Fatalf("querying %s: %v", key, err)
		}
		if col != "1|fresh:" {
			t.Errorf("%s rank column = %q, want %q", key, col, "1|fresh:")
		}
		if got := string(parseIssueFields(raw)["customfield_10019"]); got != `"1|fresh:"` {
			t.Errorf("%s raw_json rank = %s, want \"1|fresh:\"", key, got)
		}
	}
}

func TestReconcileFields_RefetchFailureSurfacesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/3/search/jql") {
			t.Errorf("unexpected request to %s", r.URL.Path)
			return
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		// Only the scan (project-scoped) query succeeds; the refetch fails. Match
		// on "project in" rather than "key in" so that a client retry which
		// re-sends an empty body (5xx retries reuse the consumed reader) also
		// fails instead of falling through to the scan branch.
		if jql, _ := body["jql"].(string); strings.Contains(jql, "project in") {
			f, _ := json.Marshal(map[string]interface{}{"summary": "x", "customfield_10019": "1|fresh:"})
			json.NewEncoder(w).Encode(jira.SearchResponse{Issues: []*jira.Issue{{ID: "TEST-1", Key: "TEST-1", Fields: f}}})
			return
		}
		http.Error(w, `{"errorMessages":["boom"]}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	seedRankIssue(t, database, "TEST-1", "1|old:")

	cfg := &config.Config{SyncSources: []config.SyncSource{{Name: "TEST", Projects: []string{"TEST"}}}}
	e := New(database, jira.New(srv.URL, "t@t.com", "tok", 100), cfg)

	ch, err := e.ReconcileFields(context.Background(), "", []string{"rank"})
	if err != nil {
		t.Fatalf("ReconcileFields: %v", err)
	}
	var doneErr error
	var changed, refetched int
	for p := range ch {
		if p.Done {
			doneErr = p.Err
			changed = p.Changed
			refetched = p.Refetched
		}
	}
	if doneErr == nil {
		t.Error("expected reconcile Done event to carry the refetch error")
	}
	if changed != 1 || refetched != 0 {
		t.Errorf("changed=%d refetched=%d, want 1 and 0", changed, refetched)
	}
}

func TestReconcileFields_UnknownFieldSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no Jira request expected for an unknown field, got %s", r.URL.Path)
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{SyncSources: []config.SyncSource{{Name: "TEST", Projects: []string{"TEST"}}}}
	e := New(database, jira.New(srv.URL, "t@t.com", "tok", 100), cfg)

	ch, err := e.ReconcileFields(context.Background(), "", []string{"does_not_exist"})
	if err != nil {
		t.Fatalf("ReconcileFields: %v", err)
	}
	for p := range ch {
		t.Errorf("expected no progress events for an unknown field, got %+v", p)
	}
}

func TestFullSyncOverdue(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := database.Exec(
		`INSERT INTO sync_metadata (project, last_full_sync) VALUES ('STALE', ?), ('FRESH', ?), ('NEVER', NULL)`,
		old, recent); err != nil {
		t.Fatalf("seeding sync_metadata: %v", err)
	}

	cfg := &config.Config{Sync: config.SyncConfig{FullSyncWarningAge: "24h"}}
	e := New(database, nil, cfg)

	stale := e.FullSyncOverdue("")
	got := map[string]bool{}
	for _, s := range stale {
		got[s] = true
	}
	if !got["STALE"] || !got["NEVER"] {
		t.Errorf("expected STALE and NEVER overdue, got %v", stale)
	}
	if got["FRESH"] {
		t.Errorf("FRESH should not be overdue, got %v", stale)
	}

	// Source filter restricts the check.
	if filtered := e.FullSyncOverdue("FRESH"); len(filtered) != 0 {
		t.Errorf("FullSyncOverdue(FRESH) = %v, want empty", filtered)
	}
}
