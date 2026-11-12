package live

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/sthadka/jai/internal/metrics/dashboard/specs"
)

// assets holds the single-page app and the VENDORED Vega-Lite stack (vega,
// vega-lite, vega-embed). They are embedded (go:embed) and served by this server
// — NOT loaded from any CDN. The served index.html references only /assets/*.js.
//
//go:embed assets/index.html assets/vega.min.js assets/vega-lite.min.js assets/vega-embed.min.js
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

// Handler exposes the route mux (for tests and embedding).
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	sub, _ := fs.Sub(assets, "assets")
	s.mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(sub))))
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/meta", s.handleMeta)
	s.mux.HandleFunc("/q", s.handleQuery)
	s.mux.HandleFunc("/dashboards", s.handleDashboards)
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
	projects := append([]string(nil), s.projects...)
	sort.Strings(projects)
	writeJSON(w, http.StatusOK, map[string]any{
		"generation": s.generation,
		"projects":   projects,
		"measures":   MeasureTokens(),
		"dimensions": DimensionTokens(),
		"grains":     GrainTokens(),
		"cohorts":    CohortTokens(),
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

// ActiveGeneration returns the active generation id bound at engine setup.
func (s *Server) ActiveGeneration() any { return s.generation }
