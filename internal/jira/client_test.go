package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(srv *httptest.Server) *Client {
	return New(srv.URL, "test@example.com", "token123", 100)
}

func TestMySelf(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/myself" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(MySelf{
			DisplayName:  "Test User",
			EmailAddress: "test@example.com",
		})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	me, err := client.MySelf(context.Background())
	if err != nil {
		t.Fatalf("MySelf: %v", err)
	}
	if me.DisplayName != "Test User" {
		t.Errorf("expected 'Test User', got %q", me.DisplayName)
	}
}

func TestFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := []*Field{
			{ID: "summary", Name: "Summary", Custom: false, Schema: &FieldSchema{Type: "string"}},
			{ID: "customfield_12345", Name: "Team", Custom: true, Schema: &FieldSchema{Type: "option"}},
		}
		json.NewEncoder(w).Encode(fields)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	fields, err := client.Fields(context.Background())
	if err != nil {
		t.Fatalf("Fields: %v", err)
	}
	if len(fields) != 2 {
		t.Errorf("expected 2 fields, got %d", len(fields))
	}
}

func TestSearchAll(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := SearchResponse{
			Issues: []*Issue{
				{Key: "TEST-1", Fields: json.RawMessage(`{"summary":"Issue 1"}`)},
				{Key: "TEST-2", Fields: json.RawMessage(`{"summary":"Issue 2"}`)},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newTestClient(srv)

	var allIssues []*Issue
	for page, err := range client.SearchAll(context.Background(), "project = TEST", []string{"summary"}) {
		if err != nil {
			t.Fatalf("SearchAll error: %v", err)
		}
		allIssues = append(allIssues, page...)
	}

	if len(allIssues) != 2 {
		t.Errorf("expected 2 issues, got %d", len(allIssues))
	}
}

func TestGetIssueChangelog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/TEST-1" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("expand") != "changelog" {
			t.Errorf("expected expand=changelog, got %q", r.URL.Query().Get("expand"))
		}
		resp := ChangelogResponse{
			Key: "TEST-1",
			Changelog: &Changelog{
				Histories: []ChangelogHistory{
					{
						ID:      "100",
						Author:  &struct{ DisplayName string `json:"displayName"` }{"Jane Doe"},
						Created: "2026-06-10T14:30:00.000+0000",
						Items: []ChangelogItem{
							{Field: "status", FieldType: "jira", From: "10001", FromString: "In Progress", To: "10002", ToString: "Release Pending"},
						},
					},
					{
						ID:      "101",
						Author:  &struct{ DisplayName string `json:"displayName"` }{"Bob Smith"},
						Created: "2026-06-01T10:00:00.000+0000",
						Items: []ChangelogItem{
							{Field: "status", FieldType: "jira", From: "10000", FromString: "New", To: "10001", ToString: "In Progress"},
							{Field: "assignee", FieldType: "jira", FromString: "", ToString: "Jane Doe"},
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	resp, err := client.GetIssueChangelog(context.Background(), "TEST-1")
	if err != nil {
		t.Fatalf("GetIssueChangelog: %v", err)
	}
	if resp.Key != "TEST-1" {
		t.Errorf("expected key TEST-1, got %q", resp.Key)
	}
	if resp.Changelog == nil {
		t.Fatal("expected changelog, got nil")
	}
	if len(resp.Changelog.Histories) != 2 {
		t.Fatalf("expected 2 histories, got %d", len(resp.Changelog.Histories))
	}
	if resp.Changelog.Histories[0].Items[0].ToString != "Release Pending" {
		t.Errorf("expected 'Release Pending', got %q", resp.Changelog.Histories[0].Items[0].ToString)
	}
	if len(resp.Changelog.Histories[1].Items) != 2 {
		t.Errorf("expected 2 items in second history, got %d", len(resp.Changelog.Histories[1].Items))
	}
}

func TestBulkFetchChangelogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/changelog/bulkfetch" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var req BulkChangelogRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.IssueIdsOrKeys) != 2 {
			t.Errorf("expected 2 keys, got %d", len(req.IssueIdsOrKeys))
		}

		resp := BulkChangelogResponse{
			StartAt:    0,
			MaxResults: 1000,
			Total:      3,
			Values: []BulkChangelogEntry{
				{
					ID:      "100",
					IssueID: "10042",
					Author:  &struct{ DisplayName string `json:"displayName"` }{"Jane"},
					Created: "2026-06-10T14:30:00.000+0000",
					Items:   []ChangelogItem{{Field: "status", FieldType: "jira", ToString: "Done"}},
				},
				{
					ID:      "101",
					IssueID: "10042",
					Author:  &struct{ DisplayName string `json:"displayName"` }{"Bob"},
					Created: "2026-06-01T10:00:00.000+0000",
					Items:   []ChangelogItem{{Field: "status", FieldType: "jira", ToString: "In Progress"}},
				},
				{
					ID:      "102",
					IssueID: "10043",
					Author:  &struct{ DisplayName string `json:"displayName"` }{"Jane"},
					Created: "2026-06-05T08:00:00.000+0000",
					Items:   []ChangelogItem{{Field: "priority", FieldType: "jira", ToString: "High"}},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	entries, err := client.BulkFetchChangelogs(context.Background(), []string{"TEST-1", "TEST-2"})
	if err != nil {
		t.Fatalf("BulkFetchChangelogs: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].IssueID != "10042" {
		t.Errorf("expected issueId 10042, got %q", entries[0].IssueID)
	}
	if entries[2].Items[0].ToString != "High" {
		t.Errorf("expected 'High', got %q", entries[2].Items[0].ToString)
	}
}

func TestBulkFetchChangelogs_Pagination(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		startAt := 0
		if page == 2 {
			startAt = 3
		}

		var resp BulkChangelogResponse
		if page == 1 {
			resp = BulkChangelogResponse{
				StartAt:    0,
				MaxResults: 3,
				Total:      5,
				Values: []BulkChangelogEntry{
					{ID: "100", IssueID: "10042", Items: []ChangelogItem{{Field: "status"}}},
					{ID: "101", IssueID: "10042", Items: []ChangelogItem{{Field: "priority"}}},
					{ID: "102", IssueID: "10043", Items: []ChangelogItem{{Field: "status"}}},
				},
			}
		} else {
			resp = BulkChangelogResponse{
				StartAt:    startAt,
				MaxResults: 3,
				Total:      5,
				Values: []BulkChangelogEntry{
					{ID: "103", IssueID: "10043", Items: []ChangelogItem{{Field: "assignee"}}},
					{ID: "104", IssueID: "10044", Items: []ChangelogItem{{Field: "status"}}},
				},
			}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	entries, err := client.BulkFetchChangelogs(context.Background(), []string{"TEST-1", "TEST-2", "TEST-3"})
	if err != nil {
		t.Fatalf("BulkFetchChangelogs: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries across 2 pages, got %d", len(entries))
	}
	if page != 2 {
		t.Errorf("expected 2 pages fetched, got %d", page)
	}
}

func TestBulkFetchChangelogs_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(BulkChangelogResponse{Total: 0})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	entries, err := client.BulkFetchChangelogs(context.Background(), []string{"TEST-1"})
	if err != nil {
		t.Fatalf("BulkFetchChangelogs: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestSearchAll_Retry429(t *testing.T) {
	attempt := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt < 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		resp := SearchResponse{
			Issues: []*Issue{{Key: "TEST-1", Fields: json.RawMessage(`{}`)}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newTestClient(srv)

	var issues []*Issue
	for page, err := range client.SearchAll(context.Background(), "project = TEST", []string{"summary"}) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		issues = append(issues, page...)
	}
	if len(issues) != 1 {
		t.Errorf("expected 1 issue after retry, got %d", len(issues))
	}
}
