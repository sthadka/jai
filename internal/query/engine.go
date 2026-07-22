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
	resolved := e.resolveTemplates(sql)
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
func (e *Engine) resolveTemplates(sql string) string {
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

// resolveTemplatesAt is the pure-function core of template resolution.
// It accepts the current time and config explicitly to enable deterministic testing.
func resolveTemplatesAt(sql string, now time.Time, cfg *config.Config) string {
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
