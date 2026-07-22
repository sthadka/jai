package query

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

// paramVarRe matches parameterized template variables like {{days_ago:14}}.
var paramVarRe = regexp.MustCompile(`\{\{(days_ago|weeks_ago|months_ago):(\d+)\}\}`)

// snippetVarRe matches {{name}} placeholders used for snippet expansion.
var snippetVarRe = regexp.MustCompile(`\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}`)

// maxSnippetDepth is the maximum recursion depth for snippet expansion.
const maxSnippetDepth = 10

// Results holds query output.
type Results struct {
	Columns []string
	Rows    [][]interface{}
	Count   int
}

// Engine executes SQL against the local database.
type Engine struct {
	db  *db.DB
	cfg *config.Config
}

// New creates a new query Engine.
func New(database *db.DB, cfg *config.Config) *Engine {
	return &Engine{db: database, cfg: cfg}
}

// Execute runs a SQL query with template variable resolution.
// Only SELECT and WITH (CTE) statements are permitted; write operations
// are rejected to protect the local database from accidental mutation.
func (e *Engine) Execute(sql string, args ...interface{}) (*Results, error) {
	resolved, err := e.resolveTemplates(sql)
	if err != nil {
		return nil, fmt.Errorf("template resolution: %w", err)
	}
	if err := requireReadOnly(resolved); err != nil {
		return nil, err
	}

	rows, err := e.db.Query(resolved, args...)
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var resultRows [][]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]interface{}, len(cols))
		copy(row, vals)
		resultRows = append(resultRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &Results{
		Columns: cols,
		Rows:    resultRows,
		Count:   len(resultRows),
	}, nil
}

// requireReadOnly returns an error if sql is not a SELECT or WITH statement.
func requireReadOnly(sql string) error {
	first := strings.ToUpper(strings.TrimSpace(sql))
	if strings.HasPrefix(first, "SELECT") || strings.HasPrefix(first, "WITH") {
		return nil
	}
	return fmt.Errorf("only SELECT queries are allowed; use 'jai set', 'jai comment', or 'jai push' for writes")
}

// resolveTemplates replaces {{variable}} placeholders with their values.
func (e *Engine) resolveTemplates(sql string) (string, error) {
	return resolveTemplatesAt(sql, time.Now(), e.cfg)
}

// quarterStart returns the first day of the quarter containing the given time.
func quarterStart(t time.Time) time.Time {
	q := (t.Month()-1)/3*3 + 1 // Jan=1, Apr=4, Jul=7, Oct=10
	return time.Date(t.Year(), q, 1, 0, 0, 0, 0, t.Location())
}

// mondayOfWeek returns the Monday of the week containing the given time.
func mondayOfWeek(t time.Time) time.Time {
	weekday := t.Weekday()
	if weekday == time.Sunday {
		weekday = 7
	}
	offset := int(weekday) - int(time.Monday)
	return t.AddDate(0, 0, -offset)
}

// projectKeys returns a comma-separated list of single-quoted project keys
// from all configured sync sources (e.g., 'PROJ1','PROJ2').
func projectKeys(cfg *config.Config) string {
	seen := map[string]bool{}
	var keys []string
	for _, src := range cfg.SyncSources {
		for _, p := range src.Projects {
			if !seen[p] {
				seen[p] = true
				keys = append(keys, "'"+p+"'")
			}
		}
	}
	return strings.Join(keys, ",")
}

// builtinVarNames is the set of built-in template variable names (without braces).
// Used to distinguish built-in variables from user-defined snippets.
var builtinVarNames = map[string]bool{
	"me": true, "team": true, "today": true, "yesterday": true,
	"week_ago": true, "month_ago": true, "quarter_ago": true,
	"this_week": true, "this_month": true, "this_quarter": true,
	"projects": true,
}

// resolveTemplatesAt is the pure-function core of template resolution.
// It accepts the current time and config explicitly to enable deterministic testing.
// After resolving built-in and parameterized variables, it resolves user-defined
// snippets recursively (up to maxSnippetDepth levels) and returns an error for
// circular references.
func resolveTemplatesAt(sql string, now time.Time, cfg *config.Config) (string, error) {
	// Resolve user-defined snippets first so that snippet bodies containing
	// built-in variables (e.g. {{me}}) get expanded in the next step.
	if len(cfg.Snippets) > 0 {
		var err error
		sql, err = resolveSnippets(sql, cfg, nil, 0)
		if err != nil {
			return "", err
		}
	}

	// Resolve built-in and parameterized variables after snippet expansion.
	sql = resolveBuiltins(sql, now, cfg)

	return sql, nil
}

