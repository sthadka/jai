package mcp

// ToolsetRegistry manages which toolsets are enabled and enforces read-only mode.
type ToolsetRegistry struct {
	enabled  map[string]bool
	readOnly bool
}

// NewToolsetRegistry creates a new toolset registry.
// If toolsets is empty, all toolsets are enabled by default.
func NewToolsetRegistry(toolsets []string, readOnly bool) *ToolsetRegistry {
	enabled := make(map[string]bool)
	if len(toolsets) == 0 {
		// Enable all toolsets by default
		enabled["read"] = true
		enabled["schema"] = true
		enabled["write"] = true
		enabled["sync"] = true
		enabled["config"] = true
	} else {
		for _, ts := range toolsets {
			enabled[ts] = true
		}
	}
	return &ToolsetRegistry{
		enabled:  enabled,
		readOnly: readOnly,
	}
}

// IsEnabled returns true if the given toolset is enabled.
func (r *ToolsetRegistry) IsEnabled(toolset string) bool {
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
