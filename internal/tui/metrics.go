package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sthadka/jai/internal/metrics"
)

// metricsComputeTimeout bounds a single pane computation so a slow/large metric
// cannot wedge the UI. The pane always runs off the Update loop (in a tea.Cmd
// goroutine) and this context is cancellable when the user leaves the pane.
const metricsComputeTimeout = 15 * time.Second

// metricBarWidth is the column width of the horizontal bars rendered for
// distributions and count buckets.
const metricBarWidth = 24

// sparkRunes are the eight block glyphs used to draw inline sparklines, lowest
// to highest.
var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// metricComputer is the subset of the shared service the pane depends on. It
// lets the pure compute/render path be exercised deterministically in tests
// without a live database. *metrics.Service satisfies it.
type metricComputer interface {
	Compute(q metrics.Query) (*metrics.Result, error)
}

// metricSpec names a metric the pane renders, with a human label and an optional
// caveat. The pane never writes metrics or triggers a rebuild — it only reads
// results from the single shared service, exactly as the CLI and MCP do.
type metricSpec struct {
	metric string
	title  string
	period string // throughput/net-flow bucket; empty = provider default
	note   string // caveat printed under the title (e.g. not a capacity measure)
}

// paneMetrics is the curated, ordered set the read-only pane shows. Metrics that
// are unconfigured or otherwise unavailable are skipped with a recorded reason,
// never crashing the pane.
var paneMetrics = []metricSpec{
	{metric: metrics.MetricThroughput, title: "Throughput (delivered per week)", period: "week"},
	{metric: metrics.MetricNetFlow, title: "Net flow (created − resolved per week)", period: "week"},
	{metric: metrics.MetricCycleTime, title: "Cycle time (days)"},
	{metric: metrics.MetricLeadTime, title: "Lead time (days)"},
	{metric: metrics.MetricAgedOpen, title: "Aged open items (days)"},
	{metric: metrics.MetricResolutionMix, title: "Resolution mix"},
	{
		metric: metrics.MetricWorkload,
		title:  "Open items by current owner",
		note:   "item load only — not capacity, productivity, or performance",
	},
}

// metricOutcome is one computed (or unavailable) metric for the pane.
type metricOutcome struct {
	spec metricSpec
	res  *metrics.Result
	err  error
}

// metricsData is the full payload produced off the UI loop. It is a plain value
// so buildPaneLines renders deterministically from it in tests.
type metricsData struct {
	asOf       time.Time
	projects   []string
	configured bool // whether a metrics: config block exists at all
	generation int64
	outcomes   []metricOutcome
	rejections map[string][]string
	freshness  map[string]metrics.SourceFreshness
	computeErr error // fatal (context cancelled / deadline exceeded)
}

// metricsLoadedMsg carries the result of an async pane computation.
type metricsLoadedMsg struct {
	data *metricsData
	err  string
}

// MetricsPane is the read-only metrics overlay model. It holds the last computed
// payload and a scroll offset; all expensive work happens in loadMetricsCmd.
type MetricsPane struct {
	data    *metricsData
	loading bool
	err     string
	scroll  int
	width   int
	height  int
}

// NewMetricsPane returns an empty pane sized to the viewport, in the loading
// state (the caller fires the compute command).
func NewMetricsPane(width, height int) *MetricsPane {
	return &MetricsPane{width: width, height: height, loading: true}
}

// SetSize updates the pane viewport.
func (p *MetricsPane) SetSize(width, height int) {
	p.width = width
	p.height = height
}

// ScrollUp scrolls the content up by one line.
func (p *MetricsPane) ScrollUp() {
	if p.scroll > 0 {
		p.scroll--
	}
}

// ScrollDown scrolls the content down by one line, clamped to the content end.
func (p *MetricsPane) ScrollDown() {
	lines := p.lines()
	max := len(lines) - p.height
	if max < 0 {
		max = 0
	}
	if p.scroll < max {
		p.scroll++
	}
}

// lines returns the full rendered content for the current state.
func (p *MetricsPane) lines() []string {
	if p.loading {
		return []string{"Computing metrics…"}
	}
	if p.err != "" {
		return []string{"Metrics error: " + p.err}
	}
	return buildPaneLines(p.data, p.width)
}

