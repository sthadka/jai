package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sthadka/jai/v6/pkg/clock"
	"github.com/sthadka/jai/v6/pkg/config"
	"github.com/sthadka/jai/v6/pkg/jira"
	db "github.com/sthadka/jai/v6/pkg/store"
)

func TestDeltaSync_IdleSkipsStage2(t *testing.T) {
	var bulkCalls atomic.Int32
	var probeMaxResults int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/search/jql":
			var body struct {
				Fields     []string `json:"fields"`
				MaxResults int      `json:"maxResults"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			probeMaxResults = body.MaxResults
			if len(body.Fields) != 1 || body.Fields[0] != "updated" {
				t.Errorf("Stage 1 fields = %v, want [updated]", body.Fields)
			}
			json.NewEncoder(w).Encode(jira.SearchResponse{
				Issues: []*jira.Issue{
					{ID: "1001", Key: "ENG-1", Fields: json.RawMessage(`{"updated":"2026-06-01T12:00:00.000+0000"}`)},
				},
			})
		case "/rest/api/3/issue/bulkfetch":
			bulkCalls.Add(1)
			http.Error(w, "should not be called when idle", http.StatusInternalServerError)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	seed := &db.Issue{ID: "1001", Key: "ENG-1", Project: "ENG", Summary: "Unchanged", Updated: "2026-06-01T12:00:00Z", RawJSON: "{}"}
	if err := database.UpsertIssue(seed, nil); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	e := New(database, jira.New(srv.URL, "a@b.c", "tok", 100), &config.Config{})
	e.SetTimezone(time.UTC)

	res, err := e.DeltaSync(context.Background(), DeltaOptions{
		ScopeName: "eng",
		JQL:       `project = "ENG"`,
	})
	if err != nil {
		t.Fatalf("DeltaSync: %v", err)
	}
	if probeMaxResults != jira.SearchNarrowMaxResults {
		t.Errorf("Stage-1 maxResults = %d, want %d", probeMaxResults, jira.SearchNarrowMaxResults)
	}
	if bulkCalls.Load() != 0 {
		t.Errorf("Stage-2 BulkFetchIssues called %d times on idle delta, want 0", bulkCalls.Load())
	}
	if res.Probed != 1 || res.Changed != 0 || len(res.SyncedKeys) != 0 {
		t.Errorf("unexpected DeltaResult: %+v", res)
	}
}

func TestDeltaSync_HydratesChangedOnlyAndFiltersChangelog(t *testing.T) {
	var gotProbeJQL string
	var gotBulkIDs []string
	var gotBulkFields []string
	var gotChangelogFieldIDs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/search/jql":
			var body struct {
				JQL             string  `json:"jql"`
				ReconcileIssues []int64 `json:"reconcileIssues"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotProbeJQL = body.JQL
			if len(body.ReconcileIssues) != 1 || body.ReconcileIssues[0] != 1002 {
				t.Errorf("ReconcileIssues = %v, want [1002]", body.ReconcileIssues)
			}
			json.NewEncoder(w).Encode(jira.SearchResponse{
				Issues: []*jira.Issue{
					{ID: "1001", Key: "ENG-1", Fields: json.RawMessage(`{"updated":"2026-06-01T10:00:00.000+0000"}`)}, // unchanged
					{ID: "1002", Key: "ENG-2", Fields: json.RawMessage(`{"updated":"2026-06-01T12:00:00.000+0000"}`)}, // updated
					{ID: "1003", Key: "ENG-3", Fields: json.RawMessage(`{"updated":"2026-06-01T12:05:00.000+0000"}`)}, // new
					{ID: "1004", Key: "ENG-4", Fields: json.RawMessage(`{"updated":"2026-06-01T12:06:00.000+0000"}`)}, // vanishes before Stage 2
				},
			})
		case "/rest/api/3/issue/bulkfetch":
			var body struct {
				IDs    []string `json:"issueIdsOrKeys"`
				Fields []string `json:"fields"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotBulkIDs = body.IDs
			gotBulkFields = body.Fields
			json.NewEncoder(w).Encode(jira.BulkFetchIssuesResult{
				Issues: []*jira.Issue{
					{ID: "1002", Key: "ENG-2", Fields: json.RawMessage(`{"summary":"Updated","project":{"key":"ENG"},"updated":"2026-06-01T12:00:00.000+0000"}`)},
					{ID: "1003", Key: "ENG-3", Fields: json.RawMessage(`{"summary":"Brand New","project":{"key":"ENG"},"updated":"2026-06-01T12:05:00.000+0000"}`)},
				},
			})
		case "/rest/api/3/changelog/bulkfetch":
			var body jira.BulkChangelogRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotChangelogFieldIDs = body.FieldIDs
			json.NewEncoder(w).Encode(jira.BulkChangelogResponse{
				IssueChangeLogs: []jira.IssueChangeLog{
					{
						IssueID: "1002",
						ChangeHistories: []jira.BulkChangeHistory{
							{ID: "ch-1", Created: 1780315200000, Items: []jira.ChangelogItem{{Field: "status", ToString: "In Progress"}}},
						},
					},
				},
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// Seed ENG-1 (unchanged) and ENG-2 (older updated), plus an existing HWM.
	_ = database.UpsertIssue(&db.Issue{ID: "1001", Key: "ENG-1", Project: "ENG", Summary: "Same", Updated: "2026-06-01T10:00:00Z", RawJSON: "{}"}, nil)
	_ = database.UpsertIssue(&db.Issue{ID: "1002", Key: "ENG-2", Project: "ENG", Summary: "Old", Updated: "2026-06-01T09:00:00Z", RawJSON: "{}"}, nil)
	_ = database.UpdateSyncMeta("eng", 0.1, 2, 2, "", "2026-06-01T10:00:00Z")

	e := New(database, jira.New(srv.URL, "a@b.c", "tok", 100), &config.Config{})
	e.SetTimezone(time.UTC)

	res, err := e.DeltaSync(context.Background(), DeltaOptions{
		ScopeName:         "eng",
		JQL:               `project = "ENG"`,
		WatchIDs:          []string{"1002"},
		SlimFields:        true,
		ReconcileIssues:   []int64{1002},
		SyncChangelogs:    true,
		ChangelogFieldIDs: []string{"status", "assignee"},
	})
	if err != nil {
		t.Fatalf("DeltaSync: %v", err)
	}

	if !strings.Contains(gotProbeJQL, "OR id in (1002)") || !strings.Contains(gotProbeJQL, `updated >= "2026-06-01 09:58"`) {
		t.Errorf("unexpected Stage-1 JQL: %q", gotProbeJQL)
	}
	if !slices.Equal(gotBulkIDs, []string{"1002", "1003", "1004"}) {
		t.Errorf("Stage-2 IDs = %v, want [1002 1003 1004] (skipping unchanged 1001)", gotBulkIDs)
	}
	if !slices.Contains(gotBulkFields, "comment") || slices.Contains(gotBulkFields, "attachment") {
		t.Errorf("SlimFields should include comment and exclude attachment, got %v", gotBulkFields)
	}
	if !slices.Equal(gotChangelogFieldIDs, []string{"status", "assignee"}) {
		t.Errorf("changelog fieldIds = %v, want [status assignee]", gotChangelogFieldIDs)
	}
	if res.Probed != 4 || res.New != 2 || res.Updated != 1 || len(res.SyncedKeys) != 2 {
		t.Errorf("unexpected DeltaResult: %+v", res)
	}
	if !slices.Equal(res.MissingIDs, []string{"1004"}) {
		t.Errorf("MissingIDs = %v, want [1004]", res.MissingIDs)
	}

	// Filtered changelog sync stamps changelog_synced_at but leaves changelog_full_synced_at empty.
	st, err := database.GetIssueHydrationState("ENG-2")
	if err != nil || st == nil {
		t.Fatalf("GetIssueHydrationState = (%+v, %v)", st, err)
	}
	if st.ChangelogSyncedAt == "" || st.ChangelogFullSyncedAt != "" {
		t.Errorf("filtered changelog hydration state = %+v, want tracked stamped & full empty", st)
	}

	// HWM advanced after full success.
	meta, err := database.GetSyncMeta("eng")
	if err != nil {
		t.Fatalf("GetSyncMeta: %v", err)
	}
	if meta.LastIssueUpdated.String != "2026-06-01T12:06:00Z" {
		t.Errorf("HWM = %q, want 2026-06-01T12:06:00Z", meta.LastIssueUpdated.String)
	}
}

func TestDeltaSync_Stage2FailureLeavesHWMUntouched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/search/jql":
			json.NewEncoder(w).Encode(jira.SearchResponse{
				Issues: []*jira.Issue{
					{ID: "1002", Key: "ENG-2", Fields: json.RawMessage(`{"updated":"2026-06-01T14:00:00.000+0000"}`)},
				},
			})
		case "/rest/api/3/issue/bulkfetch":
			// 400 fails immediately without retry sleeps.
			http.Error(w, `{"errorMessages":["bad request"]}`, http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	const origHWM = "2026-06-01T10:00:00Z"
	_ = database.UpdateSyncMeta("eng", 0.1, 1, 1, "", origHWM)

	e := New(database, jira.New(srv.URL, "a@b.c", "tok", 100), &config.Config{})
	e.SetTimezone(time.UTC)

	if _, err := e.DeltaSync(context.Background(), DeltaOptions{
		ScopeName: "eng",
		JQL:       `project = "ENG"`,
	}); err == nil {
		t.Fatal("expected Stage-2 error")
	}

	meta, err := database.GetSyncMeta("eng")
	if err != nil {
		t.Fatalf("GetSyncMeta: %v", err)
	}
	if meta.LastIssueUpdated.String != origHWM {
		t.Errorf("HWM moved to %q on Stage-2 failure, want unchanged %q", meta.LastIssueUpdated.String, origHWM)
	}
}

func TestEngineSync_IncrementalUsesTwoStageAfterHWM(t *testing.T) {
	var stage1Probe, stage2Bulk int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/search/jql":
			var body struct {
				Fields []string `json:"fields"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Fields) == 1 && body.Fields[0] == "updated" {
				stage1Probe++
			}
			json.NewEncoder(w).Encode(jira.SearchResponse{
				Issues: []*jira.Issue{
					{ID: "1001", Key: "ENG-1", Fields: json.RawMessage(`{"updated":"2026-06-01T12:00:00.000+0000"}`)},
				},
			})
		case "/rest/api/3/issue/bulkfetch":
			stage2Bulk++
			json.NewEncoder(w).Encode(jira.BulkFetchIssuesResult{
				Issues: []*jira.Issue{
					{ID: "1001", Key: "ENG-1", Fields: json.RawMessage(`{"summary":"Fresh","project":{"key":"ENG"},"updated":"2026-06-01T12:00:00.000+0000"}`)},
				},
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// Pre-seed HWM so Engine.Sync(full=false) runs the two-stage delta path.
	_ = database.UpdateSyncMeta("eng", 0.1, 1, 1, "", "2026-06-01T11:00:00Z")

	cfg := &config.Config{
		SyncSources: []config.SyncSource{{Name: "eng", Projects: []string{"ENG"}}},
	}
	e := New(database, jira.New(srv.URL, "a@b.c", "tok", 100), cfg)
	e.SetTimezone(time.UTC)

	ch, err := e.Sync(context.Background(), false, false, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for p := range ch {
		if p.Error != nil {
			t.Fatalf("Sync progress error: %v", p.Error)
		}
	}
	if stage1Probe != 1 || stage2Bulk != 1 {
		t.Errorf("stage1Probe=%d stage2Bulk=%d, want 1 and 1", stage1Probe, stage2Bulk)
	}
}

func TestHydrateIssueDetails_AndSyncIssueFullChangelog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/issue/ENG-1" && r.URL.Query().Get("expand") == "changelog":
			json.NewEncoder(w).Encode(jira.ChangelogResponse{
				Key: "ENG-1",
				Changelog: &jira.Changelog{
					Histories: []jira.ChangelogHistory{
						{
							ID:      "h-99",
							Created: "2026-06-01T12:00:00.000+0000",
							Items:   []jira.ChangelogItem{{Field: "description", FromString: "old", ToString: "new"}},
						},
					},
				},
			})
		case r.URL.Path == "/rest/api/3/issue/ENG-1":
			json.NewEncoder(w).Encode(jira.Issue{
				ID:  "1001",
				Key: "ENG-1",
				Fields: json.RawMessage(`{
					"summary": "Detailed",
					"project": {"key": "ENG"},
					"updated": "2026-06-01T12:00:00.000+0000",
					"attachment": [{"id": 55, "filename": "spec.pdf", "size": 1024, "mimeType": "application/pdf", "created": "2026-06-01T12:00:00.000+0000", "content": "https://example/spec.pdf"}]
				}`),
			})
		default:
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	e := New(database, jira.New(srv.URL, "a@b.c", "tok", 100), &config.Config{})
	issue, err := e.HydrateIssueDetails(context.Background(), "ENG-1")
	if err != nil {
		t.Fatalf("HydrateIssueDetails: %v", err)
	}
	if issue.Key != "ENG-1" {
		t.Errorf("Key = %q, want ENG-1", issue.Key)
	}

	n, err := e.SyncIssueFullChangelog(context.Background(), "ENG-1")
	if err != nil {
		t.Fatalf("SyncIssueFullChangelog: %v", err)
	}
	if n != 1 {
		t.Errorf("entries = %d, want 1", n)
	}

	st, err := database.GetIssueHydrationState("ENG-1")
	if err != nil || st == nil {
		t.Fatalf("GetIssueHydrationState = (%+v, %v)", st, err)
	}
	if st.DetailsSyncedAt == "" || st.ChangelogSyncedAt == "" || st.ChangelogFullSyncedAt == "" {
		t.Errorf("expected details + full changelog stamped, got %+v", st)
	}
}

func TestVerifyAuthIfStale_AndInvalidateAuth(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(jira.MySelf{DisplayName: "User", TimeZone: "UTC"})
	}))
	defer srv.Close()

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	clk := &mutableClock{t: now}
	e := New(nil, jira.New(srv.URL, "a@b.c", "tok", 100), &config.Config{}).WithClock(clk)

	if err := e.VerifyAuthIfStale(context.Background(), 10*time.Minute); err != nil {
		t.Fatalf("first VerifyAuthIfStale: %v", err)
	}
	clk.t = now.Add(5 * time.Minute)
	if err := e.VerifyAuthIfStale(context.Background(), 10*time.Minute); err != nil {
		t.Fatalf("cached VerifyAuthIfStale: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 within TTL", calls.Load())
	}

	// Past TTL refreshes.
	clk.t = now.Add(11 * time.Minute)
	if err := e.VerifyAuthIfStale(context.Background(), 10*time.Minute); err != nil {
		t.Fatalf("expired VerifyAuthIfStale: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 after TTL expiry", calls.Load())
	}

	// InvalidateAuth forces refresh immediately even within TTL.
	e.InvalidateAuth()
	if err := e.VerifyAuthIfStale(context.Background(), 10*time.Minute); err != nil {
		t.Fatalf("post-invalidate VerifyAuthIfStale: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 after InvalidateAuth", calls.Load())
	}
}

