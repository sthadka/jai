package mcp

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sthadka/jai/internal/output"
)

// registerSchemaTools registers all schema toolset tools with the MCP server.
func registerSchemaTools(s *Server, srv *server.MCPServer) {
	if !s.toolsets.IsEnabled("schema") {
		return
	}

	truePtr := true

	// jai_schema - Discover database schema, column values, templates, or snippets
	srv.AddTool(mcp.Tool{
		Name:        "jai_schema",
		Description: "Discover database schema, column values, templates, or snippets. Modes: 'db' (returns core Jira columns by default. Use tier=custom for custom fields, tier=all for everything. Use filter parameter to search columns by name), 'values' (distinct values, default limit 20), 'templates' (issue creation templates), 'snippets' (reusable SQL fragments), 'commands' (CLI command catalog).",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"mode": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"db", "values", "templates", "snippets", "commands"},
					"description": "What to discover. 'db' = column schema, 'values' = distinct values for a column, 'templates' = issue templates, 'snippets' = SQL fragments, 'commands' = CLI command catalog",
				},
				"column": map[string]interface{}{
					"type":        "string",
					"description": "Column name (required when mode='values')",
				},
				"tier": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"core", "custom", "all"},
					"description": "Schema tier (mode='db' only). 'core' (default) = ~20 standard Jira columns, 'custom' = custom fields only with population stats, 'all' = everything",
				},
				"filter": map[string]interface{}{
					"type":        "string",
					"description": "Filter columns by partial name match (mode='db' only)",
				},
			},
			Required: []string{"mode"},
		},
		Annotations: mcp.ToolAnnotation{
			Title:        "Schema Discovery",
			ReadOnlyHint: &truePtr,
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleSchema(s, ctx, request)
	})

	// jai_fields - List all Jira fields with their mappings
	srv.AddTool(mcp.Tool{
		Name:        "jai_fields",
		Description: "Search and discover field mappings. Use filter parameter to search by name.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"filter": map[string]interface{}{
					"type":        "string",
					"description": "Filter fields by partial name match",
				},
				"stats": map[string]interface{}{
					"type":        "boolean",
					"description": "Include population counts per field",
					"default":     false,
				},
			},
		},
		Annotations: mcp.ToolAnnotation{
			Title:        "Field Metadata",
			ReadOnlyHint: &truePtr,
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleFields(s, ctx, request)
	})
}

// handleSchema handles the jai_schema tool call.
func handleSchema(s *Server, ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	mode := request.GetString("mode", "")
	if mode == "" {
		return mcp.NewToolResultError("mode parameter is required"), nil
	}

	switch mode {
	case "db":
		tier := request.GetString("tier", "core")
		filter := request.GetString("filter", "")
		return handleSchemaDB(s, ctx, tier, filter)
	case "values":
		column := request.GetString("column", "")
		if column == "" {
			return mcp.NewToolResultError("column parameter is required when mode='values'"), nil
		}
		return handleSchemaValues(s, ctx, column)
	case "templates":
		return handleSchemaTemplates(s, ctx)
	case "snippets":
		return handleSchemaSnippets(s, ctx)
	case "commands":
		return handleSchemaCommands(s, ctx)
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown mode: %s", mode)), nil
	}
}

