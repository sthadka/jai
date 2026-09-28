package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
	"github.com/sthadka/jai/internal/query"
)

var pruneYes bool

var syncPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Delete local data for projects/sources no longer in config",
	Long: `Remove issues, changelog, comments, and sync metadata for sync sources and
projects that are no longer present in sync_sources. Editing the config alone
leaves this data behind; prune reclaims it and silences the "full sync overdue"
reminder for removed sources.

Runs as a dry run by default — pass --yes to apply.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Orphan sources: sync_metadata rows keyed by a source name that is no
		// longer configured.
		configuredSources := make(map[string]bool, len(g.cfg.SyncSources))
		for _, s := range g.cfg.SyncSources {
			configuredSources[s.Name] = true
		}
		metas, err := g.db.AllSyncMeta()
		if err != nil {
			return fmt.Errorf("reading sync metadata: %w", err)
		}
		var orphanSources []string
		for _, m := range metas {
			if !configuredSources[m.Project] {
				orphanSources = append(orphanSources, m.Project)
			}
		}
		sort.Strings(orphanSources)

		// Orphan projects: issue project keys not covered by any configured
		// source. When coverage is ambiguous (a JQL source with no parseable
		// project), skip issue pruning entirely rather than risk deleting live
		// data — orphan sync_metadata rows are still safe to remove by name.
		counts, err := g.db.IssueCountByProject()
		if err != nil {
			return fmt.Errorf("counting issues: %w", err)
		}
		keys, ambiguous := query.ConfiguredProjectKeys(g.cfg)
		configuredProjects := make(map[string]bool, len(keys))
		for _, k := range keys {
			configuredProjects[k] = true
		}
		orphanProjects := map[string]int{}
		if !ambiguous {
			for proj, n := range counts {
				if !configuredProjects[proj] {
					orphanProjects[proj] = n
				}
			}
		}
		sortedProjects := make([]string, 0, len(orphanProjects))
		totalIssues := 0
		for p, n := range orphanProjects {
			sortedProjects = append(sortedProjects, p)
			totalIssues += n
		}
		sort.Strings(sortedProjects)

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]any{
				"orphan_sources":  orphanSources,
				"orphan_projects": orphanProjects,
				"issues":          totalIssues,
				"jql_ambiguous":   ambiguous,
				"applied":         pruneYes,
			})))
		}

		if len(orphanSources) == 0 && len(orphanProjects) == 0 {
			if !g.jsonOut {
				if ambiguous {
					fmt.Println("Nothing to prune. (Issue pruning skipped: a JQL source has no parseable project key, so coverage can't be determined.)")
				} else {
					fmt.Println("Nothing to prune. Local data matches configured sources.")
				}
			}
			return nil
		}

		if !g.jsonOut {
			fmt.Println("Local data not covered by any configured sync source:")
			if len(orphanProjects) > 0 {
				fmt.Printf("  Projects (%d issues + changelog/comments):\n", totalIssues)
				for _, p := range sortedProjects {
					fmt.Printf("    %-12s %d issues\n", p, orphanProjects[p])
				}
			}
			if len(orphanSources) > 0 {
				fmt.Printf("  Sync metadata rows: %v\n", orphanSources)
			}
			if ambiguous {
				fmt.Println("  Note: issue pruning skipped — a JQL source has no parseable project key.")
			}
		}

		if !pruneYes {
			if !g.jsonOut {
				fmt.Println("\nDry run. Re-run with --yes to delete.")
			}
			return nil
		}

		removed, err := g.db.PruneProjects(sortedProjects)
		if err != nil {
			return fmt.Errorf("pruning issues: %w", err)
		}
		deletedMeta, err := g.db.DeleteSyncMetadata(orphanSources)
		if err != nil {
			return fmt.Errorf("deleting sync metadata: %w", err)
		}
		if !g.jsonOut {
			fmt.Printf("✓ Pruned %d issues across %d project(s), %d sync metadata row(s).\n",
				removed, len(sortedProjects), deletedMeta)
		}
		return nil
	},
}

func init() {
	syncPruneCmd.Flags().BoolVarP(&pruneYes, "yes", "y", false, "apply deletions (default is a dry run)")
	syncCmd.AddCommand(syncPruneCmd)
}
