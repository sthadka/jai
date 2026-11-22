package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchAllWithOptions_AutoSizeAndReconcileIssues(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, body)
		json.NewEncoder(w).Encode(SearchResponse{
			Issues: []*Issue{{ID: "1001", Key: "ENG-1", Fields: json.RawMessage(`{"updated":"2026-06-01T00:00:00.000+0000"}`)}},
		})
	}))
	defer srv.Close()

	client := newTestClient(srv)

	// Narrow scalar fields auto-size to 5000.
	for _, err := range client.SearchAllWithOptions(context.Background(), "project = ENG", []string{"updated"}, SearchOptions{
		ReconcileIssues: []int64{1001, 1002},
	}) {
		if err != nil {
			t.Fatalf("narrow SearchAllWithOptions: %v", err)
		}
	}
	if got := int(requests[0]["maxResults"].(float64)); got != SearchNarrowMaxResults {
		t.Errorf("narrow maxResults = %d, want %d", got, SearchNarrowMaxResults)
	}
	if rec, ok := requests[0]["reconcileIssues"].([]any); !ok || len(rec) != 2 {
		t.Errorf("reconcileIssues = %v, want 2 IDs", requests[0]["reconcileIssues"])
	}

	// Broad fields (with comment) stay at 100.
	for _, err := range client.SearchAll(context.Background(), "project = ENG", []string{"summary", "comment"}) {
		if err != nil {
			t.Fatalf("broad SearchAll: %v", err)
		}
	}
	if got := int(requests[1]["maxResults"].(float64)); got != SearchDefaultMaxResults {
		t.Errorf("broad maxResults = %d, want %d", got, SearchDefaultMaxResults)
	}

	// >50 reconcileIssues fails fast without hitting Jira.
	tooMany := make([]int64, SearchMaxReconcileIssues+1)
	var gotErr error
	for _, err := range client.SearchAllWithOptions(context.Background(), "project = ENG", []string{"key"}, SearchOptions{
		ReconcileIssues: tooMany,
	}) {
		gotErr = err
	}
	if gotErr == nil {
		t.Fatal("expected error for >50 reconcileIssues")
	}
	if len(requests) != 2 {
		t.Errorf("expected no network call for invalid reconcileIssues, got %d requests", len(requests))
	}
}

func TestSearchApproximateCount_AndSearchCount(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		json.NewEncoder(w).Encode(map[string]int{"count": 1420})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	n, err := client.SearchCount(context.Background(), "project = ENG")
	if err != nil {
		t.Fatalf("SearchCount: %v", err)
	}
	if n != 1420 {
		t.Errorf("count = %d, want 1420", n)
	}
	if len(paths) != 1 || paths[0] != "/rest/api/3/search/approximate-count" {
		t.Errorf("paths = %v, want single /rest/api/3/search/approximate-count call", paths)
	}
}

