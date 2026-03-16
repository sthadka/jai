package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
	"github.com/sthadka/jai/internal/output"
	"github.com/sthadka/jai/internal/query"
	synce "github.com/sthadka/jai/internal/sync"
)

// globals holds shared state passed to sub-commands.
type globals struct {
	cfgPath string
	dbPath  string
	jsonOut bool
	noSync  bool
	fields  string

	cfg    *config.Config
	db     *db.DB
	jira   *jira.Client
	query  *query.Engine
	sync   *synce.Engine
}

var g globals

// noAutoSync lists commands that should never trigger auto-sync.
var noAutoSync = map[string]bool{
	"sync":       true,
	"init":       true,
	"schema":     true,
	"db":         true, // schema db sub-command
	"values":     true, // schema values sub-command
	"fields":     true,
	"completion": true,
	"help":       true,
}

func newRootCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "jai",
		Short: "Query Jira with SQL",
		Long:  "jai syncs Jira Cloud data to a local SQLite database and lets you query it with SQL.",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "help" || cmd.Name() == "completion" {
				return nil
			}

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

			if g.dbPath != "" {
				g.cfg.DB.Path = g.dbPath
			}

			database, err := db.Open(g.cfg.DB.Path)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			g.db = database

			g.jira = jira.New(cfg.Jira.URL, cfg.Jira.Email, cfg.Jira.Token, cfg.Sync.RateLimit)
			g.query = query.New(database, cfg)
			g.sync = synce.New(database, g.jira, cfg)

			if !g.noSync && !noAutoSync[cmd.Name()] {
				if shouldAutoSync(database, cfg) {
					runAutoSync(cmd.Context())
				}
			}

			return nil
		},
	}
}

var rootCmd = newRootCmd()

// shouldAutoSync returns true if any sync source's last sync is older than the configured interval.
func shouldAutoSync(database *db.DB, cfg *config.Config) bool {
	interval, err := time.ParseDuration(cfg.Sync.Interval)
	if err != nil {
		interval = 15 * time.Minute
	}

	for _, src := range cfg.SyncSources {
		meta, err := database.GetSyncMeta(src.Name)
		if err != nil || !meta.LastSyncTime.Valid || meta.LastSyncTime.String == "" {
			return true
		}
		t, err := time.Parse(time.RFC3339, meta.LastSyncTime.String)
		if err != nil || time.Since(t) > interval {
			return true
		}
	}
	return false
}

func runAutoSync(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	ch, err := g.sync.Sync(ctx, false, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync: %v\n", err)
		return
	}

	spinFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinIdx := 0
	total := 0
	var syncErr error

	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	// Drain channel in a goroutine, spinner runs on the ticker.
	done := make(chan struct{})
	go func() {
		for p := range ch {
			if p.Done {
				if p.Error != nil {
					syncErr = p.Error
				} else {
					total += p.New + p.Updated
				}
			}
		}
		close(done)
	}()

	fmt.Fprint(os.Stderr, spinFrames[0]+" Syncing...  ")
	for {
		select {
		case <-ticker.C:
			spinIdx++
			fmt.Fprintf(os.Stderr, "\r%s Syncing...  ", spinFrames[spinIdx%len(spinFrames)])
		case <-done:
			if syncErr != nil {
				fmt.Fprintf(os.Stderr, "\r✗ Sync failed: %v\n", syncErr)
			} else {
				fmt.Fprintf(os.Stderr, "\r✓ Synced %d issues\n%s\n", total, strings.Repeat("─", 40))
			}
			return
		}
	}
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
	rootCmd.PersistentFlags().StringVar(&g.fields, "fields", "", "comma-separated field names to include in output")
}

// jsonErr prints a JSON error envelope to stdout and exits.
func jsonErr(errType, msg string) {
	fmt.Println(string(output.Err(errType, msg)))
	os.Exit(1)
}

// jsonError is an alias kept for backward compat within this package.
func jsonError(errType, msg string) {
	jsonErr(errType, msg)
}
