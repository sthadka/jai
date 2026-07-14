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
