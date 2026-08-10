package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestVerifyAuth_RefreshesJiraTimezone(t *testing.T) {
	zones := []string{"America/New_York", "Asia/Tokyo"}
	request := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jira.MySelf{TimeZone: zones[request]})
		request++
	}))
	defer srv.Close()

	e := newTestEngine(t, srv.URL)
	for _, want := range zones {
		if err := e.VerifyAuth(context.Background()); err != nil {
			t.Fatalf("VerifyAuth: %v", err)
		}
		if got := e.jiraTimezone().String(); got != want {
			t.Errorf("Jira timezone = %q, want %q", got, want)
		}
	}
}

func TestVerifyAuth_InvalidTimezoneWarnsAndFallsBackToUTC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jira.MySelf{TimeZone: "Not/A_Timezone"})
	}))
	defer srv.Close()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	previousStderr := os.Stderr
	os.Stderr = writer
	t.Cleanup(func() { os.Stderr = previousStderr })

	e := newTestEngine(t, srv.URL)
	if err := e.VerifyAuth(context.Background()); err != nil {
		t.Fatalf("VerifyAuth: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing stderr pipe: %v", err)
	}
	os.Stderr = previousStderr
	warning, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if got := e.jiraTimezone(); got != time.UTC {
		t.Errorf("Jira timezone = %v, want UTC", got)
	}
	if !strings.Contains(string(warning), "falling back to UTC") {
		t.Errorf("warning = %q, want UTC fallback warning", warning)
	}
}

func TestSyncRequiresVerifyAuth(t *testing.T) {
	e := New(nil, nil, &config.Config{})
	_, err := e.Sync(context.Background(), false, false, "")
	if err == nil || !strings.Contains(err.Error(), "VerifyAuth") {
		t.Fatalf("Sync error = %v, want VerifyAuth requirement", err)
	}
}

func TestCursorToJQL(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	tests := []struct {
		name   string
		cursor string
		loc    *time.Location
		want   string
	}{
		{
			name:   "Jira timezone",
			cursor: "2026-08-10T00:15:59Z",
			loc:    loc,
			want:   "2026-08-09 20:15",
		},
		{
			name:   "nil location defaults to UTC",
			cursor: "2026-08-10T00:15:59Z",
			want:   "2026-08-10 00:15",
		},
		{
			name:   "malformed cursor is unchanged",
			cursor: "not-a-timestamp",
			loc:    loc,
			want:   "not-a-timestamp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cursorToJQL(tt.cursor, tt.loc); got != tt.want {
				t.Errorf("cursorToJQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEngineTimezoneConcurrentAuthAndSync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jira.MySelf{
			DisplayName: "Test User",
			TimeZone:    "America/New_York",
		})
	}))
	defer srv.Close()

	client := jira.New(srv.URL, "test@test.com", "token", 100)
	e := New(nil, client, &config.Config{})
	if err := e.VerifyAuth(context.Background()); err != nil {
		t.Fatalf("VerifyAuth: %v", err)
	}

	const workers = 16
	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(verify bool) {
			defer wg.Done()
			<-start
			if verify {
				errCh <- e.VerifyAuth(context.Background())
				return
			}
			ch, err := e.Sync(context.Background(), false, false, "")
			if err == nil {
				for range ch {
				}
			}
			errCh <- err
		}(i%2 == 0)
	}
	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent operation failed: %v", err)
		}
	}
}
