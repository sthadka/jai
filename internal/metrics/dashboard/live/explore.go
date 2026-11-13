package live

// explore.go implements the free-form, read-only Explore path: a SELECT-only
// validator, a same-origin CSRF token, and the POST /explore/sql handler that
// runs an arbitrary (but read-only) user SELECT against the locked-down DuckDB
// engine and returns typed columns + capped rows. It implements
// docs/query-driven-explore-design.md §6 (Validation), §9.1, §9.3, §10.2.
//
// Defense in depth: the engine session is already hard-locked (ATTACH READ_ONLY,
// enable_external_access=false, lock_configuration=true — see engine.go setup),
// so even a write/escape statement that slipped past this validator could not
// mutate anything. validateSelectSQL is the first, friendly 400-worthy gate that
// keeps obvious non-SELECT input from ever reaching the engine.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sthadka/jai/internal/jql"
	"github.com/sthadka/jai/internal/metrics/dashboard/specs"
	"gopkg.in/yaml.v3"
)

// exploreRowCap bounds how many rows /explore/sql returns. We fetch one past the
// cap so truncation can be reported cheaply without a separate COUNT.
const exploreRowCap = 5000

// disallowedVerbs are statement keywords rejected (as whole, bare words) in the
// read-only Explore path. The check is token-level, not substring, so column
// names like created/updated never trip CREATE/UPDATE.
var disallowedVerbs = map[string]bool{
	"INSERT":  true,
	"UPDATE":  true,
	"DELETE":  true,
	"MERGE":   true,
	"CREATE":  true,
	"ALTER":   true,
	"DROP":    true,
	"ATTACH":  true,
	"COPY":    true,
	"PRAGMA":  true,
	"INSTALL": true,
	"LOAD":    true,
	"SET":     true,
	"EXPORT":  true,
	"CALL":    true,
}

// validateSelectSQL is the SELECT-only guard for /explore/sql. Rules (§6):
//   - trimmed input must begin with SELECT or WITH (case-insensitive, as a whole
//     word — "SELECTED" does not qualify);
//   - only one statement: a single trailing ';' is allowed and ignored, but any
//     ';' that separates two statements is rejected;
//   - no disallowed statement keyword may appear as a bare word anywhere.
//
// The scan ignores ';' and keywords that occur inside string literals, quoted
// identifiers, or comments, so e.g. SELECT ';' or a "update" column name is fine.
func validateSelectSQL(sql string) error {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return fmt.Errorf("empty query")
	}
	words, multiStatement := scanSQL(trimmed)
	if multiStatement {
		return fmt.Errorf("multiple statements are not allowed; submit a single SELECT")
	}
	if len(words) == 0 || (words[0] != "SELECT" && words[0] != "WITH") {
		return fmt.Errorf("only read-only SELECT (or WITH … SELECT) queries are allowed")
	}
	for _, w := range words {
		if disallowedVerbs[w] {
			return fmt.Errorf("statement keyword %q is not allowed in a read-only query", w)
		}
	}
	return nil
}

// scanSQL walks sql once and returns the uppercased bare-word tokens found
// OUTSIDE string literals ('…', with ” escape), quoted identifiers ("…", with
// "" escape), and comments (-- line, /* block */). multiStatement is true when a
// ';' is followed by further meaningful content (i.e. a second statement); a lone
// trailing ';' therefore does not set it.
func scanSQL(sql string) (words []string, multiStatement bool) {
	var word strings.Builder
	rs := []rune(sql)
	pendingSemicolon := false

	flush := func() {
		if word.Len() > 0 {
			words = append(words, strings.ToUpper(word.String()))
			word.Reset()
		}
	}
	// content marks that meaningful (non-space, non-comment) input was seen; if a
	// ';' preceded it, a second statement exists.
	content := func() {
		if pendingSemicolon {
			multiStatement = true
		}
	}

	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\'': // string literal
			flush()
			content()
			i++
			for i < len(rs) {
				if rs[i] == '\'' {
					if i+1 < len(rs) && rs[i+1] == '\'' {
						i += 2
						continue
					}
					break
				}
				i++
			}
		case c == '"': // quoted identifier
			flush()
			content()
			i++
			for i < len(rs) {
				if rs[i] == '"' {
					if i+1 < len(rs) && rs[i+1] == '"' {
						i += 2
						continue
					}
					break
				}
				i++
			}
		case c == '-' && i+1 < len(rs) && rs[i+1] == '-': // line comment
			flush()
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(rs) && rs[i+1] == '*': // block comment
			flush()
			i += 2
			for i+1 < len(rs) && (rs[i] != '*' || rs[i+1] != '/') {
				i++
			}
			i++ // land on the closing '/'; the loop's i++ steps past it
		case c == ';':
			flush()
			content() // a ';' after a pending ';' is itself a second (empty) statement
			pendingSemicolon = true
		case unicode.IsSpace(c):
			flush()
		case isWordChar(c):
			if word.Len() == 0 {
				content()
			}
			word.WriteRune(c)
		default: // operators, parens, commas, etc.
			flush()
			content()
		}
	}
	flush()
	return words, multiStatement
}

