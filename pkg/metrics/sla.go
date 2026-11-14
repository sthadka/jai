package metrics

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sthadka/jai/internal/config"
)

// MetricSecuritySLA is the security remediation-timing / SLA metric. It is deliberately
// two-tiered (spec §Vulnerability / security SLA):
//
//   - Remediation timing — resolution duration (clock_start -> resolved) for the security
//     type class. This is always available when there are resolved security issues; it is
//     NOT a compliance claim (CVE/CVSS + a duration is timing, not an SLA).
//   - SLA compliance — only when a *complete* policy is configured: a categorical severity
//     source with per-severity deadlines, an explicit clock_start, and a missing-severity
//     rule. Without a complete policy the metric reports timing plus an actionable
//     unavailable-policy diagnostic, never a fabricated compliance number.
const MetricSecuritySLA = "security_sla"

// classSecurity is this package's canonical class name for the security cohort (like the
// canonical stage names). A tenant maps its own vulnerability issue types to this class via
// metrics.type_classes.security; the code never hard-codes a tenant issue type.
const classSecurity = "security"

// ErrSLANotConfigured is returned when no sla.security policy exists at all. The metric is
// a security SLA: with no policy there is nothing to report, so it fails closed (disabled)
// rather than guessing a severity source or deadline.
var ErrSLANotConfigured = errors.New("metrics: security SLA not configured (set metrics.sla.security)")

func init() { Register(securitySLAProvider{}) }

// slaPolicy is the SLA configuration resolved for a run. numeric selects the severity
// source (bands) vs categorical (deadlines). complianceReady reports whether the policy is
// complete enough to judge compliance; when false the metric still reports remediation
// timing and publishes why compliance is unavailable.
type slaPolicy struct {
	severityField string
	numeric       bool
	deadlines     map[string]int        // categorical label -> deadline days
	bands         []config.SeverityBand // numeric score -> label (descending by Ge)
	clockStart    string                // timestamp column; explicit for compliance
	onMissingExcl bool                  // true => exclude missing-severity issues
	treatAsLabel  string                // non-empty => treat missing severity as this label
}

// securitySLAProvider computes remediation timing and (when the policy is complete)
// SLA compliance over the security type class.
type securitySLAProvider struct{}

func (securitySLAProvider) Metric() string { return MetricSecuritySLA }

