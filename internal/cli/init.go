package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
	synce "github.com/sthadka/jai/internal/sync"
)

// ── ANSI helpers ──────────────────────────────────────────────────────────────

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiRed    = "\033[31m"
)

func ansi(code, s string) string { return code + s + ansiReset }
func bold(s string) string       { return ansi(ansiBold, s) }
func dim(s string) string        { return ansi(ansiDim, s) }
func green(s string) string      { return ansi(ansiGreen, s) }
func cyan(s string) string       { return ansi(ansiCyan, s) }
func yellow(s string) string     { return ansi(ansiYellow, s) }
func red(s string) string        { return ansi(ansiRed, s) }

func stepOK(msg string)   { fmt.Printf("  %s %s\n", green("✓"), msg) }
func stepFail(msg string) { fmt.Printf("  %s %s\n", red("✗"), msg) }
func stepInfo(msg string) { fmt.Printf("  %s %s\n", dim("→"), msg) }
func stepWarn(msg string) { fmt.Printf("  %s %s\n", yellow("!"), msg) }

// boxLine prints a box content row, padding to boxContentWidth visible columns.
// colored is the ANSI-escaped string; visibleLen is its display width (no escape codes).
const boxContentWidth = 42

func boxLine(colored, plain string) {
	padding := strings.Repeat(" ", boxContentWidth-utf8.RuneCountInString(plain))
	fmt.Printf("  %s  %s%s%s\n", bold("│"), colored, padding, bold("│"))
}

const initStepTotal = 4

func initStep(n int, label string) {
	fmt.Printf("\n  %s %s\n", cyan(fmt.Sprintf("[%d/%d]", n, initStepTotal)), bold(label))
	fmt.Printf("  %s\n", strings.Repeat("─", 44))
}

// ── Command ───────────────────────────────────────────────────────────────────

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Interactive setup wizard",
	// Override PersistentPreRunE — init doesn't require existing config.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
	RunE:              runInit,
}

