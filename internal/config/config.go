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
	Jira   JiraConfig   `yaml:"jira"`
	Sync   SyncConfig   `yaml:"sync"`
	DB     DBConfig     `yaml:"db"`
	Fields FieldsConfig `yaml:"fields"`
	Views  []ViewConfig `yaml:"views"`
	Me     string       `yaml:"me"`
	Team   string       `yaml:"team"`
}

// JiraConfig holds Jira connection settings.
type JiraConfig struct {
	URL      string   `yaml:"url"`
	Email    string   `yaml:"email"`
	Token    string   `yaml:"token"`
	Projects []string `yaml:"projects"`
}

// SyncConfig holds sync behavior settings.
type SyncConfig struct {
	Interval     string   `yaml:"interval"`      // e.g. "15m"
	RateLimit    float64  `yaml:"rate_limit"`    // requests per second
	History      bool     `yaml:"history"`       // sync changelog
	FTSFields    []string `yaml:"fts_fields"`    // extra fields for FTS index
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
	}

	return cfg, nil
}

// defaults returns a Config with sensible default values.
func defaults() *Config {
	return &Config{
		Sync: SyncConfig{
			Interval:  "15m",
			RateLimit: 10,
		},
		DB: DBConfig{
			Path: DefaultDBPath(),
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
	if len(c.Jira.Projects) == 0 {
		errs = append(errs, "jira.projects is required (at least one project key)")
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