type mutableClock struct{ t time.Time }

func (c *mutableClock) Now() time.Time { return c.t }

var _ clock.Clock = (*mutableClock)(nil)

func TestSyncProjects_BatchedSearchProjects(t *testing.T) {
	var searchCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/project/search" {
			t.Errorf("unexpected path %s (expected batched /project/search)", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		searchCalls.Add(1)
		json.NewEncoder(w).Encode(jira.ProjectSearchResponse{
			Values: []jira.ProjectInfo{
				{Key: "ENG", Name: "Engineering"},
				{Key: "OPS", Name: "Operations"},
			},
			IsLast: true,
		})
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	_ = database.UpsertIssue(&db.Issue{ID: "1", Key: "ENG-1", Project: "ENG", Summary: "A", RawJSON: "{}"}, nil)
	_ = database.UpsertIssue(&db.Issue{ID: "2", Key: "OPS-1", Project: "OPS", Summary: "B", RawJSON: "{}"}, nil)

	e := New(database, jira.New(srv.URL, "a@b.c", "tok", 100), &config.Config{})
	e.SyncProjects(context.Background())

	if searchCalls.Load() != 1 {
		t.Errorf("expected 1 batched /project/search call, got %d", searchCalls.Load())
	}
	if got := database.GetProjectName("ENG"); got != "Engineering" {
		t.Errorf("ENG name = %q, want Engineering", got)
	}
	if got := database.GetProjectName("OPS"); got != "Operations" {
		t.Errorf("OPS name = %q, want Operations", got)
	}
}

func TestDetectDeletedKeys_StableIDSurvivesRename(t *testing.T) {
	var gotMaxResults int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxResults int `json:"maxResults"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotMaxResults = body.MaxResults
		// Issue ID 1001 still exists remotely with a renamed key NEW-1; ID 1002 was deleted.
		json.NewEncoder(w).Encode(jira.SearchResponse{
			Issues: []*jira.Issue{
				{ID: "1001", Key: "NEW-1", Fields: json.RawMessage(`{}`)},
			},
		})
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	_ = database.UpsertIssue(&db.Issue{ID: "1001", Key: "OLD-1", Project: "OLD", Summary: "Renamed", RawJSON: "{}"}, nil)
	_ = database.UpsertIssue(&db.Issue{ID: "1002", Key: "OLD-2", Project: "OLD", Summary: "Deleted", RawJSON: "{}"}, nil)

	deleted, err := DetectDeletedKeys(context.Background(), database, jira.New(srv.URL, "a@b.c", "tok", 100), "OLD")
	if err != nil {
		t.Fatalf("DetectDeletedKeys: %v", err)
	}
	if gotMaxResults != jira.SearchNarrowMaxResults {
		t.Errorf("maxResults = %d, want %d", gotMaxResults, jira.SearchNarrowMaxResults)
	}
	if !slices.Equal(deleted, []string{"OLD-2"}) {
		t.Errorf("deleted = %v, want [OLD-2] (OLD-1 has surviving stable ID 1001)", deleted)
	}
}

func TestSyncSprints_StoresKanbanSkipsSprints(t *testing.T) {
	var sprintBoardIDs []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/agile/1.0/board":
			json.NewEncoder(w).Encode(jira.BoardSearchResponse{
				Values: []jira.Board{
					{ID: 10, Name: "Team Scrum", Type: "scrum"},
					{ID: 20, Name: "Team Kanban", Type: "kanban"},
				},
				IsLast: true,
			})
		case "/rest/agile/1.0/board/10/sprint":
			sprintBoardIDs = append(sprintBoardIDs, 10)
			json.NewEncoder(w).Encode(jira.SprintResponse{})
		case "/rest/agile/1.0/board/20/sprint":
			sprintBoardIDs = append(sprintBoardIDs, 20)
			http.Error(w, "kanban has no sprints", http.StatusBadRequest)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	e := New(database, jira.New(srv.URL, "a@b.c", "tok", 100), &config.Config{})
	e.SyncSprints(context.Background(), config.SyncSource{Name: "eng", Projects: []string{"ENG"}})

	if !slices.Equal(sprintBoardIDs, []int{10}) {
		t.Errorf("GetSprints called for board IDs %v, want only [10]", sprintBoardIDs)
	}

	var boardCount int
	if err := database.QueryRow(`SELECT count(*) FROM boards`).Scan(&boardCount); err != nil {
		t.Fatalf("counting boards: %v", err)
	}
	if boardCount != 2 {
		t.Errorf("stored board count = %d, want 2 (both scrum and kanban)", boardCount)
	}
}
