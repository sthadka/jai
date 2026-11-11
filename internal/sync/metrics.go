package sync

import (
	"errors"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/metrics"
)

// metricWriter drives the flow-metric fact store (internal/metrics) from sync
// events. It is the single sync-side entry point that keeps fact_issue_flow and
// fact_field_interval advancing in lock-step with the issues table, holding the
// cross-process advisory lock (invariant #6) for every publication.
//
// A nil *metricWriter is a safe no-op for every method, so sync works unchanged
// for users who have not opted into flow metrics: newMetricWriter returns nil
// when the config carries no `metrics:` block. Metrics are never forced on.
type metricWriter struct {
	builder *metrics.Builder
}

// newMetricWriter binds a metric writer to the database and config, or returns
// nil when metrics are not configured. The Builder resolves its cross-process
// advisory-lock path from cfg.DB.Path (the same path used to open the DB), so
// every process — CLI auto-sync, serve/MCP, TUI — serializes on one sidecar.
func newMetricWriter(database *db.DB, cfg *config.Config) *metricWriter {
	if cfg == nil || cfg.Metrics == nil {
		return nil
	}
	store := metrics.NewStore(database, cfg.DB.Path)
	pol := metrics.CompilePolicy(cfg.Metrics)
	return &metricWriter{builder: metrics.NewBuilder(store, pol)}
}

// refreshFull restates every in-scope fact from scratch: it stages a brand-new
// generation and swaps the active pointer in one transaction (BuildAll). projects
// scopes the rebuild; nil/empty restates all issues. Used when the whole dataset
// must be re-derived (e.g. a forced global rebuild). A nil writer is a no-op.
func (m *metricWriter) refreshFull(projects []string) error {
	if m == nil {
		return nil
	}
	_, err := m.builder.BuildAll(time.Now().UTC(), projects)
	return err
}

// refreshTouched advances the facts for the given touched/deleted issue keys
// within the active generation, holding the cross-process lock. In one
// transaction it replaces the facts of keys that still exist in issues and
// removes the facts of keys that no longer do (deletions/backfills), so facts and
// rollup slices advance together (invariant: incremental touches only changed
// issues within the active generation).
//
// A config/mapping change (policy fingerprint mismatch) or the absence of any
// active generation returns metrics.ErrFullRebuildRequired; per invariant #7 a
// map change restates history, so we fall back to a full global rebuild. A nil
// writer, or an empty key set, is a no-op.
func (m *metricWriter) refreshTouched(keys []string) error {
	if m == nil || len(keys) == 0 {
		return nil
	}
	_, err := m.builder.Incremental(time.Now().UTC(), keys)
	if errors.Is(err, metrics.ErrFullRebuildRequired) {
		_, err = m.builder.BuildAll(time.Now().UTC(), nil)
	}
	return err
}
