package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/syethadk/jai/internal/config"
	"github.com/syethadk/jai/internal/db"
	"github.com/syethadk/jai/internal/jira"
	"github.com/syethadk/jai/internal/query"
	synce "github.com/syethadk/jai/internal/sync"
)

// globals holds shared state passed to sub-commands.
type globals struct {
	cfgPath string
	dbPath  string
	jsonOut bool
	noSync  bool

	cfg    *config.Config
	db     *db.DB
	jira   *jira.Client
	query  *query.Engine
	sync   *synce.Engine
}

var g globals

var rootCmd = &cobra.Command{
	Use:   "jai",
	Short: "Query Jira with SQL",
	Long:  "jai syncs Jira Cloud data to a local SQLite database and lets you query it with SQL.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Skip init for commands that don't need DB/config.
		skip := map[string]bool{"help": true, "completion": true}
		if skip[cmd.Name()] {
			return nil
		}

		// Load config.
		cfgPath := g.cfgPath
		if cfgPath == "" {
			cfgPath = config.DefaultConfigPath()
		}

		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("loading config: %w\n\nRun 'jai init' to set up jai.", err)
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		g.cfg = cfg

		// Override DB path if specified.
		if g.dbPath != "" {
			g.cfg.DB.Path = g.dbPath
		}

		// Open DB.
		database, err := db.Open(g.cfg.DB.Path)
		if err != nil {
			return fmt.Errorf("opening database: %w", err)
		}
		g.db = database

		// Initialize Jira client.
		g.jira = jira.New(cfg.Jira.URL, cfg.Jira.Email, cfg.Jira.Token, cfg.Sync.RateLimit)

		// Initialize query engine.
		g.query = query.New(database, cfg)

		// Initialize sync engine.
		g.sync = synce.New(database, g.jira, cfg)

		return nil
	},
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&g.cfgPath, "config", "", "config file (default: ~/.config/jai/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&g.dbPath, "db", "", "database file path")
	rootCmd.PersistentFlags().BoolVar(&g.jsonOut, "json", false, "output as JSON")
	rootCmd.PersistentFlags().BoolVar(&g.noSync, "no-sync", false, "skip auto-sync")
}

// exitError prints an error and exits with code 1.
func exitError(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

// jsonError prints a JSON error envelope and exits.
func jsonError(errType, msg string) {
	fmt.Printf(`{"ok":false,"error":{"type":%q,"message":%q}}%s`, errType, msg, "\n")
	os.Exit(1)
}