// handleSchemaDB returns the database schema (issues table columns).
func handleSchemaDB(s *Server, ctx context.Context, tier, filter string) (*mcp.CallToolResult, error) {
	// Core standard Jira columns (tier=core default)
	coreColumns := map[string]bool{
		"key": true, "summary": true, "status": true, "status_category": true,
		"issue_type": true, "priority": true, "assignee": true, "assignee_email": true,
		"reporter": true, "reporter_email": true, "labels": true, "components": true,
		"fix_versions": true, "parent_key": true, "project": true, "created": true,
		"updated": true, "resolved": true, "resolution": true, "story_points": true,
		"sprint_name": true, "sprint_id": true, "description": true,
	}

	type col struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		Custom    bool   `json:"custom,omitempty"`
		JiraName  string `json:"jira_name,omitempty"`
		Populated int    `json:"populated,omitempty"`
		Total     int    `json:"total,omitempty"`
	}

	// Query table_info for issues table
	rows, err := s.db.Query("PRAGMA table_info(issues)")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer rows.Close()

	// Gather all columns
	allColumns := []col{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			continue
		}
		// Always skip internal columns
		if excludedColumns[name] {
			continue
		}
		allColumns = append(allColumns, col{Name: name, Type: strings.ToLower(colType)})
	}
	rows.Close()

	// Enrich columns with jira_name and custom flag from field_map
	metaRows, err := s.db.Query("SELECT name, jira_name, is_custom FROM field_map WHERE is_column = 1")
	if err == nil {
		type fieldMeta struct {
			jiraName string
			isCustom bool
		}
		metas := map[string]fieldMeta{}
		for metaRows.Next() {
			var n, jn string
			var custom bool
			if err := metaRows.Scan(&n, &jn, &custom); err == nil {
				metas[n] = fieldMeta{jiraName: jn, isCustom: custom}
			}
		}
		metaRows.Close()
		for i, c := range allColumns {
			if fm, ok := metas[c.Name]; ok {
				allColumns[i].Custom = fm.isCustom
				allColumns[i].JiraName = fm.jiraName
			}
		}
	}

	// Apply tier filtering
	var columns []col
	switch tier {
	case "core":
		// Return only core standard Jira columns
		for _, c := range allColumns {
			if coreColumns[c.Name] {
				columns = append(columns, c)
			}
		}
	case "custom":
		// Return only custom fields with population stats
		for _, c := range allColumns {
			if c.Custom {
				columns = append(columns, c)
			}
		}
		// Add population stats for custom fields
		if len(columns) > 0 {
			var totalCount int
			countRow := s.db.QueryRow("SELECT COUNT(*) FROM issues")
			countRow.Scan(&totalCount)

			for i, c := range columns {
				var nonNullCount int
				sql := fmt.Sprintf(`SELECT COUNT(*) FROM issues WHERE "%s" IS NOT NULL AND "%s" != ''`, c.Name, c.Name)
				row := s.db.QueryRow(sql)
				if err := row.Scan(&nonNullCount); err == nil {
					columns[i].Populated = nonNullCount
					columns[i].Total = totalCount
				}
			}
		}
	case "all":
		// Return everything
		columns = allColumns
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown tier: %s (valid: core, custom, all)", tier)), nil
	}

	// Apply filter if specified
	if filter != "" {
		filterLower := strings.ToLower(filter)
		var filtered []col
		for _, c := range columns {
			if strings.Contains(strings.ToLower(c.Name), filterLower) ||
				strings.Contains(strings.ToLower(c.JiraName), filterLower) {
				filtered = append(filtered, c)
			}
		}
		columns = filtered
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"table":   "issues",
		"tier":    tier,
		"columns": columns,
		"count":   len(columns),
		"hint":    "Use 'jai_schema' with mode='values' to see distinct values for any column",
	})))), nil
}

// safeColumnRe matches valid SQLite column names.
var safeColumnRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// handleSchemaValues returns distinct values for a column.
func handleSchemaValues(s *Server, ctx context.Context, column string) (*mcp.CallToolResult, error) {
	if !safeColumnRe.MatchString(column) {
		return mcp.NewToolResultError("column name contains invalid characters"), nil
	}

	// Query distinct values with frequency counts
	sql := fmt.Sprintf(`
		SELECT "%s", COUNT(*) as count
		FROM issues
		WHERE "%s" IS NOT NULL AND "%s" != ''
		GROUP BY "%s"
		ORDER BY count DESC
		LIMIT 20`, column, column, column, column)

	rows, err := s.db.Query(sql)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer rows.Close()

	type valueCount struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	}

	var values []valueCount
	for rows.Next() {
		var v interface{}
		var count int
		if err := rows.Scan(&v, &count); err == nil {
			values = append(values, valueCount{
				Value: fmt.Sprintf("%v", v),
				Count: count,
			})
		}
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"column": column,
		"values": values,
		"count":  len(values),
	})))), nil
}