// isWordChar reports whether c can appear in a SQL bare word (identifier/keyword).
func isWordChar(c rune) bool {
	return c == '_' || unicode.IsLetter(c) || unicode.IsDigit(c)
}

// exploreColumn is one typed column in the /explore/sql response, carrying the
// projection name and the inferred logical type ("number" | "date" | "string").
type exploreColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// dateLayouts are the formats a value must match to be classified as a date. The
// engine reads jai.db columns as VARCHAR (sqlite_all_varchar), so timestamps/
// dates arrive as strings in these shapes.
var dateLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// inferColumns classifies each column (in the given projection order) by sampling
// its non-null values across rows. Order is supplied by the caller (DuckDB JSON
// rows decode into maps, which lose key order) — see runExplore.
func inferColumns(names []string, rows []map[string]any) []exploreColumn {
	cols := make([]exploreColumn, len(names))
	for i, name := range names {
		cols[i] = exploreColumn{Name: name, Type: inferType(name, rows)}
	}
	return cols
}

// inferType returns "number" when every sampled value parses as a float, "date"
// when every sampled value parses as a date/timestamp, else "string". A column
// with no non-null samples (all null / empty) is "string".
func inferType(name string, rows []map[string]any) string {
	sawSample := false
	allNumber := true
	allDate := true
	for _, row := range rows {
		s, ok := sampleString(row[name])
		if !ok {
			continue
		}
		sawSample = true
		if !isNumber(s) {
			allNumber = false
		}
		if !isDate(s) {
			allDate = false
		}
		if !allNumber && !allDate {
			return "string"
		}
	}
	if !sawSample {
		return "string"
	}
	if allNumber {
		return "number"
	}
	if allDate {
		return "date"
	}
	return "string"
}

// sampleString renders one cell value as a string sample for type inference,
// reporting ok=false for values that count as null (nil or blank string). DuckDB
// JSON mode emits INTEGER columns as JSON numbers but DOUBLE and VARCHAR columns
// as strings, so both arrive here.
func sampleString(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		if strings.TrimSpace(x) == "" {
			return "", false
		}
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	default:
		return fmt.Sprint(x), true
	}
}

// isNumber reports whether s parses as a float (covers ints and decimals).
func isNumber(s string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err == nil
}

// isDate reports whether s matches any accepted date/timestamp layout.
func isDate(s string) bool {
	s = strings.TrimSpace(s)
	for _, layout := range dateLayouts {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}

// exploreRequest is the POST /explore/sql body.
type exploreRequest struct {
	SQL string `json:"sql"`
}

// exploreJQLRequest is the POST /explore/jql body.
type exploreJQLRequest struct {
	JQL string `json:"jql"`
}

// newCSRFToken mints a 128-bit, hex-encoded same-origin token at server startup.
// It is echoed to the page via /meta (csrf_token) and required on POST
// /explore/* via the X-Jai-CSRF header. crypto/rand.Read does not fail on the
// supported platforms; a read error leaves a zero token, which still forces the
// header to be present and match.
func newCSRFToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// exploreToken returns the server's same-origin CSRF token.
func (s *Server) exploreToken() string { return s.csrfToken }

// handleExploreSQL is the read-only free-form SQL endpoint. It requires POST +
// a matching X-Jai-CSRF header, accepts {"sql": "…"}, enforces the SELECT-only
// guard, and executes the query against the locked-down engine with a row cap.
// Returns {columns:[{name,type}], rows, truncated, sql}. 403 on CSRF mismatch,
// 400 on a validation error, 500 on an engine execution error.
func (s *Server) handleExploreSQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if r.Header.Get("X-Jai-CSRF") != s.exploreToken() {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid or missing CSRF token"})
		return
	}
	var req exploreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if err := validateSelectSQL(req.SQL); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	cols, rows, truncated, err := s.runExplore(req.SQL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"columns":   cols,
		"rows":      rows,
		"truncated": truncated,
		"sql":       req.SQL,
	})
}

