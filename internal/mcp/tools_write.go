package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
	"github.com/sthadka/jai/internal/output"
	synce "github.com/sthadka/jai/internal/sync"
)

// registerWriteTools registers all write-related tools.
func registerWriteTools(s *Server, srv *server.MCPServer) {
	if !s.toolsets.IsEnabled("write") {
		return
	}

	falsePtr := false

	// jai_set tool
	srv.AddTool(mcp.Tool{
		Name:        "jai_set",
		Description: "Set a field value on one or more Jira issues. Supports scalar fields, array add/remove, and SQL-driven bulk operations. Writes to Jira immediately by default.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"keys": map[string]string{
					"type":        "string",
					"description": "Issue key(s), comma-separated (e.g., 'PROJ-1' or 'PROJ-1,PROJ-2,PROJ-3')",
				},
				"query": map[string]string{
					"type":        "string",
					"description": "SQL query that returns 'key' column. Alternative to 'keys' for bulk operations.",
				},
				"field": map[string]string{
					"type":        "string",
					"description": "Field name to set (e.g., 'priority', 'assignee', 'labels')",
				},
				"value": map[string]string{
					"type":        "string",
					"description": "Value to set. For arrays with add/remove operation, this is the item to add/remove.",
				},
				"operation": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"set", "add", "remove"},
					"description": "Operation type. 'set' replaces the value, 'add'/'remove' for array fields (labels, components).",
					"default":     "set",
				},
				"queue": map[string]interface{}{
					"type":        "boolean",
					"description": "Queue the change instead of writing immediately. Use jai_push to flush.",
					"default":     false,
				},
			},
			Required: []string{"field", "value"},
		},
		Annotations: mcp.ToolAnnotation{
			Title:           "Set Field",
			ReadOnlyHint:    &falsePtr,
			DestructiveHint: &falsePtr,
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.handleJaiSet(ctx, req)
	})

	// jai_comment tool
	srv.AddTool(mcp.Tool{
		Name:        "jai_comment",
		Description: "Add a comment to a Jira issue. Writes to Jira immediately by default.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"key": map[string]string{
					"type":        "string",
					"description": "Issue key (e.g., PROJ-123)",
				},
				"text": map[string]string{
					"type":        "string",
					"description": "Comment body (plain text, converted to ADF for Jira)",
				},
				"queue": map[string]interface{}{
					"type":        "boolean",
					"description": "Queue instead of writing immediately",
					"default":     false,
				},
			},
			Required: []string{"key", "text"},
		},
		Annotations: mcp.ToolAnnotation{
			Title:           "Add Comment",
			ReadOnlyHint:    &falsePtr,
			DestructiveHint: &falsePtr,
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.handleJaiComment(ctx, req)
	})

	// jai_transition tool
	srv.AddTool(mcp.Tool{
		Name:        "jai_transition",
		Description: "Transition a Jira issue to a new workflow status. Use with no 'status' to list available transitions. Status name matching is case-insensitive.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"key": map[string]string{
					"type":        "string",
					"description": "Issue key (e.g., PROJ-123)",
				},
				"status": map[string]string{
					"type":        "string",
					"description": "Target status name (case-insensitive). Omit to list available transitions.",
				},
				"queue": map[string]interface{}{
					"type":    "boolean",
					"default": false,
				},
			},
			Required: []string{"key"},
		},
		Annotations: mcp.ToolAnnotation{
			Title:           "Transition Issue",
			ReadOnlyHint:    &falsePtr,
			DestructiveHint: &falsePtr,
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.handleJaiTransition(ctx, req)
	})

	// jai_create tool
	srv.AddTool(mcp.Tool{
		Name:        "jai_create",
		Description: "Create a new Jira issue. Use jai_schema(mode='templates') to discover available templates. Use jai_schema(mode='values', column='issue_type') to see valid issue types.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"project": map[string]string{
					"type":        "string",
					"description": "Project key (e.g., PROJ)",
				},
				"type": map[string]string{
					"type":        "string",
					"description": "Issue type (e.g., Bug, Story, Task, Epic)",
				},
				"summary": map[string]string{
					"type":        "string",
					"description": "Issue summary/title",
				},
				"description": map[string]string{
					"type":        "string",
					"description": "Issue description (plain text)",
				},
				"parent": map[string]string{
					"type":        "string",
					"description": "Parent issue key for sub-tasks or stories under epics",
				},
				"priority": map[string]string{
					"type":        "string",
					"description": "Priority name (e.g., High, Medium, Low)",
				},
				"assignee": map[string]string{
					"type":        "string",
					"description": "Assignee email or display name",
				},
				"labels": map[string]interface{}{
					"type": "array",
					"items": map[string]string{
						"type": "string",
					},
					"description": "Labels to apply",
				},
				"components": map[string]interface{}{
					"type": "array",
					"items": map[string]string{
						"type": "string",
					},
					"description": "Component names",
				},
				"fix_version": map[string]string{
					"type":        "string",
					"description": "Fix version name",
				},
				"due_date": map[string]string{
					"type":        "string",
					"description": "Due date (YYYY-MM-DD)",
				},
				"template": map[string]string{
					"type":        "string",
					"description": "Description template name (from config)",
				},
				"custom_fields": map[string]interface{}{
					"type":                 "object",
					"description":          "Additional custom fields as key-value pairs",
					"additionalProperties": map[string]string{"type": "string"},
				},
			},
			Required: []string{"project", "type", "summary"},
		},
		Annotations: mcp.ToolAnnotation{
			Title:           "Create Issue",
			ReadOnlyHint:    &falsePtr,
			DestructiveHint: &falsePtr,
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.handleJaiCreate(ctx, req)
	})

	// jai_clone tool
	srv.AddTool(mcp.Tool{
		Name:        "jai_clone",
		Description: "Clone an existing Jira issue with optional field overrides. Preserves all clonable fields from the source.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"key": map[string]string{
					"type":        "string",
					"description": "Source issue key to clone",
				},
				"summary": map[string]string{
					"type":        "string",
					"description": "Override summary (default: 'Clone of <original>')",
				},
				"overrides": map[string]interface{}{
					"type":                 "object",
					"description":          "Field overrides as key-value pairs",
					"additionalProperties": map[string]string{"type": "string"},
				},
			},
			Required: []string{"key"},
		},
		Annotations: mcp.ToolAnnotation{
			Title:           "Clone Issue",
			ReadOnlyHint:    &falsePtr,
			DestructiveHint: &falsePtr,
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.handleJaiClone(ctx, req)
	})

	// jai_link tool
	srv.AddTool(mcp.Tool{
		Name:        "jai_link",
		Description: "Create a link between two issues or add a remote URL link. Use with list_types=true to see available link types.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"key": map[string]string{
					"type":        "string",
					"description": "Source issue key",
				},
				"target": map[string]string{
					"type":        "string",
					"description": "Target issue key or URL",
				},
				"type": map[string]interface{}{
					"type":        "string",
					"description": "Link type name (e.g., 'Blocks', 'Duplicate'). Default: 'Relates'",
					"default":     "Relates",
				},
				"title": map[string]string{
					"type":        "string",
					"description": "Link title (for remote URL links)",
				},
				"list_types": map[string]interface{}{
					"type":        "boolean",
					"description": "If true, return available link types instead of creating a link",
					"default":     false,
				},
			},
			Required: []string{"key"},
		},
		Annotations: mcp.ToolAnnotation{
			Title:           "Link Issues",
			ReadOnlyHint:    &falsePtr,
			DestructiveHint: &falsePtr,
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.handleJaiLink(ctx, req)
	})

	// jai_update tool (composite)
	srv.AddTool(mcp.Tool{
		Name:        "jai_update",
		Description: "Composite update: set fields, transition, and add comment in a single call. Each operation is independent — partial success is reported. Reduces agent token cost by 3-5x vs individual calls.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"key": map[string]string{
					"type":        "string",
					"description": "Issue key (e.g., PROJ-123)",
				},
				"set": map[string]interface{}{
					"type":                 "object",
					"description":          "Fields to set as key-value pairs (e.g., {\"priority\": \"High\", \"assignee\": \"alice\"})",
					"additionalProperties": map[string]string{"type": "string"},
				},
				"transition": map[string]string{
					"type":        "string",
					"description": "Target status name (case-insensitive)",
				},
				"comment": map[string]string{
					"type":        "string",
					"description": "Comment text to add",
				},
				"queue": map[string]interface{}{
					"type":    "boolean",
					"default": false,
				},
			},
			Required: []string{"key"},
		},
		Annotations: mcp.ToolAnnotation{
			Title:           "Composite Update",
			ReadOnlyHint:    &falsePtr,
			DestructiveHint: &falsePtr,
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.handleJaiUpdate(ctx, req)
	})
}