// Render returns the visible window of the pane, applying the scroll offset.
func (p *MetricsPane) Render(width, height int) string {
	p.width = width
	p.height = height
	lines := p.lines()

	start := p.scroll
	if start > len(lines) {
		start = len(lines)
	}
	end := start + height
	if end > len(lines) {
		end = len(lines)
	}
	visible := lines[start:end]
	return strings.Join(visible, "\n")
}

// computeMetrics runs the curated metric set through the shared service, honoring
// cancellation. It never panics on unavailable metrics: each failure is recorded
// as an outcome error and rendered as an "unavailable" note. The passed asOf is
// used for every open-interval/age computation so refreshing advances ages
// without requiring new Jira activity.
func computeMetrics(ctx context.Context, svc metricComputer, pol *metrics.Policy, generation int64, configured bool, projects []string, asOf time.Time) *metricsData {
	d := &metricsData{
		asOf:       asOf,
		projects:   append([]string(nil), projects...),
		configured: configured,
		generation: generation,
	}
	if pol != nil {
		d.rejections = pol.Rejections()
	}
	if !configured || svc == nil {
		return d
	}

	for _, spec := range paneMetrics {
		if err := ctx.Err(); err != nil {
			d.computeErr = err
			return d
		}
		q := metrics.Query{Metric: spec.metric, Projects: projects, AsOf: asOf}
		if spec.period != "" {
			q.Period = spec.period
		}
		r, err := svc.Compute(q)
		d.outcomes = append(d.outcomes, metricOutcome{spec: spec, res: r, err: err})
		if r != nil && d.freshness == nil && len(r.Freshness) > 0 {
			d.freshness = r.Freshness
		}
	}
	return d
}

// buildPaneLines renders the pane payload to plain text lines (no color), so the
// output is deterministic and testable. Styling/scrolling is applied by Render.
func buildPaneLines(d *metricsData, width int) []string {
	if d == nil {
		return []string{"No metrics data."}
	}

	var out []string
	out = append(out, "Metrics (read-only)")

	scope := "all projects"
	if len(d.projects) > 0 {
		scope = strings.Join(d.projects, ", ")
	}
	out = append(out, "scope: "+scope+"   as of: "+d.asOf.UTC().Format(time.RFC3339))

	gen := "none built"
	if d.generation != 0 {
		gen = fmt.Sprintf("generation %d", d.generation)
	}
	out = append(out, "fact store: "+gen+"   "+freshnessSummary(d.freshness))

	if !d.configured {
		out = append(out,
			"",
			"Metrics are not configured.",
			"Add a `metrics:` section to your config to map statuses to flow stages,",
			"then run `jai metrics rebuild` to populate the fact store.",
		)
		return out
	}

	if d.computeErr != nil {
		out = append(out, "", "Computation stopped: "+d.computeErr.Error())
	}
	if d.generation == 0 {
		out = append(out, "",
			"No fact generation built — fact-based metrics (cycle/lead/throughput)",
			"will be empty until `jai metrics rebuild` runs.")
	}
	for _, m := range sortedRejectionKeys(d.rejections) {
		out = append(out, "config: metric "+m+" disabled: "+strings.Join(d.rejections[m], "; "))
	}

	for _, o := range d.outcomes {
		out = append(out, "")
		out = append(out, sectionLines(o, width)...)
	}

	out = append(out, "", "r refresh   esc back   j/k scroll")
	return out
}

// sectionLines renders a single metric outcome: its title, caveat, and a type-
// appropriate visualization (bars for distributions/counts, sparkline for
// period series, value for ratios), followed by its cohort/exclusion caveats.
func sectionLines(o metricOutcome, width int) []string {
	lines := []string{"▸ " + o.spec.title}
	if o.spec.note != "" {
		lines = append(lines, "  ("+o.spec.note+")")
	}
	if o.err != nil {
		return append(lines, "  unavailable: "+o.err.Error())
	}
	r := o.res
	if r == nil {
		return append(lines, "  no result")
	}

	switch {
	case r.Distribution != nil:
		lines = append(lines, distributionLines(r.Distribution)...)
	case len(r.Series) > 0:
		lines = append(lines, seriesLines(r.Series)...)
	case len(r.Counts) > 0:
		lines = append(lines, countLines(r.Counts)...)
	case r.Ratio != nil:
		lines = append(lines, ratioLines(r.Ratio)...)
	default:
		lines = append(lines, fmt.Sprintf("  eligible: %d", r.Eligible))
	}

	lines = append(lines, caveatLines(r)...)
	return lines
}