// runExplore executes a validated user SELECT and returns typed columns, capped
// rows, and a truncation flag. It uses QueryCheck (not Query) so a DuckDB
// execution error — which otherwise surfaces as empty rows + nil error — becomes
// a real error the handler reports as 500.
//
// Column order: DuckDB JSON rows decode into Go maps, losing projection order, so
// we ask DuckDB for the ordered column names with DESCRIBE (which lists columns
// in projection order and also validates the query binds). The data query wraps
// the user SQL as a subquery with an outer LIMIT to enforce the row cap.
func (s *Server) runExplore(sql string) ([]exploreColumn, []map[string]any, bool, error) {
	clean := strings.TrimRight(strings.TrimSpace(sql), "; \t\r\n")

	descRows, err := s.engine.QueryCheck("DESCRIBE (" + clean + ")")
	if err != nil {
		return nil, nil, false, err
	}
	names := make([]string, 0, len(descRows))
	for _, dr := range descRows {
		if n, ok := dr["column_name"].(string); ok {
			names = append(names, n)
		}
	}

	dataSQL := fmt.Sprintf("SELECT * FROM (%s) LIMIT %d", clean, exploreRowCap+1)
	rows, err := s.engine.QueryCheck(dataSQL)
	if err != nil {
		return nil, nil, false, err
	}
	truncated := false
	if len(rows) > exploreRowCap {
		rows = rows[:exploreRowCap]
		truncated = true
	}
	return inferColumns(names, rows), rows, truncated, nil
}

// handleExploreJQL is the read-only free-form JQL endpoint. It mirrors
// handleExploreSQL (POST + matching X-Jai-CSRF), accepts {"jql": "…"}, translates
// the JQL to parameter-safe DuckDB SQL via the internal/jql pipeline, and runs
// the compiled statement through the SAME runExplore helper as /explore/sql.
// Returns {columns:[{name,type}], rows, truncated, sql, compiled_sql}. 403 on a
// CSRF mismatch, 400 on a translation error (parse / unknown field / unsupported
// function, carrying the translator's precise offset+token message), 500 on an
// engine execution error. A compiled statement that fails the SELECT-only guard
// is a translator bug, not user input, so it surfaces as 500.
func (s *Server) handleExploreJQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if r.Header.Get("X-Jai-CSRF") != s.exploreToken() {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid or missing CSRF token"})
		return
	}
	var req exploreJQLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	fm := s.exploreFieldMap()
	res, err := jql.Translate(req.JQL, jql.Options{
		Dialect:    jql.DuckDB,
		FieldMap:   fm,
		IDResolver: s.makeIDResolver(fm),
		Now:        time.Now(),
	})
	if err != nil {
		// Parse / resolve errors (unknown field, unsupported function, …) are user
		// input problems carrying a precise message; report them as 400.
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	// Belt-and-braces: the translator only ever emits a single SELECT. If this
	// ever fails it is a translator defect, never user input, so report 500.
	if err := validateSelectSQL(res.SQL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "translator produced a non-SELECT statement: " + err.Error()})
		return
	}
	cols, rows, truncated, err := s.runExplore(res.SQL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"columns":      cols,
		"rows":         rows,
		"truncated":    truncated,
		"sql":          res.SQL,
		"compiled_sql": res.SQL,
	})
}

// exploreFieldMap returns the cached JQL field map, building it from the live DB
// on first use. The map is rebuilt alongside /meta's generation refresh (see
// refreshFieldMap) so a resync that adds fields is picked up without a restart.
func (s *Server) exploreFieldMap() jql.FieldMap {
	s.mu.RLock()
	fm := s.jqlFields
	s.mu.RUnlock()
	if fm != nil {
		return fm
	}
	return s.refreshFieldMap()
}

// refreshFieldMap rebuilds the JQL field map from jai.field_map and caches it.
// It is called on first use and on every /meta refresh so a resync is honored
// live. On a build error (or an empty map) it keeps and returns any previously
// cached map; the translator falls back to its built-in alias table regardless.
func (s *Server) refreshFieldMap() jql.FieldMap {
	fm := buildFieldMap(s.engine)
	s.mu.Lock()
	defer s.mu.Unlock()
	if fm != nil {
		s.jqlFields = fm
	}
	return s.jqlFields
}

