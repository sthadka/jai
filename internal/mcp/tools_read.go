package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sthadka/jai/internal/output"
)

// registerReadTools registers all read toolset tools with the MCP server.
func registerReadTools(s *Server, srv *server.MCPServer) {
	if !s.toolsets.IsEnabled("read") {
		return
	}

	truePtr := true

	// jai_query - Execute SQL against local Jira SQLite database
	srv.AddTool(mcp.Tool{
		Name:        "jai_query",
		Description: "Execute SQL against local Jira SQLite database. Returns rows as JSON. The main table is `issues` with columns discoverable via jai_schema. Use template variables: {{me}}, {{today}}, {{week_ago}}, etc. TOKEN-SAVING TIP: Always specify columns in SELECT instead of using *. Use LIMIT to cap results.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"sql": map[string]interface{}{
					"type":        "string",
					"description": "SQL SELECT or WITH statement. Only read queries allowed.",
				},
				"fields": map[string]interface{}{
					"type":        "string",
					"description": "Comma-separated column filter applied to output (optional, further limits output beyond SELECT clause)",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Max rows to return. Default 100. Use to prevent accidentally large results.",
					"default":     100,
				},
			},
			Required: []string{"sql"},
		},
		Annotations: mcp.ToolAnnotation{
			Title:        "SQL Query",
			ReadOnlyHint: &truePtr,
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleQuery(s, ctx, request)
	})

	// jai_get - Get a single Jira issue by key
	srv.AddTool(mcp.Tool{
		Name:        "jai_get",
		Description: "Get a single Jira issue by key from local DB. Falls back to Jira API if not found locally. For multiple issues, prefer jai_query with IN clause.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"key": map[string]interface{}{
					"type":        "string",
					"description": "Jira issue key (e.g., PROJ-123)",
					"pattern":     "^[A-Z][A-Z0-9]+-[0-9]+$",
				},
				"fields": map[string]interface{}{
					"type":        "string",
					"description": "Comma-separated fields to include. Omit for all fields.",
				},
				"comments": map[string]interface{}{
					"type":        "boolean",
					"description": "Include issue comments. Default false.",
					"default":     false,
				},
			},
			Required: []string{"key"},
		},
		Annotations: mcp.ToolAnnotation{
			Title:        "Get Issue",
			ReadOnlyHint: &truePtr,
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleGet(s, ctx, request)
	})

	// jai_search - Full-text search across issues
	srv.AddTool(mcp.Tool{
		Name:        "jai_search",
		Description: "Full-text search across issue summary, description, comments, and labels using FTS5. Faster and more token-efficient than LIKE queries. Returns ranked results with highlighted matches.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"text": map[string]interface{}{
					"type":        "string",
					"description": "Search query. Supports FTS5 syntax: AND, OR, NOT, prefix*, \"exact phrase\"",
				},
				"fields": map[string]interface{}{
					"type":        "string",
					"description": "Comma-separated output fields. Default: key, summary, status, priority",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Max results. Default 20.",
					"default":     20,
				},
			},
			Required: []string{"text"},
		},
		Annotations: mcp.ToolAnnotation{
			Title:        "Full-Text Search",
			ReadOnlyHint: &truePtr,
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleSearch(s, ctx, request)
	})

	// jai_view - Execute a named view
	srv.AddTool(mcp.Tool{
		Name:        "jai_view",
		Description: "Execute a named view (pre-defined SQL query from config). Call with no name to list available views and their descriptions.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "View name. Omit to list all available views.",
				},
				"fields": map[string]interface{}{
					"type":        "string",
					"description": "Comma-separated output field filter",
				},
			},
		},
		Annotations: mcp.ToolAnnotation{
			Title:        "Run Named View",
			ReadOnlyHint: &truePtr,
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleView(s, ctx, request)
	})
}

// handleQuery executes a SQL query against the local database.
func handleQuery(s *Server, ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sql := request.GetString("sql", "")
	if sql == "" {
		return mcp.NewToolResultError("sql parameter is required"), nil
	}

	// Enforce read-only queries
	sqlUpper := strings.ToUpper(strings.TrimSpace(sql))
	if !strings.HasPrefix(sqlUpper, "SELECT") && !strings.HasPrefix(sqlUpper, "WITH") {
		return mcp.NewToolResultError("only SELECT and WITH queries are allowed"), nil
	}

	// Apply limit if specified
	limit := request.GetInt("limit", 100)

	// Apply LIMIT clause if not present
	if !strings.Contains(sqlUpper, "LIMIT") {
		sql = fmt.Sprintf("%s LIMIT %d", sql, limit)
	}

	// Execute query
	results, err := s.query.Execute(sql)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	cols, rows := results.Columns, results.Rows

	// Apply fields filter if specified
	fieldsStr := request.GetString("fields", "")
	if fieldsStr != "" {
		cols, rows = output.FilterColumns(cols, rows, output.ParseFields(fieldsStr))
	}

	return mcp.NewToolResultText(string(output.OKQuery(cols, rows, len(rows)))), nil
}