// handleJaiSet handles the jai_set tool call.
func (s *Server) handleJaiSet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.toolsets.CanWrite() {
		return mcp.NewToolResultError("write operations are disabled"), nil
	}

	keysStr := req.GetString("keys", "")
	queryStr := req.GetString("query", "")
	fieldName := req.GetString("field", "")
	value := req.GetString("value", "")
	operation := req.GetString("operation", "set")
	queue := req.GetBool("queue", false)

	// Extract keys from query or comma-separated list
	var keys []string
	if queryStr != "" {
		results, err := s.query.Execute(queryStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
		}
		keys, err = extractKeys(results.Columns, results.Rows)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(keys) == 0 {
			return mcp.NewToolResultError("query returned 0 rows"), nil
		}
	} else if keysStr != "" {
		keys = expandKeys(keysStr)
	} else {
		return mcp.NewToolResultError("either 'keys' or 'query' is required"), nil
	}

	// Resolve field via field map
	fieldMap, err := s.db.FieldMapByJiraID()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("field map error: %v", err)), nil
	}

	var jiraID string
	var fieldType string
	for id, f := range fieldMap {
		if f.Name == fieldName {
			jiraID = id
			fieldType = f.Type
			break
		}
	}
	if jiraID == "" {
		return mcp.NewToolResultError(fmt.Sprintf("unknown field: %s", fieldName)), nil
	}

	// Validate operation for field type
	if (operation == "add" || operation == "remove") && fieldType != "array" {
		return mcp.NewToolResultError(fmt.Sprintf("%s is not an array field", fieldName)), nil
	}

	if queue {
		if err := s.db.EnsurePendingChangesTable(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("database error: %v", err)), nil
		}
	}

	// Handle single vs bulk
	if len(keys) == 1 {
		result, err := s.setField(ctx, keys[0], fieldName, jiraID, value, fieldType, operation, queue)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(result)), nil
	}

	// Bulk operation
	result, err := s.setBulk(ctx, keys, fieldName, jiraID, value, fieldType, operation, queue)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(result)), nil
}

