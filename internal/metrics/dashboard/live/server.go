package live

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sthadka/jai/internal/metrics/dashboard/specs"
)

// assets holds the single-page app and the VENDORED Apache ECharts runtime plus
// the IBM Plex Sans webfont. They are embedded (go:embed) and served by this
// server — NOT loaded from any CDN. The served index.html references only
// /assets/* (echarts.min.js, the font under /assets/fonts/, and inline styles).
//
//go:embed assets/index.html assets/echarts.min.js assets/fonts/*
var assets embed.FS

// Server is the live dashboard HTTP server. It is localhost-only and read-only:
// every data request goes through the whitelisted /q template.
type Server struct {
	engine     *Engine
	whitelist  *Whitelist
	projects   []string
	generation any
	mux        *http.ServeMux
	// specDirs are the runtime custom-spec directories (lowest->highest precedence),
	// re-read on every /dashboards request so custom specs appear without a rebuild.
	specDirs []string
	// caps names the satisfied server capabilities (e.g. blocker_link_types) used to
	// resolve config-dependent panels to explicit unavailable cards.
	caps map[string]bool
	// jiraBaseURL is the Jira site root (trailing slash trimmed) used by the SPA to
	// build /browse/<key> links for drill-through issue lists. Empty = no links.
	jiraBaseURL string
	// csrfToken is a same-origin token minted at startup, emitted to the page via
	// /meta and required on POST /explore/* (X-Jai-CSRF header) so a cross-site
	// page cannot drive the free-form SQL endpoint.
	csrfToken string
	// mu guards the live-refreshed projects/generation metadata (updated on /meta
	// so a rebuild is honored without a restart).
	mu sync.RWMutex
}

// NewServer wires routes over the given engine and active-generation metadata.
func NewServer(engine *Engine, projects []string, generation any) *Server {
	s := &Server{
		engine:     engine,
		whitelist:  NewWhitelist(projects),
		projects:   projects,
		generation: generation,
		mux:        http.NewServeMux(),
		specDirs:   specs.DefaultDirs(),
		caps:       map[string]bool{},
		csrfToken:  newCSRFToken(),
	}
	s.routes()
	return s
}

// SetDashboards overrides the capability set and the custom-spec directories
// (lowest->highest precedence). Called by the CLI with policy-derived capabilities
// and the resolved runtime dirs; tests may use it to point at a fixture dir.
func (s *Server) SetDashboards(caps map[string]bool, dirs []string) {
	if caps != nil {
		s.caps = caps
	}
	if dirs != nil {
		s.specDirs = dirs
	}
}

// SetJiraBaseURL sets the Jira site root used for drill-through issue links.
func (s *Server) SetJiraBaseURL(u string) { s.jiraBaseURL = u }

// Handler exposes the route mux (for tests and embedding).
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	sub, _ := fs.Sub(assets, "assets")
	s.mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(sub))))
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/meta", s.handleMeta)
	s.mux.HandleFunc("/q", s.handleQuery)
	s.mux.HandleFunc("/dashboards", s.handleDashboards)
	s.mux.HandleFunc("/issues", s.handleIssues)
	s.mux.HandleFunc("/explore/sql", s.handleExploreSQL)
}

// handleDashboards serves the embedded default dashboards merged with custom specs
// from the runtime dirs (project-local overrides user, which overrides embedded).
// Loaded fresh per request so a newly-dropped spec appears on reload without a
// rebuild. Config-dependent panels are resolved to explicit unavailable cards.
func (s *Server) handleDashboards(w http.ResponseWriter, r *http.Request) {
	dbs, err := specs.Load(s.specDirs...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dashboards": specs.ResolveAvailability(dbs, s.caps),
	})
}

// ListenAndServe binds 127.0.0.1 on the given port (0 => ephemeral), returns the
// chosen URL, and serves in a goroutine. The caller opens the browser at the URL.
func (s *Server) ListenAndServe(port int) (string, *http.Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", nil, err
	}
	httpSrv := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	url := fmt.Sprintf("http://%s/", ln.Addr().String())
	go func() { _ = httpSrv.Serve(ln) }()
	return url, httpSrv, nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, "index unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// handleMeta serves the control-bar vocabulary: the active generation's projects
