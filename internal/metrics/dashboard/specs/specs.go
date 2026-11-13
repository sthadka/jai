// Package specs defines the declarative dashboard spec format and loads the
// batteries-included default dashboards (embedded) plus custom dashboards from
// local directories at runtime.
//
// A dashboard is a titled set of panels. A panel is EITHER a metric panel or a
// kind:query panel.
//
// A metric panel ("" kind, today's behavior) is purely declarative —
// {title, measure, slice, segment, chart, defaults} — and renders THROUGH the
// existing whitelisted /q template: measure/slice/segment/cohort/grain are the
// same tokens the /q whitelist validates, never raw SQL. The SPA picks an ECharts
// render (number tile, bar, line, area, scatter) from `chart`. Panels tied to a
// metric whose data path the /q template
// cannot serve, or that need unconfigured taxonomy, carry an explicit
// `unavailable` reason (or a `requires` capability resolved at serve time) so
// they render an honest "unavailable" card — never fabricated data.
//
// A kind:query panel ("query" kind) carries a saved Explore query and renders
// THROUGH /explore/{sql,jql} plus client-side shaping: {source, query, chart, x,
// y, agg, segment, filters} drive the fetch and the ECharts render. The
// metric-only fields (measure/slice/stat/unit/metric/requires and the
// cohort/grain defaults) are ignored for kind:query panels, and the query fields
// are ignored for metric panels.
package specs

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// defaultsFS holds the embedded batteries-included dashboards. go:embed makes a
// fresh install useful with zero config: every feasible metric of the 24-metric
// suite is covered by a default panel (or an explicit unavailable reason).
//
//go:embed dashboards/*.yaml
var defaultsFS embed.FS

// Source identifiers for provenance (shown in the dropdown / API).
const (
	SourceEmbedded = "embedded"
	SourceUser     = "user"
	SourceProject  = "project"
)

// PanelDefaults are the control-bar initial values a panel fixes. They map to the
// same /q tokens the whitelist validates; unset fields fall back to /q defaults.
type PanelDefaults struct {
	Cohort  string `yaml:"cohort,omitempty" json:"cohort,omitempty"`
	Grain   string `yaml:"grain,omitempty" json:"grain,omitempty"`
	Project string `yaml:"project,omitempty" json:"project,omitempty"`
	From    string `yaml:"from,omitempty" json:"from,omitempty"`
	To      string `yaml:"to,omitempty" json:"to,omitempty"`
}