// setField sets a field on a single issue.
func (s *Server) setField(ctx context.Context, issueKey, fieldName, jiraID, value, fieldType, operation string, queue bool) ([]byte, error) {
	var payloadVal interface{} = value
	localVal := value

	if operation == "set" {
		if fieldType == "array" {
			items := parseArrayValue(value)
			wrapped := make([]interface{}, len(items))
			for i, item := range items {
				if w, ok := wrapArrayItemValue(jiraID, item); ok {
					wrapped[i] = w
				} else {
					wrapped[i] = item
				}
			}
			payloadVal = wrapped
			j, _ := json.Marshal(items)
			localVal = string(j)
		} else {
			if wrapped, ok, err := wrapScalarFieldValue(jiraID, value, s.jira, ctx); ok {
				if err != nil {
					return nil, fmt.Errorf("resolving %s: %v", fieldName, err)
				}
				payloadVal = wrapped
			}
		}
	} else {
		// add/remove operation on array field
		if w, ok := wrapArrayItemValue(jiraID, value); ok {
			payloadVal = w
		}
	}

	status := "synced"
	if queue {
		var payload []byte
		if operation == "set" {
			payload, _ = json.Marshal(map[string]interface{}{"field": jiraID, "value": payloadVal})
		} else {
			payload, _ = json.Marshal(map[string]interface{}{"field": jiraID, "op": operation, "value": payloadVal})
		}
		if err := s.db.InsertPendingChange(issueKey, "set_field", string(payload)); err != nil {
			return nil, err
		}
		status = "queued"
	} else {
		if operation == "set" {
			if err := s.jira.UpdateField(ctx, issueKey, jiraID, payloadVal); err != nil {
				return nil, fmt.Errorf("setting %s on %s: %v", fieldName, issueKey, err)
			}
		} else {
			if err := s.jira.UpdateFieldOp(ctx, issueKey, jiraID, operation, payloadVal); err != nil {
				return nil, fmt.Errorf("%s %v on %s: %v", operation, value, issueKey, err)
			}
		}
	}

	// Update local DB. Best-effort: the write to Jira above already
	// succeeded (or was queued), so a local cache-update failure is non-fatal.
	if operation == "set" {
		_, _ = s.db.Exec(
			fmt.Sprintf("UPDATE issues SET %s = ?, synced_at = datetime('now') WHERE key = ?", fieldName),
			localVal, issueKey,
		)
	} else {
		// For add/remove, update the local array
		current := s.readCurrentArray(issueKey, fieldName)
		var updated []string
		if operation == "add" {
			updated = applyArrayOps(current, []string{value}, nil)
		} else {
			updated = applyArrayOps(current, nil, []string{value})
		}
		var updatedVal string
		if len(updated) > 0 {
			b, _ := json.Marshal(updated)
			updatedVal = string(b)
		}
		s.db.Exec(
			fmt.Sprintf("UPDATE issues SET %s = ?, synced_at = datetime('now') WHERE key = ?", fieldName),
			updatedVal, issueKey,
		)
	}

	return output.OK(stripNulls(map[string]interface{}{
		"issue_key": issueKey,
		"field":     fieldName,
		"value":     value,
		"operation": operation,
		"status":    status,
	})), nil
}

