package metrics

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template"
	"time"
)

// dashboardAssets holds the self-contained viz template, stylesheet, and the hand-rolled
// pure-SVG chart renderer. Everything the browser needs is embedded here and inlined into a
// single HTML file: no CDN, no <script src=…>, no runtime fetch, no local server. The
// renderer is a small vetted primitive (plain SVG, no framework) so the artifact is fully
// offline and reviewable rather than a vendored multi-hundred-KB minified blob.
//
//go:embed dashboard/template.html dashboard/chart.js dashboard/style.css
var dashboardAssets embed.FS

// DashboardOptions scopes the exported dashboard. It mirrors the shared service query knobs
// so the dashboard is fed by exactly the same Compute path as the CLI/MCP/TUI consumers;
// there is no forked metric logic.
type DashboardOptions struct {
	Title    string     // dashboard heading (default "Flow Metrics")
	Projects []string   // scope; empty = all projects
	AsOf     time.Time  // open-interval/age reference; zero = now UTC at render time
	Range    *TimeRange // trend window; zero = last 6 months ending at AsOf
}

// dashboardPayload is the complete, self-contained data model inlined into the HTML as a
// single JSON document. Every Jira-controlled string lives here (not in raw markup) and is
// rendered into the DOM via textContent by the renderer, so hostile issue/sprint/assignee
// strings cannot inject markup or script. json.Marshal escapes <, >, & so the embedded JSON
// cannot break out of its <script> container either.
type dashboardPayload struct {
	Title       string                   `json:"title"`
	GeneratedAt string                   `json:"generatedAt"`
	AsOf        string                   `json:"asOf"`
	Projects    []string                 `json:"projects"`
	Range       *rangeJSON               `json:"range,omitempty"`
	Build       *buildJSON               `json:"build,omitempty"`
	Freshness   map[string]freshnessJSON `json:"freshness,omitempty"`
	Disclosure  string                   `json:"disclosure"`
	Sections    []section                `json:"sections"`
}

type rangeJSON struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type buildJSON struct {
	Generation int64  `json:"generation"`
	BuiltAt    string `json:"builtAt,omitempty"`
}

type freshnessJSON struct {
	LastSync string `json:"lastSync,omitempty"`
}

// section is one chart (or an explicit unavailable placeholder). Chart names the renderer
// primitive; Data is the chart-specific payload. Unavailable sections carry a Reason instead
// of fabricating data, so unsupported/unknown coverage is surfaced, never invented.
type section struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Chart     string      `json:"chart"`
	Subtitle  string      `json:"subtitle,omitempty"`
	Available bool        `json:"available"`
	Reason    string      `json:"reason,omitempty"`
	Notes     []string    `json:"notes,omitempty"`
	Data      interface{} `json:"data,omitempty"`
}