// handleGet fetches a single issue by key.
func handleGet(s *Server, ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := request.GetString("key", "")
	if key == "" {
		return mcp.NewToolResultError("key parameter is required"), nil
	}

	// Query local database first
	results, err := s.query.Execute("SELECT * FROM issues WHERE key = ?", key)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var data map[string]interface{}
	includeComments := request.GetBool("comments", false)

	// If not found locally, fallback to Jira API
	if len(results.Rows) == 0 {
		issue, apiErr := s.jira.GetIssue(ctx, key)
		if apiErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("issue %s not found in local database or Jira API", key)), nil
		}

		// Convert API issue to map
		data = make(map[string]interface{})
		data["key"] = issue.Key
		// Note: Full conversion would require parsing issue.Fields JSON
		// For now, include raw fields
		data["_source"] = "api"
	} else {
		// Convert DB row to map
		data = make(map[string]interface{}, len(results.Columns))
		for i, col := range results.Columns {
			data[col] = results.Rows[0][i]
		}
	}

	// Apply fields filter if specified
	fieldsStr := request.GetString("fields", "")
	if fieldsStr != "" {
		data = output.FilterFields(data, output.ParseFields(fieldsStr))
	}

	// Include comments if requested
	if includeComments && data["_source"] != "api" {
		comments, err := s.db.GetComments(key)
		if err == nil && len(comments) > 0 {
			commentMaps := make([]map[string]interface{}, len(comments))
			for i, c := range comments {
				commentMaps[i] = map[string]interface{}{
					"id":      c.ID,
					"author":  c.Author,
					"created": c.Created,
					"body":    c.Body,
				}
			}
			data["comments"] = commentMaps
		}
	}

	return mcp.NewToolResultText(string(output.OK(data))), nil
}

// handleSearch executes a full-text search query.
func handleSearch(s *Server, ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	text := request.GetString("text", "")
	if text == "" {
		return mcp.NewToolResultError("text parameter is required"), nil
	}

	limit := request.GetInt("limit", 20)

	// Build FTS5 search query
	sql := fmt.Sprintf(`
		SELECT i.key, i.summary, i.status, i.assignee, highlight(issues_fts, 1, '[', ']') AS match
		FROM issues_fts
		JOIN issues i ON i.key = issues_fts.key
		WHERE issues_fts MATCH ?
		ORDER BY issues_fts.rank
		LIMIT %d`, limit)

	results, err := s.query.Execute(sql, text)
	if err != nil {
		// If FTS index is out of sync, try rebuilding
		if strings.Contains(err.Error(), "fts5: missing row") {
			if rbErr := s.db.RebuildFTS(); rbErr == nil {
				results, err = s.query.Execute(sql, text)
			}
		}
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	}

	cols, rows := results.Columns, results.Rows

	// Apply fields filter if specified
	fieldsStr := request.GetString("fields", "")
	if fieldsStr != "" {
		cols, rows = output.FilterColumns(cols, rows, output.ParseFields(fieldsStr))
	}

	return mcp.NewToolResultText(string(output.OKQuery(cols, rows, len(rows)))), nil
}

// handleView executes a named view or lists available views.
func handleView(s *Server, ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := request.GetString("name", "")

	// If no name provided, list available views
	if name == "" {
		views := s.cfg.Views
		if len(views) == 0 {
			return mcp.NewToolResultText(string(output.OK(map[string]interface{}{
				"views": []interface{}{},
				"count": 0,
			}))), nil
		}

		list := make([]map[string]string, len(views))
		for i, v := range views {
			list[i] = map[string]string{
				"name":  v.Name,
				"title": v.Title,
			}
		}
		return mcp.NewToolResultText(string(output.OK(map[string]interface{}{
			"views": list,
			"count": len(list),
		}))), nil
	}

	// Find the view by name
	view := s.cfg.ViewByName(name)
	if view == nil {
		return mcp.NewToolResultError(fmt.Sprintf("unknown view: %s", name)), nil
	}

	// Execute the view query
	results, err := s.query.Execute(view.Query)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	cols, rows := results.Columns, results.Rows

	// Apply view column filter if defined
	if len(view.Columns) > 0 {
		cols, rows = output.FilterColumns(cols, rows, view.Columns)
	}

	// Apply fields override if specified
	fieldsStr := request.GetString("fields", "")
	if fieldsStr != "" {
		cols, rows = output.FilterColumns(cols, rows, output.ParseFields(fieldsStr))
	}

	return mcp.NewToolResultText(string(output.OKQuery(cols, rows, len(rows)))), nil
}