// distributionLines renders percentile bars scaled to the max observed value.
func distributionLines(d *metrics.Distribution) []string {
	unit := d.Unit
	if unit == "" {
		unit = "days"
	}
	if d.Count == 0 {
		return []string{"  no eligible items (empty cohort)"}
	}
	scale := d.Max
	if scale <= 0 {
		scale = d.P95
	}
	rows := []struct {
		label string
		val   float64
	}{
		{"p50", d.P50},
		{"p85", d.P85},
		{"p95", d.P95},
		{"max", d.Max},
	}
	out := []string{fmt.Sprintf("  n=%d  mean=%s  (%s)", d.Count, fmtNum(d.Mean), unit)}
	for _, r := range rows {
		out = append(out, fmt.Sprintf("  %-4s %-*s %s", r.label, metricBarWidth, bar(r.val, scale, metricBarWidth), fmtNum(r.val)))
	}
	return out
}

// seriesLines renders a throughput/net-flow period series as a sparkline plus a
// compact numeric summary of the trailing buckets.
func seriesLines(series []metrics.PeriodPoint) []string {
	vals := make([]float64, len(series))
	sum := 0
	for i, p := range series {
		vals[i] = float64(p.Count)
		sum += p.Count
	}
	last := series[len(series)-1]
	out := []string{
		"  " + sparkline(vals),
		fmt.Sprintf("  %d buckets  last(%s)=%d  total=%d", len(series), last.Period, last.Count, sum),
	}
	// Show the trailing few buckets explicitly for readable detail.
	tail := series
	if len(tail) > 6 {
		tail = tail[len(tail)-6:]
	}
	parts := make([]string, 0, len(tail))
	for _, p := range tail {
		parts = append(parts, fmt.Sprintf("%s:%d", p.Period, p.Count))
	}
	out = append(out, "  "+strings.Join(parts, "  "))
	return out
}

// countLines renders a bucket->count map as descending horizontal bars (top
// buckets), scaled to the largest count.
func countLines(counts map[string]int) []string {
	type kv struct {
		k string
		v int
	}
	items := make([]kv, 0, len(counts))
	max := 0
	for k, v := range counts {
		items = append(items, kv{k, v})
		if v > max {
			max = v
		}
	}
	// Descending by count, then name for stable ordering.
	sort.Slice(items, func(i, j int) bool {
		if items[i].v != items[j].v {
			return items[i].v > items[j].v
		}
		return items[i].k < items[j].k
	})
	const topN = 8
	truncated := 0
	if len(items) > topN {
		truncated = len(items) - topN
		items = items[:topN]
	}
	out := make([]string, 0, len(items)+1)
	for _, it := range items {
		label := it.k
		if label == "" {
			label = "(none)"
		}
		if len(label) > 18 {
			label = label[:17] + "…"
		}
		out = append(out, fmt.Sprintf("  %-18s %-*s %d", label, metricBarWidth, bar(float64(it.v), float64(max), metricBarWidth), it.v))
	}
	if truncated > 0 {
		out = append(out, fmt.Sprintf("  …and %d more", truncated))
	}
	if len(out) == 0 {
		out = append(out, "  no data")
	}
	return out
}

// ratioLines renders a configured class ratio and its class counts.
func ratioLines(rr *metrics.RatioResult) []string {
	val := "undefined (zero denominator)"
	if rr.Value != nil {
		val = fmtNum(*rr.Value)
	}
	out := []string{fmt.Sprintf("  %s / %s = %s  (%d / %d)", rr.Numerator, rr.Denominator, val, rr.NumCount, rr.DenCount)}
	return out
}

