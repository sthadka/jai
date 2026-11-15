package embed

// This test lives in a SEPARATE Go module (github.com/sthadka/jai/examples/embed). It proves the
// jai engine is embeddable through github.com/sthadka/jai/v5/pkg/... using only
// exported APIs, without importing cobra, the TUI, or any internal/CLI package.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sthadka/jai/v5/pkg/clock"
	"github.com/sthadka/jai/v5/pkg/config"
	"github.com/sthadka/jai/v5/pkg/core"
	"github.com/sthadka/jai/v5/pkg/jira"
	"github.com/sthadka/jai/v5/pkg/jql"
	"github.com/sthadka/jai/v5/pkg/metrics"
	"github.com/sthadka/jai/v5/pkg/query"
	"github.com/sthadka/jai/v5/pkg/store"
	"github.com/sthadka/jai/v5/pkg/sync"
)

// mockJira implements jira.JiraClient by embedding the interface (so unused
// methods exist) and overriding only what a minimal sync exercises.
type mockJira struct {
	jira.JiraClient
}

func (mockJira) MySelf(ctx context.Context) (*jira.MySelf, error) {
	return &jira.MySelf{DisplayName: "Embedder", TimeZone: "UTC"}, nil
}
func (mockJira) Fields(ctx context.Context) ([]*jira.Field, error)           { return nil, nil }
func (mockJira) SearchCount(ctx context.Context, jqlStr string) (int, error) { return 0, nil }
func (mockJira) SearchAll(ctx context.Context, jqlStr string, fields []string) func(yield func([]*jira.Issue, error) bool) {
	return func(yield func([]*jira.Issue, error) bool) {} // yield no issues
}

func TestEmbedEngineEndToEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "embed.db")

	// 1) Open a store (SQLite impl behind the store.Store interface).
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	var _ store.Store = st // the concrete *DB satisfies the seam interface

	cfg := config.Defaults()
	cfg.DB.Path = dbPath
	cfg.Jira.URL = "https://example.atlassian.net"
	cfg.SyncSources = []config.SyncSource{{Name: "proj", JQL: "project = PROJ"}}
	cfg.Sync.Sprints = false

	// 2) Assemble the engine from its parts, injecting a mock Jira client.
	eng := core.New(cfg, st, mockJira{})

	// 3) Run a sync (mock yields no issues; proves the seam drives end to end).
	if err := eng.Sync.VerifyAuth(context.Background()); err != nil {
		t.Fatalf("VerifyAuth: %v", err)
	}
	ch, err := eng.Sync.Sync(context.Background(), false, false, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for p := range ch {
		if p.Done && p.Error != nil {
			t.Fatalf("sync progress error: %v", p.Error)
		}
	}

	// 4) Run a query through the query engine.
	res, err := eng.Query.Execute("SELECT count(*) AS n FROM issues")
	if err != nil {
		t.Fatalf("Query.Execute: %v", err)
	}
	if len(res.Columns) == 0 {
		t.Fatal("expected at least one column from query")
	}

	// 5) Compute a metric through the shared metrics service.
	if len(metrics.Metrics()) == 0 {
		t.Fatal("expected registered metrics")
	}
	if _, err := eng.Metrics.Compute(metrics.Query{Metric: "throughput"}); err != nil {
		t.Fatalf("Metrics.Compute(throughput): %v", err)
	}

	// 6) Translate JQL to SQL.
	out, err := jql.Translate("project = PROJ AND status = Done", jql.Options{Dialect: jql.SQLite})
	if err != nil {
		t.Fatalf("jql.Translate: %v", err)
	}
	if out.SQL == "" {
		t.Fatal("expected non-empty SQL from jql.Translate")
	}

	// Exercise the remaining injectable seams so their APIs are covered.
	_ = sync.New(st, mockJira{}, cfg).WithClock(clock.Fixed{})
	_ = metrics.NewService(st, metrics.NewStore(st, dbPath), metrics.CompilePolicy(cfg.Metrics), cfg)
	_ = query.New(st, cfg)
}
