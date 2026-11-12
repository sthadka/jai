package live

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// Whitelists — the ONLY identifiers that may reach SQL.
// -----------------------------------------------------------------------------
//
// The /q endpoint is read-only, SELECT-only, and parameterized from a FIXED SQL
// template. Client tokens NEVER reach SQL: each token is looked up in one of the
// maps below and mapped to a SERVER-DEFINED SQL fragment (column name, temporal
// join, aggregation). Anything not present in a whitelist is rejected with 400,
// so there is no injection or write surface. The only value-based inputs are the
// project (validated against the active generation's project set) and the date
// window (validated to YYYY-MM-DD), both re-emitted as canonical literals.

// measureSpec describes an aggregatable measure. valueExpr is the per-row numeric
// expression (empty => count-only, e.g. throughput); filter is an extra WHERE
// fragment dropping rows the measure is undefined for; join is an optional
// server-authored JOIN that provides an alias referenced by valueExpr (used for
// per-issue changelog change-counts, pre-aggregated so no correlated subquery
// reaches an aggregate). Exactly one measure is emitted per query, so every
// measure join may reuse the same alias without collision.
type measureSpec struct {
	valueExpr string
	filter    string
	unit      string
	join      string
}

// changelogCountJoin builds a server-authored, pre-aggregated per-issue count of
// changelog rows for one tracked field. The field literal is a fixed, code-defined
// token (priority/assignee/Rank) — never client text — so there is no injection
// surface. LEFT JOIN + COALESCE keeps issues with zero changes in the population.
func changelogCountJoin(alias, field string) string {
	return fmt.Sprintf(
		"LEFT JOIN (SELECT issue_key, count(*) AS cnt FROM jai.changelog WHERE field = '%s' GROUP BY issue_key) %s ON %s.issue_key = f.issue_key",
		field, alias, alias)
}

// measures is the whitelist of measure tokens. All SQL fragments are constant,
// server-authored strings over the `f` (fact_issue_flow) alias.
var measures = map[string]measureSpec{
	"cycle_time":     {valueExpr: "f.cycle_seconds / 86400.0", filter: "f.cycle_seconds IS NOT NULL", unit: "days"},
	"lead_time":      {valueExpr: "f.lead_seconds / 86400.0", filter: "f.lead_seconds IS NOT NULL", unit: "days"},
	"flow_occupancy": {valueExpr: "f.flow_occupancy", filter: "f.flow_occupancy IS NOT NULL", unit: "ratio"},
	"triage_latency": {valueExpr: "(epoch(f.first_active_at) - epoch(f.created)) / 86400.0", filter: "f.first_active_at IS NOT NULL AND f.created IS NOT NULL AND f.first_active_at >= f.created", unit: "days"},
	"estimate":       {valueExpr: "f.estimate", filter: "f.estimate IS NOT NULL", unit: "points"},
	"throughput":     {valueExpr: "", filter: "", unit: "count"},

	// Reopen-count distribution (per-issue reopen tally; 0 for never-reopened).
	"reopen_count": {valueExpr: "f.reopen_count", unit: "count"},

	// Age of OPEN issues as-of now, in days. Pair with cohort=open; defaults leave
	// the date window unset so open issues (resolved IS NULL) are not filtered out.
	"aged_open": {valueExpr: "(epoch(CURRENT_TIMESTAMP) - epoch(f.created)) / 86400.0", filter: "f.resolved IS NULL AND f.created IS NOT NULL", unit: "days"},

	// Per-issue change-count distributions over the changelog. Each uses a
	// pre-aggregated grouped join (changelogCountJoin) — a server-authored constant
	// fragment, never client SQL.
	"priority_changes":    {valueExpr: "COALESCE(mj.cnt, 0)", unit: "count", join: changelogCountJoin("mj", "priority")},
	"rank_volatility":     {valueExpr: "COALESCE(mj.cnt, 0)", unit: "count", join: changelogCountJoin("mj", "Rank")},
	"reassignment_count":  {valueExpr: "COALESCE(mj.cnt, 0)", unit: "count", join: changelogCountJoin("mj", "assignee")},
	"reassignment_thrash": {valueExpr: "GREATEST(COALESCE(mj.cnt, 0) - 1, 0)", unit: "count", join: changelogCountJoin("mj", "assignee")},

	// Blocker-ageing proxy: age (days) of OPEN issues currently linked via a
	// CONFIGURED blocker link type. The link-type set is bound as the server-side
	// `blocker_links` variable (from metrics.blocker_link_types — no tenant literal
	// in code); when unconfigured the variable is empty and the EXISTS never matches,
	// so zero rows are returned and nothing is fabricated. The dashboard marks the
	// panel explicitly unavailable in that case (requires: blocker_link_types).
	"blocker_ageing": {
		valueExpr: "(epoch(CURRENT_TIMESTAMP) - epoch(f.created)) / 86400.0",
		filter:    "f.resolved IS NULL AND f.created IS NOT NULL AND EXISTS (SELECT 1 FROM jai.issue_links bl WHERE bl.issue_key = f.issue_key AND list_contains(getvariable('blocker_links'), bl.link_type))",
		unit:      "days",
	},
}

