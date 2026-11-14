package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sthadka/jai/internal/config"
	"gopkg.in/yaml.v3"
)

// registerConfigTools registers the config toolset.
func registerConfigTools(s *Server, srv *server.MCPServer) {
	if !s.toolsets.IsEnabled("config") {
		return
	}

	srv.AddTool(mcp.Tool{
		Name:        "jai_config",
		Description: "Inspect or modify jai configuration. Add sync sources (to track new Jira projects), views (saved SQL reports), snippets (reusable SQL fragments), and templates (issue creation templates). Will not modify credentials.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"action": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"get", "add_source", "add_view", "add_snippet", "add_template", "set"},
					"description": "Action: 'get' shows config (credentials redacted), others modify it",
				},
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Name for the new source/view/snippet/template",
				},
				"value": map[string]interface{}{
					"type":        "object",
					"description": "Config value. For add_source: {jql: '...'}. For add_view: {query: '...', columns: [...]}. For add_snippet: {query: '...'}. For add_template: {description: '...'}.",
				},
				"key": map[string]interface{}{
					"type":        "string",
					"description": "For 'set': dot-path config key (e.g., 'sync.interval'). Cannot set jira.token or jira.email.",
				},
			},
			Required: []string{"action"},
		},
		Annotations: mcp.ToolAnnotation{
			Title: "Manage Config",
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleConfigTool(s, ctx, request)
	})
}

// handleConfigTool executes the jai_config tool.
func handleConfigTool(s *Server, ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := request.GetString("action", "")
	if action == "" {
		return mcp.NewToolResultError("action parameter is required"), nil
	}

	// Security check: block if read-only mode is enabled
	if s.toolsets.IsReadOnly() && action != "get" {
		return mcp.NewToolResultError("config modification blocked in read-only mode"), nil
	}

	// Execute action
	switch action {
	case "get":
		return getConfig(s)
	case "add_source":
		return addSyncSource(s, request)
	case "add_view":
		return addView(s, request)
	case "add_snippet":
		return addSnippet(s, request)
	case "add_template":
		return addTemplate(s, request)
	case "set":
		return setConfigValue(s, request)
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown action: %s", action)), nil
	}
}

// getConfig returns the current config with credentials redacted.
func getConfig(s *Server) (*mcp.CallToolResult, error) {
	// Create a copy for redaction
	cfg := *s.cfg
	cfg.Jira.Token = "***"
	cfg.Jira.Email = "***"

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to marshal config: %v", err)), nil
	}

	result := map[string]interface{}{
		"ok":   true,
		"data": json.RawMessage(data),
	}
	out, _ := json.Marshal(result)

	return mcp.NewToolResultText(string(out)), nil
}

// addSyncSource adds a new sync source to the config.
func addSyncSource(s *Server, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := request.GetString("name", "")
	if name == "" {
		return mcp.NewToolResultError("name is required for add_source"), nil
	}

	// Parse value object - convert Params.Arguments to map
	argsMap, ok := request.Params.Arguments.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("failed to parse arguments"), nil
	}

	valueRaw, ok := argsMap["value"]
	if !ok {
		return mcp.NewToolResultError("value is required for add_source"), nil
	}

	valueMap, ok := valueRaw.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("value must be an object"), nil
	}

	jql, ok := valueMap["jql"].(string)
	if !ok || jql == "" {
		return mcp.NewToolResultError("value.jql is required for add_source"), nil
	}

	var projects []string
	if projsRaw, ok := valueMap["projects"]; ok {
		if projsArr, ok := projsRaw.([]interface{}); ok {
			for _, p := range projsArr {
				if pStr, ok := p.(string); ok {
					projects = append(projects, pStr)
				}
			}
		}
	}

	// Load and modify config
	configPath := getConfigPath(s.cfg)
	if err := backupConfig(configPath); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to backup config: %v", err)), nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read config: %v", err)), nil
	}

	var rawConfig map[string]interface{}
	if err := yaml.Unmarshal(data, &rawConfig); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse config: %v", err)), nil
	}

	// Add sync source
	if rawConfig["sync_sources"] == nil {
		rawConfig["sync_sources"] = make(map[string]interface{})
	}
	sources, ok := rawConfig["sync_sources"].(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("sync_sources is not a map"), nil
	}

	sourceData := map[string]interface{}{
		"jql": jql,
	}
	if len(projects) > 0 {
		sourceData["projects"] = projects
	}
	sources[name] = sourceData

	// Write back
	if err := writeYAML(configPath, rawConfig); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to write config: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(`{"ok":true,"message":"added sync source '%s'"}`, name)), nil
}

