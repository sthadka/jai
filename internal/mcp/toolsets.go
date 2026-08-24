package mcp

import (
	"os"
	"strings"
)

// toolsetMap defines which tools belong to each toolset.
var toolsetMap = map[string][]string{
	"read":    {"jai_query", "jai_get", "jai_search", "jai_view"},
	"schema":  {"jai_schema", "jai_fields"},
	"write":   {"jai_set", "jai_comment", "jai_transition", "jai_create", "jai_clone", "jai_link", "jai_update"},
	"sync":    {"jai_sync", "jai_status", "jai_push"},
	"browser": {"jai_open"},
	"config":  {"jai_config"},
}

// defaultToolsets defines which toolsets are enabled by default.
var defaultToolsets = []string{"read", "schema", "write", "sync", "browser"}

// ToolsetRegistry manages which toolsets are enabled and enforces read-only mode.
type ToolsetRegistry struct {
	enabled  map[string]bool
	readOnly bool
	toolMap  map[string]string // tool name → toolset name
}

// NewToolsetRegistry creates a new toolset registry.
// If toolsets is empty, default toolsets are enabled.
// Environment variables can override: JAI_MCP_TOOLSETS, JAI_MCP_READ_ONLY.
func NewToolsetRegistry(toolsets []string, readOnly bool) *ToolsetRegistry {
	// Check env var overrides
	if envToolsets := os.Getenv("JAI_MCP_TOOLSETS"); envToolsets != "" {
		toolsets = strings.Split(envToolsets, ",")
		// Trim spaces
		for i := range toolsets {
			toolsets[i] = strings.TrimSpace(toolsets[i])
		}
	}

	if os.Getenv("JAI_MCP_READ_ONLY") == "true" {
		readOnly = true
	}

	enabled := make(map[string]bool)

	// Handle "all" special value
	if len(toolsets) == 1 && toolsets[0] == "all" {
		for ts := range toolsetMap {
			enabled[ts] = true
		}
	} else if len(toolsets) == 0 {
		// Enable default toolsets
		for _, ts := range defaultToolsets {
			enabled[ts] = true
		}
	} else {
		// Enable specified toolsets
		for _, ts := range toolsets {
			if ts != "" {
				enabled[ts] = true
			}
		}
	}

	// Read-only mode: disable write and config toolsets
	if readOnly {
		enabled["write"] = false
		enabled["config"] = false
	}

	// Build reverse map: tool name → toolset name
	toolMap := make(map[string]string)
	for toolset, tools := range toolsetMap {
		for _, tool := range tools {
			toolMap[tool] = toolset
		}
	}

	return &ToolsetRegistry{
		enabled:  enabled,
		readOnly: readOnly,
		toolMap:  toolMap,
	}
}

// IsEnabled returns true if the given toolset is enabled.
func (r *ToolsetRegistry) IsEnabled(toolset string) bool {
	return r.enabled[toolset]
}

// IsToolEnabled returns true if the given tool is enabled.
// This checks both list time and call time (security boundary).
func (r *ToolsetRegistry) IsToolEnabled(toolName string) bool {
	toolset, ok := r.toolMap[toolName]
	if !ok {
		// Unknown tool - default to disabled
		return false
	}
	return r.enabled[toolset]
}

// IsReadOnly returns true if the server is in read-only mode.
func (r *ToolsetRegistry) IsReadOnly() bool {
	return r.readOnly
}

// CanWrite returns true if write operations are allowed.
// Write operations are blocked if read-only mode is enabled or write toolset is disabled.
func (r *ToolsetRegistry) CanWrite() bool {
	return !r.readOnly && r.enabled["write"]
}

// ListEnabledTools returns a list of all enabled tool names.
func (r *ToolsetRegistry) ListEnabledTools() []string {
	var tools []string
	for toolset, enabled := range r.enabled {
		if enabled {
			tools = append(tools, toolsetMap[toolset]...)
		}
	}
	return tools
}