// setBulk sets a field on multiple issues.
func (s *Server) setBulk(ctx context.Context, keys []string, fieldName, jiraID, value, fieldType, operation string, queue bool) ([]byte, error) {
	var scalarPayloadVal interface{}
	var scalarLocalVal string

	if operation == "set" {
		scalarPayloadVal = value
		scalarLocalVal = value
		if fieldType == "array" {
			items := parseArrayValue(value)
			wrapped := make([]interface{}, len(items))
			for i, item := range items {
				if w, ok := wrapArrayItemValue(jiraID, item); ok {
					wrapped[i] = w
				} else {
					wrapped[i] = item
				}
			}
			scalarPayloadVal = wrapped
			j, _ := json.Marshal(items)
			scalarLocalVal = string(j)
		} else {
			if wrapped, ok, err := wrapScalarFieldValue(jiraID, value, s.jira, ctx); ok {
				if err != nil {
					return nil, fmt.Errorf("resolving %s: %v", fieldName, err)
				}
				scalarPayloadVal = wrapped
			}
		}
	}

	var succeeded, failed int
	for _, key := range keys {
		if operation != "set" {
			var val interface{} = value
			if w, ok := wrapArrayItemValue(jiraID, value); ok {
				val = w
			}
			if queue {
				payload, _ := json.Marshal(map[string]interface{}{"field": jiraID, "op": operation, "value": val})
				if err := s.db.InsertPendingChange(key, "update_field", string(payload)); err != nil {
					failed++
					continue
				}
			} else {
				if err := s.jira.UpdateFieldOp(ctx, key, jiraID, operation, val); err != nil {
					failed++
					continue
				}
			}
			succeeded++
		} else {
			if queue {
				payload, _ := json.Marshal(map[string]interface{}{"field": jiraID, "value": scalarPayloadVal})
				if err := s.db.InsertPendingChange(key, "set_field", string(payload)); err != nil {
					failed++
					continue
				}
			} else {
				if err := s.jira.UpdateField(ctx, key, jiraID, scalarPayloadVal); err != nil {
					failed++
					continue
				}
			}
			s.db.Exec(
				fmt.Sprintf("UPDATE issues SET %s = ?, synced_at = datetime('now') WHERE key = ?", fieldName),
				scalarLocalVal, key,
			)
			succeeded++
		}
	}

	status := "synced"
	if queue {
		status = "queued"
	}
	return output.OK(stripNulls(map[string]interface{}{
		"count":     len(keys),
		"keys":      keys,
		"succeeded": succeeded,
		"failed":    failed,
		"status":    status,
	})), nil
}

// Helper functions (mirrored from CLI)

func expandKeys(keyArg string) []string {
	parts := strings.Split(keyArg, ",")
	keys := make([]string, 0, len(parts))
	for _, p := range parts {
		if k := strings.TrimSpace(p); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

func extractKeys(columns []string, rows [][]interface{}) ([]string, error) {
	keyCol := -1
	for i, col := range columns {
		if strings.EqualFold(col, "key") {
			keyCol = i
			break
		}
	}
	if keyCol == -1 {
		return nil, fmt.Errorf("query must return a 'key' column")
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		if keyCol < len(row) && row[keyCol] != nil {
			keys = append(keys, fmt.Sprint(row[keyCol]))
		}
	}
	return keys, nil
}

func parseArrayValue(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			result = append(result, v)
		}
	}
	return result
}

func wrapScalarFieldValue(jiraID, value string, jiraClient *jira.Client, ctx context.Context) (result interface{}, ok bool, err error) {
	switch jiraID {
	case "priority":
		return map[string]string{"name": value}, true, nil
	case "assignee", "reporter":
		accountID, rerr := jiraClient.ResolveAccountID(ctx, value)
		if rerr != nil {
			return nil, true, rerr
		}
		return map[string]string{"accountId": accountID}, true, nil
	}
	return nil, false, nil
}

func wrapArrayItemValue(jiraID, value string) (result interface{}, ok bool) {
	switch jiraID {
	case "components", "fixVersions":
		return map[string]string{"name": value}, true
	}
	return nil, false
}

func (s *Server) readCurrentArray(issueKey, fieldName string) []string {
	var raw sql.NullString
	_ = s.db.QueryRow(
		fmt.Sprintf("SELECT %s FROM issues WHERE key = ?", fieldName),
		issueKey,
	).Scan(&raw)
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw.String), &arr); err != nil {
		return []string{raw.String}
	}
	return arr
}

func applyArrayOps(current, adds, removes []string) []string {
	removeSet := make(map[string]bool, len(removes))
	for _, v := range removes {
		removeSet[v] = true
	}
	var result []string
	for _, v := range current {
		if !removeSet[v] {
			result = append(result, v)
		}
	}
	existSet := make(map[string]bool, len(result))
	for _, v := range result {
		existSet[v] = true
	}
	for _, v := range adds {
		if !existSet[v] {
			result = append(result, v)
			existSet[v] = true
		}
	}
	return result
}

func resolveFieldID(fieldMap map[string]*db.FieldMapping, name string) string {
	if _, ok := fieldMap[name]; ok {
		return name
	}
	for id, f := range fieldMap {
		if f.Name == name || f.JiraName == name {
			return id
		}
	}
	return ""
}

// handleJaiComment handles the jai_comment tool call.
func (s *Server) handleJaiComment(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.toolsets.CanWrite() {
		return mcp.NewToolResultError("write operations are disabled"), nil
	}

	issueKey := req.GetString("key", "")
	text := req.GetString("text", "")
	queue := req.GetBool("queue", false)

	if issueKey == "" || text == "" {
		return mcp.NewToolResultError("key and text are required"), nil
	}

	status := "synced"
	var commentID string

	if queue {
		if err := s.db.EnsurePendingChangesTable(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("database error: %v", err)), nil
		}
		payload, _ := json.Marshal(map[string]string{"body": text})
		if err := s.db.InsertPendingChange(issueKey, "add_comment", string(payload)); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("queue error: %v", err)), nil
		}
		commentID = fmt.Sprintf("local_%d", time.Now().UnixNano())
		status = "queued"
	} else {
		id, err := s.jira.AddComment(ctx, issueKey, text)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("adding comment: %v", err)), nil
		}
		commentID = id
	}

	// Update local DB
	now := time.Now().UTC().Format(time.RFC3339)
	localComment := &db.Comment{
		ID:       commentID,
		IssueKey: issueKey,
		Author:   s.cfg.Me,
		Body:     text,
		Created:  now,
		Updated:  now,
	}
	_ = s.db.UpsertComment(localComment)
	_ = s.db.UpdateIssueCommentsText(issueKey)

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"issue_key": issueKey,
		"status":    status,
	})))), nil
}