// dimKind selects how a dimension produces its group value.
type dimKind int

const (
	dimNone            dimKind = iota // no grouping
	dimColumn                         // a column on fact_issue_flow
	dimPeriod                         // date_trunc(grain, resolved) — resolution time series
	dimCreatedPeriod                  // date_trunc(grain, created) — creation time series
	dimMembership                     // event-time fact_issue_membership temporal join
	dimInterval                       // event-time fact_field_interval temporal join (value at resolved)
	dimCurrentInterval                // fact_field_interval CURRENT (open) interval value
)

// dimSpec describes a slice/segment dimension. For column dims, expr is the SQL
// column. For membership/interval dims, joinValue is the fixed dimension/field
// literal and the group value comes from the joined alias.
type dimSpec struct {
	kind      dimKind
	expr      string // dimColumn only
	joinValue string // dimMembership (dimension) / dimInterval/dimCurrentInterval (field)
}

// dimensions is the whitelist of slice/segment tokens.
var dimensions = map[string]dimSpec{
	"none":             {kind: dimNone},
	"period":           {kind: dimPeriod},
	"created_period":   {kind: dimCreatedPeriod},
	"project":          {kind: dimColumn, expr: "f.project"},
	"type":             {kind: dimColumn, expr: "f.type"},
	"resolution":       {kind: dimColumn, expr: "f.resolution"},
	"status":           {kind: dimColumn, expr: "f.current_stage"},
	"priority":         {kind: dimInterval, joinValue: "priority"},
	"assignee":         {kind: dimInterval, joinValue: "assignee"},
	"assignee_current": {kind: dimCurrentInterval, joinValue: "assignee"},
	"component":        {kind: dimMembership, joinValue: "component"},
	"label":            {kind: dimMembership, joinValue: "label"},
	"sprint":           {kind: dimMembership, joinValue: "sprint"},
}

// grains is the whitelist of time-grain tokens; each maps to a DuckDB
// date_trunc part (server-authored, never client text).
var grains = map[string]string{
	"day":     "day",
	"week":    "week",
	"month":   "month",
	"quarter": "quarter",
	"year":    "year",
}

// cohorts is the whitelist of completion cohorts. The delivered cohort reads the
// config-derived `done_res` variable (bound at engine setup — no tenant literal).
var cohorts = map[string]string{
	"delivered": "f.resolved IS NOT NULL AND list_contains(getvariable('done_res'), f.resolution)",
	"resolved":  "f.resolved IS NOT NULL",
	"open":      "f.resolved IS NULL",
	"all":       "1 = 1",
}

// QueryParams are the raw client tokens from /q. All are validated before use.
type QueryParams struct {
	Measure string
	Slice   string
	Segment string
	Grain   string
	Project string
	From    string
	To      string
	Cohort  string
}

// Whitelist builds queries against the fixed template. It holds the set of
// projects present in the active generation so the project token is validated,
// never interpolated blindly.
type Whitelist struct {
	mu       sync.RWMutex
	projects map[string]bool
}

// NewWhitelist binds the active generation's project set.
func NewWhitelist(projects []string) *Whitelist {
	return &Whitelist{projects: projectSet(projects)}
}

// SetProjects replaces the validated project set (called from /meta so a
// `metrics rebuild` that changed the project set is honored without a restart).
func (w *Whitelist) SetProjects(projects []string) {
	set := projectSet(projects)
	w.mu.Lock()
	w.projects = set
	w.mu.Unlock()
}

