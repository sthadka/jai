package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sync and queue status",
	RunE: func(cmd *cobra.Command, args []string) error {
		metas, err := g.db.AllSyncMeta()
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("QueryError", err.Error())))
				return nil
			}
			return err
		}

		pendingCount, err := g.db.CountPendingChanges()
		if err != nil {
			pendingCount = -1
		}

		dbInfo, _ := os.Stat(g.cfg.DB.Path)

		if g.jsonOut {
			projects := make([]map[string]interface{}, len(metas))
			for i, m := range metas {
				projects[i] = map[string]interface{}{
					"project":            m.Project,
					"last_sync_time":     m.LastSyncTime.String,
					"issues_total":       m.IssuesTotal.Int64,
					"issues_synced":      m.IssuesSynced.Int64,
					"last_sync_duration": m.LastSyncDuration.Float64,
					"last_sync_error":    m.LastSyncError.String,
				}
			}
			data := map[string]interface{}{
				"projects":        projects,
				"pending_changes": pendingCount,
			}
			if dbInfo != nil {
				data["db_size_bytes"] = dbInfo.Size()
			}
			fmt.Println(string(output.OK(data)))
			return nil
		}

		// Human output.
		fmt.Println("Projects:")
		for _, m := range metas {
			lastSync := "never"
			if m.LastSyncTime.Valid && m.LastSyncTime.String != "" {
				if t, err := time.Parse(time.RFC3339, m.LastSyncTime.String); err == nil {
					lastSync = humanDuration(time.Since(t)) + " ago"
				}
			}
			fmt.Printf("  %s: %d issues, last sync %s\n",
				m.Project, m.IssuesSynced.Int64, lastSync)
			if m.LastSyncError.Valid && m.LastSyncError.String != "" {
				fmt.Printf("    Error: %s\n", m.LastSyncError.String)
			}
		}

		fmt.Printf("\nPending changes: %d\n", pendingCount)

		if dbInfo != nil {
			fmt.Printf("DB size: %s\n", humanBytes(dbInfo.Size()))
		}

		return nil
	},
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
