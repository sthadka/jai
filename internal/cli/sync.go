package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var syncFull bool

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync Jira issues to local database",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Discover fields first.
		var overrides map[string]string
		if g.cfg.Fields.Overrides != nil {
			overrides = g.cfg.Fields.Overrides
		}
		if err := g.sync.DiscoverFields(ctx, overrides); err != nil {
			return fmt.Errorf("discovering fields: %w", err)
		}

		ch, err := g.sync.Sync(ctx, syncFull)
		if err != nil {
			return err
		}

		total := 0
		for p := range ch {
			if p.Error != nil {
				fmt.Printf("Error syncing %s: %v\n", p.Project, p.Error)
				continue
			}
			fmt.Printf("Synced %s: %d new, %d updated\n", p.Project, p.New, p.Updated)
			total += p.New + p.Updated
		}
		fmt.Printf("Done. %d issues synced.\n", total)
		return nil
	},
}

func init() {
	syncCmd.Flags().BoolVar(&syncFull, "full", false, "full resync (delete + re-fetch)")
	rootCmd.AddCommand(syncCmd)
}