func projectSet(projects []string) map[string]bool {
	set := make(map[string]bool, len(projects))
	for _, p := range projects {
		set[p] = true
	}
	return set
}

// defaults fills unset tokens with safe defaults.
func (p QueryParams) withDefaults() QueryParams {
	if p.Measure == "" {
		p.Measure = "cycle_time"
	}
	if p.Slice == "" {
		p.Slice = "none"
	}
	if p.Segment == "" {
		p.Segment = "none"
	}
	if p.Grain == "" {
		p.Grain = "month"
	}
	if p.Cohort == "" {
		p.Cohort = "delivered"
	}
	return p
}

// dimSelect returns the SELECT expression and (optional) JOIN clause for a
// dimension, using a server-generated alias. grainPart is the validated
// date_trunc part for period dims.
func dimSelect(spec dimSpec, alias, grainPart string) (sel, join string) {
	switch spec.kind {
	case dimNone:
		return "''", ""
	case dimColumn:
		return "CAST(" + spec.expr + " AS VARCHAR)", ""
	case dimPeriod:
		return "CAST(date_trunc('" + grainPart + "', f.resolved) AS VARCHAR)", ""
	case dimCreatedPeriod:
		return "CAST(date_trunc('" + grainPart + "', f.created) AS VARCHAR)", ""
	case dimMembership:
		// Event-time membership as-of resolution; for still-open issues (resolved
		// IS NULL) fall back to the CURRENTLY-open interval (exited_at IS NULL) so
		// the open cohort gets its current members. Delivered path is unchanged, so
		// reconciliation holds. No CURRENT_TIMESTAMP → no VARCHAR/timestamp type mix.
		j := fmt.Sprintf("JOIN jai.fact_issue_membership %s ON %s.generation = f.generation AND %s.issue_key = f.issue_key AND %s.dimension = '%s' AND ((%s.entered_at <= f.resolved AND (%s.exited_at IS NULL OR %s.exited_at > f.resolved)) OR (f.resolved IS NULL AND %s.exited_at IS NULL))",
			alias, alias, alias, alias, spec.joinValue, alias, alias, alias, alias)
		return "CAST(" + alias + ".member_name AS VARCHAR)", j
	case dimInterval:
		j := fmt.Sprintf("JOIN jai.fact_field_interval %s ON %s.generation = f.generation AND %s.issue_key = f.issue_key AND %s.field = '%s' AND ((%s.entered_at <= f.resolved AND (%s.exited_at IS NULL OR %s.exited_at > f.resolved)) OR (f.resolved IS NULL AND %s.exited_at IS NULL))",
			alias, alias, alias, alias, spec.joinValue, alias, alias, alias, alias)
		return "CAST(" + alias + ".value AS VARCHAR)", j
	case dimCurrentInterval:
		// CURRENT value = the open interval (exited_at IS NULL): the owner/value in
		// effect right now, independent of resolution time (workload_*_current).
		j := fmt.Sprintf("JOIN jai.fact_field_interval %s ON %s.generation = f.generation AND %s.issue_key = f.issue_key AND %s.field = '%s' AND %s.exited_at IS NULL",
			alias, alias, alias, alias, spec.joinValue, alias)
		return "CAST(" + alias + ".value AS VARCHAR)", j
	}
	return "''", ""
}

