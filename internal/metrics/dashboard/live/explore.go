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
	"strconv"
	"strings"
	"time"
	"unicode"
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
			for i+1 < len(rs) && !(rs[i] == '*' && rs[i+1] == '/') {
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
