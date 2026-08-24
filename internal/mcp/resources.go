package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerResources registers all MCP resource URIs and templates.
func (s *Server) registerResources(srv *server.MCPServer) {
	// Issue resource - subscribable
	srv.AddResource(
		mcp.Resource{
			URI:         "jira://issue/{key}",
			Name:        "Jira Issue",
			Description: "Get full issue data as JSON. Subscribable - updates after sync.",
			MIMEType:    "application/json",
		},
		s.handleIssueResource,
	)

	// Database schema resource
	srv.AddResource(
		mcp.Resource{
			URI:         "jira://schema/db",
			Name:        "Database Schema",
			Description: "Get database schema with field mappings",
			MIMEType:    "application/json",
		},
		s.handleSchemaResource,
	)

	// Schema values resource (template)
	srv.AddResource(
		mcp.Resource{
			URI:         "jira://schema/values/{column}",
			Name:        "Column Values",
			Description: "Get distinct values and counts for a column",
			MIMEType:    "application/json",
		},
		s.handleSchemaValuesResource,
	)

	// View resource (template) - subscribable
	srv.AddResource(
		mcp.Resource{
			URI:         "jira://view/{name}",
			Name:        "Named View",
			Description: "Get named view results. Subscribable - updates after sync.",
			MIMEType:    "application/json",
		},
		s.handleViewResource,
	)

	// Views list resource
	srv.AddResource(
		mcp.Resource{
			URI:         "jira://views",
			Name:        "Available Views",
			Description: "List all available views",
			MIMEType:    "application/json",
		},
		s.handleViewsListResource,
	)

	// Status resource - subscribable
	srv.AddResource(
		mcp.Resource{
			URI:         "jira://status",
			Name:        "System Status",
			Description: "Get system status (auth, sync time, DB stats). Subscribable - updates after sync.",
			MIMEType:    "application/json",
		},
		s.handleStatusResource,
	)

	// Fields resource
	srv.AddResource(
		mcp.Resource{
			URI:         "jira://fields",
			Name:        "Field Mappings",
			Description: "Get all field metadata with mappings",
			MIMEType:    "application/json",
		},
		s.handleFieldsResource,
	)

	// Query resource (template)
	srv.AddResource(
		mcp.Resource{
			URI:         "jira://query/{sql}",
			Name:        "SQL Query",
			Description: "Execute SQL query and return results (SQL is URL-encoded in path)",
			MIMEType:    "application/json",
		},
		s.handleQueryResource,
	)
}