// plus the measure/dimension/grain/cohort whitelists, so the SPA builds its
// dropdowns from the same source of truth that validates /q requests.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	// Re-read the live generation + project set so a `metrics rebuild` that
	// published a new generation (and deleted the obsolete one) is reflected
	// without a server restart; also refresh the /q project whitelist to match.
	// Data queries already re-bind `g` per call (see Engine.Query).
	if projects, err := FetchProjects(s.engine); err == nil {
		s.whitelist.SetProjects(projects)
		s.mu.Lock()
		s.projects = projects
		s.mu.Unlock()
	}
	if gen, err := FetchGeneration(s.engine); err == nil && gen != nil {
		s.mu.Lock()
		s.generation = gen
		s.mu.Unlock()
	}
	s.mu.RLock()
	projects := append([]string(nil), s.projects...)
	gen := s.generation
	s.mu.RUnlock()
	sort.Strings(projects)
	writeJSON(w, http.StatusOK, map[string]any{
		"generation":    gen,
		"projects":      projects,
		"measures":      MeasureOptions(),
		"dimensions":    DimensionOptions(),
		"grains":        GrainOptions(),
		"cohorts":       CohortOptions(),
		"jira_base_url": s.jiraBaseURL,
		"csrf_token":    s.exploreToken(),
	})
}

// handleQuery is the read-only, SELECT-only, parameterized data endpoint. It
// builds SQL from the fixed template + whitelisted tokens only; any
// non-whitelisted token or malformed date is rejected with 400 before a single
// character reaches DuckDB.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := QueryParams{
		Measure: q.Get("measure"),
		Slice:   q.Get("slice"),
		Segment: q.Get("segment"),
		Grain:   q.Get("grain"),
		Project: q.Get("project"),
		From:    q.Get("from"),
		To:      q.Get("to"),
		Cohort:  q.Get("cohort"),
	}
	sql, err := s.whitelist.Build(params)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	rows, err := s.engine.Query(sql)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sql":  sql,
		"rows": rows,
	})
}

// handleIssues is the read-only drill-through: it lists the individual issues
// behind a chart cell (the clicked slice/segment value under the panel's measure/
// cohort/project/date filters). Same whitelist + SELECT-only guarantees as /q; the
// slice_value/segment_value are emitted as escaped literals, never identifiers. It
// also returns a ready-to-run `jai query` over the local DB for the matched keys.
func (s *Server) handleIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := QueryParams{
		Measure: q.Get("measure"),
		Slice:   q.Get("slice"),
		Segment: q.Get("segment"),
		Grain:   q.Get("grain"),
		Project: q.Get("project"),
		From:    q.Get("from"),
		To:      q.Get("to"),
		Cohort:  q.Get("cohort"),
	}
	sql, err := s.whitelist.BuildIssues(params, q.Get("slice_value"), q.Get("segment_value"), 500)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	rows, err := s.engine.Query(sql)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		if k, ok := row["issue_key"].(string); ok && k != "" {
			keys = append(keys, k)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sql":       sql,
		"count":     len(rows),
		"rows":      rows,
		"jai_query": buildJaiQuery(keys),
	})
}

// buildJaiQuery renders a copy-paste `jai query` (SQLite over the local DB) that
// lists the matched issues' current details by key. Capped so the command stays
// shell-friendly; empty key set yields an empty string.
func buildJaiQuery(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	const cap = 200
	capped := keys
	if len(capped) > cap {
		capped = capped[:cap]
	}
	quoted := make([]string, len(capped))
	for i, k := range capped {
		quoted[i] = "'" + sqlEscape(k) + "'"
	}
	sql := "SELECT key, status, priority, assignee, team, summary FROM issues WHERE key IN (" +
		strings.Join(quoted, ", ") + ") ORDER BY key"
	return "jai query \"" + sql + "\""
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// FetchProjects returns the distinct projects present in the active generation,
// so the project dropdown reflects the active fact generation (never hardcoded).
func FetchProjects(engine *Engine) ([]string, error) {
	rows, err := engine.Query("SELECT DISTINCT project FROM jai.fact_issue_flow WHERE generation = getvariable('g') ORDER BY project")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if p, ok := row["project"].(string); ok && p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// ActiveGeneration returns the current active generation id (refreshed on /meta).
func (s *Server) ActiveGeneration() any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

// FetchGeneration returns the active fact generation id from the live DB. The id
// is TRY_CAST to BIGINT because the engine scans jai.db with sqlite_all_varchar
// (every SQLite column arrives as VARCHAR); without the cast /meta would emit the
// generation as a JSON string instead of a number.
func FetchGeneration(engine *Engine) (any, error) {
	rows, err := engine.Query("SELECT TRY_CAST(generation AS BIGINT) AS generation FROM jai.metrics_active_generation WHERE id = 1")
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0]["generation"], nil
}
