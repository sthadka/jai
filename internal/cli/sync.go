package cli

import (
	"context"
	"fmt"
	"os"
	"sync"
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
// spinner line per source. The spinner runs on its own 80ms ticker so it
// keeps moving even between Jira page responses. Rate is computed from
// deltas so it stabilises quickly instead of averaging from t=0.
// Returns total issues synced across all sources.
func displaySyncProgress(ch <-chan synce.Progress) int {
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinIdx := 0
	total := 0

	// curSource holds the state of the currently-syncing source.
	// Done sources are printed immediately by the drain goroutine.
	type curSource struct {
		name      string
		total     int
		rate      float64 // issues/sec (delta-based, not cumulative)
		lastTotal int
		lastT     time.Time
		start     time.Time
	}

	var mu sync.Mutex
	var cur *curSource
	allDone := make(chan struct{})

	// Drain goroutine: receives progress events and updates shared state.
	// For Done events it also prints the final line (holding mu to avoid
	// interleaving with the ticker's \r updates).
	go func() {
		defer close(allDone)
		for p := range ch {
			mu.Lock()

			// New source started (sources are sequential).
			if cur == nil || cur.name != p.Project {
				now := time.Now()
				cur = &curSource{name: p.Project, start: now, lastT: now}
			}

			// Delta rate: only update when ≥500ms have elapsed and count grew.
			now := time.Now()
			if dt := now.Sub(cur.lastT).Seconds(); dt >= 0.5 && p.Total > cur.lastTotal {
				cur.rate = float64(p.Total-cur.lastTotal) / dt
				cur.lastTotal = p.Total
				cur.lastT = now
			}
			cur.total = p.Total

			if p.Done {
				c := cur
				cur = nil
				elapsed := time.Since(c.start).Round(100 * time.Millisecond)
				if p.Error != nil {
					fmt.Fprintf(os.Stderr, "\r  ✗ %-25s ERROR: %v\033[K\n", c.name, p.Error)
				} else {
					fmt.Fprintf(os.Stderr, "\r  ✓ %-25s %d issues (%d new, %d updated) in %s\033[K\n",
						c.name, p.Total, p.New, p.Updated, elapsed)
					total += p.New + p.Updated
				}
			}

			mu.Unlock()
		}
	}()

	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			mu.Lock()
			if cur != nil {
				spin := spinners[spinIdx%len(spinners)]
				rateStr := ""
				if cur.rate > 0 {
					rateStr = fmt.Sprintf("  %.0f/s", cur.rate)
				}
				fmt.Fprintf(os.Stderr, "\r  %s %-25s %d issues%s\033[K",
					spin, cur.name, cur.total, rateStr)
				spinIdx++
			}
			mu.Unlock()

		case <-allDone:
			return total
		}
	}
}

func init() {
	syncCmd.Flags().BoolVar(&syncFull, "full", false, "full resync (delete + re-fetch)")
	syncCmd.Flags().StringVar(&syncSourceFlag, "source", "", "sync only this named source (from sync_sources in config)")
	rootCmd.AddCommand(syncCmd)
}
