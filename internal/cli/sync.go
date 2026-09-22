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
var syncResume bool
var syncSourceFlag string
var syncVerbose bool
var syncChangelogsForce bool

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync Jira issues to local database",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Verify authentication before touching Jira: an unauthenticated sync
		// can silently pull anonymous (or empty) data and overwrite the local DB.
		if err := g.sync.VerifyAuth(ctx); err != nil {
			return err
		}

		// Discover fields first.
		var overrides map[string]string
		if g.cfg.Fields.Overrides != nil {
			overrides = g.cfg.Fields.Overrides
		}
		if err := g.sync.DiscoverFields(ctx, overrides); err != nil {
			return fmt.Errorf("discovering fields: %w", err)
		}

		ch, err := g.sync.Sync(ctx, syncFull, syncResume, syncSourceFlag)
		if err != nil {
			return err
		}

		total := displaySyncProgress(ch, syncVerbose)
		fmt.Printf("Done. %d issues synced.\n", total)

		// Reconcile silently-changed fields (rank etc.) that the "updated" gate
		// misses. Only meaningful for incremental syncs — a full sync already
		// re-fetches every issue. Skipped when reconcile_fields is empty.
		if !syncFull && len(g.cfg.Sync.ReconcileFields) > 0 {
			rcCh, err := g.sync.ReconcileFields(ctx, syncSourceFlag, g.cfg.Sync.ReconcileFields)
			if err != nil {
				return fmt.Errorf("reconcile: %w", err)
			}
			displayReconcileProgress(rcCh)
		}

		// Changelog history is now synced on every run (previously behind
		// --changelogs). Incremental by default: only issues whose changelog was
		// never synced or that were updated since are re-fetched. --force resets
		// all changelog_synced_at timestamps first for a full re-fetch.
		clCh, err := g.sync.SyncChangelogs(ctx, syncSourceFlag, syncChangelogsForce)
		if err != nil {
			return fmt.Errorf("changelog sync: %w", err)
		}
		displayChangelogProgress(clCh)

		// Remind the user if a full sync is overdue: the reconcile pass only
		// covers configured fields, so arbitrary silently-changed fields still
		// need a periodic full sync to reconcile.
		if !syncFull && g.cfg.Sync.FullSyncWarning {
			warnIfFullSyncOverdue(g, syncSourceFlag)
		}

		return nil
	},
}

