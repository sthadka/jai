package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	synce "github.com/sthadka/jai/v6/pkg/sync"
)

var rankBoard int
var rankJQL string
var rankForce bool

var rankCmd = &cobra.Command{
	Use:   "rank",
	Short: "Refresh Jira rank (issue ordering) for one board or JQL scope",
	Long: `Refresh the local LexoRank ordering for a bounded set of issues.

Jira does not bump an issue's "updated" timestamp when it is re-ranked, so
incremental sync misses reorders. The full reconcile pass (run during 'jai sync')
scans every in-scope issue; for a large project that is expensive. This command
refreshes only the issues on one board — resolved via the board's filter JQL —
or an arbitrary --jql scope. Because rank is a global LexoRank, refreshing a
subset yields a correct ordering for that subset without rescanning the project.

Freshness is tracked per scope ("board:<id>"), so repeated runs within
sync.reconcile_interval are skipped; use --force to run regardless.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		app := cliFrom(cmd)
		ctx := context.Background()

		if rankBoard == 0 && strings.TrimSpace(rankJQL) == "" {
			return fmt.Errorf("specify --board <id> or --jql <query>")
		}
		if rankBoard != 0 && strings.TrimSpace(rankJQL) != "" {
			return fmt.Errorf("--board and --jql are mutually exclusive")
		}

		// The rank command reconciles rank by definition; fall back to ["rank"]
		// when reconcile_fields is empty (disabled for the regular sync pass).
		fields := app.engine.Config.Sync.ReconcileFields
		if len(fields) == 0 {
			fields = []string{"rank"}
		}

		// Verify auth before touching Jira, then ensure the field map is present
		// so "rank" resolves to its custom field id.
		if err := app.engine.Sync.VerifyAuth(ctx); err != nil {
			return err
		}
		var overrides map[string]string
		if app.engine.Config.Fields.Overrides != nil {
			overrides = app.engine.Config.Fields.Overrides
		}
		if err := app.engine.Sync.DiscoverFields(ctx, overrides); err != nil {
			return fmt.Errorf("discovering fields: %w", err)
		}

		interval := app.engine.Config.Sync.ReconcileInterval
		if rankForce {
			interval = "0" // bypass the per-scope freshness gate
		}

		var ch <-chan synce.ReconcileProgress
		var err error
		if rankBoard != 0 {
			ch, err = app.engine.Sync.ReconcileBoard(ctx, rankBoard, interval, fields)
		} else {
			ch, err = app.engine.Sync.ReconcileScoped(ctx, "jql:"+rankJQL, rankJQL, interval, fields)
		}
		if err != nil {
			return err
		}
		displayReconcileProgress(ch)
		return nil
	},
}

func init() {
	rankCmd.Flags().IntVar(&rankBoard, "board", 0, "refresh ranks for this Agile board id (resolves its filter JQL)")
	rankCmd.Flags().StringVar(&rankJQL, "jql", "", "refresh ranks for issues matching this JQL (alternative to --board)")
	rankCmd.Flags().BoolVar(&rankForce, "force", false, "run even if this scope was reconciled within sync.reconcile_interval")
	rootCmd.AddCommand(rankCmd)
}