// resolveBuiltins replaces built-in template variables and parameterized date
// variables. This is separated from snippet resolution so snippets can also
// contain built-in variables that get expanded during recursive resolution.
func resolveBuiltins(sql string, now time.Time, cfg *config.Config) string {
	dateFmt := "2006-01-02"

	replacements := map[string]string{
		"{{me}}":           cfg.Me,
		"{{team}}":         cfg.Team,
		"{{today}}":        now.Format(dateFmt),
		"{{yesterday}}":    now.AddDate(0, 0, -1).Format(dateFmt),
		"{{week_ago}}":     now.AddDate(0, 0, -7).Format(dateFmt),
		"{{month_ago}}":    now.AddDate(0, 0, -30).Format(dateFmt),
		"{{quarter_ago}}":  now.AddDate(0, 0, -90).Format(dateFmt),
		"{{this_week}}":    mondayOfWeek(now).Format(dateFmt),
		"{{this_month}}":   time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format(dateFmt),
		"{{this_quarter}}": quarterStart(now).Format(dateFmt),
		"{{projects}}":     projectKeys(cfg),
	}
	for k, v := range replacements {
		sql = strings.ReplaceAll(sql, k, v)
	}

	// Handle parameterized variables: {{days_ago:N}}, {{weeks_ago:N}}, {{months_ago:N}}.
	sql = paramVarRe.ReplaceAllStringFunc(sql, func(match string) string {
		parts := paramVarRe.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match // leave as-is
		}
		n, err := strconv.Atoi(parts[2])
		if err != nil {
			return match // non-numeric → leave as-is
		}
		switch parts[1] {
		case "days_ago":
			return now.AddDate(0, 0, -n).Format(dateFmt)
		case "weeks_ago":
			return now.AddDate(0, 0, -n*7).Format(dateFmt)
		case "months_ago":
			return now.AddDate(0, -n, 0).Format(dateFmt)
		}
		return match
	})

	return sql
}

// resolveSnippets recursively expands user-defined snippet references in sql.
// The seen set tracks which snippets are currently being expanded to detect cycles.
// depth tracks recursion depth to enforce maxSnippetDepth.
func resolveSnippets(sql string, cfg *config.Config, seen map[string]bool, depth int) (string, error) {
	if depth > maxSnippetDepth {
		return "", fmt.Errorf("snippet recursion depth exceeded (max %d) — check for circular references", maxSnippetDepth)
	}

	var replaceErr error
	result := snippetVarRe.ReplaceAllStringFunc(sql, func(match string) string {
		if replaceErr != nil {
			return match // short-circuit on prior error
		}
		name := match[2 : len(match)-2] // strip {{ and }}

		// Skip built-in variable names — they are already resolved or intentionally
		// left as-is (e.g. unknown variables).
		if builtinVarNames[name] {
			return match
		}

		body, ok := cfg.Snippets[name]
		if !ok {
			return match // unknown snippet → leave as-is
		}

		if seen[name] {
			replaceErr = fmt.Errorf("circular snippet reference: %s", name)
			return match
		}

		// Clone the seen set so sibling branches don't interfere.
		next := make(map[string]bool, len(seen)+1)
		for k, v := range seen {
			next[k] = v
		}
		next[name] = true

		// Recursively resolve the snippet body.
		expanded, err := resolveSnippets(body, cfg, next, depth+1)
		if err != nil {
			replaceErr = err
			return match
		}
		return expanded
	})
	if replaceErr != nil {
		return "", replaceErr
	}
	return result, nil
}

// ExpandSnippet resolves a single snippet value by expanding any snippet or
// built-in variable references it contains. It is exported for use by the
// schema snippets command to show fully expanded values.
func ExpandSnippet(value string, now time.Time, cfg *config.Config) (string, error) {
	expanded := value
	if len(cfg.Snippets) > 0 {
		var err error
		expanded, err = resolveSnippets(expanded, cfg, nil, 0)
		if err != nil {
			return "", err
		}
	}
	expanded = resolveBuiltins(expanded, now, cfg)
	return expanded, nil
}