// addView adds a new view to the config.
func addView(s *Server, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := request.GetString("name", "")
	if name == "" {
		return mcp.NewToolResultError("name is required for add_view"), nil
	}

	argsMap, ok := request.Params.Arguments.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("failed to parse arguments"), nil
	}

	valueRaw, ok := argsMap["value"]
	if !ok {
		return mcp.NewToolResultError("value is required for add_view"), nil
	}

	valueMap, ok := valueRaw.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("value must be an object"), nil
	}

	query, ok := valueMap["query"].(string)
	if !ok || query == "" {
		return mcp.NewToolResultError("value.query is required for add_view"), nil
	}

	configPath := getConfigPath(s.cfg)
	if err := backupConfig(configPath); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to backup config: %v", err)), nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read config: %v", err)), nil
	}

	var rawConfig map[string]interface{}
	if err := yaml.Unmarshal(data, &rawConfig); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse config: %v", err)), nil
	}

	// Add view
	if rawConfig["views"] == nil {
		rawConfig["views"] = make([]interface{}, 0)
	}
	views, ok := rawConfig["views"].([]interface{})
	if !ok {
		return mcp.NewToolResultError("views is not an array"), nil
	}

	viewData := map[string]interface{}{
		"name":  name,
		"query": query,
	}

	// Add optional fields
	if title, ok := valueMap["title"].(string); ok {
		viewData["title"] = title
	}
	if columns, ok := valueMap["columns"].([]interface{}); ok {
		viewData["columns"] = columns
	}
	if groupBy, ok := valueMap["group_by"].(string); ok {
		viewData["group_by"] = groupBy
	}

	views = append(views, viewData)
	rawConfig["views"] = views

	if err := writeYAML(configPath, rawConfig); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to write config: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(`{"ok":true,"message":"added view '%s'"}`, name)), nil
}

// addSnippet adds a new snippet to the config.
func addSnippet(s *Server, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := request.GetString("name", "")
	if name == "" {
		return mcp.NewToolResultError("name is required for add_snippet"), nil
	}

	argsMap, ok := request.Params.Arguments.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("failed to parse arguments"), nil
	}

	valueRaw, ok := argsMap["value"]
	if !ok {
		return mcp.NewToolResultError("value is required for add_snippet"), nil
	}

	valueMap, ok := valueRaw.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("value must be an object"), nil
	}

	query, ok := valueMap["query"].(string)
	if !ok || query == "" {
		return mcp.NewToolResultError("value.query is required for add_snippet"), nil
	}

	configPath := getConfigPath(s.cfg)
	if err := backupConfig(configPath); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to backup config: %v", err)), nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read config: %v", err)), nil
	}

	var rawConfig map[string]interface{}
	if err := yaml.Unmarshal(data, &rawConfig); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse config: %v", err)), nil
	}

	// Add snippet
	if rawConfig["snippets"] == nil {
		rawConfig["snippets"] = make(map[string]interface{})
	}
	snippets, ok := rawConfig["snippets"].(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("snippets is not a map"), nil
	}

	snippets[name] = query
	if err := writeYAML(configPath, rawConfig); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to write config: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(`{"ok":true,"message":"added snippet '%s'"}`, name)), nil
}

