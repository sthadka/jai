package query

import (
	"fmt"
	"strings"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

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
func (e *Engine) Execute(sql string, args ...interface{}) (*Results, error) {
	resolved := e.resolveTemplates(sql)

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

// resolveTemplates replaces {{variable}} placeholders with their values.
func (e *Engine) resolveTemplates(sql string) string {
	now := time.Now()
	replacements := map[string]string{
		"{{me}}":       e.cfg.Me,
		"{{team}}":     e.cfg.Team,
		"{{today}}":    now.Format("2006-01-02"),
		"{{week_ago}}": now.AddDate(0, 0, -7).Format("2006-01-02"),
	}
	for k, v := range replacements {
		sql = strings.ReplaceAll(sql, k, v)
	}
	return sql
}
