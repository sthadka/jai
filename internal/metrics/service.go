package metrics

import (
	"strings"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

// Service is the single query/result entry point shared by every consumer (CLI, MCP, TUI,
// dashboard, and view definitions). There is exactly one definition of each metric, routed
// through the provider registry, so the three consumers never fork the SQL or the policy.
type Service struct {
	db    *db.DB
	store *Store
	pol   *Policy
	cfg   *config.Config
}

// NewService builds the shared service. store is used by fact-based metrics (delivery);
// issues-only metrics read db directly. cfg is optional (used for freshness/coverage).
func NewService(database *db.DB, store *Store, pol *Policy, cfg *config.Config) *Service {
	return &Service{db: database, store: store, pol: pol, cfg: cfg}
}

// Policy exposes the compiled policy to consumers that must surface rejections.
func (s *Service) Policy() *Policy { return s.pol }

// Compute runs a registered metric. It validates the basis (Phase 1 rejects non-snapshot
// bases rather than silently downgrading), dispatches to the provider, and lets the
// provider fill the metric-specific data on a Result pre-populated with the common
// envelope (basis, scope, as_of, taxonomy version, policies, freshness).
func (s *Service) Compute(q Query) (*Result, error) {
	if q.Basis != "" && q.Basis != BasisSnapshot {
		return nil, ErrUnsupportedBasis
	}
	p, ok := lookup(q.Metric)
	if !ok {
		return nil, ErrUnknownMetric
	}
	return p.Compute(s, q)
}

// asOf resolves the query as_of, defaulting to now (UTC). Open intervals and aged cohorts
// are computed against this value, never a sync-frozen now.
func (s *Service) asOf(q Query) time.Time {
	if q.AsOf.IsZero() {
		return time.Now().UTC()
	}
	return q.AsOf.UTC()
}

// newResult pre-fills the common envelope every metric carries. Providers set the
// metric-specific fields (Distribution/Series/Counts/Ratio, Eligible, Excluded, Cohort,
// GenuineDone) afterward.
func (s *Service) newResult(q Query) *Result {
	r := &Result{
		Metric:          q.Metric,
		Basis:           BasisSnapshot,
		Projects:        append([]string(nil), q.Projects...),
		AsOf:            s.asOf(q),
		Range:           q.Range,
		TaxonomyVersion: s.pol.Fingerprint(),
		Policies:        s.policySummary(),
		Freshness:       s.freshness(q.Projects),
	}
	return r
}

// attachBuild records the active fact generation and its build watermark on the result,
// for fact-based metrics. It is a no-op when no generation is active.
func (s *Service) attachBuild(r *Result) error {
	if s.store == nil {
		return nil
	}
	gen, err := s.store.ActiveGeneration()
	if err != nil {
		return err
	}
	if gen == nil {
		r.warn("no active fact generation; build the fact store before querying delivery metrics")
		return nil
	}
	r.BuildGeneration = gen.Generation
	b := gen.BuiltAt
	r.BuiltAt = &b
	if gen.Fingerprint != s.pol.Fingerprint() {
		r.warn("active fact generation was built under a different config; a full rebuild is required for accurate results")
	}
	return nil
}

// policySummary renders the active completion/stage policies for the result envelope.
func (s *Service) policySummary() map[string]string {
	m := map[string]string{}
	if s.pol.DoneResolutionsConfigured() {
		m["done_resolutions"] = "configured"
	} else {
		m["done_resolutions"] = "unset (includes non-delivery resolutions)"
	}
	return m
}

// deliveredCohort applies the shared completion policy to flow rows. When done_resolutions
// is configured, only resolved issues with a genuine-done resolution are delivered and the
// rest are excluded as non-delivery; otherwise every resolved issue is delivered and the
// genuine variant is disabled (labelled). The same policy is used by cycle, lead,
// throughput, and the completed-cohort ratio so the cohorts agree.
func (s *Service) deliveredCohort(rows []FlowRow) (delivered []FlowRow, nonDelivery, notCompleted int, genuine bool, cohort string) {
	genuine = s.pol.DoneResolutionsConfigured()
	for i := range rows {
		fr := rows[i]
		if fr.Resolved == nil {
			notCompleted++
			continue
		}
		if genuine && !s.pol.IsDoneResolution(fr.Resolution) {
			nonDelivery++
			continue
		}
		delivered = append(delivered, fr)
	}
	if genuine {
		cohort = "completed (genuine-done)"
	} else {
		cohort = "completed (includes non-delivery resolutions)"
	}
	return delivered, nonDelivery, notCompleted, genuine, cohort
}

// freshness reads per-project source freshness from sync_metadata. Snapshot freshness only
// (when the local DB last pulled from Jira), never a claim of live-Jira completeness.
func (s *Service) freshness(projects []string) map[string]SourceFreshness {
	out := map[string]SourceFreshness{}
	q := `SELECT project, last_sync_time, last_full_sync FROM sync_metadata`
	var args []any
	if len(projects) > 0 {
		ph := make([]string, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			args = append(args, p)
		}
		q += " WHERE project IN (" + strings.Join(ph, ", ") + ")"
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return out
	}
	scanFreshness(rows, out)
	return out
}

// flowRows loads the active generation's flow facts for the query scope.
func (s *Service) flowRows(q Query) ([]FlowRow, error) {
	return s.store.FlowRows(q.Projects)
}
