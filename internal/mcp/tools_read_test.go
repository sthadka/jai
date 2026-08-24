//go:build fts5

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sthadka/jai/internal/db"
)

// insertTestIssue inserts a test issue into the database.
func insertTestIssue(t *testing.T, database *db.DB, key, project, summary, status string) {
	t.Helper()

	issue := &db.Issue{
		Key:     key,
		Project: project,
		Summary: summary,
		Status:  status,
		RawJSON: `{}`,
	}

	if err := database.UpsertIssue(issue, nil); err != nil {
		t.Fatalf("failed to insert test issue: %v", err)
	}
}

func TestHandleQuery(t *testing.T) {
	srv := testServer(t, false, nil)

	// Insert test data
	insertTestIssue(t, srv.db, "TEST-1", "TEST", "First issue", "Open")
	insertTestIssue(t, srv.db, "TEST-2", "TEST", "Second issue", "In Progress")
	insertTestIssue(t, srv.db, "TEST-3", "TEST", "Third issue", "Done")

	tests := []struct {
		name    string
		request map[string]interface{}
		wantErr bool
	}{
		{
			name: "simple SELECT query",
			request: map[string]interface{}{
				"sql": "SELECT key, summary FROM issues WHERE project = 'TEST'",
			},
			wantErr: false,
		},
		{
			name: "query with LIMIT",
			request: map[string]interface{}{
				"sql":   "SELECT key, summary FROM issues WHERE project = 'TEST'",
				"limit": 2,
			},
			wantErr: false,
		},
		{
			name: "query with fields filter",
			request: map[string]interface{}{
				"sql":    "SELECT key, summary, status FROM issues WHERE project = 'TEST'",
				"fields": "key,summary",
			},
			wantErr: false,
		},
		{
			name: "reject non-SELECT query",
			request: map[string]interface{}{
				"sql": "DELETE FROM issues WHERE key = 'TEST-1'",
			},
			wantErr: true,
		},
		{
			name: "reject UPDATE query",
			request: map[string]interface{}{
				"sql": "UPDATE issues SET status = 'Done' WHERE key = 'TEST-1'",
			},
			wantErr: true,
		},
		{
			name: "WITH (CTE) query allowed",
			request: map[string]interface{}{
				"sql": "WITH test_cte AS (SELECT key FROM issues WHERE project = 'TEST') SELECT * FROM test_cte",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create MCP request
			mcpReq := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: tt.request,
				},
			}

			result, err := handleQuery(srv, context.Background(), mcpReq)
			if err != nil {
				t.Fatalf("handleQuery returned error: %v", err)
			}

			// Check if result is an error
			isError := false
			if len(result.Content) > 0 {
				if textContent, ok := result.Content[0].(mcp.TextContent); ok {
					isError = strings.Contains(textContent.Text, "error") ||
						strings.Contains(textContent.Text, "only SELECT") ||
						strings.Contains(textContent.Text, "required")
				}
			}

			if isError != tt.wantErr {
				t.Errorf("handleQuery error = %v, wantErr %v", isError, tt.wantErr)
			}
		})
	}
}

func TestHandleQueryLimit(t *testing.T) {
	srv := testServer(t, false, nil)

	// Insert more test data than the default limit
	for i := 1; i <= 150; i++ {
		key := strings.Replace("TEST-000", "000", strings.TrimLeft(strings.Repeat("0", 3)+string(rune(i)), "0"), 1)
		insertTestIssue(t, srv.db, key, "TEST", "Test issue", "Open")
	}

	mcpReq := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{
				"sql":   "SELECT key FROM issues WHERE project = 'TEST'",
				"limit": 50,
			},
		},
	}

	result, err := handleQuery(srv, context.Background(), mcpReq)
	if err != nil {
		t.Fatalf("handleQuery returned error: %v", err)
	}

	// Parse result to check row count
	if len(result.Content) == 0 {
		t.Fatal("handleQuery returned no content")
	}

	textContent, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatal("result content is not TextContent")
	}

	// Parse JSON response
	var response map[string]interface{}
	if err := json.Unmarshal([]byte(textContent.Text), &response); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	// Check that count is limited
	if count, ok := response["count"].(float64); ok {
		if count > 50 {
			t.Errorf("query returned %v rows, expected max 50", count)
		}
	}
}

