//go:build fts5

package mcp

import (
	"os"
	"testing"
)

func TestNewToolsetRegistry(t *testing.T) {
	tests := []struct {
		name      string
		toolsets  []string
		readOnly  bool
		wantRead  bool
		wantWrite bool
	}{
		{
			name:      "default toolsets",
			toolsets:  nil,
			readOnly:  false,
			wantRead:  true,
			wantWrite: true,
		},
		{
			name:      "all toolsets",
			toolsets:  []string{"all"},
			readOnly:  false,
			wantRead:  true,
			wantWrite: true,
		},
		{
			name:      "read-only mode blocks write",
			toolsets:  []string{"all"},
			readOnly:  true,
			wantRead:  true,
			wantWrite: false,
		},
		{
			name:      "explicit read toolset only",
			toolsets:  []string{"read"},
			readOnly:  false,
			wantRead:  true,
			wantWrite: false,
		},
		{
			name:      "empty toolsets uses defaults",
			toolsets:  []string{},
			readOnly:  false,
			wantRead:  true,
			wantWrite: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear env vars to avoid interference
			os.Unsetenv("JAI_MCP_TOOLSETS")
			os.Unsetenv("JAI_MCP_READ_ONLY")

			registry := NewToolsetRegistry(tt.toolsets, tt.readOnly)

			if got := registry.IsEnabled("read"); got != tt.wantRead {
				t.Errorf("read toolset enabled = %v, want %v", got, tt.wantRead)
			}

			if got := registry.IsEnabled("write"); got != tt.wantWrite {
				t.Errorf("write toolset enabled = %v, want %v", got, tt.wantWrite)
			}

			if got := registry.IsReadOnly(); got != tt.readOnly {
				t.Errorf("IsReadOnly() = %v, want %v", got, tt.readOnly)
			}
		})
	}
}

func TestToolsetRegistryEnvOverrides(t *testing.T) {
	// Test JAI_MCP_TOOLSETS override
	t.Run("env toolsets override", func(t *testing.T) {
		defer os.Unsetenv("JAI_MCP_TOOLSETS")
		os.Setenv("JAI_MCP_TOOLSETS", "read,schema")

		registry := NewToolsetRegistry([]string{"all"}, false)

		if !registry.IsEnabled("read") {
			t.Error("read toolset should be enabled via env var")
		}
		if !registry.IsEnabled("schema") {
			t.Error("schema toolset should be enabled via env var")
		}
		if registry.IsEnabled("write") {
			t.Error("write toolset should not be enabled (not in env var)")
		}
	})

	// Test JAI_MCP_READ_ONLY override
	t.Run("env read-only override", func(t *testing.T) {
		defer os.Unsetenv("JAI_MCP_READ_ONLY")
		os.Setenv("JAI_MCP_READ_ONLY", "true")

		registry := NewToolsetRegistry([]string{"all"}, false)

		if !registry.IsReadOnly() {
			t.Error("should be in read-only mode via env var")
		}
		if registry.IsEnabled("write") {
			t.Error("write toolset should be disabled in read-only mode")
		}
		if registry.IsEnabled("config") {
			t.Error("config toolset should be disabled in read-only mode")
		}
	})
}

func TestIsToolEnabled(t *testing.T) {
	tests := []struct {
		name     string
		toolsets []string
		readOnly bool
		tool     string
		want     bool
	}{
		{
			name:     "jai_query enabled in read toolset",
			toolsets: []string{"read"},
			readOnly: false,
			tool:     "jai_query",
			want:     true,
		},
		{
			name:     "jai_set disabled when write toolset off",
			toolsets: []string{"read"},
			readOnly: false,
			tool:     "jai_set",
			want:     false,
		},
		{
			name:     "jai_set disabled in read-only mode",
			toolsets: []string{"all"},
			readOnly: true,
			tool:     "jai_set",
			want:     false,
		},
		{
			name:     "unknown tool disabled",
			toolsets: []string{"all"},
			readOnly: false,
			tool:     "jai_unknown",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("JAI_MCP_TOOLSETS")
			os.Unsetenv("JAI_MCP_READ_ONLY")

			registry := NewToolsetRegistry(tt.toolsets, tt.readOnly)

			if got := registry.IsToolEnabled(tt.tool); got != tt.want {
				t.Errorf("IsToolEnabled(%q) = %v, want %v", tt.tool, got, tt.want)
			}
		})
	}
}

func TestCanWrite(t *testing.T) {
	tests := []struct {
		name     string
		toolsets []string
		readOnly bool
		want     bool
	}{
		{
			name:     "can write with default toolsets",
			toolsets: nil,
			readOnly: false,
			want:     true,
		},
		{
			name:     "cannot write in read-only mode",
			toolsets: []string{"all"},
			readOnly: true,
			want:     false,
		},
		{
			name:     "cannot write when write toolset disabled",
			toolsets: []string{"read", "schema"},
			readOnly: false,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("JAI_MCP_TOOLSETS")
			os.Unsetenv("JAI_MCP_READ_ONLY")

			registry := NewToolsetRegistry(tt.toolsets, tt.readOnly)

			if got := registry.CanWrite(); got != tt.want {
				t.Errorf("CanWrite() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestListEnabledTools(t *testing.T) {
	os.Unsetenv("JAI_MCP_TOOLSETS")
	os.Unsetenv("JAI_MCP_READ_ONLY")

	registry := NewToolsetRegistry([]string{"read", "schema"}, false)
	tools := registry.ListEnabledTools()

	// Should include tools from read and schema toolsets
	expected := map[string]bool{
		"jai_query":  true,
		"jai_get":    true,
		"jai_search": true,
		"jai_view":   true,
		"jai_schema": true,
		"jai_fields": true,
	}

	for _, tool := range tools {
		if !expected[tool] {
			t.Errorf("unexpected tool in list: %s", tool)
		}
		delete(expected, tool)
	}

	if len(expected) > 0 {
		t.Errorf("missing tools in list: %v", expected)
	}
}
