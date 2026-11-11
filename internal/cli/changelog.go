package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/metrics"
	"github.com/sthadka/jai/internal/output"
)

var changelogFieldFilter string

var changelogCmd = &cobra.Command{
	Use:   "changelog <key>",
	Short: "Show changelog history for an issue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := strings.ToUpper(args[0])

		// Build query with optional field filter.
		query := "SELECT changed_at, author, field, from_string, to_string FROM changelog WHERE issue_key = ?"
		params := []interface{}{key}

		if changelogFieldFilter != "" {
			query += " AND field = ?"
			params = append(params, changelogFieldFilter)
		}

		query += " ORDER BY changed_at ASC"

		rows, err := g.db.Query(query, params...)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("QueryError", err.Error())))
				return nil
			}
			return fmt.Errorf("querying changelog: %w", err)
		}
		defer rows.Close()

		type entry struct {
			ChangedAt  string `json:"changed_at"`
			Author     string `json:"author"`
			Field      string `json:"field"`
			FromString string `json:"from,omitempty"`
			ToString   string `json:"to"`
		}

		var entries []entry
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.ChangedAt, &e.Author, &e.Field, &e.FromString, &e.ToString); err != nil {
				continue
			}
			entries = append(entries, e)
		}

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]interface{}{
				"issue_key": key,
				"entries":   entries,
			})))
			return nil
		}

		// Human-readable output.
		if len(entries) == 0 {
			fmt.Printf("%s Changelog\n", key)
			fmt.Println(strings.Repeat("─", len(key)+10))
			fmt.Println("(no changelog entries)")
			return nil
		}

		fmt.Printf("%s Changelog\n", key)
		fmt.Println(strings.Repeat("─", len(key)+10))

		for _, e := range entries {
			// Format: 2026-08-20 10:30  alice    status      To Do → In Progress
			timestamp := e.ChangedAt
			if len(timestamp) > 16 {
				timestamp = timestamp[:16] // Trim to "2026-08-20 10:30"
			}

			author := e.Author
			if len(author) > 12 {
				author = author[:12]
			}

			field := e.Field
			if len(field) > 12 {
				field = field[:12]
			}

			change := fmt.Sprintf("%s → %s", e.FromString, e.ToString)

			fmt.Printf("%-16s  %-12s  %-12s  %s\n", timestamp, author, field, change)
		}

		return nil
	},
}

// changelogSync flags for the standalone `jai changelog sync` command.
var (
	changelogSyncSource string
	changelogSyncForce  bool
)

var changelogSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Fetch changelog history for issues that need it",
	Long: `Sync only changelog history, without re-fetching issue snapshots.

Incremental by default: fetches changelog for issues whose history was never
synced or whose "updated" timestamp is newer than the last check. --force
re-fetches all changelog history from scratch.

After the fetch, flow-metric facts are refreshed for the affected issues so
changelog-derived facts (cycle time, WIP) advance even though a history-only
fetch never changes the issue snapshot's "updated" timestamp. The refresh is a
no-op when flow metrics are not configured.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Capture the keys that will be (re)fetched before the sync stamps them
		// as checked, so the metric refresh advances exactly those facts. This
		// mirrors SyncChangelogs' own candidate scoping (a single named source is
		// limited to its projects).
		touched, err := g.db.GetChangelogSyncCandidates(changelogSyncProjectFilter())
		if err != nil {
			return fmt.Errorf("finding changelog candidates: %w", err)
		}

		ch, err := g.sync.SyncChangelogs(ctx, changelogSyncSource, changelogSyncForce)
		if err != nil {
			return fmt.Errorf("changelog sync: %w", err)
		}
		displayChangelogProgress(ch)

		// Advance flow-metric facts for the issues whose history changed. Metric
		// publication is separate from the changelog fetch: a build failure is
		// logged but never fails an otherwise-successful sync.
		if err := refreshMetricFacts(touched); err != nil {
			fmt.Fprintf(os.Stderr, "warning: metric refresh after changelog sync failed: %v\n", err)
		}
		return nil
	},
}

// changelogSyncProjectFilter mirrors SyncChangelogs' scoping: a single named
// source restricts the candidate (and refresh) set to that source's projects;
// no filter — or a source with no project keys (JQL) — covers all candidates.
func changelogSyncProjectFilter() []string {
	if changelogSyncSource == "" || g.cfg == nil {
		return nil
	}
	for _, s := range g.cfg.SyncSources {
		if s.Name == changelogSyncSource {
			return s.Projects
		}
	}
	return nil
}

// refreshMetricFacts advances the flow-metric fact store for the given touched or
// deleted issue keys, holding the same cross-process advisory lock the sync
// engine uses (invariant #6). Keys that still exist in `issues` have their facts
// restated; keys that no longer exist have their facts removed, so a standalone
// changelog backfill or a prune keeps facts in step without leaving visible
// orphan facts.
//
// The refresh is incremental within the active generation; a policy/mapping
// change (or the absence of any active generation) returns
// metrics.ErrFullRebuildRequired and falls back to a full restatement
// (invariant #7). A nil metrics config, or an empty key set, is a no-op so the
// commands work unchanged when flow metrics are not configured.
func refreshMetricFacts(keys []string) error {
	if g.cfg == nil || g.cfg.Metrics == nil || len(keys) == 0 {
		return nil
	}
	pol := metrics.CompilePolicy(g.cfg.Metrics)
	store := metrics.NewStore(g.db, g.cfg.DB.Path)
	builder := metrics.NewBuilder(store, pol)

	asOf := time.Now().UTC()
	_, err := builder.Incremental(asOf, keys)
	if errors.Is(err, metrics.ErrFullRebuildRequired) {
		_, err = builder.BuildAll(asOf, nil)
	}
	return err
}

func init() {
	changelogCmd.Flags().StringVar(&changelogFieldFilter, "field", "", "filter by field name")
	changelogSyncCmd.Flags().StringVar(&changelogSyncSource, "source", "", "limit to a single configured sync source")
	changelogSyncCmd.Flags().BoolVar(&changelogSyncForce, "force", false, "re-fetch all changelog history from scratch")
	changelogCmd.AddCommand(changelogSyncCmd)
	rootCmd.AddCommand(changelogCmd)
}