// handleSchemaTemplates returns available issue templates from config.
func handleSchemaTemplates(s *Server, ctx context.Context) (*mcp.CallToolResult, error) {
	if s.cfg == nil || len(s.cfg.Templates) == 0 {
		return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
			"templates": []string{},
			"count":     0,
		})))), nil
	}

	names := make([]string, 0, len(s.cfg.Templates))
	for name := range s.cfg.Templates {
		names = append(names, name)
	}
	sort.Strings(names)

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"templates": names,
		"count":     len(names),
		"hint":      "Use template name with 'jai_create' tool to load a template as the description",
	})))), nil
}

// handleSchemaSnippets returns available SQL snippets from config.
func handleSchemaSnippets(s *Server, ctx context.Context) (*mcp.CallToolResult, error) {
	if s.cfg == nil || len(s.cfg.Snippets) == 0 {
		return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
			"snippets": []interface{}{},
			"count":    0,
		})))), nil
	}

	names := make([]string, 0, len(s.cfg.Snippets))
	for name := range s.cfg.Snippets {
		names = append(names, name)
	}
	sort.Strings(names)

	type snippetInfo struct {
		Name string `json:"name"`
		Raw  string `json:"raw"`
	}

	snippets := make([]snippetInfo, 0, len(names))
	for _, name := range names {
		raw := s.cfg.Snippets[name]
		snippets = append(snippets, snippetInfo{
			Name: name,
			Raw:  raw,
		})
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"snippets": snippets,
		"count":    len(snippets),
		"hint":     "Use {{snippet_name}} in any SQL query to expand a snippet",
	})))), nil
}

// CommandSchema describes a command's parameters and flags.
type CommandSchema struct {
	Name        string                 `json:"command"`
	Description string                 `json:"description"`
	Params      map[string]ParamSchema `json:"params,omitempty"`
	Flags       map[string]ParamSchema `json:"flags,omitempty"`
}

// ParamSchema describes a single parameter.
type ParamSchema struct {
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}

// commandSchemas is a hardcoded catalog of all jai commands.
var commandSchemas = []CommandSchema{
	{
		Name:        "get",
		Description: "Fetch a single issue from the local database",
		Params: map[string]ParamSchema{
			"key": {Type: "string", Required: true, Description: "Issue key (e.g. ROX-123)"},
		},
		Flags: map[string]ParamSchema{
			"json":   {Type: "bool", Description: "Output as JSON"},
			"fields": {Type: "string", Description: "Comma-separated field names to include"},
		},
	},
	{
		Name:        "query",
		Description: "Execute a SQL query against the local database",
		Params: map[string]ParamSchema{
			"sql": {Type: "string", Required: true, Description: "SQL query to execute"},
		},
		Flags: map[string]ParamSchema{
			"json":   {Type: "bool", Description: "Output as JSON"},
			"fields": {Type: "string", Description: "Filter output columns"},
		},
	},
	{
		Name:        "search",
		Description: "Full-text search across issues",
		Params: map[string]ParamSchema{
			"text": {Type: "string", Required: true, Description: "Search text"},
		},
		Flags: map[string]ParamSchema{
			"json":   {Type: "bool", Description: "Output as JSON"},
			"fields": {Type: "string", Description: "Filter output columns"},
			"limit":  {Type: "int", Description: "Max results (default: 20)"},
		},
	},
	{
		Name:        "sync",
		Description: "Sync Jira issues to local database",
		Flags: map[string]ParamSchema{
			"full": {Type: "bool", Description: "Full resync (delete + re-fetch)"},
		},
	},
	{
		Name:        "set",
		Description: "Set a field value on a Jira issue",
		Params: map[string]ParamSchema{
			"key":   {Type: "string", Required: true, Description: "Issue key"},
			"field": {Type: "string", Required: true, Description: "Field name"},
			"value": {Type: "string", Required: true, Description: "New value"},
		},
		Flags: map[string]ParamSchema{
			"queue": {Type: "bool", Description: "Queue change locally instead of writing through to Jira"},
		},
	},
	{
		Name:        "comment",
		Description: "Add a comment to a Jira issue",
		Params: map[string]ParamSchema{
			"key":  {Type: "string", Required: true, Description: "Issue key"},
			"text": {Type: "string", Required: true, Description: "Comment text"},
		},
		Flags: map[string]ParamSchema{
			"queue": {Type: "bool", Description: "Queue change locally instead of writing through to Jira"},
		},
	},
	{
		Name:        "push",
		Description: "Push pending changes to Jira",
	},
	{
		Name:        "fields",
		Description: "List available fields and their Jira mappings",
		Flags: map[string]ParamSchema{
			"json":    {Type: "bool", Description: "Output as JSON"},
			"filter":  {Type: "string", Description: "Filter by name pattern"},
			"stats":   {Type: "bool", Description: "Show population counts per field"},
			"project": {Type: "string", Description: "Scope --stats to a specific project"},
		},
	},
	{
		Name:        "status",
		Description: "Show sync and queue status",
		Flags: map[string]ParamSchema{
			"json": {Type: "bool", Description: "Output as JSON"},
		},
	},
}