// Panel is one declarative chart. A metric panel ("" kind) renders through the
// shared /q template; a kind:query panel renders through /explore/{sql,jql} plus
// client-side shaping.
type Panel struct {
	Title   string `yaml:"title" json:"title"`
	Measure string `yaml:"measure" json:"measure"`
	Slice   string `yaml:"slice" json:"slice"`
	Segment string `yaml:"segment" json:"segment"`
	// Chart selects the renderer: number (headline tile), bar, line, area, scatter.
	Chart string `yaml:"chart" json:"chart"`

	// Stat selects which aggregate column a panel reads: p50|p85|p95|avg|n|total.
	// For chart:number it is the headline value (default: n when measure=throughput,
	// else p50). For bar/line it optionally overrides the plotted column (e.g. a
	// count chart over a distribution measure uses stat:n).
	Stat string `yaml:"stat,omitempty" json:"stat,omitempty"`

	// Unit optionally overrides the display unit (days|ratio|points|count);
	// otherwise the client derives it from the measure's unit via /meta.
	Unit string `yaml:"unit,omitempty" json:"unit,omitempty"`

	Defaults PanelDefaults `yaml:"defaults" json:"defaults"`

	// Metric is the canonical 24-metric-suite id this panel covers (for coverage
	// enumeration). A single metric may be covered by one panel or marked
	// unavailable on exactly one panel.
	Metric string `yaml:"metric,omitempty" json:"metric,omitempty"`

	// Requires names a server capability (e.g. blocker_link_types, security_sla)
	// that must be satisfied for live data to exist. When unsatisfied, the serving
	// layer sets Unavailable so the panel renders an explicit unavailable card.
	Requires string `yaml:"requires,omitempty" json:"requires,omitempty"`

	// Unavailable, when non-empty, makes the panel render an explicit
	// "unavailable: <reason>" card and fetch no data. Set statically in the spec
	// (query-shape not served by /q) or injected at serve time (missing config).
	Unavailable string `yaml:"unavailable,omitempty" json:"unavailable,omitempty"`

	// Kind selects the panel family: "" (a metric panel, today's behavior) or
	// "query" (an Explore query widget rendered through /explore/{sql,jql}).
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`

	// Source is the Explore backend for a kind:query panel: sql|jql.
	Source string `yaml:"source,omitempty" json:"source,omitempty"`

	// Query is the raw SQL or JQL text executed for a kind:query panel.
	Query string `yaml:"query,omitempty" json:"query,omitempty"`

	// X is the result column used as the group-by/category axis (kind:query).
	X string `yaml:"x,omitempty" json:"x,omitempty"`

	// Y is the result column used as the measure, or "count"/"**count**"
	// (kind:query).
	Y string `yaml:"y,omitempty" json:"y,omitempty"`

	// Agg is the client-side aggregation over Y: count|sum|avg|min|max
	// (kind:query).
	Agg string `yaml:"agg,omitempty" json:"agg,omitempty"`

	// Filters are optional client-side per-column value filters applied to the
	// Explore result before shaping (kind:query).
	Filters map[string][]string `yaml:"filters,omitempty" json:"filters,omitempty"`
}

// Dashboard is a titled set of panels.
type Dashboard struct {
	ID          string  `yaml:"id" json:"id"`
	Title       string  `yaml:"title" json:"title"`
	Description string  `yaml:"description,omitempty" json:"description,omitempty"`
	Panels      []Panel `yaml:"panels" json:"panels"`

	// Source is set by the loader: embedded | user | project.
	Source string `yaml:"-" json:"source"`
}

// Embedded parses and returns the batteries-included default dashboards, in a
// stable order (by embedded filename).
func Embedded() ([]Dashboard, error) {
	entries, err := defaultsFS.ReadDir("dashboards")
	if err != nil {
		return nil, fmt.Errorf("read embedded dashboards: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]Dashboard, 0, len(names))
	for _, name := range names {
		data, err := defaultsFS.ReadFile("dashboards/" + name)
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", name, err)
		}
		d, err := parse(data, name, false)
		if err != nil {
			return nil, fmt.Errorf("parse embedded %s: %w", name, err)
		}
		d.Source = SourceEmbedded
		out = append(out, d)
	}
	return out, nil
}

// DefaultDirs returns the runtime custom-spec directories in precedence order
// LOWEST→HIGHEST: user (~/.config/jai/dashboards) then project (./.jai/dashboards).
// Project-local specs therefore override user specs, which override embedded
// defaults (by dashboard id). Missing directories are simply skipped.
func DefaultDirs() []string {
	dirs := []string{}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".config", "jai", "dashboards"))
	}
	dirs = append(dirs, filepath.Join(".jai", "dashboards"))
	return dirs
}

// UserDashboardDir returns the user-level custom-spec directory
// (~/.config/jai/dashboards), using the same resolution as DefaultDirs. The save
// endpoint writes saved query widgets here. It errors when the home directory
// cannot be resolved.
func UserDashboardDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".config", "jai", "dashboards"), nil
}

// Load returns the merged dashboard set: embedded defaults first, then each dir
// in order, each overriding earlier dashboards sharing an id (last wins). Pass
// dirs LOWEST→HIGHEST precedence (see DefaultDirs). Custom specs appear WITHOUT a
// rebuild — the server calls Load on every /dashboards request, so dropping a new
// spec file and reloading the page is enough.
func Load(dirs ...string) ([]Dashboard, error) {
	merged := []Dashboard{}
	index := map[string]int{} // dashboard id -> position in merged

	apply := func(dbs []Dashboard) {
		for _, d := range dbs {
			if pos, ok := index[d.ID]; ok {
				merged[pos] = d // override in place, preserving original order
				continue
			}
			index[d.ID] = len(merged)
			merged = append(merged, d)
		}
	}

	emb, err := Embedded()
	if err != nil {
		return nil, err
	}
	apply(emb)

	for _, dir := range dirs {
		dbs, err := loadDir(dir)
		if err != nil {
			return nil, err
		}
		apply(dbs)
	}
	return merged, nil
}

// loadDir parses every *.yaml/*.yml/*.json spec in dir (non-recursive). A missing
// dir yields no dashboards (not an error). Each dashboard's Source is SourceUser
// for the user config dir and SourceProject for a project-local `.jai` dir.
func loadDir(dir string) ([]Dashboard, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dashboards dir %s: %w", dir, err)
	}
	source := SourceUser
	if isProjectDir(dir) {
		source = SourceProject
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yaml", ".yml", ".json":
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]Dashboard, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		isJSON := strings.EqualFold(filepath.Ext(name), ".json")
		d, err := parse(data, name, isJSON)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		d.Source = source
		out = append(out, d)
	}
	return out, nil
}

// isProjectDir reports whether dir is the project-local `.jai/dashboards` path.
func isProjectDir(dir string) bool {
	return filepath.Base(filepath.Dir(filepath.Clean(dir))) == ".jai"
}

// parse decodes one spec file (YAML or JSON) and fills a missing id from the
// filename so a hand-dropped custom spec needs only panels.
func parse(data []byte, filename string, isJSON bool) (Dashboard, error) {
	var d Dashboard
	var err error
	if isJSON {
		err = json.Unmarshal(data, &d)
	} else {
		err = yaml.Unmarshal(data, &d)
	}
	if err != nil {
		return Dashboard{}, err
	}
	if strings.TrimSpace(d.ID) == "" {
		base := filepath.Base(filename)
		d.ID = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if strings.TrimSpace(d.Title) == "" {
		d.Title = d.ID
	}
	return d, nil
}

// ResolveAvailability returns dbs with each panel's Unavailable set when its
// Requires capability is not satisfied by caps. Panels carrying a static
// Unavailable are left untouched. This is where config-dependent metrics
// (blocker_ageing, security_sla) become explicit unavailable cards instead of
// fabricating data.
func ResolveAvailability(dbs []Dashboard, caps map[string]bool) []Dashboard {
	out := make([]Dashboard, len(dbs))
	for i, d := range dbs {
		panels := make([]Panel, len(d.Panels))
		for j, p := range d.Panels {
			if p.Unavailable == "" && p.Requires != "" && !caps[p.Requires] {
				p.Unavailable = fmt.Sprintf("requires %s (not configured in metrics policy)", p.Requires)
			}
			panels[j] = p
		}
		d.Panels = panels
		out[i] = d
	}
	return out
}