// jaiTransitionTool returns the jai_transition tool definition.
// handleJaiTransition handles the jai_transition tool call.
func (s *Server) handleJaiTransition(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.toolsets.CanWrite() {
		return mcp.NewToolResultError("write operations are disabled"), nil
	}

	issueKey := req.GetString("key", "")
	targetStatus := req.GetString("status", "")
	queue := req.GetBool("queue", false)

	if issueKey == "" {
		return mcp.NewToolResultError("key is required"), nil
	}

	transitions, err := s.jira.GetTransitions(ctx, issueKey)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("fetching transitions: %v", err)), nil
	}

	// If no status specified, list available transitions
	if targetStatus == "" {
		type transitionInfo struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		items := make([]transitionInfo, len(transitions))
		for i, t := range transitions {
			items[i] = transitionInfo{ID: t.ID, Name: t.Name}
		}
		return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
			"issue_key":   issueKey,
			"transitions": items,
		})))), nil
	}

	// Resolve transition
	match, ambiguous := resolveTransition(targetStatus, transitions)
	if match == nil && ambiguous != nil {
		names := make([]string, len(ambiguous))
		for i, t := range ambiguous {
			names[i] = fmt.Sprintf("%s (id: %s)", t.Name, t.ID)
		}
		return mcp.NewToolResultError(fmt.Sprintf("ambiguous transition %q matches: %s", targetStatus, strings.Join(names, ", "))), nil
	}
	if match == nil {
		names := make([]string, len(transitions))
		for i, t := range transitions {
			names[i] = t.Name
		}
		return mcp.NewToolResultError(fmt.Sprintf("unknown transition %q (available: %s)", targetStatus, strings.Join(names, ", "))), nil
	}

	status := "synced"
	if queue {
		if err := s.db.EnsurePendingChangesTable(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("database error: %v", err)), nil
		}
		payload, _ := json.Marshal(map[string]string{"transition_id": match.ID})
		if err := s.db.InsertPendingChange(issueKey, "transition", string(payload)); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("queue error: %v", err)), nil
		}
		status = "queued"
	} else {
		if err := s.jira.ExecuteTransition(ctx, issueKey, match.ID); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("transition failed: %v", err)), nil
		}

		// Refresh local DB
		if apiIssue, fetchErr := s.jira.GetIssue(ctx, issueKey); fetchErr == nil {
			rawJSON, _ := json.Marshal(apiIssue)
			if fieldMap, fmErr := s.db.FieldMapByJiraID(); fmErr == nil {
				if dbIssue, extra, denormErr := synce.Denormalize(rawJSON, fieldMap); denormErr == nil {
					_ = s.db.UpsertIssue(dbIssue, extra)
				}
			}
		}
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"issue_key":     issueKey,
		"transition":    match.Name,
		"transition_id": match.ID,
		"status":        status,
	})))), nil
}

