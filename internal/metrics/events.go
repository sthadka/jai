package metrics

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Changelog-backed count/latency signals (spec §Additional metrics; §fact_field_event;
// §How to query).
//
// These metrics are COUNT / MIN over the `changelog` (and the `comments` table for
// first-response) and are queried DIRECTLY off the partial index `idx_cl_tracked`
// (migration 15) — there is deliberately NO materialized fact_field_event copy. The spec
// is explicit: the count-based signals may not warrant a second persistence schema; default
// to the partial index and only introduce fact_field_event if profiling shows the view is
// too slow (no such evidence here).
//
// Tenant-neutrality guardrails baked in:
//   - Priority DIRECTION (escalation vs de-escalation) requires an explicit tenant ordering,
//     which there is no config for; so priority_changes exposes priority-CHANGE counts and
//     marks escalation unavailable rather than assuming numeric priority ids imply severity.
//   - Rank must be a tracked/configured field before its churn signal is promised;
//     rank_volatility is unavailable (not zero) when Rank is not in tracked_fields.
//   - Counts ignore no-op / display-only changes using the stable id (an id that did not
//     change is not a change even if the display string churned); when no id is recorded the
//     display string is the fallback identity.
//   - First-response uses the defined first-assignment / first-comment semantics and credits
//     assignment AT creation when evidenced; a first reassignment or status move is NOT taken
//     as proof of first response.
const (
	// MetricPriorityChanges is the per-issue distribution of priority CHANGES (not
	// escalations — direction needs a configured ordering).
	MetricPriorityChanges = "priority_changes"
	// MetricRankVolatility is the per-issue distribution of Rank (backlog order) changes.
	// Unavailable unless Rank is a tracked field.
	MetricRankVolatility = "rank_volatility"
	// MetricReassignmentCount is the per-issue distribution of assignee changes counted
	// directly off the changelog by stable account id (distinct from the interval-based
	// reassignment_thrash, which counts assignee-interval boundaries).
	MetricReassignmentCount = "reassignment_count"
	// MetricTriageLatency is the per-issue distribution of first-response / triage latency
	// in days: created -> first assignment or first comment (whichever is earliest).
	MetricTriageLatency = "triage_latency"
)

// changelog field names these metrics read. Jira platform field identifiers (like
// "status"), not tenant taxonomy. They are exactly the fields covered by idx_cl_tracked.
const (
	priorityField = "priority"
	rankField     = "Rank"
)

func init() {
	Register(priorityChangesProvider{})
	Register(rankVolatilityProvider{})
	Register(reassignmentCountProvider{})
	Register(triageLatencyProvider{})
}

// priorityChangesProvider reports priority-change counts per issue. Escalation direction is
// marked unavailable (no configured priority ordering); the counts themselves are reported.
type priorityChangesProvider struct{}

func (priorityChangesProvider) Metric() string { return MetricPriorityChanges }

func (priorityChangesProvider) Compute(s *Service, q Query) (*Result, error) {
	r, err := s.changeCountDistribution(q, priorityField,
		"priority changes per issue (changelog, stable-id; display-only changes excluded)",
		"changes")
	if err != nil {
		return nil, err
	}
	r.Policies["priority_escalation"] = "unavailable (no priority ordering configured)"
	r.warn("priority escalation direction is unavailable: no priority ordering is configured, so numeric priority ids are not assumed to imply severity; reporting priority-change counts, not escalations")
	return r, nil
}

// rankVolatilityProvider reports Rank (backlog order) change counts per issue. It is
// unavailable — not silently zero — unless Rank is a tracked field, since Rank churn can
// only be promised once the field is configured for capture.
type rankVolatilityProvider struct{}

func (rankVolatilityProvider) Metric() string { return MetricRankVolatility }

func (rankVolatilityProvider) Compute(s *Service, q Query) (*Result, error) {
	if !trackedContains(s.pol, rankField) {
		r := s.newResult(q)
		r.Cohort = "rank volatility (unavailable)"
		r.Distribution = &Distribution{Unit: "changes"}
		r.Policies["rank"] = "untracked"
		r.warn("rank volatility is unavailable: Rank is not in metrics.tracked_fields; add Rank to tracked_fields to enable this signal (it is not reported as zero)")
		return r, nil
	}
	return s.changeCountDistribution(q, rankField,
		"rank changes per issue (changelog Rank; display-only changes excluded)",
		"changes")
}

// reassignmentCountProvider reports assignee-change counts per issue read directly off the
// changelog by stable account id.
type reassignmentCountProvider struct{}

func (reassignmentCountProvider) Metric() string { return MetricReassignmentCount }

func (reassignmentCountProvider) Compute(s *Service, q Query) (*Result, error) {
	return s.changeCountDistribution(q, assigneeField,
		"reassignment count per issue (changelog, stable account id; display-name-only changes excluded)",
		"changes")
}

// changeCountDistribution builds the per-issue distribution of real changes for a changelog
// field over the query scope. The population is every issue in scope: an issue with no
// recorded change contributes 0 (never-changed issues are not dropped). A change is counted
// only when the stable id actually changed (or, when no id is recorded, the display string
// changed) — no-op and display-only churn are excluded.
func (s *Service) changeCountDistribution(q Query, field, cohort, unit string) (*Result, error) {
	counts, err := s.fieldChangeCounts(q.Projects, field)
	if err != nil {
		return nil, err
	}
	keys, err := s.issueKeys(q.Projects)
	if err != nil {
		return nil, err
	}
	r := s.newResult(q)
	r.Cohort = cohort
	samples := make([]float64, 0, len(keys))
	for _, k := range keys {
		samples = append(samples, float64(counts[k]))
		r.Eligible++
	}
	r.Distribution = distribution(samples)
	r.Distribution.Unit = unit
	if len(keys) == 0 {
		r.warn("no issues in scope")
	}
	return r, nil
}

// fieldChangeCounts returns, per issue, the number of real changes recorded for field. The
// realness predicate uses the stable id (from_value/to_value) when present and falls back to
// the display string only when no id is recorded, so display-only churn is not counted. The
// query seeks the partial index idx_cl_tracked on its leading `field` column.
func (s *Service) fieldChangeCounts(projects []string, field string) (map[string]int, error) {
	cq := `SELECT issue_key, COUNT(*) FROM changelog
	        WHERE field = ?
	          AND (CASE WHEN COALESCE(from_value,'') <> '' OR COALESCE(to_value,'') <> ''
	                    THEN COALESCE(from_value,'') <> COALESCE(to_value,'')
	                    ELSE COALESCE(from_string,'') <> COALESCE(to_string,'') END)`
	args := []any{field}
	if in, pa := projectInClause(projects); in != "" {
		cq += " AND issue_key IN (SELECT key FROM issues WHERE project IN " + in + ")"
		args = append(args, pa...)
	}
	cq += " GROUP BY issue_key"

	rows, err := s.db.Query(cq, args...)
	if err != nil {
		return nil, fmt.Errorf("count %s changes: %w", field, err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		out[key] = n
	}
	return out, rows.Err()
}

// issueKeys returns the sorted set of issue keys in scope.
func (s *Service) issueKeys(projects []string) ([]string, error) {
	q := `SELECT key FROM issues`
	var args []any
	if in, pa := projectInClause(projects); in != "" {
		q += " WHERE project IN " + in
		args = pa
	}
	q += " ORDER BY key"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("load issue keys: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// triageLatencyProvider reports first-response / triage latency per issue: the number of
// days from an issue's creation to the earliest of (first assignment, first comment).
type triageLatencyProvider struct{}

func (triageLatencyProvider) Metric() string { return MetricTriageLatency }

func (triageLatencyProvider) Compute(s *Service, q Query) (*Result, error) {
	issues, err := s.loadTriageIssues(q.Projects)
	if err != nil {
		return nil, err
	}
	firstAssign, err := s.firstAssignmentTimes(q.Projects, issues)
	if err != nil {
		return nil, err
	}
	firstComment, err := s.firstCommentTimes(q.Projects)
	if err != nil {
		return nil, err
	}

	r := s.newResult(q)
	r.Cohort = "first-response / triage latency: created -> first assignment or first comment (days)"
	r.Policies["first_response"] = "min(first assignment, first comment); assignment-at-creation credited (latency 0)"

	var (
		samples    []float64
		noCreated  int
		noResponse int
	)
	for _, it := range issues {
		if it.created == nil {
			noCreated++
			continue
		}
		var resp *time.Time
		if t := firstAssign[it.key]; t != nil {
			resp = t
		}
		if t := firstComment[it.key]; t != nil && (resp == nil || t.Before(*resp)) {
			resp = t
		}
		if resp == nil {
			noResponse++
			continue
		}
		days := resp.Sub(*it.created).Hours() / 24
		if days < 0 {
			days = 0
		}
		samples = append(samples, days)
		r.Eligible++
	}

	r.Distribution = distribution(samples)
	r.Distribution.Unit = "days"
	r.exclude("no first response (never assigned, no comment)", noResponse)
	r.exclude("unknown creation time", noCreated)
	return r, nil
}

// triageIssue is a scope issue projected for the triage computation.
type triageIssue struct {
	key             string
	created         *time.Time
	assignee        string
	historyComplete bool
}

// loadTriageIssues projects the issues in scope with their creation time, current assignee
// and whether changelog history is complete (needed to credit assignment at creation).
func (s *Service) loadTriageIssues(projects []string) ([]triageIssue, error) {
	q := `SELECT key, created, assignee, changelog_synced_at FROM issues`
	var args []any
	if in, pa := projectInClause(projects); in != "" {
		q += " WHERE project IN " + in
		args = pa
	}
	q += " ORDER BY key"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("load triage issues: %w", err)
	}
	defer rows.Close()
	var out []triageIssue
	for rows.Next() {
		var (
			key                       string
			created, assignee, synced sql.NullString
		)
		if err := rows.Scan(&key, &created, &assignee, &synced); err != nil {
			return nil, err
		}
		out = append(out, triageIssue{
			key:             key,
			created:         nullTime(created),
			assignee:        assignee.String,
			historyComplete: synced.Valid && strings.TrimSpace(synced.String) != "",
		})
	}
	return out, rows.Err()
}

// firstAssignmentTimes resolves, per issue, the instant it was first assigned to a non-empty
// assignee, using the defined semantics: an assignee already present before the first
// recorded assignee event (non-empty from), or a current assignee with no assignee events on
// a complete history, is treated as assigned AT creation (returned as the issue's created
// time). Otherwise it is the first event that transitions to a non-empty assignee. Issues
// never assigned have no entry.
func (s *Service) firstAssignmentTimes(projects []string, issues []triageIssue) (map[string]*time.Time, error) {
	type ev struct {
		fromAssigned bool
		toAssigned   bool
		at           time.Time
	}
	byKey := map[string][]ev{}

	cq := `SELECT issue_key, from_value, from_string, to_value, to_string, changed_at
	         FROM changelog WHERE field = ?`
	args := []any{assigneeField}
	if in, pa := projectInClause(projects); in != "" {
		cq += " AND issue_key IN (SELECT key FROM issues WHERE project IN " + in + ")"
		args = append(args, pa...)
	}
	cq += " ORDER BY issue_key, changed_at, id"
	rows, err := s.db.Query(cq, args...)
	if err != nil {
		return nil, fmt.Errorf("load assignee events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			key                             string
			fromV, fromS, toV, toS, changed sql.NullString
		)
		if err := rows.Scan(&key, &fromV, &fromS, &toV, &toS, &changed); err != nil {
			return nil, err
		}
		if !changed.Valid || strings.TrimSpace(changed.String) == "" {
			continue
		}
		byKey[key] = append(byKey[key], ev{
			fromAssigned: assigneeSet(fromV.String, fromS.String),
			toAssigned:   assigneeSet(toV.String, toS.String),
			at:           parseDBTime(changed.String),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := map[string]*time.Time{}
	for _, it := range issues {
		evs := byKey[it.key]
		if len(evs) == 0 {
			// No assignee history: a current assignee on a complete history means the
			// issue was assigned at creation (evidenced). Otherwise unknown/never.
			if it.historyComplete && assigneeSet(it.assignee, "") && it.created != nil {
				t := *it.created
				out[it.key] = &t
			}
			continue
		}
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].at.Before(evs[j].at) })
		if evs[0].fromAssigned {
			// Already assigned before the first recorded change -> assigned at/before
			// creation.
			if it.created != nil {
				t := *it.created
				out[it.key] = &t
			}
			continue
		}
		for _, e := range evs {
			if e.toAssigned {
				t := e.at
				out[it.key] = &t
				break
			}
		}
	}
	return out, nil
}

// firstCommentTimes returns the earliest comment time per issue in scope.
func (s *Service) firstCommentTimes(projects []string) (map[string]*time.Time, error) {
	q := `SELECT issue_key, MIN(CAST(created AS TEXT)) FROM comments WHERE created IS NOT NULL`
	var args []any
	if in, pa := projectInClause(projects); in != "" {
		q += " AND issue_key IN (SELECT key FROM issues WHERE project IN " + in + ")"
		args = pa
	}
	q += " GROUP BY issue_key"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("load first comments: %w", err)
	}
	defer rows.Close()
	out := map[string]*time.Time{}
	for rows.Next() {
		var key string
		var created sql.NullString
		if err := rows.Scan(&key, &created); err != nil {
			return nil, err
		}
		if created.Valid && strings.TrimSpace(created.String) != "" {
			t := parseDBTime(created.String)
			if !t.IsZero() {
				out[key] = &t
			}
		}
	}
	return out, rows.Err()
}

// assigneeSet reports whether an assignee value/display pair denotes a genuine assignment
// (non-empty account id or display name), i.e. not the unassigned state.
func assigneeSet(value, display string) bool {
	return strings.TrimSpace(value) != "" || strings.TrimSpace(display) != ""
}

// trackedContains reports whether field is among the configured tracked fields
// (case-insensitive), used to gate signals that must be configured before being promised.
func trackedContains(pol *Policy, field string) bool {
	for _, f := range pol.TrackedFields() {
		if strings.EqualFold(f, field) {
			return true
		}
	}
	return false
}

// projectInClause builds a parenthesized placeholder list and args for a project IN filter,
// or ("", nil) when the scope is unrestricted.
func projectInClause(projects []string) (string, []any) {
	if len(projects) == 0 {
		return "", nil
	}
	ph := make([]string, len(projects))
	args := make([]any, len(projects))
	for i, p := range projects {
		ph[i] = "?"
		args[i] = p
	}
	return "(" + strings.Join(ph, ", ") + ")", args
}