func TestBulkFetchBatchLimit(t *testing.T) {
	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprintf("customfield_%d", 10000+i)
	}

	tests := []struct {
		name   string
		fields []string
		expand []string
		want   int
	}{
		{"explicit narrow scalars", []string{"updated", "status", "customfield_10019"}, nil, BulkFetchScalarLimit},
		{"empty fields", nil, nil, BulkFetchFullLimit},
		{"wildcard *all", []string{"*all"}, nil, BulkFetchFullLimit},
		{"wildcard *navigable", []string{"*navigable"}, nil, BulkFetchFullLimit},
		{"exclusions only", []string{"-comment"}, nil, BulkFetchFullLimit},
		{"over 100 fields", many, nil, BulkFetchFullLimit},
		{"includes comment", []string{"summary", "comment"}, nil, BulkFetchFullLimit},
		{"includes attachment", []string{"summary", "attachment"}, nil, BulkFetchFullLimit},
		{"includes worklog", []string{"summary", "worklog"}, nil, BulkFetchFullLimit},
		{"expand changelog", []string{"summary"}, []string{"changelog"}, BulkFetchFullLimit},
		{"expand renderedFields", []string{"summary"}, []string{"renderedFields"}, BulkFetchFullLimit},
		{"benign expand names", []string{"summary"}, []string{"names"}, BulkFetchScalarLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BulkFetchBatchLimit(tt.fields, tt.expand); got != tt.want {
				t.Errorf("BulkFetchBatchLimit() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBulkFetchIssues_Chunking(t *testing.T) {
	var batchSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/bulkfetch" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			IDs []string `json:"issueIdsOrKeys"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		batchSizes = append(batchSizes, len(req.IDs))
		issues := make([]*Issue, len(req.IDs))
		for i, id := range req.IDs {
			issues[i] = &Issue{ID: id, Key: "ENG-" + id, Fields: json.RawMessage(`{"summary":"ok"}`)}
		}
		json.NewEncoder(w).Encode(BulkFetchIssuesResult{Issues: issues})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	ids := make([]string, 150)
	for i := range ids {
		ids[i] = fmt.Sprintf("%d", 1000+i)
	}

	// With comment: chunks into 100 + 50.
	res, err := client.BulkFetchIssues(context.Background(), ids, BulkFetchIssuesOptions{
		Fields: []string{"summary", "comment"},
	})
	if err != nil {
		t.Fatalf("BulkFetchIssues full: %v", err)
	}
	if len(res.Issues) != 150 {
		t.Fatalf("got %d issues, want 150", len(res.Issues))
	}
	if len(batchSizes) != 2 || batchSizes[0] != 100 || batchSizes[1] != 50 {
		t.Errorf("comment batch sizes = %v, want [100 50]", batchSizes)
	}

	// Scalar-only: all 150 fit in 1 call (up to 1000).
	batchSizes = nil
	res, err = client.BulkFetchIssues(context.Background(), ids, BulkFetchIssuesOptions{
		Fields: []string{"updated", "status"},
	})
	if err != nil {
		t.Fatalf("BulkFetchIssues scalar: %v", err)
	}
	if len(res.Issues) != 150 {
		t.Fatalf("got %d issues, want 150", len(res.Issues))
	}
	if len(batchSizes) != 1 || batchSizes[0] != 150 {
		t.Errorf("scalar batch sizes = %v, want [150]", batchSizes)
	}
}

func TestBulkFetchChangelogsWithOptions(t *testing.T) {
	var gotReqs []BulkChangelogRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req BulkChangelogRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotReqs = append(gotReqs, req)
		json.NewEncoder(w).Encode(BulkChangelogResponse{})
	}))
	defer srv.Close()

	client := newTestClient(srv)

	// Rejects >10 fieldIds.
	if _, err := client.BulkFetchChangelogsWithOptions(context.Background(), []string{"ENG-1"}, BulkChangelogOptions{
		FieldIDs: make([]string, BulkChangelogMaxFieldIDs+1),
	}); err == nil {
		t.Fatal("expected error for >10 fieldIds")
	}

	// Chunks >1000 issues into 1000 + 50 with maxResults=10000 and fieldIds.
	keys := make([]string, 1050)
	for i := range keys {
		keys[i] = fmt.Sprintf("ENG-%d", i+1)
	}
	_, err := client.BulkFetchChangelogsWithOptions(context.Background(), keys, BulkChangelogOptions{
		FieldIDs: []string{"status", "assignee"},
	})
	if err != nil {
		t.Fatalf("BulkFetchChangelogsWithOptions: %v", err)
	}
	if len(gotReqs) != 2 {
		t.Fatalf("expected 2 chunked calls, got %d", len(gotReqs))
	}
	if len(gotReqs[0].IssueIdsOrKeys) != 1000 || len(gotReqs[1].IssueIdsOrKeys) != 50 {
		t.Errorf("chunk sizes = [%d, %d], want [1000, 50]", len(gotReqs[0].IssueIdsOrKeys), len(gotReqs[1].IssueIdsOrKeys))
	}
	if gotReqs[0].MaxResults != BulkChangelogMaxResults {
		t.Errorf("MaxResults = %d, want %d", gotReqs[0].MaxResults, BulkChangelogMaxResults)
	}
	if len(gotReqs[0].FieldIDs) != 2 || gotReqs[0].FieldIDs[0] != "status" {
		t.Errorf("FieldIDs = %v, want [status assignee]", gotReqs[0].FieldIDs)
	}
}