func (securitySLAProvider) Compute(s *Service, q Query) (*Result, error) {
	sec := slaSecurityConfig(s)
	if sec == nil {
		return nil, ErrSLANotConfigured // fail closed: no policy, no guessed SLA.
	}

	asOf := s.asOf(q)
	r := s.newResult(q)
	r.Counts = map[string]int{}
	pol := resolveSLAPolicy(sec)

	// Compliance requires a structurally-valid, complete policy. Any gap downgrades the
	// result to remediation timing plus an explicit unavailable-policy diagnostic.
	complianceReady := true
	if !s.pol.Enabled(MetricSLA) {
		complianceReady = false
		r.warn("SLA compliance unavailable: sla policy is structurally invalid (fail closed): " +
			strings.Join(s.pol.Rejections()[MetricSLA], "; "))
	}
	if pol.numeric {
		complianceReady = false
		r.warn("SLA compliance unavailable: numeric severity_bands classify severity but define no per-severity deadline; configure categorical deadlines_days for compliance")
	}
	if strings.TrimSpace(pol.clockStart) == "" {
		complianceReady = false
		r.warn("SLA compliance unavailable: clock_start is not set; a compliance clock origin must be declared explicitly, not defaulted")
	}

	// Resolve the severity column (validated against the schema before interpolation).
	sevCol := strings.TrimSpace(pol.severityField)
	if sevCol != "" {
		ok, err := s.columnExists("issues", sevCol)
		if err != nil {
			return nil, err
		}
		if !ok {
			complianceReady = false
			sevCol = ""
			r.warn(fmt.Sprintf("severity source field %q is not a readable column; severity cannot be resolved", pol.severityField))
		}
	} else if pol.numeric {
		r.warn("numeric severity_bands require severity_field to name the numeric score column; severity cannot be resolved without it")
	}

	// Resolve the clock-origin column. clock_start names a timestamp column; remediation
	// timing falls back to created when clock_start is unset (labelled), compliance does not.
	clockCol := strings.TrimSpace(pol.clockStart)
	clockLabel := clockCol
	if clockCol == "" {
		clockCol = "created"
		clockLabel = "created (default; clock_start unset)"
	}
	if ok, err := s.columnExists("issues", clockCol); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("metrics: clock_start column %q not found", clockCol)
	}

	issues, err := s.loadSecurityIssues(q.Projects, sevCol, clockCol)
	if err != nil {
		return nil, err
	}

	r.Cohort = fmt.Sprintf("security type-class issues; remediation clock from %s", clockLabel)
	r.Policies["sla_mode"] = ternary(pol.numeric, "numeric_bands", "categorical_deadlines")
	r.Policies["sla_compliance"] = ternary(complianceReady, "evaluated", "unavailable")
	r.warn("no approved-pause policy is configured; durations are wall-clock from the clock origin, not pause-adjusted")

	var (
		remediationDays       []float64
		notResolved           int
		noClockOrigin         int
		compliant, breached   int
		openOverdue, openOK   int
		excludedMissing       int
		missingSeverityCohort int
	)
	for _, it := range issues {
		if s.pol.ClassForType(it.itype) != classSecurity {
			continue // non-security issue; not in the SLA cohort.
		}
		r.Eligible++

		// Severity classification (for cohorts and, categorically, for deadlines).
		label, haveSeverity := pol.classify(it.sevRaw)
		if haveSeverity {
			r.Counts["severity:"+label]++
		} else {
			missingSeverityCohort++
			r.Counts["severity:(missing)"]++
		}

		// Remediation timing: resolved issues only, clock origin required.
		if it.resolved == nil {
			notResolved++
		} else if it.clockAt == nil {
			noClockOrigin++
		} else {
			d := it.resolved.Sub(*it.clockAt).Hours() / 24
			if d < 0 {
				d = 0
			}
			remediationDays = append(remediationDays, d)
		}

		if !complianceReady {
			continue
		}
		// Compliance evaluation (categorical deadlines).
		deadlineDays, ok := pol.deadlineFor(label, haveSeverity)
		if !ok {
			// Missing/unknown severity under compliance: exclude (surfaced) or treat_as.
			excludedMissing++
			continue
		}
		if it.clockAt == nil {
			noClockOrigin++
			continue
		}
		deadlineTime := it.clockAt.Add(time.Duration(deadlineDays) * 24 * time.Hour)
		switch {
		case it.resolved != nil:
			if !it.resolved.After(deadlineTime) {
				compliant++
			} else {
				breached++ // resolved after the deadline.
			}
		default: // open issue
			if asOf.After(deadlineTime) {
				breached++
				openOverdue++ // open and past deadline: still a breach, per explicit policy.
			} else {
				openOK++ // open but within the SLA window (not yet a breach, not compliant).
			}
		}
	}

	r.Distribution = distribution(remediationDays)
	r.Counts["remediation_resolved"] = len(remediationDays)
	r.exclude("not_resolved", notResolved)
	r.exclude("no_clock_origin", noClockOrigin)

	if complianceReady {
		r.Counts["sla:compliant"] = compliant
		r.Counts["sla:breached"] = breached
		r.Counts["sla:open_overdue"] = openOverdue
		r.Counts["sla:open_within"] = openOK
		if pol.onMissingExcl {
			r.exclude("missing_severity", excludedMissing)
		}
		if pol.treatAsLabel != "" {
			r.Counts["missing_severity_treated_as:"+pol.treatAsLabel] = missingSeverityCohort
		}
		judged := compliant + breached
		if judged == 0 {
			r.warn("no issues were judged against the SLA (empty or all-excluded cohort)")
		}
	} else {
		if missingSeverityCohort > 0 {
			r.Counts["severity_unresolved_cohort"] = missingSeverityCohort
		}
		r.warn("result is remediation timing only; no compliance verdict is claimed")
	}
	if r.Eligible == 0 {
		r.warn("no issues map to the security type class (configure metrics.type_classes.security)")
	}
	return r, nil
}

// slaIssue is a security-cohort snapshot row with the resolved severity value and the
// clock-origin timestamp already projected.
type slaIssue struct {
	key, project, itype string
	category, sevRaw    string
	resolved, clockAt   *time.Time
}

// slaSecurityConfig returns the raw sla.security block, or nil when absent (fail closed).
func slaSecurityConfig(s *Service) *config.SLASecurityConfig {
	if s.cfg == nil || s.cfg.Metrics == nil || s.cfg.Metrics.SLA == nil {
		return nil
	}
	return s.cfg.Metrics.SLA.Security
}

