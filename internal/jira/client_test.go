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
