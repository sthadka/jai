package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
)

func newTestEngine(t *testing.T, srvURL string) *Engine {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	client := jira.New(srvURL, "test@test.com", "token", 100)
	return New(database, client, &config.Config{})
}

func TestVerifyAuth_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jira.MySelf{DisplayName: "Test User"})
	}))
	defer srv.Close()

	e := newTestEngine(t, srv.URL)
	if err := e.VerifyAuth(context.Background()); err != nil {
		t.Fatalf("VerifyAuth: %v", err)
	}
}

func TestVerifyAuth_Unauthenticated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errorMessages":["Unauthorized"]}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	e := newTestEngine(t, srv.URL)
	err := e.VerifyAuth(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var authErr *jira.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected wrapped *jira.AuthError, got %T: %v", err, err)
	}
}
