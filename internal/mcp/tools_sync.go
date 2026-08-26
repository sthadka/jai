package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sthadka/jai/internal/output"
	synce "github.com/sthadka/jai/internal/sync"
)

// registerSyncTools registers all sync-related tools.
func registerSyncTools(s *Server, srv *server.MCPServer) {
	if !s.toolsets.IsEnabled("sync") {
		return
	}

	// jai_sync tool
	srv.AddTool(mcp.Tool{
		Name:        "jai_sync",
		Description: "Sync local database with Jira. Incremental by default (only changed issues). Full sync re-fetches everything. Usually runs automatically — only call if data seems stale.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"full": map[string]interface{}{
					"type":        "boolean",
					"description": "Full re-sync instead of incremental",
					"default":     false,
				},
				"source": map[string]string{
					"type":        "string",
					"description": "Sync only a specific named source",
				},
			},
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleJaiSync(s, ctx, req)
	})

	// jai_status tool
	srv.AddTool(mcp.Tool{
		Name:        "jai_status",
		Description: "Show system status: auth, last sync time, DB stats, pending changes count. Use to diagnose issues or check data freshness.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleJaiStatus(s, ctx, req)
	})

	// jai_push tool
	srv.AddTool(mcp.Tool{
		Name:        "jai_push",
		Description: "Flush all pending (queued) changes to Jira. Only needed if operations were run with queue=true.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleJaiPush(s, ctx, req)
	})

	// jai_open is part of the browser toolset
	if s.toolsets.IsEnabled("browser") {
		srv.AddTool(mcp.Tool{
			Name:        "jai_open",
			Description: "Get the Jira web URL for an issue. Returns the URL without opening a browser.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"key": map[string]string{
						"type":        "string",
						"description": "Issue key (e.g., PROJ-123)",
					},
				},
				Required: []string{"key"},
			},
		}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleJaiOpen(s, ctx, req)
		})
	}
}

// handleJaiSync handles the jai_sync tool call.
func handleJaiSync(s *Server, ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	full := req.GetBool("full", false)
	source := req.GetString("source", "")

	// Discover fields first
	var overrides map[string]string
	if s.cfg.Fields.Overrides != nil {
		overrides = s.cfg.Fields.Overrides
	}
	if err := s.sync.DiscoverFields(ctx, overrides); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("discovering fields: %v", err)), nil
	}

	// Run sync
	ch, err := s.sync.Sync(ctx, full, false, source)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("sync error: %v", err)), nil
	}

	// Collect results
	var totalSynced, totalNew, totalUpdated int
	var sources []map[string]interface{}
	var lastError error

	for p := range ch {
		if p.Done {
			sourceInfo := map[string]interface{}{
				"source":  p.Project,
				"total":   p.Total,
				"new":     p.New,
				"updated": p.Updated,
			}
			if p.Error != nil {
				sourceInfo["error"] = p.Error.Error()
				lastError = p.Error
			} else {
				totalSynced += p.Total
				totalNew += p.New
				totalUpdated += p.Updated
			}
			sources = append(sources, sourceInfo)
		}
	}

	if lastError != nil {
		return mcp.NewToolResultError(fmt.Sprintf("sync completed with errors: %v", lastError)), nil
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"total_synced": totalSynced,
		"new":          totalNew,
		"updated":      totalUpdated,
		"sources":      sources,
	})))), nil
}

// handleJaiStatus handles the jai_status tool call.
func handleJaiStatus(s *Server, ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Auth check
	me, authErr := s.jira.MySelf(ctx)

	metas, err := s.db.AllSyncMeta()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("reading sync metadata: %v", err)), nil
	}

	totalIssues, _ := s.db.TotalIssueCount()
	countByProject, _ := s.db.IssueCountByProject()

	pendingCount, err := s.db.CountPendingChanges()
	if err != nil {
		pendingCount = 0
	}

	dbInfo, _ := os.Stat(s.cfg.DB.Path)

	// Build response
	auth := map[string]interface{}{"ok": authErr == nil}
	if authErr != nil {
		auth["error"] = authErr.Error()
	} else {
		auth["user"] = me.DisplayName
		auth["email"] = me.EmailAddress
	}

	sources := make([]map[string]interface{}, len(metas))
	for i, m := range metas {
		sourceInfo := map[string]interface{}{
			"source": m.Project,
		}
		if m.LastSyncTime.Valid && m.LastSyncTime.String != "" {
			sourceInfo["last_sync_time"] = m.LastSyncTime.String
		}
		if m.LastSyncDuration.Valid {
			sourceInfo["last_sync_duration_seconds"] = m.LastSyncDuration.Float64
		}
		if m.LastSyncError.Valid && m.LastSyncError.String != "" {
			sourceInfo["last_sync_error"] = m.LastSyncError.String
		}
		if m.IssuesSynced.Valid {
			sourceInfo["issues_synced"] = m.IssuesSynced.Int64
		}
		if m.LastIssueUpdated.Valid && m.LastIssueUpdated.String != "" {
			sourceInfo["last_issue_updated"] = m.LastIssueUpdated.String
		}
		sources[i] = sourceInfo
	}

	data := map[string]interface{}{
		"auth":              auth,
		"sources":           sources,
		"total_issues":      totalIssues,
		"issues_by_project": countByProject,
		"pending_changes":   pendingCount,
	}
	if dbInfo != nil {
		data["db_size_bytes"] = dbInfo.Size()
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(data)))), nil
}

// handleJaiPush handles the jai_push tool call.
func handleJaiPush(s *Server, ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.toolsets.CanWrite() {
		return mcp.NewToolResultError("write operations are disabled"), nil
	}

	// Ensure pending_changes table exists
	if err := s.db.EnsurePendingChangesTable(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("database error: %v", err)), nil
	}

	count, err := s.db.CountPendingChanges()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("database error: %v", err)), nil
	}
	if count == 0 {
		return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
			"pending":   0,
			"succeeded": 0,
			"failed":    0,
			"message":   "No pending changes",
		})))), nil
	}

	writer := synce.NewWriter(s.db, s.jira)
	results, err := writer.ProcessQueue(ctx)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("processing queue: %v", err)), nil
	}

	succeeded, failed := 0, 0
	var failedOps []map[string]string
	for _, r := range results {
		if r.Success {
			succeeded++
		} else {
			failed++
			failedOps = append(failedOps, map[string]string{
				"issue_key": r.IssueKey,
				"operation": r.Operation,
				"error":     fmt.Sprint(r.Error),
			})
		}
	}

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"pending":    count,
		"succeeded":  succeeded,
		"failed":     failed,
		"failed_ops": failedOps,
	})))), nil
}

// handleJaiOpen handles the jai_open tool call.
func handleJaiOpen(s *Server, ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := req.GetString("key", "")

	if key == "" {
		return mcp.NewToolResultError("key is required"), nil
	}

	baseURL := strings.TrimRight(s.cfg.Jira.URL, "/")
	issueURL := baseURL + "/browse/" + key

	return mcp.NewToolResultText(string(output.OK(stripNulls(map[string]interface{}{
		"url": issueURL,
	})))), nil
}
