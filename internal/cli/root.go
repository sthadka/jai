package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/v7/internal/cli/configloader"
	"github.com/sthadka/jai/v7/pkg/config"
	"github.com/sthadka/jai/v7/pkg/core"
	"github.com/sthadka/jai/v7/pkg/jira"
	db "github.com/sthadka/jai/v7/pkg/store"
	synce "github.com/sthadka/jai/v7/pkg/sync"
)

// CLI holds one invocation's flag values and the assembled engine. It replaces
// the former process-global `var g globals`: Execute constructs exactly one CLI
// and threads it to every subcommand through the command context (see cliFrom),
// so no package reaches for process-global engine state.
type CLI struct {
	cfgPath  string
	dbPath   string
	jsonOut  bool
	rawOut   bool
	noSync   bool
	waitSync bool
	fields   string
	format   string

	engine   *core.Engine
	bgWorker *synce.BackgroundWorker
}

// cliCtxKey is the context key under which the active *CLI is stored.
type cliCtxKey struct{}

// cliFrom returns the *CLI threaded through the command context by the root
// command's PersistentPreRunE. It is the accessor every subcommand uses instead
// of a package global.
func cliFrom(cmd *cobra.Command) *CLI {
	if v, ok := cmd.Context().Value(cliCtxKey{}).(*CLI); ok {
		return v
	}
	return nil
}

// noAutoSync lists commands that should never trigger auto-sync.
var noAutoSync = map[string]bool{
	"sync":       true,
	"init":       true,
	"clone":      true,
	"create":     true,
	"schema":     true,
	"db":         true, // schema db sub-command
	"values":     true, // schema values sub-command
	"snippets":   true, // schema snippets sub-command
	"fields":     true,
	"completion": true,
	"open":       true,
	"help":       true,
	"serve":      true, // MCP server with background sync
	"changelog":  true,
	"prune":      true, // sync prune sub-command: never sync before deleting
	"metrics":    true, // reads/rebuilds local facts; never auto-sync first
	"prime":      true, // reports freshness; must not block on a sync
}

// noDBRequired lists `db` sub-commands that only need config loaded, not an
// open DB connection — they operate on the DB file directly (reset) or don't
// touch it at all (path), so they must work even if the DB is corrupt/missing.
var noDBRequired = map[string]bool{
	"reset": true,
	"path":  true,
}

// rootCmd is the cobra command tree. Subcommand files register themselves on it
// in their init funcs; Execute binds an invocation's CLI to it.
var rootCmd = &cobra.Command{
	Use:   "jai",
	Short: "Query Jira with SQL",
	Long:  "jai syncs Jira Cloud data to a local SQLite database and lets you query it with SQL.",
}

// bindRoot wires one invocation's CLI to the root command: persistent flags bind
// to app's fields, and PersistentPreRunE loads config, assembles the engine via
// core.New, and stores app in the command context for subcommands to retrieve.
func bindRoot(root *cobra.Command, app *CLI) {
	root.PersistentFlags().StringVar(&app.cfgPath, "config", "", "config file (default: ~/.config/jai/config.yaml)")
	root.PersistentFlags().StringVar(&app.dbPath, "db", "", "database file path")
	root.PersistentFlags().BoolVar(&app.jsonOut, "json", false, "output as JSON")
	root.PersistentFlags().BoolVar(&app.rawOut, "raw", false, "dump full Jira API JSON (get, query --jql)")
	root.PersistentFlags().BoolVar(&app.noSync, "no-sync", false, "skip auto-sync")
	root.PersistentFlags().BoolVar(&app.waitSync, "wait-sync", false, "wait for sync to complete before returning results")
	root.PersistentFlags().StringVar(&app.fields, "fields", "", "comma-separated field names to include in output")
	root.PersistentFlags().StringVar(&app.format, "format", "table", "output format: table, json, csv, tsv, markdown")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// Make the invocation's CLI available to every subcommand's RunE.
		cmd.SetContext(context.WithValue(cmd.Context(), cliCtxKey{}, app))

		if cmd.Name() == "help" || cmd.Name() == "completion" {
			return nil
		}

		cfgPath := app.cfgPath
		if cfgPath == "" {
			cfgPath = config.DefaultConfigPath()
		}

		cfg, err := configloader.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("loading config: %w -- run 'jai init' to set up jai", err)
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		if app.dbPath != "" {
			cfg.DB.Path = app.dbPath
		}

		client := jira.New(cfg.Jira.URL, cfg.Jira.Email, cfg.Jira.Token, cfg.Sync.RateLimit)

		underDB := cmd.Parent() != nil && cmd.Parent().Name() == "db"
		if underDB && noDBRequired[cmd.Name()] {
			// No DB connection needed; assemble an engine with config+client only.
			app.engine = core.New(cfg, nil, client)
			return nil
		}

		database, err := db.Open(cfg.DB.Path)
		if err != nil {
			return fmt.Errorf("opening database: %w", err)
		}
		app.engine = core.New(cfg, database, client)

		if !app.noSync && !noAutoSync[cmd.Name()] && !underDB {
			if shouldAutoSync(database, cfg) {
				app.runAutoSync(cmd.Context())
			}
		}

		return nil
	}
}