// ExportDashboard computes the curated metric set through the shared service and renders ONE
// self-contained offline dashboard.html (returned as bytes). The caller writes it wherever
// it wants. Unconfigured metrics / a missing fact generation degrade gracefully into
// explicit unavailable sections rather than failing the whole export.
func ExportDashboard(svc *Service, opts DashboardOptions) ([]byte, error) {
	if svc == nil {
		return nil, fmt.Errorf("metrics: nil service")
	}
	asOf := opts.AsOf.UTC()
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	trend := opts.Range
	if trend == nil || (trend.From.IsZero() && trend.To.IsZero()) {
		trend = &TimeRange{From: asOf.AddDate(0, -6, 0), To: asOf}
	}

	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "Flow Metrics"
	}

	payload := &dashboardPayload{
		Title:       title,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		AsOf:        asOf.Format(time.RFC3339),
		Projects:    opts.Projects,
		Disclosure: "This dashboard embeds the selected Jira issue data for the scoped " +
			"project(s). Treat it as the source data's sensitivity requires and share it only " +
			"with viewers authorized to see those issues. No credentials are included.",
	}
	if !trend.From.IsZero() || !trend.To.IsZero() {
		payload.Range = &rangeJSON{}
		if !trend.From.IsZero() {
			payload.Range.From = trend.From.UTC().Format("2006-01-02")
		}
		if !trend.To.IsZero() {
			payload.Range.To = trend.To.UTC().Format("2006-01-02")
		}
	}

	base := Query{Projects: opts.Projects, AsOf: asOf}

	// Build/freshness envelope from one representative fact-based compute (throughput),
	// so the dashboard discloses the generation watermark and source freshness it rests on.
	if r, err := svc.Compute(withMetric(base, MetricThroughput)); err == nil {
		if r.BuildGeneration != 0 {
			b := &buildJSON{Generation: r.BuildGeneration}
			if r.BuiltAt != nil {
				b.BuiltAt = r.BuiltAt.UTC().Format(time.RFC3339)
			}
			payload.Build = b
		}
		if len(r.Freshness) > 0 {
			payload.Freshness = map[string]freshnessJSON{}
			for k, f := range r.Freshness {
				payload.Freshness[k] = freshnessJSON{LastSync: freshnessString(f)}
			}
		}
	}
	if svc.store != nil {
		if active, _ := svc.store.ActiveGeneration(); active == nil {
			payload.Sections = append(payload.Sections, noteSection(
				"no-generation",
				"Fact store not built",
				"Fact-based charts (cycle/lead time, CFD, throughput, stage dwell) are empty until a "+
					"generation is built. Run `jai metrics rebuild` first."))
		}
	}

	// --- Curated chart set, per the spec chart-mapping table. ---
	payload.Sections = append(payload.Sections,
		distSection(svc, base, "cycle-time-dist", "Cycle time distribution", MetricCycleTime,
			"first active-stage entry → resolved, delivered cohort"),
		distSection(svc, base, "lead-time-dist", "Lead time distribution", MetricLeadTime,
			"created → resolved, delivered cohort"),
		monthlyPercentileSection(svc, base, trend, "cycle-time-trend",
			"Cycle time percentile trend (p50/p85/p95 by month)", MetricCycleTime),
		monthlyPercentileSection(svc, base, trend, "lead-time-trend",
			"Lead time percentile trend (p50/p85/p95 by month)", MetricLeadTime),
		cfdSection(svc, base),
		seriesSection(svc, base, trend, "throughput", "Completed over time (throughput)",
			MetricThroughput, "bars", "completed issues per month (snapshot semantics)"),
		seriesSection(svc, base, trend, "net-flow", "Net flow (arrivals − departures)",
			MetricNetFlow, "lines", "created vs currently-resolved per month"),
		countsSection(svc, base, "aged-open", "Aged open issues", MetricAgedOpen, "bars",
			"open issues by age bucket, against as_of", "age:"),
		countsSection(svc, base, "resolution-mix", "Resolution mix", MetricResolutionMix, "bars",
			"resolutions among resolved issues", ""),
		ratioSection(svc, base, "ratio", "Type-class ratio", MetricRatio,
			"configured numerator/denominator class ratio"),
		countsSection(svc, base, "workload", "Workload by assignee (open load)", MetricWorkload,
			"hbars", "current-owner open item load", ""),
		sprintSection(svc, base),
		blockerSection(svc, base),
	)

	return renderDashboard(payload)
}

// withMetric clones a base query onto a metric name.
func withMetric(base Query, metric string) Query {
	base.Metric = metric
	return base
}

// freshnessString renders a SourceFreshness into a compact RFC3339 timestamp (local sync
// freshness only), returning "" when no sync time is recorded.
func freshnessString(f SourceFreshness) string {
	if f.LastSyncTime != nil {
		return f.LastSyncTime.UTC().Format(time.RFC3339)
	}
	return ""
}

// round2 rounds to two decimals for compact, stable chart values.
func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

// noteSection makes an unavailable/informational placeholder card.
func noteSection(id, title, reason string) section {
	return section{ID: id, Title: title, Chart: "note", Available: false, Reason: reason}
}

// computeOr returns the Result or, on error, an unavailable section carrying the reason.
func computeOr(svc *Service, q Query, id, title, chart, subtitle string) (*Result, *section) {
	r, err := svc.Compute(q)
	if err != nil {
		s := section{ID: id, Title: title, Chart: chart, Subtitle: subtitle, Available: false,
			Reason: err.Error()}
		return nil, &s
	}
	return r, nil
}

// envelopeNotes collects the published caveats (cohort, genuine-done state, exclusions,
// warnings) so the dashboard discloses them alongside every chart — never a bare number.
func envelopeNotes(r *Result) []string {
	var notes []string
	if r.Cohort != "" {
		notes = append(notes, "cohort: "+r.Cohort)
	}
	if !r.GenuineDone {
		notes = append(notes, "genuine-done not applied (includes non-delivery resolutions or N/A)")
	}
	for _, k := range sortedIntKeys(r.Excluded) {
		notes = append(notes, fmt.Sprintf("excluded %s: %d", k, r.Excluded[k]))
	}
	notes = append(notes, r.Warnings...)
	return notes
}

