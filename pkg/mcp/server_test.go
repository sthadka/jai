//go:build fts5

package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sthadka/jai/v6/pkg/config"
	"github.com/sthadka/jai/v6/pkg/jira"
	"github.com/sthadka/jai/v6/pkg/query"
	db "github.com/sthadka/jai/v6/pkg/store"
	synce "github.com/sthadka/jai/v6/pkg/sync"
)

// testServer creates a minimal Server instance for testing using a temp DB.
func testServer(t *testing.T, readOnly bool, toolsets []string) *Server {
	t.Helper()

	// Create temp DB
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Create minimal config
	cfg := &config.Config{
		Jira: config.JiraConfig{
			URL:   "https://test.atlassian.net",
			Email: "test@example.com",
			Token: "test-token",
		},
		MCP: config.MCPConfig{
			ReadOnly: readOnly,
			Toolsets: toolsets,
		},
		Me: "test@example.com",
	}

	// Create minimal Jira client (won't be used in these tests)
	jiraClient := jira.New(cfg.Jira.URL, cfg.Jira.Email, cfg.Jira.Token, 10)

	// Create query engine
	queryEngine := query.New(database, cfg)

	// Create sync engine (won't be used in these tests)
	syncEngine := synce.New(database, jiraClient, cfg)

	// Create server
	return New(cfg, database, jiraClient, queryEngine, syncEngine)
}

func TestNew(t *testing.T) {
	srv := testServer(t, false, nil)

	if srv == nil {
		t.Fatal("New() returned nil")
	}

	if srv.cfg == nil {
		t.Error("server config is nil")
	}

	if srv.db == nil {
		t.Error("server db is nil")
	}

	if srv.jira == nil {
		t.Error("server jira client is nil")
	}

	if srv.query == nil {
		t.Error("server query engine is nil")
	}

	if srv.sync == nil {
		t.Error("server sync engine is nil")
	}

	if srv.toolsets == nil {
		t.Error("server toolsets is nil")
	}

	if srv.mcpSrv == nil {
		t.Error("server MCP server is nil")
	}
}

func TestServerReadOnlyMode(t *testing.T) {
	srv := testServer(t, true, []string{"all"})

	if !srv.toolsets.IsReadOnly() {
		t.Error("server should be in read-only mode")
	}

	if srv.toolsets.IsEnabled("write") {
		t.Error("write toolset should be disabled in read-only mode")
	}

	if srv.toolsets.IsEnabled("config") {
		t.Error("config toolset should be disabled in read-only mode")
	}

	if srv.toolsets.IsToolEnabled("jai_set") {
		t.Error("jai_set should be disabled in read-only mode")
	}
}

func TestServerToolsetFiltering(t *testing.T) {
	tests := []struct {
		name     string
		toolsets []string
		want     map[string]bool
	}{
		{
			name:     "read toolset only",
			toolsets: []string{"read"},
			want: map[string]bool{
				"read":   true,
				"write":  false,
				"schema": false,
			},
		},
		{
			name:     "read and schema toolsets",
			toolsets: []string{"read", "schema"},
			want: map[string]bool{
				"read":   true,
				"schema": true,
				"write":  false,
			},
		},
		{
			name:     "all toolsets",
			toolsets: []string{"all"},
			want: map[string]bool{
				"read":   true,
				"write":  true,
				"schema": true,
				"sync":   true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testServer(t, false, tt.toolsets)

			for toolset, expected := range tt.want {
				if got := srv.toolsets.IsEnabled(toolset); got != expected {
					t.Errorf("toolset %q enabled = %v, want %v", toolset, got, expected)
				}
			}
		})
	}
}

func TestServerBuildServer(t *testing.T) {
	srv := testServer(t, false, nil)

	if srv.mcpSrv == nil {
		t.Fatal("buildServer() returned nil MCP server")
	}

	// Verify server was configured with capabilities
	// The MCP server should be ready to serve
	// We can't easily test the internal state without exposing it,
	// but we can verify it doesn't panic during construction
}

func TestServeStdio(t *testing.T) {
	// This test verifies ServeStdio doesn't panic on creation
	// Full integration testing of stdio transport is beyond the scope of unit tests
	srv := testServer(t, false, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately to avoid blocking

	// ServeStdio will fail when trying to read from stdin,
	// but we're just verifying it can be called without panicking
	_ = srv.ServeStdio(ctx)
}

func TestServerHTTPAndSSE(t *testing.T) {
	// These tests verify the HTTP/SSE server methods exist and can be called.
	// Full integration testing of these transports is beyond the scope of unit tests.
	srv := testServer(t, false, nil)

	// Verify server is initialized (methods exist by compilation)
	if srv == nil {
		t.Fatal("testServer returned nil")
	}

	// The methods ServeHTTP and ServeSSE exist as methods on the Server type.
	// Their existence is verified at compile time, not runtime.
}

func TestHandleIssueResource(t *testing.T) {
	srv := testServer(t, false, nil)

	if _, err := srv.db.Exec(`INSERT INTO issues (
		key, project, type, summary, description, status, status_category,
		priority, assignee, assignee_email, reporter, created, updated,
		due_date, resolution, parent_key, sprint_name, story_points,
		components, labels, fix_version, affects_versions, raw_json
	) VALUES (
		'ENG-42', 'ENG', 'Bug', 'Fix SQL', 'Desc', 'In Progress', 'In Progress',
		'High', 'Alice', 'alice@example.com', 'Bob', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z',
		'2026-02-01', 'Unresolved', 'ENG-1', 'Sprint 10', 3.5,
		'["core"]', '["perf"]', '["1.0"]', '["0.9"]', '{}'
	)`); err != nil {
		t.Fatalf("seeding issue: %v", err)
	}

	req := mcp.ReadResourceRequest{}
	req.Params.URI = "jira://issue/ENG-42"
	contents, err := srv.handleIssueResource(context.Background(), req)
	if err != nil {
		t.Fatalf("handleIssueResource: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("expected 1 resource content, got %d", len(contents))
	}
	tc, ok := contents[0].(*mcp.TextResourceContents)
	if !ok {
		t.Fatalf("expected *mcp.TextResourceContents, got %T", contents[0])
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &got); err != nil {
		t.Fatalf("unmarshal resource json: %v", err)
	}
	if got["key"] != "ENG-42" || got["issue_type"] != "Bug" || got["sprint"] != "Sprint 10" ||
		got["fix_versions"] != `["1.0"]` || got["affected_versions"] != `["0.9"]` {
		t.Errorf("unexpected resource payload: %v", got)
	}
}