// Build validates all tokens and assembles the fixed SELECT template. It returns
// a 400-worthy error for any non-whitelisted token or malformed date.
func (w *Whitelist) Build(raw QueryParams) (string, error) {
	p := raw.withDefaults()

	m, ok := measures[p.Measure]
	if !ok {
		return "", fmt.Errorf("unknown measure %q", p.Measure)
	}
	sliceSpec, ok := dimensions[p.Slice]
	if !ok {
		return "", fmt.Errorf("unknown slice %q", p.Slice)
	}
	segSpec, ok := dimensions[p.Segment]
	if !ok {
		return "", fmt.Errorf("unknown segment %q", p.Segment)
	}
	grainPart, ok := grains[p.Grain]
	if !ok {
		return "", fmt.Errorf("unknown grain %q", p.Grain)
	}
	cohortExpr, ok := cohorts[p.Cohort]
	if !ok {
		return "", fmt.Errorf("unknown cohort %q", p.Cohort)
	}

	where := []string{
		"f.generation = getvariable('g')",
		"(" + cohortExpr + ")",
	}

	// Project: validated against the active generation's set, then re-emitted as a
	// canonical literal (never raw client text reaching SQL).
	if p.Project != "" && p.Project != "all" {
		w.mu.RLock()
		known := w.projects[p.Project]
		w.mu.RUnlock()
		if !known {
			return "", fmt.Errorf("unknown project %q", p.Project)
		}
		where = append(where, "f.project = '"+sqlEscape(p.Project)+"'")
	}

	// Date window: parsed as YYYY-MM-DD and re-emitted canonically. The window
	// applies to the cohort's time basis: completion time for delivered/resolved,
	// but CREATION time for the open cohort (open issues have resolved IS NULL, so a
	// resolved-based window would wipe them) and for the catch-all cohort.
	dateCol := "f.resolved"
	if p.Cohort == "open" || p.Cohort == "all" {
		dateCol = "f.created"
	}
	if p.From != "" {
		d, err := parseDate(p.From)
		if err != nil {
			return "", fmt.Errorf("invalid from date %q", p.From)
		}
		where = append(where, dateCol+" >= DATE '"+d+"'")
	}
	if p.To != "" {
		d, err := parseDate(p.To)
		if err != nil {
			return "", fmt.Errorf("invalid to date %q", p.To)
		}
		where = append(where, dateCol+" < DATE '"+d+"'")
	}

	if m.filter != "" {
		where = append(where, m.filter)
	}

	sliceSel, sliceJoin := dimSelect(sliceSpec, "d0", grainPart)
	segSel, segJoin := dimSelect(segSpec, "d1", grainPart)

	// Aggregation columns: percentiles/avg/total for value measures; NULL for
	// count-only measures (n carries the count).
	var aggCols string
	if m.valueExpr == "" {
		aggCols = "NULL AS p50, NULL AS p85, NULL AS p95, NULL AS avg, NULL AS total"
	} else {
		v := m.valueExpr
		aggCols = fmt.Sprintf(
			"round(quantile_cont(%s, 0.50), 3) AS p50, round(quantile_cont(%s, 0.85), 3) AS p85, round(quantile_cont(%s, 0.95), 3) AS p95, round(avg(%s), 3) AS avg, round(sum(%s), 3) AS total",
			v, v, v, v, v)
	}

	var b strings.Builder
	b.WriteString("SELECT\n")
	fmt.Fprintf(&b, "  %s AS slice,\n", sliceSel)
	fmt.Fprintf(&b, "  %s AS segment,\n", segSel)
	b.WriteString("  count(*) AS n,\n")
	b.WriteString("  " + aggCols + "\n")
	b.WriteString("FROM jai.fact_issue_flow f\n")
	if m.join != "" {
		b.WriteString(m.join + "\n")
	}
	if sliceJoin != "" {
		b.WriteString(sliceJoin + "\n")
	}
	if segJoin != "" {
		b.WriteString(segJoin + "\n")
	}
	b.WriteString("WHERE " + strings.Join(where, "\n  AND ") + "\n")
	b.WriteString("GROUP BY slice, segment\n")
	b.WriteString("ORDER BY n DESC\n")
	b.WriteString("LIMIT 1000")
	return b.String(), nil
}

// -----------------------------------------------------------------------------
// Small SQL / JSON helpers
// -----------------------------------------------------------------------------

// sqlEscape doubles single quotes so a server-controlled literal cannot break
// out of its quotes. (Client-derived values also pass a whitelist first.)
func sqlEscape(s string) string { return strings.ReplaceAll(s, "'", "''") }

// sqlStringList renders a Go slice as a DuckDB list literal: ['a', 'b'].
func sqlStringList(items []string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = "'" + sqlEscape(it) + "'"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// parseDate validates YYYY-MM-DD and returns it in canonical form.
func parseDate(s string) (string, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return "", err
	}
	return t.Format("2006-01-02"), nil
}

