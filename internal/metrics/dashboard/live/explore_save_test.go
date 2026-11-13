package live

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/metrics/dashboard/specs"
)

// panelTestServer returns a Server wired with only a known CSRF token — the
// /explore/panel handler never touches the engine, so no DuckDB is needed.
func panelTestServer() *Server {
	s := &Server{mux: http.NewServeMux(), csrfToken: "test-token"}
	s.routes()
	return s
}

// postPanel drives the /explore/panel handler with the given body and CSRF token
// and returns the recorder.
func postPanel(t *testing.T, s *Server, token string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/explore/panel", bytes.NewReader(buf))
	if token != "" {
		req.Header.Set("X-Jai-CSRF", token)
	}
	rec := httptest.NewRecorder()
	s.handleExplorePanel(rec, req)
	return rec
}

// decodePanel decodes the JSON response body into a generic map.
func decodePanel(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return out
}

// TestExplorePanelPreview proves save=false returns the dashboard YAML (for the
// copy-to-clipboard button) carrying the kind:query panel, its source and query,
// and never touches the filesystem.
func TestExplorePanelPreview(t *testing.T) {
	s := panelTestServer()
	rec := postPanel(t, s, "test-token", map[string]any{
		"board_id":    "My Board",
		"board_title": "My Board",
		"title":       "Open bugs",
		"source":      "sql",
		"query":       "SELECT key FROM jai.issues",
		"chart":       "bar",
		"save":        false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (%s)", rec.Code, rec.Body.String())
	}
	out := decodePanel(t, rec)
	y, _ := out["yaml"].(string)
	for _, want := range []string{"kind: query", "source: sql", "SELECT key FROM jai.issues"} {
		if !strings.Contains(y, want) {
			t.Errorf("preview yaml missing %q:\n%s", want, y)
		}
	}
	if _, ok := out["file"]; ok {
		t.Errorf("preview must not report a file: %v", out)
	}
}

// TestExplorePanelBadCSRF proves a missing/mismatched token is rejected with 403.
func TestExplorePanelBadCSRF(t *testing.T) {
	s := panelTestServer()
	rec := postPanel(t, s, "wrong", map[string]any{
		"board_id": "b", "source": "sql", "query": "SELECT 1",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403", rec.Code)
	}
}

// TestExplorePanelBadSource proves an unknown source is rejected with 400.
func TestExplorePanelBadSource(t *testing.T) {
	s := panelTestServer()
	rec := postPanel(t, s, "test-token", map[string]any{
		"board_id": "b", "source": "bogus", "query": "SELECT 1",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// TestExplorePanelEmptyQuery proves an empty query is rejected with 400.
func TestExplorePanelEmptyQuery(t *testing.T) {
	s := panelTestServer()
	rec := postPanel(t, s, "test-token", map[string]any{
		"board_id": "b", "source": "sql", "query": "   ",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// TestSlugifyBoardID proves board_id is reduced to a path-safe slug: traversal
// sequences collapse away, mixed input lowercases/dashes, and an all-symbol id
// sanitizes to empty (a caller-rejected 400).
func TestSlugifyBoardID(t *testing.T) {
	cases := map[string]string{
		"../evil":            "evil",
		"My Board":           "my-board",
		"a/../../b":          "a-b",
		"Foo__Bar!!":         "foo-bar",
		"  trim-me  ":        "trim-me",
		"///":                "",
		"ALLCAPS123":         "allcaps123",
		"keep-existing-dash": "keep-existing-dash",
	}
	for in, want := range cases {
		if got := slugifyBoardID(in); got != want {
			t.Errorf("slugifyBoardID(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestSavePanelToDir proves the write helper persists a re-loadable kind:query
// dashboard into the target dir, upserts panels by Title (append on a new Title,
// replace on a repeated one), and keeps a traversal-shaped id inside the dir.
func TestSavePanelToDir(t *testing.T) {
	dir := t.TempDir()
	slug := slugifyBoardID("Team Board")
	if slug != "team-board" {
		t.Fatalf("slug = %q; want team-board", slug)
	}

	p1 := specs.Panel{Kind: "query", Title: "Open bugs", Source: "sql", Query: "SELECT 1", Chart: "bar"}
	content, file, err := savePanelToDir(dir, slug, "Team Board", p1)
	if err != nil {
		t.Fatalf("savePanelToDir: %v", err)
	}
	if filepath.Dir(file) != dir {
		t.Fatalf("file %q escaped dir %q", file, dir)
	}
	if !strings.Contains(content, "kind: query") {
		t.Errorf("written yaml missing kind: query:\n%s", content)
	}

	// Re-parse via the real loader; the saved panel must round-trip as kind:query.
	loaded := loadBoard(t, dir, slug)
	if len(loaded.Panels) != 1 {
		t.Fatalf("panels = %d; want 1", len(loaded.Panels))
	}
	if loaded.Panels[0].Kind != "query" || loaded.Panels[0].Source != "sql" {
		t.Errorf("panel = %+v; want kind:query source:sql", loaded.Panels[0])
	}
	if loaded.Title != "Team Board" {
		t.Errorf("board title = %q; want Team Board", loaded.Title)
	}

	// A new Title appends -> two panels, original board Title preserved.
	p2 := specs.Panel{Kind: "query", Title: "Throughput", Source: "jql", Query: "project = ABC", Chart: "line"}
	if _, _, err := savePanelToDir(dir, slug, "Ignored Title", p2); err != nil {
		t.Fatalf("savePanelToDir append: %v", err)
	}
	loaded = loadBoard(t, dir, slug)
	if len(loaded.Panels) != 2 {
		t.Fatalf("panels after append = %d; want 2", len(loaded.Panels))
	}
	if loaded.Title != "Team Board" {
		t.Errorf("board title changed to %q; want preserved Team Board", loaded.Title)
	}

	// Same Title replaces in place -> still two panels, updated query.
	p2b := specs.Panel{Kind: "query", Title: "Throughput", Source: "jql", Query: "project = XYZ", Chart: "line"}
	if _, _, err := savePanelToDir(dir, slug, "Team Board", p2b); err != nil {
		t.Fatalf("savePanelToDir replace: %v", err)
	}
	loaded = loadBoard(t, dir, slug)
	if len(loaded.Panels) != 2 {
		t.Fatalf("panels after replace = %d; want 2", len(loaded.Panels))
	}
	var throughput *specs.Panel
	for i := range loaded.Panels {
		if loaded.Panels[i].Title == "Throughput" {
			throughput = &loaded.Panels[i]
		}
	}
	if throughput == nil || throughput.Query != "project = XYZ" {
		t.Errorf("throughput panel not replaced: %+v", throughput)
	}
}

// TestSavePanelTraversalStaysInDir proves a "../evil" board_id cannot escape the
// target dir: the sanitized slug is written as a sibling inside dir.
func TestSavePanelTraversalStaysInDir(t *testing.T) {
	dir := t.TempDir()
	slug := slugifyBoardID("../evil")
	if slug != "evil" {
		t.Fatalf("slug = %q; want evil", slug)
	}
	_, file, err := savePanelToDir(dir, slug, "", specs.Panel{Kind: "query", Title: "p", Source: "sql", Query: "SELECT 1"})
	if err != nil {
		t.Fatalf("savePanelToDir: %v", err)
	}
	if filepath.Dir(file) != dir {
		t.Fatalf("file %q escaped dir %q", file, dir)
	}
	if filepath.Base(file) != "evil.yaml" {
		t.Errorf("file base = %q; want evil.yaml", filepath.Base(file))
	}
}

// loadBoard loads the dashboards from dir via the real specs loader and returns
// the one whose ID matches slug.
func loadBoard(t *testing.T, dir, slug string) specs.Dashboard {
	t.Helper()
	dbs, err := specs.Load(dir)
	if err != nil {
		t.Fatalf("specs.Load: %v", err)
	}
	for _, d := range dbs {
		if d.ID == slug {
			return d
		}
	}
	t.Fatalf("dashboard %q not found among %d loaded", slug, len(dbs))
	return specs.Dashboard{}
}