func TestHandleGet(t *testing.T) {
	srv := testServer(t, false, nil)

	// Insert test data
	insertTestIssue(t, srv.db, "TEST-123", "TEST", "Test issue", "Open")

	tests := []struct {
		name    string
		request map[string]interface{}
		wantErr bool
	}{
		{
			name: "get existing issue",
			request: map[string]interface{}{
				"key": "TEST-123",
			},
			wantErr: false,
		},
		{
			name: "get with fields filter",
			request: map[string]interface{}{
				"key":    "TEST-123",
				"fields": "key,summary",
			},
			wantErr: false,
		},
		{
			name:    "missing key parameter",
			request: map[string]interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mcpReq := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: tt.request,
				},
			}

			result, err := handleGet(srv, context.Background(), mcpReq)
			if err != nil {
				t.Fatalf("handleGet returned error: %v", err)
			}

			// Check if result is an error
			isError := false
			if len(result.Content) > 0 {
				if textContent, ok := result.Content[0].(mcp.TextContent); ok {
					isError = strings.Contains(textContent.Text, "error") ||
						strings.Contains(textContent.Text, "required")
				}
			}

			if isError != tt.wantErr {
				t.Errorf("handleGet error = %v, wantErr %v", isError, tt.wantErr)
			}
		})
	}
}

func TestHandleSearch(t *testing.T) {
	srv := testServer(t, false, nil)

	// Insert test data with searchable content
	insertTestIssue(t, srv.db, "TEST-1", "TEST", "Bug in authentication flow", "Open")
	insertTestIssue(t, srv.db, "TEST-2", "TEST", "Feature request for dashboard", "Open")

	// Rebuild FTS index to ensure test data is searchable
	if err := srv.db.RebuildFTS(); err != nil {
		t.Fatalf("failed to rebuild FTS index: %v", err)
	}

	tests := []struct {
		name    string
		request map[string]interface{}
		wantErr bool
	}{
		{
			name: "simple text search",
			request: map[string]interface{}{
				"text": "authentication",
			},
			wantErr: false,
		},
		{
			name: "search with limit",
			request: map[string]interface{}{
				"text":  "bug OR feature",
				"limit": 1,
			},
			wantErr: false,
		},
		{
			name: "search with fields filter",
			request: map[string]interface{}{
				"text":   "dashboard",
				"fields": "key,summary",
			},
			wantErr: false,
		},
		{
			name:    "missing text parameter",
			request: map[string]interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mcpReq := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: tt.request,
				},
			}

			result, err := handleSearch(srv, context.Background(), mcpReq)
			if err != nil {
				t.Fatalf("handleSearch returned error: %v", err)
			}

			// Check if result is an error
			isError := false
			if len(result.Content) > 0 {
				if textContent, ok := result.Content[0].(mcp.TextContent); ok {
					isError = strings.Contains(textContent.Text, "error") ||
						strings.Contains(textContent.Text, "required")
				}
			}

			if isError != tt.wantErr {
				t.Errorf("handleSearch error = %v, wantErr %v", isError, tt.wantErr)
			}
		})
	}
}

func TestHandleView(t *testing.T) {
	srv := testServer(t, false, nil)

	// Insert test data
	insertTestIssue(t, srv.db, "TEST-1", "TEST", "Test issue", "Open")

	tests := []struct {
		name    string
		request map[string]interface{}
		wantErr bool
	}{
		{
			name:    "list views when no name provided",
			request: map[string]interface{}{},
			wantErr: false,
		},
		{
			name: "unknown view name",
			request: map[string]interface{}{
				"name": "nonexistent-view",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mcpReq := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: tt.request,
				},
			}

			result, err := handleView(srv, context.Background(), mcpReq)
			if err != nil {
				t.Fatalf("handleView returned error: %v", err)
			}

			// Check if result is an error
			isError := false
			if len(result.Content) > 0 {
				if textContent, ok := result.Content[0].(mcp.TextContent); ok {
					isError = strings.Contains(textContent.Text, "error") ||
						strings.Contains(textContent.Text, "unknown view")
				}
			}

			if isError != tt.wantErr {
				t.Errorf("handleView error = %v, wantErr %v", isError, tt.wantErr)
			}
		})
	}
}
