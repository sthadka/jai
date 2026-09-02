package mcp

import (
	"context"
	"encoding/json"
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

	// jai_query - Execute SQL against local Jira SQLite database or JQL against live Jira API
	srv.AddTool(mcp.Tool{
		Name:        "jai_query",
		Description: "Execute SQL against local Jira database, or JQL against live Jira API. Use sql for local queries (fast, free), jql for live queries (slower, for non-synced projects). Default limit 20.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"sql": map[string]interface{}{
					"type":        "string",
					"description": "SQL SELECT or WITH statement. Only read queries allowed. Mutually exclusive with jql.",
				},
				"jql": map[string]interface{}{
					"type":        "string",
					"description": "JQL query string to execute against live Jira API. Mutually exclusive with sql.",
				},
				"fields": map[string]interface{}{
					"type":        "string",
					"description": "Comma-separated column filter applied to output (optional, further limits output beyond SELECT clause)",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Max rows to return. Default 20. Use to prevent accidentally large results.",
					"default":     20,
				},
			},
		},
		Annotations: mcp.ToolAnnotation{
			Title:        "SQL/JQL Query",
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
					"description": "Fields to include (default: key, summary, status, priority, assignee, type, etc). Use 'all' for every column including custom fields.",
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

// handleQuery executes a SQL query against the local database or a JQL query against live Jira API.
func handleQuery(s *Server, ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sql := request.GetString("sql", "")
	jql := request.GetString("jql", "")

	// Ensure one and only one query type is provided
	if sql == "" && jql == "" {
		return mcp.NewToolResultError("either sql or jql parameter is required"), nil
	}
	if sql != "" && jql != "" {
		return mcp.NewToolResultError("sql and jql parameters are mutually exclusive"), nil
	}

	// Route to appropriate handler
	if jql != "" {
		return handleJQLQuery(s, ctx, request, jql)
	}
	return handleSQLQuery(s, ctx, request, sql)
}

// handleSQLQuery executes a SQL query against the local database.
func handleSQLQuery(s *Server, ctx context.Context, request mcp.CallToolRequest, sql string) (*mcp.CallToolResult, error) {
	// Enforce read-only queries
	sqlUpper := strings.ToUpper(strings.TrimSpace(sql))
	if !strings.HasPrefix(sqlUpper, "SELECT") && !strings.HasPrefix(sqlUpper, "WITH") {
		return mcp.NewToolResultError("only SELECT and WITH queries are allowed"), nil
	}

	// Apply limit if specified
	limit := request.GetInt("limit", 20)

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

// handleJQLQuery executes a JQL query against the live Jira API.
func handleJQLQuery(s *Server, ctx context.Context, request mcp.CallToolRequest, jql string) (*mcp.CallToolResult, error) {
	// Default columns for JQL queries (matches CLI implementation)
	defaultCols := []string{"key", "summary", "status", "priority", "assignee", "updated"}
	cols := defaultCols

	// Override with fields parameter if provided
	fieldsStr := request.GetString("fields", "")
	if fieldsStr != "" {
		cols = output.ParseFields(fieldsStr)
	}

	// Determine which API fields to request based on requested columns
	apiFields := jqlColumnsToAPIFields(cols)

	// Apply limit
	limit := request.GetInt("limit", 20)
	count := 0

	// Execute JQL search via Jira API
	var rows [][]interface{}
	for page, err := range s.jira.SearchAll(ctx, jql, apiFields) {
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("JQL query failed: %s", err.Error())), nil
		}

		for _, issue := range page {
			if count >= limit {
				goto done
			}
			row, err := jqlIssueToRow(issue, cols)
			if err != nil {
				// Skip issues that fail to parse
				continue
			}
			rows = append(rows, row)
			count++
		}
	}
done:

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

		// Convert API issue to map using proper field extraction
		data = issueFieldsToMap(issue.Key, issue.Fields)
		data["_source"] = "api"
	} else {
		// Convert DB row to map
		data = make(map[string]interface{}, len(results.Columns))
		for i, col := range results.Columns {
			data[col] = results.Rows[0][i]
		}
	}

	// Apply response filters (field selection, null stripping, excluded columns)
	fieldsStr := request.GetString("fields", "")
	data = applyResponseFilters(data, fieldsStr)

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

