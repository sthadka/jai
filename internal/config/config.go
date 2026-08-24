package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "jai", "config.yaml")
}

// DefaultDBPath returns the default database file path.
func DefaultDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "jai", "jai.db")
}

// Config holds the full jai configuration.
type Config struct {
	Jira        JiraConfig      `yaml:"jira"`
	Sync        SyncConfig      `yaml:"sync"`
	DB          DBConfig        `yaml:"db"`
	Fields      FieldsConfig    `yaml:"fields"`
	Views       []ViewConfig    `yaml:"views"`
	Me          string          `yaml:"me"`
	Team        string          `yaml:"team"`
	SyncSources []SyncSource    `yaml:"sync_sources"`
	Hierarchy   HierarchyConfig   `yaml:"hierarchy"`
	Detail      DetailConfig      `yaml:"detail"`
	Templates   map[string]string `yaml:"templates"`
	Snippets    map[string]string `yaml:"snippets"`
	MCP         MCPConfig         `yaml:"mcp"`
}

// HierarchyLevel defines one level in the issue hierarchy.
type HierarchyLevel struct {
	Name string `yaml:"name"`
	JQL  string `yaml:"jql"`
}

// HierarchyConfig defines the hierarchy tree view.
// From/To are level names; levels between them (inclusive) are shown.
type HierarchyConfig struct {
	Levels []HierarchyLevel `yaml:"levels"`
	From   string           `yaml:"from"` // topmost level name to display
	To     string           `yaml:"to"`   // bottommost level name to display
}

// DetailConfig controls the issue detail pane.
type DetailConfig struct {
	// SidebarFields lists Jira field display names (jira_name in field_map)
	// to include in the right sidebar, in addition to the defaults.
	// Example: ["Contributors", "Product Manager"]
	SidebarFields []string `yaml:"sidebar_fields"`
}

// SyncSource defines a named set of issues to sync.
// Set JQL for a raw query, or Projects for a shorthand project-key list.
// If both are set, JQL takes precedence.
// If neither SyncSources nor jira.projects is set, sync_sources are derived from jira.projects.
type SyncSource struct {
	Name     string   `yaml:"name"`
	JQL      string   `yaml:"jql"`
	Projects []string `yaml:"projects"`
}

// JiraConfig holds Jira connection settings.
type JiraConfig struct {
	URL   string `yaml:"url"`
	Email string `yaml:"email"`
	Token string `yaml:"token"`
}

// SyncConfig holds sync behavior settings.
type SyncConfig struct {
	Interval     string   `yaml:"interval"`      // e.g. "15m"
	RateLimit    float64  `yaml:"rate_limit"`    // requests per second
	History      bool     `yaml:"history"`       // sync changelog
	FTSFields    []string `yaml:"fts_fields"`    // extra fields for FTS index
	Sprints      bool     `yaml:"sprints"`       // sync sprint and board data
}

// DBConfig holds database settings.
type DBConfig struct {
	Path string `yaml:"path"`
}

// FieldsConfig holds custom field name overrides.
type FieldsConfig struct {
	Overrides map[string]string `yaml:"overrides"` // jira_id → readable name
}

// ViewConfig defines a named query view.
type ViewConfig struct {
	Name          string      `yaml:"name"`
	Title         string      `yaml:"title"`
	Query         string      `yaml:"query"`
	Columns       []string    `yaml:"columns"`
	GroupBy       string      `yaml:"group_by"`
	ColorRules    []ColorRule `yaml:"color_rules"`
	StatusSummary bool        `yaml:"status_summary"`
	SortBy        string      `yaml:"sort_by"`
	SortDesc      bool        `yaml:"sort_desc"`
}

// ColorRule defines a conditional row color in TUI views.
type ColorRule struct {
	Field     string `yaml:"field"`
	Condition string `yaml:"condition"` // older_than, equals, not_equals, contains, in
	Value     string `yaml:"value"`
	Color     string `yaml:"color"` // lipgloss color string
}

// MCPConfig holds MCP server settings.
type MCPConfig struct {
	ReadOnly bool     `yaml:"read_only"` // block write operations
	Toolsets []string `yaml:"toolsets"`  // enabled toolsets: read,schema,write,sync,browser,config
}

var envVarRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// substituteEnvVars replaces ${VAR} with environment variable values.
func substituteEnvVars(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(m string) string {
		key := m[2 : len(m)-1] // strip ${ and }
		if val, ok := os.LookupEnv(key); ok {
			return val
		}
		return m
	})
}

// Load reads and parses the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	// Substitute env vars before parsing YAML.
	expanded := substituteEnvVars(string(data))

	cfg := defaults()
	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	if cfg.DB.Path == "" {
		cfg.DB.Path = DefaultDBPath()
	} else if strings.HasPrefix(cfg.DB.Path, "~/") {
		home, _ := os.UserHomeDir()
		cfg.DB.Path = filepath.Join(home, cfg.DB.Path[2:])
	}

	return cfg, nil
}

// defaults returns a Config with sensible default values.
func defaults() *Config {
	return &Config{
		Sync: SyncConfig{
			Interval:  "15m",
			RateLimit: 10,
			Sprints:   true,
		},
		DB: DBConfig{
			Path: DefaultDBPath(),
		},
		Snippets: map[string]string{
			"cycle_time": `SELECT key, summary,
  CAST(julianday(
    (SELECT MIN(c.changed_at) FROM changelog c WHERE c.issue_key = issues.key AND c.field = 'status' AND c.to_string IN ('Done','Closed'))
  ) - julianday(
    (SELECT MIN(c.changed_at) FROM changelog c WHERE c.issue_key = issues.key AND c.field = 'status' AND c.to_string = 'In Progress')
  ) AS INTEGER) as cycle_days
FROM issues
WHERE status IN ('Done','Closed')`,
			"time_in_status": `SELECT c1.issue_key, c1.to_string as status,
  CAST(julianday(COALESCE(c2.changed_at, datetime('now'))) - julianday(c1.changed_at) AS REAL) as days
FROM changelog c1
LEFT JOIN changelog c2 ON c1.issue_key = c2.issue_key
  AND c2.field = 'status' AND c2.changed_at > c1.changed_at
  AND NOT EXISTS (
    SELECT 1 FROM changelog cx WHERE cx.issue_key = c1.issue_key
    AND cx.field = 'status' AND cx.changed_at > c1.changed_at AND cx.changed_at < c2.changed_at
  )
WHERE c1.field = 'status'`,
			"reassignment_count": `SELECT issue_key, COUNT(*) as reassignments
FROM changelog
WHERE field = 'assignee'
GROUP BY issue_key
HAVING reassignments > 1
ORDER BY reassignments DESC`,
			"reopened_issues": `SELECT issue_key, COUNT(*) as reopen_count
FROM changelog
WHERE field = 'status' AND from_string IN ('Done','Closed','Resolved')
  AND to_string NOT IN ('Done','Closed','Resolved')
GROUP BY issue_key
ORDER BY reopen_count DESC`,
		},
	}
}

// Validate checks that required fields are present.
func (c *Config) Validate() error {
	var errs []string

	if c.Jira.URL == "" {
		errs = append(errs, "jira.url is required")
	}
	if c.Jira.Email == "" {
		errs = append(errs, "jira.email is required")
	}
	if c.Jira.Token == "" {
		errs = append(errs, "jira.token is required (use ${JAI_TOKEN} env var)")
	}
	if len(c.SyncSources) == 0 {
		errs = append(errs, "sync_sources is required — run 'jai init' to configure")
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
