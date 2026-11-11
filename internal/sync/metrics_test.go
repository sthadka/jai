package sync

import (
	"path/filepath"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/metrics"
)

// metricsCfg builds a config whose metrics: block maps the given done resolutions.
// Varying doneResolutions changes the compiled policy fingerprint, which drives the
// full-rebuild-on-config-change path.
func metricsCfg(dbPath string, doneResolutions []string) *config.Config {
	return &config.Config{
		DB: config.DBConfig{Path: dbPath},
		Metrics: &config.MetricsConfig{
			StatusStages: map[string][]string{
				"todo":   {"Backlog"},
				"active": {"In Progress"},
				"done":   {"Closed"},
			},
			WIPStages:       []string{"active"},
			DoneResolutions: doneResolutions,
		},
	}
}

func newMetricTestDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jai.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database, path
}

// putIssue inserts/updates a resolved issue with its status changelog marked complete.
func putIssue(t *testing.T, database *db.DB, key string) {
	t.Helper()
	iss := &db.Issue{
		Key: key, Project: "TEST", Type: "Bug", Summary: key,
		Status: "Closed", StatusCategory: "Done",
		Created: "2024-01-01T00:00:00Z", Resolved: "2024-01-05T00:00:00Z", Resolution: "Done",
	}
	extra := map[string]interface{}{"changelog_synced_at": "2030-01-01T00:00:00Z"}
	if err := database.UpsertIssue(iss, extra); err != nil {
		t.Fatalf("UpsertIssue %s: %v", key, err)
	}
	if err := database.InsertChangelog(&db.ChangelogEntry{
		ID: key + "-1", IssueKey: key, Field: "status",
		FromString: "Backlog", ToString: "In Progress", ChangedAt: "2024-01-02T00:00:00Z",
	}); err != nil {
		t.Fatalf("InsertChangelog %s: %v", key, err)
	}
	if err := database.InsertChangelog(&db.ChangelogEntry{
		ID: key + "-2", IssueKey: key, Field: "status",
		FromString: "In Progress", ToString: "Closed", ChangedAt: "2024-01-05T00:00:00Z",
	}); err != nil {
		t.Fatalf("InsertChangelog %s: %v", key, err)
	}
}