// caveatLines appends the uncertainty labels every result carries: cohort,
// genuine-done state, and published exclusions. These are never dropped so a
// pane number is never shown unlabelled.
func caveatLines(r *metrics.Result) []string {
	var out []string
	cohort := r.Cohort
	if cohort == "" {
		cohort = "—"
	}
	genuine := "includes non-delivery resolutions"
	if r.GenuineDone {
		genuine = "delivery resolutions only"
	}
	out = append(out, fmt.Sprintf("  cohort: %s · %s", cohort, genuine))
	if len(r.Excluded) > 0 {
		parts := make([]string, 0, len(r.Excluded))
		for _, k := range sortedCountKeys(r.Excluded) {
			parts = append(parts, fmt.Sprintf("%s=%d", k, r.Excluded[k]))
		}
		out = append(out, "  excluded: "+strings.Join(parts, ", "))
	}
	for _, w := range r.Warnings {
		out = append(out, "  ! "+w)
	}
	return out
}

// freshnessSummary renders per-project source freshness (when the local DB last
// pulled from Jira) — never a claim of live completeness.
func freshnessSummary(f map[string]metrics.SourceFreshness) string {
	if len(f) == 0 {
		return "freshness: unknown"
	}
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		when := "never synced"
		if t := f[k].LastSyncTime; t != nil {
			when = t.UTC().Format("2006-01-02 15:04")
		}
		parts = append(parts, k+" "+when)
	}
	return "freshness: " + strings.Join(parts, "; ")
}

// sparkline draws a block-glyph sparkline. Values are normalized across their
// own min/max so signed series (e.g. net flow) render sensibly.
func sparkline(vals []float64) string {
	if len(vals) == 0 {
		return ""
	}
	min, max := vals[0], vals[0]
	for _, v := range vals {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	span := max - min
	var b strings.Builder
	for _, v := range vals {
		var idx int
		switch {
		case span <= 0:
			if max > 0 {
				idx = len(sparkRunes) - 1
			} else {
				idx = 0
			}
		default:
			idx = int((v-min)/span*float64(len(sparkRunes)-1) + 0.5)
		}
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkRunes) {
			idx = len(sparkRunes) - 1
		}
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}

// bar renders a horizontal bar of the given width proportional to value/max.
func bar(value, max float64, width int) string {
	if width <= 0 || max <= 0 || value <= 0 {
		return ""
	}
	n := int(value/max*float64(width) + 0.5)
	if n < 0 {
		n = 0
	}
	if n > width {
		n = width
	}
	return strings.Repeat("█", n)
}

// fmtNum formats a float with a single decimal place.
func fmtNum(f float64) string {
	return strconv.FormatFloat(f, 'f', 1, 64)
}

// metricsServiceComponents builds the shared service, policy, and active
// generation from the app config/DB — the SAME construction the CLI and MCP use
// (one definition, three consumers). Returns configured=false when no metrics:
// block exists so the pane can degrade gracefully.
func (a *App) metricsServiceComponents() (svc *metrics.Service, pol *metrics.Policy, generation int64, configured bool) {
	if a.cfg == nil || a.database == nil {
		return nil, nil, 0, false
	}
	pol = metrics.CompilePolicy(a.cfg.Metrics)
	store := metrics.NewStore(a.database, a.cfg.DB.Path)
	svc = metrics.NewService(a.database, store, pol, a.cfg)
	if gen, err := store.ActiveGeneration(); err == nil && gen != nil {
		generation = gen.Generation
	}
	return svc, pol, generation, a.cfg.Metrics != nil
}

// loadMetricsCmd computes the pane payload off the Update loop under the supplied
// (cancellable, deadline-bound) context. as_of is fixed at command time so a
// refresh advances open-interval ages without requiring new Jira activity.
func (a *App) loadMetricsCmd(ctx context.Context) tea.Cmd {
	svc, pol, generation, configured := a.metricsServiceComponents()
	var projects []string
	return func() tea.Msg {
		if svc == nil && configured {
			return metricsLoadedMsg{err: "no configuration loaded"}
		}
		data := computeMetrics(ctx, svc, pol, generation, configured, projects, time.Now().UTC())
		return metricsLoadedMsg{data: data}
	}
}

// sortedRejectionKeys returns metric names with structural rejections, sorted.
func sortedRejectionKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedCountKeys returns the keys of a count map, sorted.
func sortedCountKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