func resolveTransition(name string, transitions []*jira.Transition) (match *jira.Transition, ambiguous []*jira.Transition) {
	lower := strings.ToLower(name)
	var matches []*jira.Transition
	for _, t := range transitions {
		if strings.ToLower(t.Name) == lower {
			matches = append(matches, t)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return nil, matches
	}
	return nil, nil
}

// handleJaiCreate handles the jai_create tool call.
func (s *Server) handleJaiCreate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.toolsets.CanWrite() {
		return mcp.NewToolResultError("write operations are disabled"), nil
	}

	project := req.GetString("project", "")
	issueType := req.GetString("type", "")
	summary := req.GetString("summary", "")
	description := req.GetString("description", "")
	parent := req.GetString("parent", "")
	priority := req.GetString("priority", "")
	assignee := req.GetString("assignee", "")
	fixVersion := req.GetString("fix_version", "")
	dueDate := req.GetString("due_date", "")
	template := req.GetString("template", "")

	if project == "" || issueType == "" || summary == "" {
		return mcp.NewToolResultError("project, type, and summary are required"), nil
	}

	// Resolve template if specified
	if template != "" {
		if s.cfg != nil && s.cfg.Templates != nil {
			if tmpl, ok := s.cfg.Templates[template]; ok {
				description = tmpl
			} else {
				return mcp.NewToolResultError(fmt.Sprintf("template not found: %s", template)), nil
			}
		}
	}

	fields := map[string]interface{}{
		"project":   map[string]string{"key": strings.ToUpper(project)},
		"summary":   summary,
		"issuetype": map[string]string{"name": issueType},
	}

	if description != "" {
		fields["description"] = map[string]interface{}{
			"type":    "doc",
			"version": 1,
			"content": []map[string]interface{}{
				{
					"type": "paragraph",
					"content": []map[string]interface{}{
						{"type": "text", "text": description},
					},
				},
			},
		}
	}

	if parent != "" {
		fields["parent"] = map[string]string{"key": parent}
	}

	if priority != "" {
		fields["priority"] = map[string]string{"name": priority}
	}

	if assignee != "" {
		accountID, err := s.jira.ResolveAccountID(ctx, assignee)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolving assignee: %v", err)), nil
		}
		fields["assignee"] = map[string]string{"accountId": accountID}
	}

	// Handle array fields
	if args, ok := req.Params.Arguments.(map[string]interface{}); ok {
		if labelsRaw, ok := args["labels"]; ok {
			if labelsArr, ok := labelsRaw.([]interface{}); ok {
				labels := make([]string, 0, len(labelsArr))
				for _, l := range labelsArr {
					if ls, ok := l.(string); ok {
						labels = append(labels, ls)
					}
				}
				if len(labels) > 0 {
					fields["labels"] = labels
				}
			}
		}

		if componentsRaw, ok := args["components"]; ok {
			if componentsArr, ok := componentsRaw.([]interface{}); ok {
				comps := make([]map[string]string, 0, len(componentsArr))
				for _, c := range componentsArr {
					if cs, ok := c.(string); ok {
						comps = append(comps, map[string]string{"name": cs})
					}
				}
				if len(comps) > 0 {
					fields["components"] = comps
				}
			}
		}

		// Custom fields
		if customFieldsRaw, ok := args["custom_fields"]; ok {
			if customFields, ok := customFieldsRaw.(map[string]interface{}); ok {
				fieldMap, err := s.db.FieldMapByJiraID()
				if err == nil {
					for name, value := range customFields {
						jiraID := resolveFieldID(fieldMap, name)
						if jiraID != "" {
							fields[jiraID] = value
						}
					}
				}
			}
		}
	}

	if fixVersion != "" {
		fields["fixVersions"] = []map[string]string{{"name": fixVersion}}
	}

	if dueDate != "" {
		fields["duedate"] = dueDate
	}

	resp, err := s.jira.CreateIssue(ctx, fields)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("creating issue: %v", err)), nil
	}

	// Fetch and insert into local DB
	if apiIssue, fetchErr := s.jira.GetIssue(ctx, resp.Key); fetchErr == nil {
		rawJSON, _ := json.Marshal(apiIssue)
		if fieldMap, fmErr := s.db.FieldMapByJiraID(); fmErr == nil {
			if dbIssue, extra, denormErr := synce.Denormalize(rawJSON, fieldMap); denormErr == nil {
				_ = s.db.UpsertIssue(dbIssue, extra)
			}
		}
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"key":     resp.Key,
		"id":      resp.ID,
		"project": project,
		"status":  "created",
	})))), nil
}

// handleJaiClone handles the jai_clone tool call.
func (s *Server) handleJaiClone(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.toolsets.CanWrite() {
		return mcp.NewToolResultError("write operations are disabled"), nil
	}

	sourceKey := req.GetString("key", "")
	summaryOverride := req.GetString("summary", "")

	if sourceKey == "" {
		return mcp.NewToolResultError("key is required"), nil
	}

	// Read source issue from local DB
	issue, err := s.db.GetIssue(sourceKey)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("reading issue: %v", err)), nil
	}
	if issue == nil {
		return mcp.NewToolResultError(fmt.Sprintf("issue %s not found in local database", sourceKey)), nil
	}

	rawJSON, ok := issue["raw_json"].(string)
	if !ok || rawJSON == "" {
		return mcp.NewToolResultError(fmt.Sprintf("issue %s has no raw_json data", sourceKey)), nil
	}

	fieldMap, err := s.db.FieldMapByJiraID()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("field map error: %v", err)), nil
	}

	fields, project, err := extractCloneFields(rawJSON, fieldMap)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("extracting fields: %v", err)), nil
	}

	// Apply summary override
	if summaryOverride != "" {
		fields["summary"] = summaryOverride
	}

	// Apply field overrides
	if args, ok := req.Params.Arguments.(map[string]interface{}); ok {
		if overridesRaw, ok := args["overrides"]; ok {
			if overrides, ok := overridesRaw.(map[string]interface{}); ok {
				for name, value := range overrides {
					valueStr, _ := value.(string)
					if err := applyFieldOverride(fields, fieldMap, name, valueStr, s.jira, ctx); err != nil {
						return mcp.NewToolResultError(err.Error()), nil
					}
				}
			}
		}
	}

	resp, err := s.jira.CreateIssue(ctx, fields)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("creating issue: %v", err)), nil
	}

	// Fetch and insert into local DB
	if apiIssue, fetchErr := s.jira.GetIssue(ctx, resp.Key); fetchErr == nil {
		rawJSON, _ := json.Marshal(apiIssue)
		if fieldMap, fmErr := s.db.FieldMapByJiraID(); fmErr == nil {
			if dbIssue, extra, denormErr := synce.Denormalize(rawJSON, fieldMap); denormErr == nil {
				_ = s.db.UpsertIssue(dbIssue, extra)
			}
		}
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"key":     resp.Key,
		"id":      resp.ID,
		"source":  sourceKey,
		"project": project,
		"status":  "created",
	})))), nil
}