func distSection(svc *Service, base Query, id, title, metric, subtitle string) section {
	r, bad := computeOr(svc, withMetric(base, metric), id, title, "box", subtitle)
	if bad != nil {
		return *bad
	}
	s := section{ID: id, Title: title, Chart: "box", Subtitle: subtitle, Available: true,
		Notes: envelopeNotes(r)}
	if r.Distribution == nil || r.Distribution.Count == 0 {
		s.Available = false
		s.Reason = "no eligible samples in scope"
		return s
	}
	d := r.Distribution
	s.Data = map[string]interface{}{
		"unit":  d.Unit,
		"count": d.Count,
		"min":   round2(d.Min), "p50": round2(d.P50), "p85": round2(d.P85),
		"p95": round2(d.P95), "max": round2(d.Max), "mean": round2(d.Mean),
	}
	return s
}

// monthlyPercentileSection computes the metric distribution independently per month over the
// trend window, yielding p50/p85/p95 lines. The percentiles are computed from the delivery
// cohort per month — fully independent of any CFD stage counts, as the spec requires.
func monthlyPercentileSection(svc *Service, base Query, trend *TimeRange, id, title, metric string) section {
	subtitle := "percentiles computed independently per month (not derived from CFD counts)"
	s := section{ID: id, Title: title, Chart: "plines", Subtitle: subtitle, Available: true}
	periods := monthBuckets(trend)
	if len(periods) == 0 {
		s.Available = false
		s.Reason = "empty trend window"
		return s
	}
	var labels []string
	var p50, p85, p95 []interface{}
	unit := "days"
	total := 0
	for _, mb := range periods {
		q := withMetric(base, metric)
		q.Range = &TimeRange{From: mb.from, To: mb.to}
		r, err := svc.Compute(q)
		labels = append(labels, mb.label)
		if err != nil || r.Distribution == nil || r.Distribution.Count == 0 {
			p50 = append(p50, nil)
			p85 = append(p85, nil)
			p95 = append(p95, nil)
			continue
		}
		total += r.Distribution.Count
		if r.Distribution.Unit != "" {
			unit = r.Distribution.Unit
		}
		p50 = append(p50, round2(r.Distribution.P50))
		p85 = append(p85, round2(r.Distribution.P85))
		p95 = append(p95, round2(r.Distribution.P95))
	}
	if total == 0 {
		s.Available = false
		s.Reason = "no eligible samples in the trend window"
		return s
	}
	s.Data = map[string]interface{}{
		"unit":    unit,
		"periods": labels,
		"series": []map[string]interface{}{
			{"name": "p50", "values": p50},
			{"name": "p85", "values": p85},
			{"name": "p95", "values": p95},
		},
	}
	return s
}

// cfdSection builds the cumulative flow diagram across ALL workflow stages for a consistent,
// documented population. It does not attempt to recover cycle/lead distribution from the
// stage counts (those are the separate percentile charts).
func cfdSection(svc *Service, base Query) section {
	const id, title = "cfd", "Cumulative flow (WIP by stage)"
	subtitle := "stacked area across all stages; consistent population (grows as issues are created)"
	r, bad := computeOr(svc, withMetric(base, MetricCFD), id, title, "cfd", subtitle)
	if bad != nil {
		return *bad
	}
	s := section{ID: id, Title: title, Chart: "cfd", Subtitle: subtitle, Available: true,
		Notes: envelopeNotes(r)}
	// Counts key: "YYYY-MM-DD|stage". Pivot into dates × stages.
	dateSet := map[string]bool{}
	stageSet := map[string]bool{}
	cells := map[string]map[string]int{} // date -> stage -> count
	for k, v := range r.Counts {
		parts := strings.SplitN(k, cfdSep, 2)
		if len(parts) != 2 {
			continue
		}
		date, stage := parts[0], parts[1]
		dateSet[date] = true
		stageSet[stage] = true
		if cells[date] == nil {
			cells[date] = map[string]int{}
		}
		cells[date][stage] = v
	}
	if len(dateSet) == 0 {
		s.Available = false
		s.Reason = "no CFD data (fact store not built or empty scope)"
		return s
	}
	dates := sortedKeysBool(dateSet)
	stages := StageOrder(sortedKeysBool(stageSet))
	series := make([]map[string]interface{}, 0, len(stages))
	for _, st := range stages {
		vals := make([]int, len(dates))
		for i, d := range dates {
			vals[i] = cells[d][st]
		}
		series = append(series, map[string]interface{}{"name": st, "values": vals})
	}
	s.Data = map[string]interface{}{"dates": dates, "stages": stages, "series": series}
	return s
}

