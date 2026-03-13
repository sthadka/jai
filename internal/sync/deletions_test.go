package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/syethadk/jai/internal/db"
	"github.com/syethadk/jai/internal/jira"
)

func TestDetectDeletions(t *testing.T) {
	// Set up a mock Jira server that returns TEST-1 but not TEST-2.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := jira.SearchResponse{
			Total: 1,
			Issues: []*jira.Issue{
				{Key: "TEST-1", Fields: json.RawMessage(`{}`)},
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

	// Insert TEST-1 and TEST-2 locally.
	for _, key := range []string{"TEST-1", "TEST-2"} {
		issue := &db.Issue{Key: key, Project: "TEST", Summary: "Issue", RawJSON: "{}"}
		if err := database.UpsertIssue(issue, nil); err != nil {
			t.Fatalf("UpsertIssue %s: %v", key, err)
		}
	}

	client := jira.New(srv.URL, "test@test.com", "token", 100)
	deleted, err := DetectDeletions(context.Background(), database, client, "TEST")
	if err != nil {
		t.Fatalf("DetectDeletions: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// TEST-2 should be gone from local DB.
	issue, err := database.GetIssue("TEST-2")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue != nil {
		t.Error("expected TEST-2 to be deleted")
	}
}