// addTemplate adds a new template to the config.
func addTemplate(s *Server, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := request.GetString("name", "")
	if name == "" {
		return mcp.NewToolResultError("name is required for add_template"), nil
	}

	argsMap, ok := request.Params.Arguments.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("failed to parse arguments"), nil
	}

	valueRaw, ok := argsMap["value"]
	if !ok {
		return mcp.NewToolResultError("value is required for add_template"), nil
	}

	valueMap, ok := valueRaw.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("value must be an object"), nil
	}

	description, ok := valueMap["description"].(string)
	if !ok || description == "" {
		return mcp.NewToolResultError("value.description is required for add_template"), nil
	}

	configPath := getConfigPath(s.cfg)
	if err := backupConfig(configPath); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to backup config: %v", err)), nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read config: %v", err)), nil
	}

	var rawConfig map[string]interface{}
	if err := yaml.Unmarshal(data, &rawConfig); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse config: %v", err)), nil
	}

	// Add template
	if rawConfig["templates"] == nil {
		rawConfig["templates"] = make(map[string]interface{})
	}
	templates, ok := rawConfig["templates"].(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("templates is not a map"), nil
	}

	templates[name] = description
	if err := writeYAML(configPath, rawConfig); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to write config: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(`{"ok":true,"message":"added template '%s'"}`, name)), nil
}

// setConfigValue sets a config value by dot-path key.
func setConfigValue(s *Server, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := request.GetString("key", "")
	if key == "" {
		return mcp.NewToolResultError("key is required for set action"), nil
	}

	// Security: block credential modification
	blockedKeys := []string{"jira.token", "jira.email", "jira.url"}
	for _, blocked := range blockedKeys {
		if strings.HasPrefix(key, blocked) {
			return mcp.NewToolResultError(fmt.Sprintf("modification of %s is not allowed", blocked)), nil
		}
	}

	// For simplicity, we only support a limited set of common settings
	supportedKeys := map[string]bool{
		"sync.interval":   true,
		"sync.rate_limit": true,
		"sync.history":    true,
		"me":              true,
		"team":            true,
	}

	if !supportedKeys[key] {
		return mcp.NewToolResultError(fmt.Sprintf("key '%s' is not supported for modification via MCP (edit config file directly)", key)), nil
	}

	argsMap, ok := request.Params.Arguments.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("failed to parse arguments"), nil
	}

	valueRaw, ok := argsMap["value"]
	if !ok {
		return mcp.NewToolResultError("value is required for set action"), nil
	}

	valueMap, ok := valueRaw.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("value must be an object"), nil
	}

	newValue, ok := valueMap["value"]
	if !ok {
		return mcp.NewToolResultError("value.value is required for set action"), nil
	}

	configPath := getConfigPath(s.cfg)
	if err := backupConfig(configPath); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to backup config: %v", err)), nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read config: %v", err)), nil
	}

	var rawConfig map[string]interface{}
	if err := yaml.Unmarshal(data, &rawConfig); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse config: %v", err)), nil
	}

	// Set value based on key
	parts := strings.Split(key, ".")
	if len(parts) == 1 {
		// Top-level key
		rawConfig[parts[0]] = newValue
	} else if len(parts) == 2 {
		// Nested key (e.g., sync.interval)
		if rawConfig[parts[0]] == nil {
			rawConfig[parts[0]] = make(map[string]interface{})
		}
		section, ok := rawConfig[parts[0]].(map[string]interface{})
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("config section '%s' is not a map", parts[0])), nil
		}
		section[parts[1]] = newValue
	}

	if err := writeYAML(configPath, rawConfig); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to write config: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(`{"ok":true,"message":"set %s"}`, key)), nil
}

// getConfigPath returns the config file path.
func getConfigPath(cfg *config.Config) string {
	// Try to detect from environment or use default
	if path := os.Getenv("JAI_CONFIG"); path != "" {
		return path
	}
	return config.DefaultConfigPath()
}

// backupConfig creates a backup of the config file.
func backupConfig(path string) error {
	backupPath := path + ".bak"

	// Open source file
	src, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening config: %w", err)
	}
	defer src.Close()

	// Create backup file
	dst, err := os.Create(backupPath)
	if err != nil {
		return fmt.Errorf("creating backup: %w", err)
	}
	defer dst.Close()

	// Copy
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copying to backup: %w", err)
	}

	return nil
}

// writeYAML writes a map to a YAML file.
func writeYAML(path string, data map[string]interface{}) error {
	out, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling YAML: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	if err := os.WriteFile(path, out, 0644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}

	return nil
}