// seriesSection renders a PeriodPoint series as bars or a line, bounded to the trend window.
func seriesSection(svc *Service, base Query, trend *TimeRange, id, title, metric, chart, subtitle string) section {
	q := withMetric(base, metric)
	q.Period = "month"
	q.Range = trend
	r, bad := computeOr(svc, q, id, title, chart, subtitle)
	if bad != nil {
		return *bad
	}
	s := section{ID: id, Title: title, Chart: chart, Subtitle: subtitle, Available: true,
		Notes: envelopeNotes(r)}
	if len(r.Series) == 0 {
		s.Available = false
		s.Reason = "no data in the trend window"
		return s
	}
	labels := make([]string, 0, len(r.Series))
	values := make([]int, 0, len(r.Series))
	for _, p := range r.Series {
		labels = append(labels, p.Period)
		values = append(values, p.Count)
	}
	s.Data = map[string]interface{}{"labels": labels, "values": values}
	return s
}

// countsSection renders a Counts map as bars/hbars. keyPrefix (optional) filters to keys
// with that prefix and strips it from the label (e.g. "age:" for aged-open buckets).
func countsSection(svc *Service, base Query, id, title, metric, chart, subtitle, keyPrefix string) section {
	r, bad := computeOr(svc, withMetric(base, metric), id, title, chart, subtitle)
	if bad != nil {
		return *bad
	}
	s := section{ID: id, Title: title, Chart: chart, Subtitle: subtitle, Available: true,
		Notes: envelopeNotes(r)}
	type kv struct {
		k string
		v int
	}
	var items []kv
	for k, v := range r.Counts {
		if keyPrefix != "" {
			if !strings.HasPrefix(k, keyPrefix) {
				continue
			}
			k = strings.TrimPrefix(k, keyPrefix)
		}
		items = append(items, kv{k, v})
	}
	if len(items) == 0 {
		s.Available = false
		s.Reason = "no data in scope"
		return s
	}
	// hbars: sort by value descending for a readable ranking; bars (buckets): sort by key.
	if chart == "hbars" {
		sort.Slice(items, func(i, j int) bool {
			if items[i].v != items[j].v {
				return items[i].v > items[j].v
			}
			return items[i].k < items[j].k
		})
	} else {
		sort.Slice(items, func(i, j int) bool { return items[i].k < items[j].k })
	}
	labels := make([]string, len(items))
	values := make([]int, len(items))
	for i, it := range items {
		labels[i] = it.k
		values[i] = it.v
	}
	s.Data = map[string]interface{}{"labels": labels, "values": values}
	return s
}

func ratioSection(svc *Service, base Query, id, title, metric, subtitle string) section {
	r, bad := computeOr(svc, withMetric(base, metric), id, title, "ratio", subtitle)
	if bad != nil {
		return *bad
	}
	s := section{ID: id, Title: title, Chart: "ratio", Subtitle: subtitle, Available: true,
		Notes: envelopeNotes(r)}
	if r.Ratio == nil {
		s.Available = false
		s.Reason = "ratio not configured"
		return s
	}
	rr := r.Ratio
	data := map[string]interface{}{
		"numerator":   rr.Numerator,
		"denominator": rr.Denominator,
		"numCount":    rr.NumCount,
		"denCount":    rr.DenCount,
		"value":       rr.Value, // nil => undefined (zero denominator)
	}
	if len(rr.Classes) > 0 {
		labels := sortedIntKeys(rr.Classes)
		values := make([]int, len(labels))
		for i, l := range labels {
			values[i] = rr.Classes[l]
		}
		data["classLabels"] = labels
		data["classValues"] = values
	}
	s.Data = data
	return s
}

// sprintSection renders per-sprint ever-members vs resolved as grouped bars. The Counts map
// carries "<label> — ever-members" and "<label> — resolved" pairs.
func sprintSection(svc *Service, base Query) section {
	const id, title = "sprint-resolution", "Sprint eventual resolution"
	subtitle := "ever-members vs currently resolved, per sprint (event-time membership)"
	r, bad := computeOr(svc, withMetric(base, MetricSprintResolution), id, title, "grouped", subtitle)
	if bad != nil {
		return *bad
	}
	s := section{ID: id, Title: title, Chart: "grouped", Subtitle: subtitle, Available: true,
		Notes: envelopeNotes(r)}
	const everSuffix = " \u2014 ever-members"
	const resSuffix = " \u2014 resolved"
	ever := map[string]int{}
	res := map[string]int{}
	for k, v := range r.Counts {
		switch {
		case strings.HasSuffix(k, everSuffix):
			ever[strings.TrimSuffix(k, everSuffix)] = v
		case strings.HasSuffix(k, resSuffix):
			res[strings.TrimSuffix(k, resSuffix)] = v
		}
	}
	if len(ever) == 0 {
		s.Available = false
		s.Reason = "no sprint membership data in scope"
		return s
	}
	labels := sortedIntKeys(ever)
	everVals := make([]int, len(labels))
	resVals := make([]int, len(labels))
	for i, l := range labels {
		everVals[i] = ever[l]
		resVals[i] = res[l]
	}
	s.Data = map[string]interface{}{
		"labels": labels,
		"series": []map[string]interface{}{
			{"name": "ever-members", "values": everVals},
			{"name": "resolved", "values": resVals},
		},
	}
	return s
}

