package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
	synce "github.com/sthadka/jai/internal/sync"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Interactive setup wizard",
	// Override PersistentPreRunE — init doesn't require existing config.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
	RunE:              runInit,
}

// prompt prints a prompt with an optional default value and reads a line.
// If the user presses Enter without input, the default is returned.
func prompt(reader *bufio.Reader, label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

func runInit(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("Welcome to jai! Let's get you set up.")
	fmt.Println()

	// Load existing config if present, to pre-populate prompts.
	var existing *config.Config
	if cfg, err := config.Load(config.DefaultConfigPath()); err == nil {
		existing = cfg
	}
	defaults := func(field string) string {
		if existing == nil {
			return ""
		}
		switch field {
		case "url":
			return existing.Jira.URL
		case "email":
			return existing.Jira.Email
		case "token":
			return existing.Jira.Token
		}
		return ""
	}

	// Jira URL.
	jiraURL := prompt(reader, "Jira URL (e.g. https://mycompany.atlassian.net)", defaults("url"))

	// Email.
	email := prompt(reader, "Email", defaults("email"))

	// API Token — show a placeholder if one is already set, never the actual value.
	existingToken := defaults("token")
	var tokenDefault string
	if existingToken != "" {
		tokenDefault = "<existing token — press Enter to keep>"
	}
	tokenInput := prompt(reader, "API Token (will be referenced as ${JAI_TOKEN})", tokenDefault)
	var token string
	if tokenInput == tokenDefault && existingToken != "" {
		token = existingToken
	} else {
		token = tokenInput
	}

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

	// Sync sources.
	fmt.Println("Sync sources define which Jira issues to sync.")
	fmt.Println("Each source needs a name and a JQL filter.")
	fmt.Println("Example JQL: project = ROX")
	fmt.Println("             project in (ROX, ACS) AND team = \"Platform\"")
	fmt.Println()

	var sources []config.SyncSource
	// Pre-populate from existing config if present.
	if existing != nil && len(existing.SyncSources) > 0 {
		fmt.Println("Existing sync sources (press Enter to keep each):")
		for _, s := range existing.SyncSources {
			name := prompt(reader, "  Source name", s.Name)
			jql := prompt(reader, "  JQL filter", s.JQL)
			if name != "" && jql != "" {
				sources = append(sources, config.SyncSource{Name: name, JQL: jql})
			}
		}
	}

	for {
		if len(sources) > 0 {
			addMore := prompt(reader, "Add another source?", "n")
			if !strings.HasPrefix(strings.ToLower(addMore), "y") {
				break
			}
		}
		name := prompt(reader, "  Source name (e.g. my-team)", "")
		jql := prompt(reader, "  JQL filter (e.g. project = ROX)", "")
		if name == "" || jql == "" {
			fmt.Println("  Both name and JQL are required — skipping.")
			continue
		}
		sources = append(sources, config.SyncSource{Name: name, JQL: jql})
	}

	if len(sources) == 0 {
		return fmt.Errorf("at least one sync source is required")
	}

	// Build config content.
	cfgContent := buildConfigYAML(jiraURL, email, me.EmailAddress, sources)

	// Write config file.
	cfgPath := config.DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	fmt.Printf("Config saved to %s\n", cfgPath)
	fmt.Printf("Add to your shell profile: export JAI_TOKEN=<your-api-token>\n\n")

	// Build a minimal config struct for sync.
	cfg := &config.Config{
		Jira: config.JiraConfig{
			URL:   jiraURL,
			Email: email,
			Token: token,
		},
		SyncSources: sources,
		Sync:        config.SyncConfig{Interval: "15m", RateLimit: 10},
		Me:          me.EmailAddress,
		DB:          config.DBConfig{Path: config.DefaultDBPath()},
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
	fmt.Printf("Syncing %d source(s) (this may take a few minutes)...\n", len(sources))
	ch, err := engine.Sync(ctx, true, "")
	if err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	total := displaySyncProgress(ch)

	fmt.Printf("\nSync complete! %d issues synced.\n\n", total)
	fmt.Println("Next steps:")
	fmt.Printf("  jai query \"SELECT key, summary, status FROM issues LIMIT 10\"\n")
	fmt.Printf("  jai tui\n")
	fmt.Printf("  jai --help\n")
	return nil
}

func buildConfigYAML(jiraURL, email, meEmail string, sources []config.SyncSource) string {
	var sb strings.Builder
	sb.WriteString("jira:\n")
	sb.WriteString(fmt.Sprintf("  url: %s\n", jiraURL))
	sb.WriteString(fmt.Sprintf("  email: %s\n", email))
	sb.WriteString("  token: ${JAI_TOKEN}\n")
	sb.WriteString("\nsync:\n  interval: 15m\n  rate_limit: 10\n")
	sb.WriteString(fmt.Sprintf("\nme: %s\n", meEmail))
	sb.WriteString("\nsync_sources:\n")
	for _, s := range sources {
		sb.WriteString(fmt.Sprintf("  - name: %s\n", s.Name))
		sb.WriteString(fmt.Sprintf("    jql: %s\n", s.JQL))
	}
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