// resolveSLAPolicy normalizes the raw config into the run-time policy, mirroring the
// structural distinctions the Policy compiler validated (categorical vs numeric source,
// missing-severity rule).
func resolveSLAPolicy(sec *config.SLASecurityConfig) slaPolicy {
	p := slaPolicy{
		severityField: strings.TrimSpace(sec.SeverityField),
		deadlines:     map[string]int{},
		bands:         sec.SeverityBands,
		clockStart:    strings.TrimSpace(sec.ClockStart),
	}
	for k, v := range sec.DeadlinesDays {
		p.deadlines[strings.TrimSpace(k)] = v
	}
	p.numeric = len(sec.SeverityBands) > 0 && len(sec.DeadlinesDays) == 0
	switch om := strings.TrimSpace(sec.OnMissingSeverity); {
	case strings.HasPrefix(om, "treat_as:"):
		p.treatAsLabel = strings.TrimSpace(strings.TrimPrefix(om, "treat_as:"))
	default: // "" or "exclude"
		p.onMissingExcl = true
	}
	return p
}

// classify resolves an issue's severity label. For categorical sources the raw field value
// is the label (when it has a configured deadline); for numeric sources the score is mapped
// through the descending bands. Returns ok=false when severity is absent or unresolvable.
func (p slaPolicy) classify(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if p.numeric {
		score, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return "", false
		}
		return bandLabel(p.bands, score)
	}
	// Categorical: the value is the severity label only when it has a configured deadline.
	if _, ok := p.deadlines[raw]; ok {
		return raw, true
	}
	return raw, false // present but unmapped: an unknown severity, surfaced not dropped.
}

// deadlineFor returns the deadline days for a severity label under a categorical policy,
// applying the missing-severity rule (exclude vs treat_as) when severity is absent/unknown.
func (p slaPolicy) deadlineFor(label string, haveSeverity bool) (int, bool) {
	if haveSeverity {
		d, ok := p.deadlines[label]
		return d, ok
	}
	if p.treatAsLabel != "" {
		if d, ok := p.deadlines[p.treatAsLabel]; ok {
			return d, true
		}
	}
	return 0, false // exclude (or treat_as a label with no deadline): no verdict.
}

// bandLabel maps a numeric score to the first band whose floor it meets. Bands are strictly
// descending by Ge (validated at compile), so the first match is the highest applicable
// severity. A score below every floor is unresolved.
func bandLabel(bands []config.SeverityBand, score float64) (string, bool) {
	for _, b := range bands {
		if score >= b.Ge {
			return strings.TrimSpace(b.Label), true
		}
	}
	return "", false
}

// columnExists reports whether table has a column named col, so a config-supplied field
// name is validated before it is interpolated into a query.
func (s *Service) columnExists(table, col string) (bool, error) {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name, ctyp string
			notNull    int
			dflt       sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctyp, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// loadSecurityIssues reads the current issues snapshot projecting the (validated) severity
// and clock-origin columns. sevCol may be empty (no severity source resolvable); clockCol
// is always a validated datetime column.
func (s *Service) loadSecurityIssues(projects []string, sevCol, clockCol string) ([]slaIssue, error) {
	cols := []string{"key", "project", "type", "resolved", "status_category", clockCol + " AS clock_at"}
	sevIdx := -1
	if sevCol != "" {
		sevIdx = len(cols)
		cols = append(cols, sevCol+" AS sev_raw")
	}
	q := "SELECT " + strings.Join(cols, ", ") + " FROM issues"
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
		return nil, fmt.Errorf("load security issues: %w", err)
	}
	defer rows.Close()

	var out []slaIssue
	for rows.Next() {
		dest := []any{}
		var (
			key, project              string
			itype, resolved, category sql.NullString
			clockAt, sevRaw           sql.NullString
		)
		dest = append(dest, &key, &project, &itype, &resolved, &category, &clockAt)
		if sevIdx >= 0 {
			dest = append(dest, &sevRaw)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, slaIssue{
			key: key, project: project, itype: itype.String,
			category: category.String, sevRaw: sevRaw.String,
			resolved: nullTime(resolved), clockAt: nullTime(clockAt),
		})
	}
	return out, rows.Err()
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