// displaySyncProgress consumes the sync progress channel, rendering a live
// aggregate spinner while one or more sources sync concurrently. Each source's
// final summary line is printed as it completes. Per-source rate is computed
// from deltas so it stabilises quickly instead of averaging from t=0.
// Returns total issues synced across all sources.
func displaySyncProgress(ch <-chan synce.Progress, verbose bool) int {
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinIdx := 0
	total := 0

	// curSource holds the live state of one syncing source. Sources run
	// concurrently, so several may be active at once.
	type curSource struct {
		total     int
		rate      float64 // issues/sec (delta-based, not cumulative)
		lastTotal int
		lastT     time.Time
		start     time.Time
	}

	var mu sync.Mutex
	active := make(map[string]*curSource)
	allDone := make(chan struct{})

	// Drain goroutine: receives progress events and updates shared state.
	// For Done events it also prints the final line (holding mu to avoid
	// interleaving with the ticker's \r updates).
	go func() {
		defer close(allDone)
		for p := range ch {
			mu.Lock()

			cur := active[p.Project]
			if cur == nil {
				now := time.Now()
				cur = &curSource{start: now, lastT: now}
				active[p.Project] = cur
			}

			// Print a one-time note when a resumed sync starts.
			if p.ResumedFrom != "" {
				fmt.Fprintf(os.Stderr, "\r  ↻ %-25s resuming from %s\033[K\n", p.Project, p.ResumedFrom[:10])
			}
			if verbose && p.JQL != "" {
				fmt.Fprintf(os.Stderr, "\r  ⋯ %-25s JQL: %s\033[K\n", p.Project, p.JQL)
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
				elapsed := time.Since(cur.start).Round(100 * time.Millisecond)
				delete(active, p.Project)
				if p.Error != nil {
					fmt.Fprintf(os.Stderr, "\r  ✗ %-25s ERROR: %v\033[K\n", p.Project, p.Error)
				} else {
					fmt.Fprintf(os.Stderr, "\r  ✓ %-25s %d issues (%d new, %d updated) in %s\033[K\n",
						p.Project, p.Total, p.New, p.Updated, elapsed)
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
			if n := len(active); n > 0 {
				var sumTotal int
				var sumRate float64
				for _, c := range active {
					sumTotal += c.total
					sumRate += c.rate
				}
				spin := spinners[spinIdx%len(spinners)]
				rateStr := ""
				if sumRate > 0 {
					rateStr = fmt.Sprintf("  %.0f/s", sumRate)
				}
				srcStr := "source"
				if n > 1 {
					srcStr = fmt.Sprintf("%d sources", n)
				}
				fmt.Fprintf(os.Stderr, "\r  %s syncing %-18s %d issues%s\033[K",
					spin, srcStr, sumTotal, rateStr)
				spinIdx++
			}
			mu.Unlock()

		case <-allDone:
			return total
		}
	}
}

// displayChangelogProgress shows changelog sync progress with a spinner.
func displayChangelogProgress(ch <-chan synce.ChangelogProgress) {
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinIdx := 0

	var mu sync.Mutex
	var latest synce.ChangelogProgress
	allDone := make(chan struct{})

	go func() {
		defer close(allDone)
		for p := range ch {
			mu.Lock()
			latest = p
			if p.Done {
				if p.Error != nil {
					fmt.Fprintf(os.Stderr, "\r  ✗ changelogs           ERROR: %v\033[K\n", p.Error)
				} else {
					fmt.Fprintf(os.Stderr, "\r  ✓ changelogs           %d/%d issues synced", p.Synced, p.Total)
					if p.Skipped > 0 {
						fmt.Fprintf(os.Stderr, " (%d skipped)", p.Skipped)
					}
					fmt.Fprintf(os.Stderr, "\033[K\n")
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
			if !latest.Done && latest.Total > 0 {
				spin := spinners[spinIdx%len(spinners)]
				fmt.Fprintf(os.Stderr, "\r  %s changelogs           %d/%d issues\033[K", spin, latest.Synced, latest.Total)
				spinIdx++
			}
			mu.Unlock()
		case <-allDone:
			return
		}
	}
}

// displayReconcileProgress consumes the reconcile progress channel, rendering a
// live per-source counter while scanning and a final summary line per source.
// It never stays silent when drift was detected: a pass that finds drift but
// fails to correct all of it reports that distinctly from a clean run, so the
// feature can't itself fail silently.
func displayReconcileProgress(ch <-chan synce.ReconcileProgress) {
	for p := range ch {
		if !p.Done {
			// Live counter for large scans (mirrors the changelog phase).
			fmt.Fprintf(os.Stderr, "\r  ⋯ reconcile %-16s scanned %d, drifted %d\033[K", p.Source, p.Scanned, p.Changed)
			continue
		}
		switch {
		case p.Err != nil:
			// Refetch failed partway: known-drifted issues may be uncorrected.
			fmt.Fprintf(os.Stderr, "\r  ✗ reconcile %-16s %d drifted, %d fixed — ERROR: %v\033[K\n",
				p.Source, p.Changed, p.Refetched, p.Err)
		case p.Skipped:
			fmt.Fprintf(os.Stderr, "\r  ⚠ reconcile %-16s %d issues drifted (> cap) — run 'jai sync --full'\033[K\n", p.Source, p.Changed)
		case p.Changed == 0:
			// Clean run — stay quiet (clear the live counter line).
			fmt.Fprintf(os.Stderr, "\r\033[K")
		case p.Refetched == p.Changed:
			fmt.Fprintf(os.Stderr, "\r  ✓ reconcile %-16s %d re-synced (%v)\033[K\n", p.Source, p.Refetched, p.Fields)
		default:
			// Drift detected but not fully corrected (some upserts failed).
			fmt.Fprintf(os.Stderr, "\r  ⚠ reconcile %-16s %d drifted, only %d fixed\033[K\n", p.Source, p.Changed, p.Refetched)
		}
	}
}

// warnIfFullSyncOverdue prints a reminder when a source's last full sync is
// missing or older than the configured threshold. Only configured fields are
// reconciled incrementally, so arbitrary silently-changed fields still rely on
// a periodic full sync.
func warnIfFullSyncOverdue(g globals, sourceFilter string) {
	stale := g.sync.FullSyncOverdue(sourceFilter)
	if len(stale) > 0 {
		fmt.Fprintf(os.Stderr,
			"  ⚠ full sync overdue for: %v — run 'jai sync --full' to reconcile all fields\n"+
				"    (disable via sync.full_sync_warning: false)\n",
			stale)
	}
}

func init() {
	syncCmd.Flags().BoolVar(&syncFull, "full", false, "full resync (re-fetch all issues)")
	syncCmd.Flags().BoolVar(&syncResume, "resume", false, "continue a previously interrupted --full sync (requires --full)")
	syncCmd.Flags().StringVar(&syncSourceFlag, "source", "", "sync only this named source (from sync_sources in config)")
	syncCmd.Flags().BoolVar(&syncVerbose, "verbose", false, "print effective JQL for each source")
	syncCmd.Flags().BoolVar(&syncChangelogsForce, "force", false,
		"re-fetch all changelog history from scratch (resets incremental changelog state)")
	rootCmd.AddCommand(syncCmd)
}
