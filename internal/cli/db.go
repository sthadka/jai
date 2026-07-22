package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/output"
)

var dbResetForce bool

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Manage the local database",
}

var dbResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Delete and recreate the database",
	Long:  "Removes the SQLite database and all WAL files, then creates a fresh empty database.",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath := g.cfg.DB.Path

		if !dbResetForce {
			fmt.Println("This will delete all local data and require a full re-sync.")
			fmt.Print("Continue? [y/N] ")
			var answer string
			fmt.Scanln(&answer)
			if answer != "y" && answer != "Y" {
				fmt.Println("Aborted.")
				return nil
			}
		}

		if info, err := os.Stat(dbPath + "-wal"); err == nil && info.Size() > 0 {
			fmt.Println("Warning: WAL file is non-empty — another jai process may be using this database.")
		}

		for _, suffix := range []string{"", "-shm", "-wal"} {
			os.Remove(dbPath + suffix) // ignore errors (file may not exist)
		}

		database, err := db.Open(dbPath)
		if err != nil {
			return fmt.Errorf("recreating database: %w", err)
		}
		database.Close()

		fmt.Println("✓ Database reset. Run 'jai sync' to re-populate.")
		return nil
	},
}

var dbPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the database file path",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println(g.cfg.DB.Path)
		return nil
	},
}

var dbInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show database statistics",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath := g.cfg.DB.Path

		info, err := os.Stat(dbPath)
		if err != nil {
			return fmt.Errorf("stat database: %w", err)
		}

		var issueCount int
		g.db.QueryRow("SELECT count(*) FROM issues").Scan(&issueCount)

		var changelogCount int
		g.db.QueryRow("SELECT count(*) FROM changelog").Scan(&changelogCount)

		var version int
		g.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)

		var lastSync string
		g.db.QueryRow("SELECT COALESCE(MAX(last_sync_time), '') FROM sync_metadata").Scan(&lastSync)

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]any{
				"path":              dbPath,
				"size_bytes":        info.Size(),
				"issues":            issueCount,
				"changelogs":        changelogCount,
				"migration_version": version,
				"last_sync":         lastSync,
			})))
			return nil
		}

		fmt.Printf("Path:       %s\n", dbPath)
		fmt.Printf("Size:       %s\n", humanBytes(info.Size()))
		fmt.Printf("Issues:     %d\n", issueCount)
		fmt.Printf("Changelogs: %d\n", changelogCount)
		fmt.Printf("Migration:  v%d\n", version)
		if lastSync != "" {
			if t, err := time.Parse(time.RFC3339, lastSync); err == nil {
				fmt.Printf("Last sync:  %s (%s ago)\n", lastSync, humanDuration(time.Since(t)))
			} else {
				fmt.Printf("Last sync:  %s\n", lastSync)
			}
		} else {
			fmt.Printf("Last sync:  never\n")
		}
		return nil
	},
}

func init() {
	dbCmd.AddCommand(dbResetCmd, dbPathCmd, dbInfoCmd)
	dbResetCmd.Flags().BoolVarP(&dbResetForce, "force", "f", false, "skip confirmation prompt")
	rootCmd.AddCommand(dbCmd)
}