// shouldAutoSync returns true if any sync source's last sync is older than the configured interval.
func shouldAutoSync(database db.Store, cfg *config.Config) bool {
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

func (app *CLI) runAutoSync(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Verify authentication before auto-syncing. If credentials are invalid,
	// exit rather than silently serving stale local data or letting an
	// anonymous sync pull the wrong results — Jira returns very different data
	// depending on auth. Transient (non-auth) failures fall through so cached
	// reads still work offline; run with --no-sync to skip this entirely.
	if err := app.engine.Sync.VerifyAuth(ctx); err != nil {
		var authErr *jira.AuthError
		if errors.As(err, &authErr) {
			fmt.Fprintf(os.Stderr, "✗ Jira authentication failed: %v\n", authErr)
			fmt.Fprintln(os.Stderr, "  Check jira.email and jira.token (run 'jai status'), or use --no-sync to work offline.")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "sync: %v\n", err)
		return
	}

	// If --wait-sync is set, use synchronous behavior
	if app.waitSync {
		app.runSyncSynchronous(ctx)
		return
	}

	// Non-blocking: start background worker and return immediately
	if app.bgWorker == nil {
		interval, err := time.ParseDuration(app.engine.Config.Sync.Interval)
		if err != nil {
			interval = 15 * time.Minute
		}
		app.bgWorker = synce.NewBackgroundWorker(app.engine.Sync, interval)
		app.bgWorker.Start(ctx)
	}

	fmt.Fprintln(os.Stderr, "(syncing...)")
}

func (app *CLI) runSyncSynchronous(ctx context.Context) {
	ch, err := app.engine.Sync.Sync(ctx, false, false, "")
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

// syncAge returns the age of the last sync in seconds. Returns -1 if never
// synced. First checks the background worker, then falls back to database sync
// metadata.
func (app *CLI) syncAge() int {
	// Check background worker first
	if app.bgWorker != nil {
		age := app.bgWorker.SyncAge()
		if age >= 0 {
			return int(age.Seconds())
		}
	}

	// Fall back to database sync metadata
	if app.engine == nil || app.engine.Store == nil || app.engine.Config == nil {
		return -1
	}

	var oldestSync time.Time
	for _, src := range app.engine.Config.SyncSources {
		meta, err := app.engine.Store.GetSyncMeta(src.Name)
		if err != nil || !meta.LastSyncTime.Valid || meta.LastSyncTime.String == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, meta.LastSyncTime.String)
		if err != nil {
			continue
		}
		if oldestSync.IsZero() || t.Before(oldestSync) {
			oldestSync = t
		}
	}

	if oldestSync.IsZero() {
		return -1
	}
	return int(time.Since(oldestSync).Seconds())
}

// commandGroups organizes top-level commands into titled help sections, the way
// `bd --help` groups its commands. Commands not listed here fall into cobra's
// default "Additional Commands" section (e.g. help, completion).
var commandGroups = []struct {
	ID    string
	Title string
	Cmds  []string
}{
	{"query", "Querying & Reading:", []string{"query", "get", "search", "view", "changelog", "deps", "open"}},
	{"write", "Writing to Jira:", []string{"create", "update", "set", "comment", "transition", "clone", "link", "watch", "unwatch", "push"}},
	{"data", "Sync & Data:", []string{"sync", "rank", "status", "db"}},
	{"insight", "Schema & Metrics:", []string{"fields", "schema", "metrics"}},
	{"interface", "Interfaces & Agents:", []string{"tui", "serve", "prime"}},
	{"setup", "Setup & Configuration:", []string{"init", "snippet"}},
}

// applyCommandGroups registers the help groups on root and assigns each listed
// subcommand to its group. It runs once before execution, after every
// subcommand's init() has registered it on rootCmd, so it can look commands up
// by name rather than threading a GroupID through each command definition.
func applyCommandGroups(root *cobra.Command) {
	byName := make(map[string]string)
	for _, g := range commandGroups {
		root.AddGroup(&cobra.Group{ID: g.ID, Title: g.Title})
		for _, name := range g.Cmds {
			byName[name] = g.ID
		}
	}
	for _, c := range root.Commands() {
		if id, ok := byName[c.Name()]; ok {
			c.GroupID = id
		}
	}
}

// Execute assembles the CLI and runs the root command.
func Execute() error {
	app := &CLI{}
	bindRoot(rootCmd, app)
	applyCommandGroups(rootCmd)
	rootCmd.SetHelpFunc(colorizedHelpFunc)
	return rootCmd.Execute()
}