func TestBulkGetUsers(t *testing.T) {
	var seenBatches [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/user/bulk" {
			http.NotFound(w, r)
			return
		}
		ids := r.URL.Query()["accountId"]
		seenBatches = append(seenBatches, ids)
		users := make([]User, len(ids))
		for i, id := range ids {
			users[i] = User{AccountID: id, DisplayName: "User " + id, Active: true}
		}
		json.NewEncoder(w).Encode(bulkUsersPage{
			Values: users,
			Total:  len(users),
			IsLast: true,
		})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	users, err := client.BulkGetUsers(context.Background(), []string{"u1", "", "u2", "u1"})
	if err != nil {
		t.Fatalf("BulkGetUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}
	if len(seenBatches) != 1 || len(seenBatches[0]) != 2 {
		t.Errorf("expected single deduplicated batch of 2, got %v", seenBatches)
	}
}

func TestDirectoryEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/project/recent":
			json.NewEncoder(w).Encode([]ProjectInfo{{ID: "100", Key: "ENG", Name: "Engineering", Style: "classic"}})
		case "/rest/api/3/project/search":
			q := r.URL.Query()
			if q.Get("maxResults") != "100" {
				t.Errorf("expected maxResults clamped to 100, got %q", q.Get("maxResults"))
			}
			if q.Get("query") != "eng" {
				t.Errorf("query = %q, want eng", q.Get("query"))
			}
			json.NewEncoder(w).Encode(ProjectSearchResponse{
				Values: []ProjectInfo{{ID: "100", Key: "ENG", Name: "Engineering"}},
				Total:  1,
				IsLast: true,
			})
		case "/rest/agile/1.0/board":
			q := r.URL.Query()
			if q.Get("type") != "" {
				t.Errorf("GetBoards/SearchBoards without type must not filter by scrum, got type=%q", q.Get("type"))
			}
			if q.Get("startAt") == "" {
				json.NewEncoder(w).Encode(BoardSearchResponse{
					Values: []Board{{ID: 1, Name: "Scrum", Type: "scrum"}},
					Total:  2,
					IsLast: false,
				})
				return
			}
			json.NewEncoder(w).Encode(BoardSearchResponse{
				Values: []Board{{ID: 2, Name: "Kanban", Type: "kanban"}},
				Total:  2,
				IsLast: true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := newTestClient(srv)

	recent, err := client.GetRecentProjects(context.Background())
	if err != nil || len(recent) != 1 || recent[0].Key != "ENG" {
		t.Fatalf("GetRecentProjects = (%+v, %v)", recent, err)
	}

	projResp, err := client.SearchProjects(context.Background(), ProjectSearchOptions{
		Query:      "eng",
		Keys:       []string{"ENG"},
		Status:     []string{"live"},
		MaxResults: 500, // clamped to 100
	})
	if err != nil || len(projResp.Values) != 1 {
		t.Fatalf("SearchProjects = (%+v, %v)", projResp, err)
	}

	boards, err := client.GetBoards(context.Background(), "ENG")
	if err != nil {
		t.Fatalf("GetBoards: %v", err)
	}
	if len(boards) != 2 || boards[0].Type != "scrum" || boards[1].Type != "kanban" {
		t.Errorf("GetBoards = %+v, want scrum + kanban across 2 pages", boards)
	}
}

func TestBulkWriteEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/bulk/issues/fields":
			json.NewEncoder(w).Encode(BulkEditableFieldsResponse{
				Fields: []BulkEditableField{{ID: "priority", Name: "Priority"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/bulk/issues/fields":
			json.NewEncoder(w).Encode(BulkTaskResponse{TaskID: "task-fields"})
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/bulk/issues/transition":
			json.NewEncoder(w).Encode(BulkTransitionsResponse{
				AvailableTransitions: []json.RawMessage{json.RawMessage(`{"id":"31","name":"Done"}`)},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/bulk/issues/transition":
			json.NewEncoder(w).Encode(BulkTaskResponse{TaskID: "task-trans"})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/bulk/issues/delete":
			// Simulate missing Global Bulk Change permission.
			http.Error(w, `{"errorMessages":["Forbidden"]}`, http.StatusForbidden)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/bulk/issues/watch":
			json.NewEncoder(w).Encode(BulkTaskResponse{TaskID: "task-watch"})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/bulk/issues/unwatch":
			json.NewEncoder(w).Encode(BulkTaskResponse{TaskID: "task-unwatch"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/api/3/bulk/queue/"):
			json.NewEncoder(w).Encode(BulkTaskStatus{
				TaskID:          "task-fields",
				Status:          "COMPLETE",
				ProgressPercent: 100,
				TotalIssueCount: 3,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := newTestClient(srv)
	ctx := context.Background()

	ef, err := client.GetBulkEditableFields(ctx, []string{"ENG-1", "ENG-2"}, BulkFieldsQueryOptions{SearchText: "prio"})
	if err != nil || len(ef.Fields) != 1 {
		t.Fatalf("GetBulkEditableFields = (%+v, %v)", ef, err)
	}
	task, err := client.BulkEditFields(ctx, BulkEditFieldsRequest{
		SelectedIssueIDsOrKeys: []string{"ENG-1", "ENG-2"},
		EditedFieldsInput:      map[string]any{"priority": map[string]string{"name": "High"}},
	})
	if err != nil || task.TaskID != "task-fields" {
		t.Fatalf("BulkEditFields = (%+v, %v)", task, err)
	}
	tr, err := client.GetBulkTransitions(ctx, []string{"ENG-1"}, "")
	if err != nil || len(tr.AvailableTransitions) != 1 {
		t.Fatalf("GetBulkTransitions = (%+v, %v)", tr, err)
	}
	task, err = client.BulkTransitionIssues(ctx, BulkTransitionRequest{
		BulkTransitionInputs: []BulkTransitionInput{{SelectedIssueIDsOrKeys: []string{"ENG-1"}, TransitionID: "31"}},
	})
	if err != nil || task.TaskID != "task-trans" {
		t.Fatalf("BulkTransitionIssues = (%+v, %v)", task, err)
	}
	_, err = client.BulkDeleteIssues(ctx, []string{"ENG-1"}, false)
	var authErr *AuthError
	if !errors.As(err, &authErr) || authErr.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 *AuthError on BulkDeleteIssues, got %T: %v", err, err)
	}
	if task, err = client.BulkWatchIssues(ctx, []string{"ENG-1"}); err != nil || task.TaskID != "task-watch" {
		t.Fatalf("BulkWatchIssues = (%+v, %v)", task, err)
	}
	if task, err = client.BulkUnwatchIssues(ctx, []string{"ENG-1"}); err != nil || task.TaskID != "task-unwatch" {
		t.Fatalf("BulkUnwatchIssues = (%+v, %v)", task, err)
	}
	status, err := client.GetBulkTask(ctx, "task-fields")
	if err != nil || status.Status != "COMPLETE" || status.TotalIssueCount != 3 {
		t.Fatalf("GetBulkTask = (%+v, %v)", status, err)
	}
}
