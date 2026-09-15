package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/output"
)

// syncWarnings returns human-readable health warnings for a source's sync
// metadata. It exists because "last sync ran N seconds ago" (last_sync_time)
// says nothing about whether the data is actually current: a sync that keeps
// erroring out, or that has never completed a full pass, can leave individual
// rows frozen while the source still looks freshly synced.
func syncWarnings(m *db.SyncMeta) []string {
	var w []string
	if m.LastSyncError.Valid && m.LastSyncError.String != "" {
		w = append(w, fmt.Sprintf("last sync did not finish: %s", m.LastSyncError.String))
	}
	if !m.LastFullSync.Valid || m.LastFullSync.String == "" {
		w = append(w, "no completed full sync on record — run `jai sync --full` and let it finish")
	}
	// A failing sync that is also far behind "now" is the stuck-watermark
	// signature: the incremental high-water mark can't advance, so recent
	// changes never get re-fetched.
	if m.LastSyncError.Valid && m.LastSyncError.String != "" &&
		m.LastIssueUpdated.Valid && m.LastIssueUpdated.String != "" {
		if t, err := time.Parse(time.RFC3339, m.LastIssueUpdated.String); err == nil {
			if behind := time.Since(t); behind > 48*time.Hour {
				w = append(w, fmt.Sprintf(
					"data only current through %s (%s behind) — sync may be stuck; run `jai sync --full --resume` to completion",
					m.LastIssueUpdated.String[:10], humanDuration(behind)))
			}
		}
	}
	return w
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sync and queue status",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		// Auth check.
		me, authErr := g.jira.MySelf(ctx)

		metas, err := g.db.AllSyncMeta()
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("QueryError", err.Error())))
				return nil
			}
			return err
		}

		totalIssues, _ := g.db.TotalIssueCount()
		countByProject, _ := g.db.IssueCountByProject()

		pendingCount, err := g.db.CountPendingChanges()
		if err != nil {
			pendingCount = 0
		}

		dbInfo, _ := os.Stat(g.cfg.DB.Path)

		if g.jsonOut {
			auth := map[string]any{"ok": authErr == nil}
			if authErr != nil {
				auth["error"] = authErr.Error()
			} else {
				auth["user"] = me.DisplayName
				auth["email"] = me.EmailAddress
			}
			sources := make([]map[string]any, len(metas))
			for i, m := range metas {
				warnings := syncWarnings(m)
				sources[i] = map[string]any{
					"source":             m.Project,
					"last_sync_time":     m.LastSyncTime.String,
					"last_full_sync":     m.LastFullSync.String,
					"last_issue_updated": m.LastIssueUpdated.String,
					"last_sync_duration": m.LastSyncDuration.Float64,
					"last_sync_error":    m.LastSyncError.String,
					"warnings":           warnings,
					"healthy":            len(warnings) == 0,
				}
			}
			data := map[string]any{
				"auth":              auth,
				"sources":           sources,
				"total_issues":      totalIssues,
				"issues_by_project": countByProject,
				"pending_changes":   pendingCount,
			}
			if dbInfo != nil {
				data["db_size_bytes"] = dbInfo.Size()
			}
			fmt.Println(string(output.OK(data)))
			return nil
		}

		// Human output.
		fmt.Println("Auth:")
		if authErr != nil {
			fmt.Printf("  ✗ Failed: %s\n", authErr)
		} else {
			fmt.Printf("  ✓ %s (%s)\n", me.DisplayName, me.EmailAddress)
		}

		fmt.Println("\nSources:")
		for _, m := range metas {
			lastSync := "never"
			if m.LastSyncTime.Valid && m.LastSyncTime.String != "" {
				if t, err := time.Parse(time.RFC3339, m.LastSyncTime.String); err == nil {
					lastSync = humanDuration(time.Since(t)) + " ago"
				}
			}
			fmt.Printf("  %s: last sync %s", m.Project, lastSync)
			if m.IssuesSynced.Valid && m.IssuesSynced.Int64 > 0 {
				fmt.Printf(" (%d synced)", m.IssuesSynced.Int64)
			}
			if m.LastIssueUpdated.Valid && m.LastIssueUpdated.String != "" {
				fmt.Printf(", data through %s", m.LastIssueUpdated.String[:10])
			}
			fmt.Println()
			for _, warn := range syncWarnings(m) {
				fmt.Printf("    ⚠ %s\n", warn)
			}
		}

		fmt.Printf("\nIssues: %d total", totalIssues)
		if len(countByProject) > 0 {
			fmt.Printf(" (")
			first := true
			for proj, n := range countByProject {
				if !first {
					fmt.Printf(", ")
				}
				fmt.Printf("%s: %d", proj, n)
				first = false
			}
			fmt.Printf(")")
		}
		fmt.Println()

		fmt.Printf("Pending changes: %d\n", pendingCount)

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