// jqlColumnsToAPIFields maps requested output columns to Jira API field IDs.
// The 'key' field is always included by the API, so we exclude it from the fields parameter.
func jqlColumnsToAPIFields(cols []string) []string {
	fieldSet := make(map[string]bool)

	for _, col := range cols {
		switch col {
		case "key":
			// Always returned by API, no need to request
			continue
		case "summary", "status", "priority", "assignee", "reporter", "created", "updated", "labels", "parent":
			fieldSet[col] = true
		case "type", "issuetype":
			fieldSet["issuetype"] = true
		case "project":
			fieldSet["project"] = true
		case "resolved", "resolution_date":
			fieldSet["resolutiondate"] = true
		}
	}

	// Convert set to slice
	fields := make([]string, 0, len(fieldSet))
	for field := range fieldSet {
		fields = append(fields, field)
	}

	// If no specific fields requested, use defaults
	if len(fields) == 0 {
		return []string{"summary", "status", "priority", "assignee", "updated"}
	}

	return fields
}

// jqlIssueToRow extracts the requested columns from a live Jira issue.
// This mirrors the CLI's jqlIssueToRow implementation.
func jqlIssueToRow(issue interface{}, cols []string) ([]interface{}, error) {
	// Type assertion to get the issue struct
	type jiraIssue struct {
		Key    string          `json:"key"`
		Fields json.RawMessage `json:"fields"`
	}

	// Marshal and unmarshal to convert interface{} to our expected type
	data, err := json.Marshal(issue)
	if err != nil {
		return nil, err
	}

	var iss jiraIssue
	if err := json.Unmarshal(data, &iss); err != nil {
		return nil, err
	}

	// Parse the fields JSON
	type issueFields struct {
		Summary        string                        `json:"summary"`
		Status         *struct{ Name string }        `json:"status"`
		Priority       *struct{ Name string }        `json:"priority"`
		Assignee       *struct{ DisplayName string } `json:"assignee"`
		Reporter       *struct{ DisplayName string } `json:"reporter"`
		IssueType      *struct{ Name string }        `json:"issuetype"`
		Project        *struct{ Key string }         `json:"project"`
		Created        string                        `json:"created"`
		Updated        string                        `json:"updated"`
		ResolutionDate string                        `json:"resolutiondate"`
		Labels         []string                      `json:"labels"`
		Parent         *struct{ Key string }         `json:"parent"`
	}

	var fields issueFields
	if err := json.Unmarshal(iss.Fields, &fields); err != nil {
		return nil, err
	}

	// Extract each requested column
	get := func(col string) interface{} {
		switch col {
		case "key":
			return iss.Key
		case "summary":
			return fields.Summary
		case "status":
			if fields.Status != nil {
				return fields.Status.Name
			}
		case "priority":
			if fields.Priority != nil {
				return fields.Priority.Name
			}
		case "assignee":
			if fields.Assignee != nil {
				return fields.Assignee.DisplayName
			}
		case "reporter":
			if fields.Reporter != nil {
				return fields.Reporter.DisplayName
			}
		case "type", "issuetype":
			if fields.IssueType != nil {
				return fields.IssueType.Name
			}
		case "project":
			if fields.Project != nil {
				return fields.Project.Key
			}
		case "created":
			return fields.Created
		case "updated":
			return fields.Updated
		case "resolved", "resolution_date":
			return fields.ResolutionDate
		case "labels":
			return strings.Join(fields.Labels, ", ")
		case "parent":
			if fields.Parent != nil {
				return fields.Parent.Key
			}
		}
		return nil
	}

	row := make([]interface{}, len(cols))
	for i, col := range cols {
		row[i] = get(col)
	}
	return row, nil
}
