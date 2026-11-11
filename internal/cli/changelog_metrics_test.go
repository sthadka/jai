package cli

import (
	"path/filepath"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/metrics"
	"github.com/sthadka/jai/internal/query"
)

// metricsTestConfig returns a config whose metrics: block maps a minimal
// todo/active/done taxonomy, so changelog-derived facts (WIP, cycle time) can be
// exercised.
func metricsTestConfig(dbPath string) *config.Config {
	return &config.Config{
		DB: config.DBConfig{Path: dbPath},
		Metrics: &config.MetricsConfig{
			StatusStages: map[string][]string{
				"todo":   {"Backlog"},
				"active": {"In Progress"},
				"done":   {"Closed"},
			},
			WIPStages:       []string{"active"},
			DoneResolutions: []string{"Done"},
		},
	}
}

// setupCLIMetrics opens a fresh DB at cfg.DB.Path and wires the package globals
// so the metric-refresh command paths run in-process. The DB handle is returned
// for direct seeding.
func setupCLIMetrics(t *testing.T, cfg *config.Config) *db.DB {
	t.Helper()
	database, err := db.Open(cfg.DB.Path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	g.db = database
	g.cfg = cfg
	g.query = query.New(database, cfg)
	g.jsonOut = false
	t.Cleanup(func() {
		g.db = nil
		g.cfg = nil
		g.query = nil
		g.jsonOut = false
	})
	return database
}

// seedResolvedIssue inserts a Closed/resolved issue in the given project without
// any changelog history (the history-only fetch is simulated separately).
func seedResolvedIssue(t *testing.T, database *db.DB, key, project string) {
	t.Helper()
	iss := &db.Issue{
		Key: key, Project: project, Type: "Bug", Summary: key, RawJSON: "{}",
		Status: "Closed", StatusCategory: "Done",
		Created: "2024-01-01T00:00:00Z", Resolved: "2024-01-05T00:00:00Z", Resolution: "Done",
	}
	if err := database.UpsertIssue(iss, nil); err != nil {
		t.Fatalf("UpsertIssue %s: %v", key, err)
	}
}

// seedStatusChangelog inserts the status transitions a standalone changelog sync
// would land for an issue (Backlog -> In Progress -> Closed) without touching the
// issue snapshot.
func seedStatusChangelog(t *testing.T, database *db.DB, key string) {
	t.Helper()
	if err := database.InsertChangelog(&db.ChangelogEntry{
		ID: key + "-1", IssueKey: key, Field: "status",
		FromString: "Backlog", ToString: "In Progress", ChangedAt: "2024-01-02T00:00:00Z",
	}); err != nil {
		t.Fatalf("InsertChangelog %s-1: %v", key, err)
	}
	if err := database.InsertChangelog(&db.ChangelogEntry{
		ID: key + "-2", IssueKey: key, Field: "status",
		FromString: "In Progress", ToString: "Closed", ChangedAt: "2024-01-05T00:00:00Z",
	}); err != nil {
		t.Fatalf("InsertChangelog %s-2: %v", key, err)
	}
}

func activeGenMeta(t *testing.T, store *metrics.Store) *metrics.GenerationMeta {
	t.Helper()
	gen, err := store.ActiveGeneration()
	if err != nil {
		t.Fatalf("ActiveGeneration: %v", err)
	}
	return gen
}

func flowRowByKey(t *testing.T, store *metrics.Store, key string) (metrics.FlowRow, bool) {
	t.Helper()
	rows, err := store.FlowRows(nil)
	if err != nil {
		t.Fatalf("FlowRows: %v", err)
	}
	for _, r := range rows {
		if r.IssueKey == key {
			return r, true
		}
	}
	return metrics.FlowRow{}, false
}

func TestChangelogSyncMetricRefresh(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			// The decisive invariant: a standalone changelog backfill advances
			// cycle/WIP facts even though the issue snapshot (and its "updated"
			// timestamp) never changed. The refresh is incremental — it reuses the
			// active generation rather than restating everything.
			name: "incremental refresh after changelog backfill updates facts",
			run: func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "jai.db")
				cfg := metricsTestConfig(path)
				database := setupCLIMetrics(t, cfg)
				store := metrics.NewStore(database, path)

				seedResolvedIssue(t, database, "TEST-1", "TEST")

				// Initial build: no status history yet, so the issue never entered
				// an active stage and cycle time is unknown.
				if err := refreshMetricFacts([]string{"TEST-1"}); err != nil {
					t.Fatalf("initial refreshMetricFacts: %v", err)
				}
				gen0 := activeGenMeta(t, store)
				if gen0 == nil {
					t.Fatal("expected an active generation after initial refresh")
				}
				row0, ok := flowRowByKey(t, store, "TEST-1")
				if !ok {
					t.Fatal("expected a flow row for TEST-1 after initial refresh")
				}
				if row0.HasActive {
					t.Fatalf("expected no active history before changelog backfill, got HasActive=true")
				}
				if row0.CycleSeconds != nil {
					t.Fatalf("expected nil cycle time before changelog backfill, got %d", *row0.CycleSeconds)
				}

				// Standalone changelog sync lands the status history; the issue
				// snapshot is untouched.
				seedStatusChangelog(t, database, "TEST-1")

				if err := refreshMetricFacts([]string{"TEST-1"}); err != nil {
					t.Fatalf("post-backfill refreshMetricFacts: %v", err)
				}
				gen1 := activeGenMeta(t, store)
				if gen1.Generation != gen0.Generation {
					t.Fatalf("expected incremental refresh to keep generation %d, got %d",
						gen0.Generation, gen1.Generation)
				}
				row1, ok := flowRowByKey(t, store, "TEST-1")
				if !ok {
					t.Fatal("expected a flow row for TEST-1 after backfill refresh")
				}
				if !row1.HasActive {
					t.Fatal("expected active history after changelog backfill, got HasActive=false")
				}
				if row1.CycleSeconds == nil {
					t.Fatal("expected cycle time to be computed after changelog backfill, got nil")
				}
			},
		},
		{
			// Metrics unconfigured: the refresh must do nothing and never create a
			// generation, so changelog sync stays usable without flow metrics.
			name: "unconfigured changelog refresh is a no-op",
			run: func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "jai.db")
				cfg := &config.Config{DB: config.DBConfig{Path: path}} // no metrics: block
				database := setupCLIMetrics(t, cfg)
				store := metrics.NewStore(database, path)

				seedResolvedIssue(t, database, "TEST-1", "TEST")
				seedStatusChangelog(t, database, "TEST-1")

				if err := refreshMetricFacts([]string{"TEST-1"}); err != nil {
					t.Fatalf("refreshMetricFacts (unconfigured): %v", err)
				}
				if gen := activeGenMeta(t, store); gen != nil {
					t.Fatalf("expected no active generation when metrics unconfigured, got %+v", gen)
				}
			},
		},
		{
			// The refresh scope must follow the same candidate scoping SyncChangelogs
			// uses: a single named source limits refresh to that source's projects,
			// while an unknown/empty filter covers everything.
			name: "project filter scopes refresh to a single source",
			run: func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "jai.db")
				cfg := metricsTestConfig(path)
				cfg.SyncSources = []config.SyncSource{
					{Name: "alpha", Projects: []string{"AAA"}},
					{Name: "beta", Projects: []string{"BBB", "CCC"}},
				}
				setupCLIMetrics(t, cfg)

				cases := []struct {
					source string
					want   []string
				}{
					{source: "", want: nil},
					{source: "alpha", want: []string{"AAA"}},
					{source: "beta", want: []string{"BBB", "CCC"}},
					{source: "missing", want: nil},
				}
				for _, c := range cases {
					changelogSyncSource = c.source
					got := changelogSyncProjectFilter()
					if !equalStrings(got, c.want) {
						t.Fatalf("source %q: got filter %v, want %v", c.source, got, c.want)
					}
				}
				changelogSyncSource = ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.run(t) })
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
