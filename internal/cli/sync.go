package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	synce "github.com/sthadka/jai/internal/sync"
)

var syncFull bool
var syncSourceFlag string

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

		ch, err := g.sync.Sync(ctx, syncFull, syncSourceFlag)
		if err != nil {
			return err
		}

		total := displaySyncProgress(ch)
		fmt.Printf("Done. %d issues synced.\n", total)
		return nil
	},
}

// displaySyncProgress consumes the sync progress channel, rendering a live
// progress line per project. Returns total issues synced across all projects.
func displaySyncProgress(ch <-chan synce.Progress) int {
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

	type state struct {
		start   time.Time
		current synce.Progress
		spin    int
	}
	states := make(map[string]*state)

	total := 0
	for p := range ch {
		s, ok := states[p.Project]
		if !ok {
			s = &state{start: time.Now()}
			states[p.Project] = s
		}
		s.current = p

		if p.Done {
			if p.Error != nil {
				fmt.Printf("\r  %-6s ERROR %v\n", p.Project+":", p.Error)
			} else {
				elapsed := time.Since(s.start).Round(time.Millisecond * 100)
				fmt.Printf("\r  %-6s %d issues (%d new, %d updated) in %s\n",
					p.Project+":", p.Total, p.New, p.Updated, elapsed)
				total += p.New + p.Updated
			}
		} else {
			elapsed := time.Since(s.start).Seconds()
			rate := 0.0
			if elapsed > 0 {
				rate = float64(p.Total) / elapsed
			}
			spin := spinners[s.spin%len(spinners)]
			s.spin++
			fmt.Printf("\r  %s %-6s %d issues @ %.0f/s   ",
				spin, p.Project, p.Total, rate)
		}
	}
	return total
}

func init() {
	syncCmd.Flags().BoolVar(&syncFull, "full", false, "full resync (delete + re-fetch)")
	syncCmd.Flags().StringVar(&syncSourceFlag, "source", "", "sync only this named source (from sync_sources in config)")
	rootCmd.AddCommand(syncCmd)
}