// buildFieldMap reads jai.field_map and builds the JQL name→column map the
// translator needs. Both the local column name and the Jira display name are
// registered (case-insensitive) for every column-backed field. The storage kind
// is derived from the field's type (datetime→date, number→number, array→array,
// else string); labels/components are always arrays. User-typed fields are marked
// as entities so their id values run through the IDResolver (design §8.5).
//
// The engine scans jai.db with sqlite_all_varchar, so every value arrives as a
// string. A missing field_map table (e.g. a bare fixture) yields zero rows via
// Query (which does not error on a failed statement), leaving the built-in alias
// table to handle the common fields.
func buildFieldMap(engine *Engine) jql.FieldMap {
	rows, err := engine.Query("SELECT jira_name, name, type, item_type, is_column FROM jai.field_map")
	if err != nil || len(rows) == 0 {
		return jql.FieldMap{}
	}
	fm := make(jql.FieldMap, len(rows)*2)
	for _, row := range rows {
		if !truthy(row["is_column"]) {
			continue
		}
		col := strValue(row["name"])
		if col == "" {
			continue
		}
		typ := strValue(row["type"])
		fd := jql.FieldDef{
			Column: col,
			Kind:   fieldKind(typ, col),
			Entity: strings.EqualFold(typ, "user"),
		}
		fm[strings.ToLower(col)] = fd
		if jiraName := strValue(row["jira_name"]); jiraName != "" {
			fm[strings.ToLower(jiraName)] = fd
		}
	}
	return fm
}

// fieldKind maps a field_map type (and column name) to the translator's storage
// kind. datetime→date, number→number, array (or the labels/components columns)→
// array, everything else→string.
func fieldKind(typ, col string) jql.FieldKind {
	switch strings.ToLower(typ) {
	case "datetime", "date":
		return jql.KindDate
	case "number":
		return jql.KindNumber
	case "array":
		return jql.KindArray
	}
	switch strings.ToLower(col) {
	case "labels", "components":
		return jql.KindArray
	}
	return jql.KindString
}

// makeIDResolver returns an IDResolver (design §8.5) that maps an entity id to its
// single display name by scanning jai.issues: an id present in raw_json pins the
// row, and the entity column on that row carries the display name. It returns
// ok=false when the id is unknown (0 rows) or ambiguous (>1 distinct name), so
// the translator rejects the clause rather than guessing. The id is emitted as an
// escaped string literal; the column comes from the resolved field map (falling
// back to the lower-cased field name, which matches the built-in entity columns).
func (s *Server) makeIDResolver(fm jql.FieldMap) jql.IDResolver {
	return func(field, id string) (string, bool, error) {
		col := strings.ToLower(field)
		if fd, ok := fm[strings.ToLower(field)]; ok && fd.Column != "" {
			col = fd.Column
		}
		q := fmt.Sprintf(
			"SELECT DISTINCT %s FROM jai.issues WHERE raw_json LIKE '%%'||%s||'%%' AND %s IS NOT NULL AND %s <> '' LIMIT 2",
			col, sqlLiteral(id), col, col,
		)
		rows, err := s.engine.Query(q)
		if err != nil {
			return "", false, err
		}
		if len(rows) != 1 {
			return "", false, nil
		}
		name := strValue(rows[0][col])
		if name == "" {
			return "", false, nil
		}
		return name, true, nil
	}
}

// sqlLiteral renders s as a single-quoted SQL string literal with embedded quotes
// doubled. Used for the IDResolver's raw_json containment probe.
func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// strValue renders an engine cell (which arrives as a string under
// sqlite_all_varchar, but may decode as a JSON number for INTEGER columns) as a
// trimmed string.
func strValue(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", t))
	}
}

// truthy reports whether an engine cell represents a true boolean/flag. SQLite
// booleans arrive as "1"/"0" strings (sqlite_all_varchar) or JSON numbers.
func truthy(v any) bool {
	switch strValue(v) {
	case "1", "true", "TRUE", "True":
		return true
	default:
		return false
	}
}

// explorePanelRequest is the POST /explore/panel body: an Explore widget's state
// plus the destination board and a save flag. It mirrors the kind:query fields of
// specs.Panel. save==false previews the dashboard YAML (copy-to-clipboard);
// save==true persists it to the user dashboards dir.
type explorePanelRequest struct {
	BoardID    string              `json:"board_id"`
	BoardTitle string              `json:"board_title"`
	Title      string              `json:"title"`
	Source     string              `json:"source"`
	Query      string              `json:"query"`
	Chart      string              `json:"chart"`
	X          string              `json:"x"`
	Y          string              `json:"y"`
	Agg        string              `json:"agg"`
	Segment    string              `json:"segment"`
	Filters    map[string][]string `json:"filters"`
	Save       bool                `json:"save"`
}

