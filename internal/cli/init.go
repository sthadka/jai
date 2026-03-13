package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/syethadk/jai/internal/config"
	"github.com/syethadk/jai/internal/db"
	"github.com/syethadk/jai/internal/jira"
	synce "github.com/syethadk/jai/internal/sync"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Interactive setup wizard",
	// Override PersistentPreRunE — init doesn't require existing config.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
	RunE:              runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("Welcome to jai! Let's get you set up.")
	fmt.Println()

	// Jira URL.
	fmt.Print("Jira URL (e.g. https://mycompany.atlassian.net): ")
	jiraURL, _ := reader.ReadString('\n')
	jiraURL = strings.TrimSpace(jiraURL)

	// Email.
	fmt.Print("Email: ")
	email, _ := reader.ReadString('\n')
	email = strings.TrimSpace(email)

	// API Token.
	fmt.Print("API Token (will be referenced as ${JAI_TOKEN}): ")
	token, _ := reader.ReadString('\n')
	token = strings.TrimSpace(token)

	// Test connection.
	fmt.Print("Testing connection... ")
	client := jira.New(jiraURL, email, token, 10)
	ctx := context.Background()
	me, err := client.MySelf(ctx)
	if err != nil {
		fmt.Println("FAILED")
		return fmt.Errorf("connection failed: %w\n\nCheck your URL, email, and API token", err)
	}
	fmt.Printf("Connected as %s\n\n", me.DisplayName)

	// Project keys.
	fmt.Print("Project keys (comma-separated, e.g. ROX,ACS): ")
	projectsStr, _ := reader.ReadString('\n')
	projectsStr = strings.TrimSpace(projectsStr)
	var projects []string
	for _, p := range strings.Split(projectsStr, ",") {
		p = strings.TrimSpace(strings.ToUpper(p))
		if p != "" {
			projects = append(projects, p)
		}
	}
	if len(projects) == 0 {
		return fmt.Errorf("at least one project is required")
	}

	// Build config content.
	cfgContent := buildConfigYAML(jiraURL, email, me.EmailAddress, projects)

	// Write config file.
	cfgPath := config.DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	fmt.Printf("Config saved to %s\n", cfgPath)
	fmt.Printf("Add to your shell profile: export JAI_TOKEN=%q\n\n", token)

	// Build a minimal config struct for sync.
	cfg := &config.Config{
		Jira: config.JiraConfig{
			URL:      jiraURL,
			Email:    email,
			Token:    token,
			Projects: projects,
		},
		Sync: config.SyncConfig{Interval: "15m", RateLimit: 10},
		Me:   me.EmailAddress,
		DB:   config.DBConfig{Path: config.DefaultDBPath()},
	}

	// Open database.
	database, err := db.Open(cfg.DB.Path)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer database.Close()

	// Discover fields.
	fmt.Print("Discovering fields... ")
	engine := synce.New(database, client, cfg)
	if err := engine.DiscoverFields(ctx, nil); err != nil {
		fmt.Printf("WARNING: %v\n", err)
	} else {
		fmt.Println("done")
	}

	// Initial sync.
	fmt.Printf("Syncing %d project(s) (this may take a few minutes)...\n", len(projects))
	ch, err := engine.Sync(ctx, true)
	if err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	total := 0
	for p := range ch {
		if p.Error != nil {
			fmt.Printf("  %s: ERROR %v\n", p.Project, p.Error)
		} else {
			fmt.Printf("  %s: %d issues synced\n", p.Project, p.New + p.Updated)
			total += p.New + p.Updated
		}
	}

	fmt.Printf("\nSync complete! %d issues synced.\n\n", total)
	fmt.Println("Next steps:")
	fmt.Printf("  jai query \"SELECT key, summary, status FROM issues LIMIT 10\"\n")
	fmt.Printf("  jai tui\n")
	fmt.Printf("  jai --help\n")
	return nil
}

func buildConfigYAML(jiraURL, email, meEmail string, projects []string) string {
	var sb strings.Builder
	sb.WriteString("jira:\n")
	sb.WriteString(fmt.Sprintf("  url: %s\n", jiraURL))
	sb.WriteString(fmt.Sprintf("  email: %s\n", email))
	sb.WriteString("  token: ${JAI_TOKEN}\n")
	sb.WriteString("  projects:\n")
	for _, p := range projects {
		sb.WriteString(fmt.Sprintf("    - %s\n", p))
	}
	sb.WriteString("\nsync:\n  interval: 15m\n  rate_limit: 10\n")
	sb.WriteString(fmt.Sprintf("\nme: %s\n", meEmail))
	sb.WriteString(`
views:
  - name: my-work
    title: My Work
    query: |
      SELECT key, summary, status, priority, updated
      FROM issues
      WHERE assignee_email = '{{me}}'
      AND status_category != 'Done'
      ORDER BY priority DESC, updated DESC
    columns: [key, summary, status, priority]
    status_summary: true

  - name: recent-updates
    title: Recent Updates
    query: |
      SELECT key, summary, status, assignee, updated
      FROM issues
      ORDER BY updated DESC
      LIMIT 100
    columns: [key, summary, status, assignee, updated]

  - name: team-board
    title: Team Board
    query: |
      SELECT key, summary, status, assignee, priority
      FROM issues
      WHERE status_category != 'Done'
      ORDER BY status, priority DESC
    columns: [key, summary, status, assignee]
    group_by: status
    status_summary: true
`)
	return sb.String()
}

func init() {
	rootCmd.AddCommand(initCmd)
}