// handleSchemaCommands returns the command catalog.
func handleSchemaCommands(s *Server, ctx context.Context) (*mcp.CallToolResult, error) {
	list := make([]map[string]string, len(commandSchemas))
	for i, cmd := range commandSchemas {
		list[i] = map[string]string{
			"command":     cmd.Name,
			"description": cmd.Description,
		}
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"commands": list,
		"count":    len(list),
	})))), nil
}

// handleFields handles the jai_fields tool call.
func handleFields(s *Server, ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Get all field mappings
	mappings, err := s.db.AllFieldMappings()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Apply filter if specified
	filterStr := request.GetString("filter", "")
	if filterStr != "" {
		filter := strings.ToLower(filterStr)
		var filtered = mappings[:0]
		for _, m := range mappings {
			if strings.Contains(strings.ToLower(m.Name), filter) ||
				strings.Contains(strings.ToLower(m.JiraName), filter) {
				filtered = append(filtered, m)
			}
		}
		mappings = filtered
	}

	// Optionally gather population stats
	includeStats := request.GetBool("stats", false)

	var stats map[string]*struct {
		NonNull int
		Total   int
		Sample  string
	}

	if includeStats {
		var colNames []string
		for _, m := range mappings {
			if m.IsColumn {
				colNames = append(colNames, m.Name)
			}
		}
		// Get population stats (simplified - project filter not implemented)
		dbStats, _ := s.db.FieldPopulationStats(colNames, "")
		stats = make(map[string]*struct {
			NonNull int
			Total   int
			Sample  string
		})
		for name, st := range dbStats {
			stats[name] = &struct {
				NonNull int
				Total   int
				Sample  string
			}{
				NonNull: st.NonNull,
				Total:   st.Total,
				Sample:  st.Sample,
			}
		}
	}

	// Build JSON response
	fields := make([]map[string]interface{}, len(mappings))
	for i, m := range mappings {
		f := map[string]interface{}{
			"name":       m.Name,
			"jira_id":    m.JiraID,
			"jira_name":  m.JiraName,
			"type":       m.Type,
			"is_custom":  m.IsCustom,
			"searchable": m.Searchable,
		}
		if s, ok := stats[m.Name]; ok {
			f["populated"] = s.NonNull
			f["total"] = s.Total
			if s.Sample != "" {
				f["sample"] = s.Sample
			}
		}
		fields[i] = f
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"fields": fields,
		"count":  len(fields),
	})))), nil
}