// handleExplorePanel turns an Explore widget's state into a dashboard-panel spec
// and either returns the dashboard YAML (save==false, for copy-to-clipboard) or
// persists it under the user dashboards dir (save==true). It requires POST + a
// matching X-Jai-CSRF header (same as /explore/{sql,jql}). It validates source
// (sql|jql) and a non-empty query, and sanitizes board_id to a path-safe slug;
// it NEVER executes the query (rendering validates it through /explore/* at view
// time). 403 on CSRF mismatch, 400 on a validation error, 500 on a write error.
func (s *Server) handleExplorePanel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if r.Header.Get("X-Jai-CSRF") != s.exploreToken() {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid or missing CSRF token"})
		return
	}
	var req explorePanelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if req.Source != "sql" && req.Source != "jql" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": `source must be "sql" or "jql"`})
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "query must not be empty"})
		return
	}
	slug := slugifyBoardID(req.BoardID)
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "board_id must contain at least one path-safe character"})
		return
	}

	panel := specs.Panel{
		Kind:    "query",
		Title:   req.Title,
		Source:  req.Source,
		Query:   req.Query,
		Chart:   req.Chart,
		X:       req.X,
		Y:       req.Y,
		Agg:     req.Agg,
		Segment: req.Segment,
		Filters: req.Filters,
	}
	boardTitle := req.BoardTitle
	if strings.TrimSpace(boardTitle) == "" {
		boardTitle = slug
	}

	if !req.Save {
		dash := specs.Dashboard{ID: slug, Title: boardTitle, Panels: []specs.Panel{panel}}
		out, err := yaml.Marshal(dash)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"yaml": string(out)})
		return
	}

	dir, err := specs.UserDashboardDir()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	content, file, err := savePanelToDir(dir, slug, boardTitle, panel)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"yaml":         content,
		"file":         file,
		"dashboard_id": slug,
	})
}

// slugifyBoardID reduces board_id to a path-safe slug: lowercase, keep [a-z0-9],
// and collapse every run of other characters (including '-') to a single '-',
// then trim leading/trailing '-'. The result is always within [a-z0-9-] with no
// consecutive dashes, so a path built from it (slug+".yaml") can never traverse
// out of the target dir — "../evil" becomes "evil". Empty is a caller-rejected
// 400.
func slugifyBoardID(id string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(id) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// savePanelToDir writes (or upserts) panel into the dashboard spec file
// <slug>.yaml under dir, returning the written file content and its path. dir is
// a parameter so tests target a temp dir instead of the real ~/.config; slug is
// assumed already sanitized by the caller. When the file exists, the board's
// Title/ID are preserved and the panel is upserted by Title (replace a same-Title
// panel, else append); otherwise a new single-panel dashboard is created. The dir
// is created 0755 and the file written 0644.
func savePanelToDir(dir, slug, boardTitle string, panel specs.Panel) (content, file string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("create dashboards dir: %w", err)
	}
	file = filepath.Join(dir, slug+".yaml")

	dash := specs.Dashboard{ID: slug, Title: boardTitle, Panels: []specs.Panel{}}
	if data, rerr := os.ReadFile(file); rerr == nil {
		var existing specs.Dashboard
		if uerr := yaml.Unmarshal(data, &existing); uerr != nil {
			return "", "", fmt.Errorf("parse existing dashboard %s: %w", file, uerr)
		}
		dash = existing
		if strings.TrimSpace(dash.ID) == "" {
			dash.ID = slug
		}
		if strings.TrimSpace(dash.Title) == "" {
			dash.Title = boardTitle
		}
	} else if !os.IsNotExist(rerr) {
		return "", "", fmt.Errorf("read existing dashboard %s: %w", file, rerr)
	}

	upsertPanel(&dash, panel)

	out, err := yaml.Marshal(dash)
	if err != nil {
		return "", "", fmt.Errorf("marshal dashboard: %w", err)
	}
	if err := os.WriteFile(file, out, 0o644); err != nil {
		return "", "", fmt.Errorf("write dashboard %s: %w", file, err)
	}
	return string(out), file, nil
}

// upsertPanel replaces the first panel sharing panel.Title in dash, else appends
// it. Title is the stable identity of a saved widget within a board.
func upsertPanel(dash *specs.Dashboard, panel specs.Panel) {
	for i := range dash.Panels {
		if dash.Panels[i].Title == panel.Title {
			dash.Panels[i] = panel
			return
		}
	}
	dash.Panels = append(dash.Panels, panel)
}