// extractCloneFields extracts clonable fields from raw JSON
func extractCloneFields(rawJSON string, fieldMap map[string]*db.FieldMapping) (map[string]interface{}, string, error) {
	var apiIssue struct {
		Fields json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &apiIssue); err != nil {
		return nil, "", fmt.Errorf("parsing raw_json: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(apiIssue.Fields, &raw); err != nil {
		return nil, "", fmt.Errorf("parsing fields: %w", err)
	}

	fields := make(map[string]interface{})
	project := ""

	// Project
	if v, ok := raw["project"]; ok {
		var proj map[string]interface{}
		if json.Unmarshal(v, &proj) == nil && proj["key"] != nil {
			project = fmt.Sprint(proj["key"])
			fields["project"] = map[string]string{"key": project}
		}
	}

	// Summary
	if v, ok := raw["summary"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			fields["summary"] = s
		}
	}

	// Issue type
	if v, ok := raw["issuetype"]; ok {
		var it map[string]interface{}
		if json.Unmarshal(v, &it) == nil && it["name"] != nil {
			fields["issuetype"] = map[string]string{"name": fmt.Sprint(it["name"])}
		}
	}

	// Description
	if v, ok := raw["description"]; ok {
		var desc map[string]interface{}
		if json.Unmarshal(v, &desc) == nil {
			fields["description"] = desc
		}
	}

	// Priority
	if v, ok := raw["priority"]; ok {
		var p map[string]interface{}
		if json.Unmarshal(v, &p) == nil && p["name"] != nil {
			fields["priority"] = map[string]string{"name": fmt.Sprint(p["name"])}
		}
	}

	// Labels
	if v, ok := raw["labels"]; ok {
		var labels []string
		if json.Unmarshal(v, &labels) == nil && len(labels) > 0 {
			fields["labels"] = labels
		}
	}

	// Components
	if v, ok := raw["components"]; ok {
		var comps []map[string]interface{}
		if json.Unmarshal(v, &comps) == nil && len(comps) > 0 {
			names := make([]map[string]string, len(comps))
			for i, c := range comps {
				names[i] = map[string]string{"name": fmt.Sprint(c["name"])}
			}
			fields["components"] = names
		}
	}

	// Assignee
	if v, ok := raw["assignee"]; ok {
		var a map[string]interface{}
		if json.Unmarshal(v, &a) == nil && a["accountId"] != nil {
			fields["assignee"] = map[string]string{"accountId": fmt.Sprint(a["accountId"])}
		}
	}

	// Parent
	if v, ok := raw["parent"]; ok {
		var p map[string]interface{}
		if json.Unmarshal(v, &p) == nil && p["key"] != nil {
			fields["parent"] = map[string]string{"key": fmt.Sprint(p["key"])}
		}
	}

	// Fix versions
	if v, ok := raw["fixVersions"]; ok {
		var versions []map[string]interface{}
		if json.Unmarshal(v, &versions) == nil && len(versions) > 0 {
			fv := make([]map[string]string, len(versions))
			for i, ver := range versions {
				fv[i] = map[string]string{"name": fmt.Sprint(ver["name"])}
			}
			fields["fixVersions"] = fv
		}
	}

	// Custom fields (skip non-clonable ones like Rank)
	nonClonableFields := map[string]bool{"Rank": true}
	for key, v := range raw {
		if strings.HasPrefix(key, "customfield_") {
			if fm, ok := fieldMap[key]; ok && nonClonableFields[fm.JiraName] {
				continue
			}
			var val interface{}
			if json.Unmarshal(v, &val) == nil && val != nil {
				fields[key] = val
			}
		}
	}

	return fields, project, nil
}

// applyFieldOverride applies a field override
func applyFieldOverride(fields map[string]interface{}, fieldMap map[string]*db.FieldMapping, name, value string, jiraClient *jira.Client, ctx context.Context) error {
	switch strings.ToLower(name) {
	case "summary":
		fields["summary"] = value
		return nil
	case "priority":
		fields["priority"] = map[string]string{"name": value}
		return nil
	case "assignee":
		accountID, err := jiraClient.ResolveAccountID(ctx, value)
		if err != nil {
			return fmt.Errorf("resolving assignee: %w", err)
		}
		fields["assignee"] = map[string]string{"accountId": accountID}
		return nil
	case "labels":
		fields["labels"] = parseArrayValue(value)
		return nil
	case "components":
		items := parseArrayValue(value)
		comps := make([]map[string]string, len(items))
		for i, c := range items {
			comps[i] = map[string]string{"name": c}
		}
		fields["components"] = comps
		return nil
	case "parent":
		fields["parent"] = map[string]string{"key": value}
		return nil
	case "fix-version", "fixversion":
		fields["fixVersions"] = []map[string]string{{"name": value}}
		return nil
	case "type", "issuetype":
		fields["issuetype"] = map[string]string{"name": value}
		return nil
	}

	// Try field map
	jiraID := resolveFieldID(fieldMap, name)
	if jiraID == "" {
		return fmt.Errorf("unknown field: %s", name)
	}

	var parsed interface{}
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		parsed = value
	}
	fields[jiraID] = parsed
	return nil
}