// blockerSection renders the relationship-age proxy histogram. Both blocker signals are
// explicitly-labelled proxies (carried in Notes), never a fabricated blocked-duration.
func blockerSection(svc *Service, base Query) section {
	const id, title = "blocker-ageing", "Blocker ageing (relationship-age proxy)"
	subtitle := "age of currently dependency-linked issues; PROXY, not blocked-duration"
	r, bad := computeOr(svc, withMetric(base, MetricBlockerAgeing), id, title, "bars", subtitle)
	if bad != nil {
		return *bad
	}
	s := section{ID: id, Title: title, Chart: "bars", Subtitle: subtitle, Available: true,
		Notes: envelopeNotes(r)}
	type kv struct {
		k string
		v int
	}
	var items []kv
	for k, v := range r.Counts {
		if strings.HasPrefix(k, "age:") {
			items = append(items, kv{strings.TrimPrefix(k, "age:"), v})
		}
	}
	total := 0
	for _, it := range items {
		total += it.v
	}
	if len(items) == 0 || total == 0 {
		s.Available = false
		s.Reason = "no currently dependency-linked issues (or blocker_link_types unset)"
		return s
	}
	sort.Slice(items, func(i, j int) bool { return items[i].k < items[j].k })
	labels := make([]string, len(items))
	values := make([]int, len(items))
	for i, it := range items {
		labels[i] = it.k
		values[i] = it.v
	}
	s.Data = map[string]interface{}{"labels": labels, "values": values}
	return s
}

// monthBucket is one [from,to) month window with its display label.
type monthBucket struct {
	from  time.Time
	to    time.Time
	label string
}

// monthBuckets enumerates whole months spanning the trend window, inclusive of the month
// containing From through the month containing To.
func monthBuckets(trend *TimeRange) []monthBucket {
	if trend == nil {
		return nil
	}
	from, to := trend.From.UTC(), trend.To.UTC()
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil
	}
	cur := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(to.Year(), to.Month(), 1, 0, 0, 0, 0, time.UTC)
	var out []monthBucket
	for !cur.After(end) {
		next := cur.AddDate(0, 1, 0)
		out = append(out, monthBucket{from: cur, to: next, label: cur.Format("2006-01")})
		cur = next
		if len(out) > 240 { // hard cap: 20 years, defensive against a pathological range
			break
		}
	}
	return out
}

func sortedIntKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysBool(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// renderDashboard inlines the embedded CSS, the embedded renderer, and the data JSON into the
// single static HTML template. The data JSON is HTML-escaped by json.Marshal (so <, >, &
// become \u003c etc.), which both prevents the embedded document from breaking out of its
// <script> container and defuses markup in any Jira-controlled string.
func renderDashboard(payload *dashboardPayload) ([]byte, error) {
	dataJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("metrics: marshal dashboard data: %w", err)
	}
	css, err := dashboardAssets.ReadFile("dashboard/style.css")
	if err != nil {
		return nil, fmt.Errorf("metrics: read dashboard style: %w", err)
	}
	js, err := dashboardAssets.ReadFile("dashboard/chart.js")
	if err != nil {
		return nil, fmt.Errorf("metrics: read dashboard renderer: %w", err)
	}
	tmplSrc, err := dashboardAssets.ReadFile("dashboard/template.html")
	if err != nil {
		return nil, fmt.Errorf("metrics: read dashboard template: %w", err)
	}
	tmpl, err := template.New("dashboard").Parse(string(tmplSrc))
	if err != nil {
		return nil, fmt.Errorf("metrics: parse dashboard template: %w", err)
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, map[string]string{
		"StyleCSS": string(css),
		"ChartJS":  string(js),
		"DataJSON": string(dataJSON),
	})
	if err != nil {
		return nil, fmt.Errorf("metrics: render dashboard: %w", err)
	}
	return buf.Bytes(), nil
}