// flowKeys returns the set of issue keys present in the active generation's flow facts.
func flowKeys(t *testing.T, store *metrics.Store) map[string]bool {
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

func activeGen(t *testing.T, store *metrics.Store) *metrics.GenerationMeta {
	t.Helper()
	g, err := store.ActiveGeneration()
	if err != nil {
		t.Fatalf("ActiveGeneration: %v", err)
	}
	return g
}

// TestMetricWriter exercises the sync-side metric writer's refresh paths: incremental
// update after a prior build, a full rebuild forced by a policy fingerprint change,
// deletion removing facts, and the unconfigured no-op.
func TestMetricWriter(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, database *db.DB, path string)
	}{
		{
			name: "incremental adds touched issue within active generation",
			run: func(t *testing.T, database *db.DB, path string) {
				mw := newMetricWriter(database, metricsCfg(path, []string{"Done"}))
				store := metrics.NewStore(database, path)

				putIssue(t, database, "TEST-1")
				if err := mw.refreshFull(nil); err != nil {
					t.Fatalf("refreshFull: %v", err)
				}
				gen0 := activeGen(t, store)
				if gen0 == nil {
					t.Fatal("expected an active generation after full build")
				}
				if got := flowKeys(t, store); !got["TEST-1"] || got["TEST-2"] {
					t.Fatalf("after full build want {TEST-1}, got %v", got)
				}

				// New issue arrives; incremental refresh of only the touched key.
				putIssue(t, database, "TEST-2")
				if err := mw.refreshTouched([]string{"TEST-2"}); err != nil {
					t.Fatalf("refreshTouched: %v", err)
				}
				gen1 := activeGen(t, store)
				if gen1.Generation != gen0.Generation {
					t.Fatalf("incremental must stay in the active generation: gen0=%d gen1=%d", gen0.Generation, gen1.Generation)
				}
				if got := flowKeys(t, store); !got["TEST-1"] || !got["TEST-2"] {
					t.Fatalf("after incremental want {TEST-1,TEST-2}, got %v", got)
				}
			},
		},
		{
			name: "fingerprint change forces full rebuild",
			run: func(t *testing.T, database *db.DB, path string) {
				store := metrics.NewStore(database, path)
				putIssue(t, database, "TEST-1")

				mw1 := newMetricWriter(database, metricsCfg(path, []string{"Done"}))
				if err := mw1.refreshFull(nil); err != nil {
					t.Fatalf("refreshFull: %v", err)
				}
				gen0 := activeGen(t, store)

				// A different done_resolutions set is a different policy fingerprint; an
				// incremental must restate history via a brand-new full generation.
				mw2 := newMetricWriter(database, metricsCfg(path, []string{"Done", "Done-Errata"}))
				if err := mw2.refreshTouched([]string{"TEST-1"}); err != nil {
					t.Fatalf("refreshTouched: %v", err)
				}
				gen1 := activeGen(t, store)
				if gen1.Generation == gen0.Generation {
					t.Fatalf("fingerprint change must publish a new generation; still %d", gen0.Generation)
				}
				if gen1.Fingerprint == gen0.Fingerprint {
					t.Fatal("expected a new policy fingerprint after config change")
				}
				if got := flowKeys(t, store); !got["TEST-1"] {
					t.Fatalf("rebuilt generation missing TEST-1: %v", got)
				}
			},
		},
		{
			name: "deletion removes facts",
			run: func(t *testing.T, database *db.DB, path string) {
				mw := newMetricWriter(database, metricsCfg(path, []string{"Done"}))
				store := metrics.NewStore(database, path)

				putIssue(t, database, "TEST-1")
				putIssue(t, database, "TEST-2")
				if err := mw.refreshFull(nil); err != nil {
					t.Fatalf("refreshFull: %v", err)
				}
				gen0 := activeGen(t, store)
				if got := flowKeys(t, store); !got["TEST-1"] || !got["TEST-2"] {
					t.Fatalf("pre-delete want {TEST-1,TEST-2}, got %v", got)
				}

				// Issue removed from the local snapshot; refreshing its key must purge
				// its facts within the active generation, leaving the rest intact.
				if _, err := database.Exec(`DELETE FROM issues WHERE key = ?`, "TEST-2"); err != nil {
					t.Fatalf("delete issue: %v", err)
				}
				if err := mw.refreshTouched([]string{"TEST-2"}); err != nil {
					t.Fatalf("refreshTouched: %v", err)
				}
				gen1 := activeGen(t, store)
				if gen1.Generation != gen0.Generation {
					t.Fatalf("deletion must stay in the active generation: gen0=%d gen1=%d", gen0.Generation, gen1.Generation)
				}
				if got := flowKeys(t, store); !got["TEST-1"] || got["TEST-2"] {
					t.Fatalf("after delete want {TEST-1}, got %v", got)
				}
			},
		},
		{
			name: "unconfigured metrics is a no-op",
			run: func(t *testing.T, database *db.DB, path string) {
				cfg := &config.Config{DB: config.DBConfig{Path: path}} // no metrics: block
				mw := newMetricWriter(database, cfg)
				if mw != nil {
					t.Fatalf("expected nil metric writer when metrics unconfigured, got %#v", mw)
				}
				putIssue(t, database, "TEST-1")
				// Every method must be a safe no-op on the nil writer.
				if err := mw.refreshFull(nil); err != nil {
					t.Fatalf("refreshFull no-op: %v", err)
				}
				if err := mw.refreshTouched([]string{"TEST-1"}); err != nil {
					t.Fatalf("refreshTouched no-op: %v", err)
				}
				store := metrics.NewStore(database, path)
				if g := activeGen(t, store); g != nil {
					t.Fatalf("no-op writer must not publish any generation, got %+v", g)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, path := newMetricTestDB(t)
			tt.run(t, database, path)
		})
	}
}