// prompt prints a styled prompt with an optional default and reads a line.
func prompt(reader *bufio.Reader, label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("  %s %s %s: ", cyan("?"), label, dim(fmt.Sprintf("[%s]", defaultVal)))
	} else {
		fmt.Printf("  %s %s: ", cyan("?"), label)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

// ── Wizard ────────────────────────────────────────────────────────────────────

func runInit(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)
	ctx := context.Background()

	// ── Banner ────────────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Printf("  %s\n", bold("╭────────────────────────────────────────────╮"))
	boxLine(cyan(bold("jai — first-run setup")), "jai — first-run setup")
	boxLine(dim("Query Jira with SQL"), "Query Jira with SQL")
	fmt.Printf("  %s\n", bold("╰────────────────────────────────────────────╯"))
	fmt.Println()
	fmt.Printf("  All Jira data is synced locally — fast queries, no rate limits.\n")

	// Load existing config to pre-populate prompts.
	var existing *config.Config
	if cfg, err := config.Load(config.DefaultConfigPath()); err == nil {
		existing = cfg
	}
	defaultFor := func(field string) string {
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

	// ── Step 1: Credentials ───────────────────────────────────────────────────
	initStep(1, "Jira credentials")

	jiraURL := prompt(reader, "Jira URL", defaultFor("url"))
	email := prompt(reader, "Email", defaultFor("email"))

	existingToken := defaultFor("token")
	tokenDefault := ""
	if existingToken != "" {
		tokenDefault = "<existing — press Enter to keep>"
	}
	tokenInput := prompt(reader, "API Token "+dim("(stored as ${JAI_TOKEN})"), tokenDefault)
	token := tokenInput
	if tokenInput == tokenDefault && existingToken != "" {
		token = existingToken
	}

	// ── Step 2: Test connection ───────────────────────────────────────────────
	initStep(2, "Test connection")

	stepInfo("Connecting to " + bold(jiraURL) + "...")
	jiraClient := jira.New(jiraURL, email, token, 10)
	me, err := jiraClient.MySelf(ctx)
	if err != nil {
		stepFail("Connection failed: " + err.Error())
		fmt.Println()
		stepInfo("Check your URL, email, and API token and try again.")
		return fmt.Errorf("connection failed: %w", err)
	}
	stepOK("Connected as " + bold(me.DisplayName))
	stepOK("Account: " + dim(me.EmailAddress))

	// Write config file.
	cfgPath := config.DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0700); err != nil {
		stepFail("Could not create config directory: " + err.Error())
		return fmt.Errorf("creating config directory: %w", err)
	}

	// ── Step 3: Sync sources ──────────────────────────────────────────────────
	initStep(3, "Sync sources")

	stepInfo("Sync sources define which Jira issues to download.")
	stepInfo("Each source needs a name and a JQL filter.")
	fmt.Println()
	fmt.Printf("  %s\n", dim("Examples:  project = ROX"))
	fmt.Printf("  %s\n", dim("           project in (ROX, ACS) AND team = \"Platform\""))
	fmt.Println()

	var sources []config.SyncSource

	if existing != nil && len(existing.SyncSources) > 0 {
		fmt.Printf("  %s Existing sources %s\n", cyan("→"), dim("(press Enter to keep)"))
		fmt.Println()
		for _, s := range existing.SyncSources {
			name := prompt(reader, "Source name", s.Name)
			jql := prompt(reader, "JQL filter", s.JQL)
			if name != "" && jql != "" {
				sources = append(sources, config.SyncSource{Name: name, JQL: jql})
				stepOK(bold(name) + "  " + dim(jql))
			}
		}
	}

	for {
		if len(sources) > 0 {
			more := prompt(reader, "Add another source?", "n")
			if !strings.HasPrefix(strings.ToLower(more), "y") {
				break
			}
			fmt.Println()
		}
		name := prompt(reader, "Source name "+dim("(e.g. my-team)"), "")
		jql := prompt(reader, "JQL filter  "+dim("(e.g. project = ROX)"), "")
		if name == "" || jql == "" {
			stepWarn("Both name and JQL are required — skipping.")
			continue
		}
		sources = append(sources, config.SyncSource{Name: name, JQL: jql})
		stepOK(bold(name) + "  " + dim(jql))
	}

	if len(sources) == 0 {
		return fmt.Errorf("at least one sync source is required")
	}

	// Persist config now that we have everything.
	cfgContent := buildConfigYAML(jiraURL, email, me.EmailAddress, sources)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		stepFail("Could not write config: " + err.Error())
		return fmt.Errorf("writing config: %w", err)
	}
	stepOK("Config saved: " + dim(cfgPath))
	stepWarn("Add to your shell profile: " + bold("export JAI_TOKEN=<your-api-token>"))

	// ── Step 4: Initial sync ──────────────────────────────────────────────────
	initStep(4, "Initial sync")

	cfg := &config.Config{
		Jira:        config.JiraConfig{URL: jiraURL, Email: email, Token: token},
		SyncSources: sources,
		Sync:        config.SyncConfig{Interval: "15m", RateLimit: 10},
		Me:          me.EmailAddress,
		DB:          config.DBConfig{Path: config.DefaultDBPath()},
	}

	database, err := db.Open(cfg.DB.Path)
	if err != nil {
		stepFail("Could not open database: " + err.Error())
		return fmt.Errorf("opening database: %w", err)
	}
	defer database.Close()

	engine := synce.New(database, jiraClient, cfg)

	stepInfo("Discovering Jira fields...")
	if err := engine.DiscoverFields(ctx, nil); err != nil {
		stepWarn("Field discovery: " + err.Error())
	} else {
		stepOK("Fields discovered")
	}

	fmt.Println()
	stepInfo(fmt.Sprintf("Syncing %d source(s) — this may take a few minutes...", len(sources)))
	fmt.Println()

	ch, err := engine.Sync(ctx, true, false, "")
	if err != nil {
		stepFail("Sync failed: " + err.Error())
		return fmt.Errorf("sync failed: %w", err)
	}
	total := displaySyncProgress(ch)
	stepOK(fmt.Sprintf("%d issues synced", total))

	// ── Done ──────────────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Printf("  %s\n", bold("╭────────────────────────────────────────────╮"))
	boxLine(green(bold("Setup complete!")), "Setup complete!")
	fmt.Printf("  %s\n", bold("╰────────────────────────────────────────────╯"))
	fmt.Println()
	fmt.Printf("  %s\n", bold("Quick reference:"))
	fmt.Println()

	type ref struct{ cmd, desc string }
	refs := []ref{
		{"jai query \"SELECT key, summary, status FROM issues LIMIT 10\"", "Run a SQL query"},
		{"jai tui", "Open the full-screen TUI"},
		{"jai get ROX-123", "Fetch a single issue"},
		{"jai sync", "Incremental sync (new/updated issues)"},
		{"jai sync --full", "Full resync of all issues"},
		{"jai schema", "Inspect available columns"},
		{"jai --help", "Show all commands"},
	}

	for _, r := range refs {
		fmt.Printf("  %-60s %s\n", cyan(r.cmd), dim(r.desc))
	}

	fmt.Println()
	fmt.Printf("  Config: %s\n", dim(cfgPath))
	fmt.Printf("  DB:     %s\n", dim(cfg.DB.Path))
	fmt.Println()

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