// jaiLinkTool returns the jai_link tool definition.
// handleJaiLink handles the jai_link tool call.
func (s *Server) handleJaiLink(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.toolsets.CanWrite() {
		return mcp.NewToolResultError("write operations are disabled"), nil
	}

	issueKey := req.GetString("key", "")
	target := req.GetString("target", "")
	linkType := req.GetString("type", "Relates")
	title := req.GetString("title", "")
	listTypes := req.GetBool("list_types", false)

	if issueKey == "" {
		return mcp.NewToolResultError("key is required"), nil
	}

	// List link types
	if listTypes {
		linkTypes, err := s.jira.GetLinkTypes(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("fetching link types: %v", err)), nil
		}
		return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
			"link_types": linkTypes,
		})))), nil
	}

	if target == "" {
		return mcp.NewToolResultError("target is required"), nil
	}

	// Remote link (URL)
	if isURL(target) {
		if title == "" {
			title = target
		}
		if err := s.jira.CreateRemoteLink(ctx, issueKey, target, title); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("creating remote link: %v", err)), nil
		}
		return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
			"issue_key": issueKey,
			"url":       target,
			"title":     title,
			"status":    "created",
		})))), nil
	}

	// Issue-to-issue link
	resolved, err := s.resolveLinkType(ctx, linkType)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	linkType = resolved

	if err := s.jira.CreateLink(ctx, linkType, issueKey, target); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("creating link: %v", err)), nil
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"from_key":  issueKey,
		"to_key":    target,
		"link_type": linkType,
		"status":    "created",
	})))), nil
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func (s *Server) resolveLinkType(ctx context.Context, name string) (string, error) {
	linkTypes, err := s.jira.GetLinkTypes(ctx)
	if err != nil {
		return "", fmt.Errorf("fetching link types: %w", err)
	}
	for _, lt := range linkTypes {
		if strings.EqualFold(lt.Name, name) {
			return lt.Name, nil
		}
	}
	names := make([]string, len(linkTypes))
	for i, lt := range linkTypes {
		names[i] = lt.Name
	}
	return "", fmt.Errorf("unknown link type %q (available: %s)", name, strings.Join(names, ", "))
}

// jaiUpdateTool returns the jai_update tool definition (composite tool).
// handleJaiUpdate handles the jai_update tool call (composite operation).
func (s *Server) handleJaiUpdate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.toolsets.CanWrite() {
		return mcp.NewToolResultError("write operations are disabled"), nil
	}

	issueKey := req.GetString("key", "")
	queue := req.GetBool("queue", false)

	if issueKey == "" {
		return mcp.NewToolResultError("key is required"), nil
	}

	operations := make(map[string]interface{})
	var failed []string

	// Set fields
	if args, ok := req.Params.Arguments.(map[string]interface{}); ok {
		if setRaw, ok := args["set"]; ok {
		if setFields, ok := setRaw.(map[string]interface{}); ok {
			fieldsSet := 0
			for field, valueRaw := range setFields {
				value, _ := valueRaw.(string)
				// Call jai_set for each field
				setReq := mcp.CallToolRequest{
					Params: mcp.CallToolParams{
						Arguments: map[string]interface{}{
							"keys":      issueKey,
							"field":     field,
							"value":     value,
							"operation": "set",
							"queue":     queue,
						},
					},
				}
				_, err := s.handleJaiSet(ctx, setReq)
				if err == nil {
					fieldsSet++
				} else {
					failed = append(failed, fmt.Sprintf("set_%s", field))
				}
			}
			operations["fields_set"] = fieldsSet
		}
		}

		// Transition
		if transitionRaw, ok := args["transition"]; ok {
			if targetStatus, ok := transitionRaw.(string); ok && targetStatus != "" {
				transReq := mcp.CallToolRequest{
					Params: mcp.CallToolParams{
						Arguments: map[string]interface{}{
							"key":    issueKey,
							"status": targetStatus,
							"queue":  queue,
						},
					},
				}
				_, err := s.handleJaiTransition(ctx, transReq)
				if err == nil {
					operations["transition"] = "ok"
				} else {
					operations["transition"] = "failed"
					failed = append(failed, "transition")
				}
			}
		}

		// Comment
		if commentRaw, ok := args["comment"]; ok {
		if commentText, ok := commentRaw.(string); ok && commentText != "" {
			commentReq := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]interface{}{
						"key":   issueKey,
						"text":  commentText,
						"queue": queue,
					},
				},
			}
			_, err := s.handleJaiComment(ctx, commentReq)
			if err == nil {
				operations["comment"] = "ok"
			} else {
				operations["comment"] = "failed"
				failed = append(failed, "comment")
			}
		}
		}
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"key":        issueKey,
		"operations": operations,
		"failed":     failed,
	})))), nil
}
