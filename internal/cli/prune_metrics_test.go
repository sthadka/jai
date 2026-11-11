package cli

import (
	"path/filepath"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/metrics"
)

// flowKeySet returns the issue keys present in the active generation's flow facts.
func flowKeySet(t *testing.T, store *metrics.Store) map[string]bool {
	t.Helper()
	rows, err := store.FlowRows(nil)
	if err != nil {
		t.Fatalf("FlowRows: %v", err)
	}
	keys := map[string]bool{}
	for _, r := range rows {
		keys[r.IssueKey] = true
	}
	return keys
}

func TestPruneMetricRefresh(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			// Pruning an orphan project must delete its issues AND remove their
			// derived facts, while a configured project's facts are untouched. The
			// removal rides the shared writer lock within the active generation.
			name: "prune removes pruned issues' facts and keeps configured ones",
			run: func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "jai.db")
				cfg := metricsTestConfig(path)
				cfg.SyncSources = []config.SyncSource{{Name: "keep", Projects: []string{"KEEP"}}}
				database := setupCLIMetrics(t, cfg)
				store := metrics.NewStore(database, path)

				seedResolvedIssue(t, database, "KEEP-1", "KEEP")
				seedStatusChangelog(t, database, "KEEP-1")
				seedResolvedIssue(t, database, "DROP-1", "DROP")
				seedStatusChangelog(t, database, "DROP-1")

				if err := refreshMetricFacts([]string{"KEEP-1", "DROP-1"}); err != nil {
					t.Fatalf("initial refreshMetricFacts: %v", err)
				}
				gen0 := activeGenMeta(t, store)
				if gen0 == nil {
					t.Fatal("expected an active generation after initial build")
				}
				if before := flowKeySet(t, store); !before["KEEP-1"] || !before["DROP-1"] {
					t.Fatalf("expected both issues in facts before prune, got %v", before)
				}

				// DROP is not covered by any configured source -> orphan project.
				prevYes, prevJSON := pruneYes, g.jsonOut
				pruneYes, g.jsonOut = true, true
				t.Cleanup(func() { pruneYes, g.jsonOut = prevYes, prevJSON })

				if err := syncPruneCmd.RunE(syncPruneCmd, nil); err != nil {
					t.Fatalf("prune RunE: %v", err)
				}

				// The DROP issue itself is gone...
				var dropCount int
				if err := database.QueryRow(
					"SELECT count(*) FROM issues WHERE project = 'DROP'").Scan(&dropCount); err != nil {
					t.Fatalf("count DROP issues: %v", err)
				}
				if dropCount != 0 {
					t.Fatalf("expected DROP issues deleted, got %d", dropCount)
				}

				// ...and so are its facts, with no visible orphan, while KEEP-1
				// stays. The active generation is preserved (incremental removal).
				gen1 := activeGenMeta(t, store)
				if gen1 == nil || gen1.Generation != gen0.Generation {
					t.Fatalf("expected incremental removal to keep generation %d, got %+v", gen0.Generation, gen1)
				}
				after := flowKeySet(t, store)
				if after["DROP-1"] {
					t.Fatalf("expected DROP-1 facts removed, still present: %v", after)
				}
				if !after["KEEP-1"] {
					t.Fatalf("expected KEEP-1 facts preserved, got %v", after)
				}
			},
		},
		{
			// With metrics unconfigured the prune still deletes issues but performs
			// no metric work and creates no generation.
			name: "unconfigured prune is a no-op for metrics",
			run: func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "jai.db")
				cfg := &config.Config{DB: config.DBConfig{Path: path}} // no metrics: block
				cfg.SyncSources = []config.SyncSource{{Name: "keep", Projects: []string{"KEEP"}}}
				database := setupCLIMetrics(t, cfg)
				store := metrics.NewStore(database, path)

				seedResolvedIssue(t, database, "KEEP-1", "KEEP")
				seedResolvedIssue(t, database, "DROP-1", "DROP")

				prevYes, prevJSON := pruneYes, g.jsonOut
				pruneYes, g.jsonOut = true, true
				t.Cleanup(func() { pruneYes, g.jsonOut = prevYes, prevJSON })

				if err := syncPruneCmd.RunE(syncPruneCmd, nil); err != nil {
					t.Fatalf("prune RunE (unconfigured): %v", err)
				}

				var dropCount int
				if err := database.QueryRow(
					"SELECT count(*) FROM issues WHERE project = 'DROP'").Scan(&dropCount); err != nil {
					t.Fatalf("count DROP issues: %v", err)
				}
				if dropCount != 0 {
					t.Fatalf("expected DROP issues deleted even when metrics unconfigured, got %d", dropCount)
				}
				if gen := activeGenMeta(t, store); gen != nil {
					t.Fatalf("expected no active generation when metrics unconfigured, got %+v", gen)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.run(t) })
	}
}