// handleSchemaResource returns database schema with field mappings.
func (s *Server) handleSchemaResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	// Get table schema from PRAGMA
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(issues)")
	if err != nil {
		return nil, fmt.Errorf("get table info: %w", err)
	}
	defer rows.Close()

	var columns []map[string]interface{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			continue
		}
		columns = append(columns, map[string]interface{}{
			"name":     name,
			"type":     typ,
			"not_null": notNull == 1,
			"pk":       pk == 1,
		})
	}

	// Get field mappings
	mappings, err := s.db.AllFieldMappings()
	if err != nil {
		return nil, fmt.Errorf("get field mappings: %w", err)
	}

	var fields []map[string]interface{}
	for _, m := range mappings {
		fields = append(fields, map[string]interface{}{
			"name":       m.Name,
			"jira_id":    m.JiraID,
			"jira_name":  m.JiraName,
			"type":       m.Type,
			"is_custom":  m.IsCustom,
			"searchable": m.Searchable,
		})
	}

	schema := map[string]interface{}{
		"columns": columns,
		"fields":  fields,
	}

	data, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}

	return []mcp.ResourceContents{
		&mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// handleSchemaValuesResource returns distinct values and counts for a column.
func (s *Server) handleSchemaValuesResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	column := extractParam(req.Params.URI, "jira://schema/values/")
	if column == "" {
		return nil, fmt.Errorf("invalid schema values URI: %s", req.Params.URI)
	}

	// Validate column name to prevent SQL injection
	if !isValidColumnName(column) {
		return nil, fmt.Errorf("invalid column name: %s", column)
	}

	query := fmt.Sprintf(`
		SELECT %s as value, COUNT(*) as count
		FROM issues
		WHERE %s IS NOT NULL AND %s != ''
		GROUP BY %s
		ORDER BY count DESC, value ASC
		LIMIT 100`, column, column, column, column)

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query values: %w", err)
	}
	defer rows.Close()

	var values []map[string]interface{}
	for rows.Next() {
		var value sql.NullString
		var count int
		if err := rows.Scan(&value, &count); err != nil {
			continue
		}
		values = append(values, map[string]interface{}{
			"value": nullStringToInterface(value),
			"count": count,
		})
	}

	result := map[string]interface{}{
		"column": column,
		"values": values,
		"total":  len(values),
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal values: %w", err)
	}

	return []mcp.ResourceContents{
		&mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// handleViewResource returns named view results.
func (s *Server) handleViewResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	name := extractParam(req.Params.URI, "jira://view/")
	if name == "" {
		return nil, fmt.Errorf("invalid view URI: %s", req.Params.URI)
	}

	view := s.cfg.ViewByName(name)
	if view == nil {
		return nil, fmt.Errorf("view not found: %s", name)
	}

	results, err := s.query.Execute(view.Query)
	if err != nil {
		return nil, fmt.Errorf("execute view query: %w", err)
	}

	result := map[string]interface{}{
		"name":    view.Name,
		"title":   view.Title,
		"columns": results.Columns,
		"rows":    results.Rows,
		"count":   len(results.Rows),
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal view: %w", err)
	}

	return []mcp.ResourceContents{
		&mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// handleViewsListResource returns list of all available views.
func (s *Server) handleViewsListResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	views := s.cfg.Views
	var viewList []map[string]string
	for _, v := range views {
		viewList = append(viewList, map[string]string{
			"name":  v.Name,
			"title": v.Title,
		})
	}

	result := map[string]interface{}{
		"views": viewList,
		"count": len(viewList),
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal views: %w", err)
	}

	return []mcp.ResourceContents{
		&mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// handleStatusResource returns system status.
func (s *Server) handleStatusResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	// Auth check
	me, authErr := s.jira.MySelf(ctx)

	metas, err := s.db.AllSyncMeta()
	if err != nil {
		return nil, fmt.Errorf("get sync meta: %w", err)
	}

	totalIssues, _ := s.db.TotalIssueCount()
	countByProject, _ := s.db.IssueCountByProject()
	pendingCount, _ := s.db.CountPendingChanges()

	auth := map[string]interface{}{"ok": authErr == nil}
	if authErr != nil {
		auth["error"] = authErr.Error()
	} else {
		auth["user"] = me.DisplayName
		auth["email"] = me.EmailAddress
	}

	sources := make([]map[string]interface{}, len(metas))
	for i, m := range metas {
		sources[i] = map[string]interface{}{
			"source":             m.Project,
			"last_sync_time":     nullStringToInterface(m.LastSyncTime),
			"last_sync_duration": nullFloat64ToInterface(m.LastSyncDuration),
			"last_sync_error":    nullStringToInterface(m.LastSyncError),
		}
	}

	status := map[string]interface{}{
		"auth":              auth,
		"sources":           sources,
		"total_issues":      totalIssues,
		"issues_by_project": countByProject,
		"pending_changes":   pendingCount,
	}

	data, err := json.Marshal(status)
	if err != nil {
		return nil, fmt.Errorf("marshal status: %w", err)
	}

	return []mcp.ResourceContents{
		&mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// handleFieldsResource returns all field metadata with mappings.
func (s *Server) handleFieldsResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	mappings, err := s.db.AllFieldMappings()
	if err != nil {
		return nil, fmt.Errorf("get field mappings: %w", err)
	}

	var fields []map[string]interface{}
	for _, m := range mappings {
		fields = append(fields, map[string]interface{}{
			"name":       m.Name,
			"jira_id":    m.JiraID,
			"jira_name":  m.JiraName,
			"type":       m.Type,
			"is_custom":  m.IsCustom,
			"searchable": m.Searchable,
		})
	}

	result := map[string]interface{}{
		"fields": fields,
		"count":  len(fields),
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal fields: %w", err)
	}

	return []mcp.ResourceContents{
		&mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// handleQueryResource executes SQL query and returns results.
func (s *Server) handleQueryResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	encodedSQL := extractParam(req.Params.URI, "jira://query/")
	if encodedSQL == "" {
		return nil, fmt.Errorf("invalid query URI: %s", req.Params.URI)
	}

	sql, err := url.QueryUnescape(encodedSQL)
	if err != nil {
		return nil, fmt.Errorf("decode SQL: %w", err)
	}

	results, err := s.query.Execute(sql)
	if err != nil {
		return nil, fmt.Errorf("execute query: %w", err)
	}

	result := map[string]interface{}{
		"columns": results.Columns,
		"rows":    results.Rows,
		"count":   len(results.Rows),
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal query result: %w", err)
	}

	return []mcp.ResourceContents{
		&mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// handleIssueResource returns full issue data as JSON.
func (s *Server) handleIssueResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	key := extractParam(req.Params.URI, "jira://issue/")
	if key == "" {
		return nil, fmt.Errorf("invalid issue URI: %s", req.Params.URI)
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT key, summary, description, status, priority, assignee, assignee_email,
		       reporter, issue_type, project, created, updated, resolved, due_date,
		       status_category, resolution, parent_key, sprint, story_points,
		       components, labels, fix_versions, affected_versions, raw_json
		FROM issues WHERE key = ?`, key)

	var k, summary, status, priority, issueType, project, created, updated, statusCategory sql.NullString
	var description, assignee, assigneeEmail, reporter, resolved, dueDate sql.NullString
	var resolution, parentKey, sprint, components, labels, fixVersions, affectedVersions sql.NullString
	var storyPoints sql.NullFloat64
	var rawJSON sql.NullString

	err := row.Scan(
		&k, &summary, &description, &status, &priority, &assignee, &assigneeEmail,
		&reporter, &issueType, &project, &created, &updated, &resolved, &dueDate,
		&statusCategory, &resolution, &parentKey, &sprint, &storyPoints,
		&components, &labels, &fixVersions, &affectedVersions, &rawJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("issue not found: %s: %w", key, err)
	}

	result := make(map[string]interface{})
	result["key"] = nullStringToInterface(k)
	result["summary"] = nullStringToInterface(summary)
	result["description"] = nullStringToInterface(description)
	result["status"] = nullStringToInterface(status)
	result["priority"] = nullStringToInterface(priority)
	result["assignee"] = nullStringToInterface(assignee)
	result["assignee_email"] = nullStringToInterface(assigneeEmail)
	result["reporter"] = nullStringToInterface(reporter)
	result["issue_type"] = nullStringToInterface(issueType)
	result["project"] = nullStringToInterface(project)
	result["created"] = nullStringToInterface(created)
	result["updated"] = nullStringToInterface(updated)
	result["resolved"] = nullStringToInterface(resolved)
	result["due_date"] = nullStringToInterface(dueDate)
	result["status_category"] = nullStringToInterface(statusCategory)
	result["resolution"] = nullStringToInterface(resolution)
	result["parent_key"] = nullStringToInterface(parentKey)
	result["sprint"] = nullStringToInterface(sprint)
	if storyPoints.Valid {
		result["story_points"] = storyPoints.Float64
	}
	result["components"] = nullStringToInterface(components)
	result["labels"] = nullStringToInterface(labels)
	result["fix_versions"] = nullStringToInterface(fixVersions)
	result["affected_versions"] = nullStringToInterface(affectedVersions)

	// Apply filters: remove excluded columns and null values
	result = filterExcluded(result)
	result = stripNulls(result)

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal issue: %w", err)
	}

	return []mcp.ResourceContents{
		&mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// extractParam extracts the parameter from a URI by removing the prefix.
func extractParam(uri, prefix string) string {
	if !strings.HasPrefix(uri, prefix) {
		return ""
	}
	return strings.TrimPrefix(uri, prefix)
}

// isValidColumnName checks if a column name is valid (alphanumeric + underscore).
func isValidColumnName(name string) bool {
	if name == "" {
		return false
	}
	for _, ch := range name {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_') {
			return false
		}
	}
	return true
}

// nullStringToInterface converts sql.NullString to interface{} (nil if not valid).
func nullStringToInterface(ns sql.NullString) interface{} {
	if ns.Valid {
		return ns.String
	}
	return nil
}

// nullFloat64ToInterface converts sql.NullFloat64 to interface{} (nil if not valid).
func nullFloat64ToInterface(nf sql.NullFloat64) interface{} {
	if nf.Valid {
		return nf.Float64
	}
	return nil
}
