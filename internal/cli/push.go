package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	synce "github.com/sthadka/jai/pkg/sync"
)

var pushCmd = &cobra.Command{
	Use:   "push",
	Short: "Push pending changes to Jira",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Ensure pending_changes table exists.
		if err := g.core.Store.EnsurePendingChangesTable(); err != nil {
			return err
		}

		count, err := g.core.Store.CountPendingChanges()
		if err != nil {
			return err
		}
		if count == 0 {
			fmt.Println("No pending changes.")
			return nil
		}

		fmt.Printf("Pushing %d pending change(s)...\n", count)

		writer := synce.NewWriter(g.core.Store, g.core.Client)
		results, err := writer.ProcessQueue(context.Background())
		if err != nil {
			return err
		}

		succeeded, failed := 0, 0
		for _, r := range results {
			if r.Success {
				succeeded++
				fmt.Printf("  ✓ %s: %s\n", r.IssueKey, r.Operation)
			} else {
				failed++
				fmt.Printf("  ✗ %s: %s (%v)\n", r.IssueKey, r.Operation, r.Error)
			}
		}

		fmt.Printf("%d succeeded, %d failed\n", succeeded, failed)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pushCmd)
}