// decodeRows parses one DuckDB JSON-array output line into row maps.
func decodeRows(line string) ([]map[string]any, error) {
	var rows []map[string]any
	if err := json.Unmarshal([]byte(line), &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// -----------------------------------------------------------------------------
// Human labels — the single source of truth for the dropdown vocabulary. The
// server emits {value,label} objects on /meta so the SPA renders readable names
// while still sending/validating the raw whitelist tokens. Measures additionally
// carry their unit so tiles format values (days/ratio/points/count).
// -----------------------------------------------------------------------------

// measureLabels maps each measure token to its human-readable display name.
var measureLabels = map[string]string{
	"cycle_time":          "Cycle Time",
	"lead_time":           "Lead Time",
	"flow_occupancy":      "Flow Occupancy",
	"triage_latency":      "Triage Time",
	"estimate":            "Effort (Estimate)",
	"throughput":          "Issues Completed",
	"reopen_count":        "Reopens",
	"aged_open":           "Issue Age",
	"priority_changes":    "Priority Changes",
	"rank_volatility":     "Rank Changes",
	"reassignment_count":  "Reassignments",
	"reassignment_thrash": "Reassignment Thrash",
	"blocker_ageing":      "Blocked Age",
}

// dimensionLabels maps each slice/segment token to its human-readable name.
var dimensionLabels = map[string]string{
	"none":             "Total",
	"period":           "Completed (by period)",
	"created_period":   "Created (by period)",
	"project":          "Project",
	"type":             "Type",
	"resolution":       "Resolution",
	"status":           "Status",
	"priority":         "Priority",
	"assignee":         "Assignee (at completion)",
	"assignee_current": "Assignee (current)",
	"component":        "Component",
	"label":            "Label",
	"sprint":           "Sprint",
}

// cohortLabels maps each cohort token to its human-readable name.
var cohortLabels = map[string]string{
	"delivered": "Delivered",
	"resolved":  "Resolved",
	"open":      "Open",
	"all":       "All",
}

// grainLabels maps each grain token to its human-readable name.
var grainLabels = map[string]string{
	"day":     "Day",
	"week":    "Week",
	"month":   "Month",
	"quarter": "Quarter",
	"year":    "Year",
}

// LabelOption is a {value,label} dropdown entry emitted on /meta.
type LabelOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// MeasureOption extends LabelOption with the measure's unit so the SPA can format
// tile values (e.g. "12.3d", "0.42", "47 pts", "47").
type MeasureOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Unit  string `json:"unit"`
}

// MeasureOptions / DimensionOptions / GrainOptions / CohortOptions expose the
// whitelists as {value,label} arrays (measures also carry unit) for the /meta
// endpoint, so the SPA builds its dropdowns from the same source of truth that
// validates requests.
func MeasureOptions() []MeasureOption {
	keys := sortedKeys(measuresKeys())
	out := make([]MeasureOption, 0, len(keys))
	for _, k := range keys {
		lbl := measureLabels[k]
		if lbl == "" {
			lbl = k
		}
		out = append(out, MeasureOption{Value: k, Label: lbl, Unit: measures[k].unit})
	}
	return out
}

func DimensionOptions() []LabelOption {
	return labelOptions(sortedKeys(dimensionKeys()), dimensionLabels)
}

func CohortOptions() []LabelOption {
	return labelOptions(sortedKeys(cohortKeys()), cohortLabels)
}

// GrainOptions returns grains in logical (not alphabetical) order.
func GrainOptions() []LabelOption {
	return labelOptions([]string{"day", "week", "month", "quarter", "year"}, grainLabels)
}

// labelOptions zips tokens with their human labels (falling back to the token).
func labelOptions(keys []string, labels map[string]string) []LabelOption {
	out := make([]LabelOption, 0, len(keys))
	for _, k := range keys {
		lbl := labels[k]
		if lbl == "" {
			lbl = k
		}
		out = append(out, LabelOption{Value: k, Label: lbl})
	}
	return out
}

func measuresKeys() []string {
	ks := make([]string, 0, len(measures))
	for k := range measures {
		ks = append(ks, k)
	}
	return ks
}

func dimensionKeys() []string {
	ks := make([]string, 0, len(dimensions))
	for k := range dimensions {
		ks = append(ks, k)
	}
	return ks
}

func cohortKeys() []string {
	ks := make([]string, 0, len(cohorts))
	for k := range cohorts {
		ks = append(ks, k)
	}
	return ks
}

func sortedKeys(ks []string) []string {
	sort.Strings(ks)
	return ks
}
