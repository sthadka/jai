package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)
var safeColumnRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// FieldMapping represents a row from the field_map table.
type FieldMapping struct {
	JiraID       string
	JiraName     string
	Name         string
	Type         string
	ItemType     string // element sub-type for array fields (user/component/version/option/string)
	IsCustom     bool
	IsColumn     bool
	UserOverride bool
	Searchable   bool
}

// UpsertFieldMapping inserts or replaces a field mapping. is_column is
// intentionally NOT updated on conflict: promotion state is owned solely by
// PromoteField/MarkFieldAsColumn. DiscoverFields re-upserts every field with
// IsColumn:false on each sync, so clobbering it here would un-mark promoted
// columns and force ensurePromotedFields to re-backfill them every run.
func (db *DB) UpsertFieldMapping(f *FieldMapping) error {
	_, err := db.Exec(`
		INSERT INTO field_map (jira_id, jira_name, name, type, item_type, is_custom, is_column, user_override, searchable)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(jira_id) DO UPDATE SET
			jira_name = excluded.jira_name,
			name = CASE WHEN field_map.user_override THEN field_map.name ELSE excluded.name END,
			type = excluded.type,
			item_type = excluded.item_type,
			is_custom = excluded.is_custom`,
		f.JiraID, f.JiraName, f.Name, f.Type, f.ItemType, f.IsCustom, f.IsColumn, f.UserOverride, f.Searchable,
	)
	return err
}

// MarkFieldAsColumn marks a field as having a column in the issues table.
func (db *DB) MarkFieldAsColumn(jiraID string) error {
	_, err := db.Exec(`UPDATE field_map SET is_column = 1 WHERE jira_id = ?`, jiraID)
	return err
}

// PromoteField promotes a non-column custom field to a typed column: it creates
// the column, backfills it from the sparse custom_fields JSON for existing rows,
// and marks the field as a column so later syncs populate the column directly.
// Query results are unchanged — the value moves from a json_extract expression to
// a typed column with no data loss. Idempotent; safe on an already-promoted field.
func (db *DB) PromoteField(jiraID, name, colType string) error {
	if !safeColumnRe.MatchString(name) {
		return fmt.Errorf("invalid field name: %s", name)
	}
	if !safeColumnRe.MatchString(jiraID) {
		return fmt.Errorf("invalid field id: %s", jiraID)
	}
	if err := db.EnsureColumn(name, colType); err != nil {
		return err
	}
	// Backfill the column from JSON for rows that carry the field. json_extract
	// returns NULL when the key is absent, so untouched rows stay NULL.
	backfill := fmt.Sprintf(
		`UPDATE issues SET "%s" = json_extract(custom_fields, '$.%s')
		 WHERE json_extract(custom_fields, '$.%s') IS NOT NULL`,
		name, jiraID, jiraID,
	)
	if _, err := db.Exec(backfill); err != nil {
		return err
	}
	return db.MarkFieldAsColumn(jiraID)
}

// AllFieldMappings returns all field mappings.
func (db *DB) AllFieldMappings() ([]*FieldMapping, error) {
	rows, err := db.ReadDB().Query(`
		SELECT jira_id, jira_name, name, type, COALESCE(item_type, ''), is_custom, is_column, user_override, searchable
		FROM field_map ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fields []*FieldMapping
	for rows.Next() {
		f := &FieldMapping{}
		if err := rows.Scan(&f.JiraID, &f.JiraName, &f.Name, &f.Type, &f.ItemType, &f.IsCustom, &f.IsColumn, &f.UserOverride, &f.Searchable); err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
	return fields, rows.Err()
}

// FieldStats holds population statistics for a single field column.
type FieldStats struct {
	NonNull int
	Total   int
	Sample  string
}

// FieldPopulationStats returns non-null counts and a sample value for the given
// columns in the issues table using a single pass (chunked at 40 columns when wide).
// If project is non-empty, counts are scoped to that project.
func (db *DB) FieldPopulationStats(columns []string, project string) (map[string]*FieldStats, error) {
	where := ""
	var args []any
	if project != "" {
		where = " WHERE project = ?"
		args = append(args, project)
	}

	// Filter to safe column names that actually exist on the issues table so
	// one bad/missing column does not fail the entire single-pass query.
	existing := make(map[string]bool)
	crows, err := db.ReadDB().Query(`PRAGMA table_info(issues)`)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var (
			cid     int
			name    string
			colType string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := crows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err == nil {
			existing[name] = true
		}
	}
	crows.Close()

	result := make(map[string]*FieldStats, len(columns))
	var validCols []string
	var missingCols []string
	for _, col := range columns {
		if !safeColumnRe.MatchString(col) {
			continue
		}
		if existing[col] {
			validCols = append(validCols, col)
		} else {
			missingCols = append(missingCols, col)
		}
	}

	total := 0
	if len(validCols) == 0 {
		if len(missingCols) > 0 {
			if err := db.ReadDB().QueryRow("SELECT COUNT(*) FROM issues"+where, args...).Scan(&total); err != nil {
				return nil, err
			}
			for _, col := range missingCols {
				result[col] = &FieldStats{Total: total}
			}
		}
		return result, nil
	}

	const colChunkSize = 40
	for i := 0; i < len(validCols); i += colChunkSize {
		end := i + colChunkSize
		if end > len(validCols) {
			end = len(validCols)
		}
		chunk := validCols[i:end]

		exprs := make([]string, 0, 1+len(chunk)*2)
		exprs = append(exprs, "COUNT(*)")
		for _, col := range chunk {
			qcol := `"` + col + `"`
			exprs = append(exprs,
				fmt.Sprintf(`COALESCE(SUM(CASE WHEN %s IS NOT NULL AND %s <> '' THEN 1 ELSE 0 END), 0)`, qcol, qcol),
				fmt.Sprintf(`MAX(CASE WHEN %s IS NOT NULL AND %s <> '' THEN SUBSTR(CAST(%s AS TEXT), 1, 61) END)`, qcol, qcol, qcol),
			)
		}
		q := "SELECT " + strings.Join(exprs, ", ") + " FROM issues" + where
		counts := make([]int, len(chunk))
		samples := make([]sql.NullString, len(chunk))
		dest := make([]any, 0, 1+len(chunk)*2)
		dest = append(dest, &total)
		for j := range chunk {
			dest = append(dest, &counts[j], &samples[j])
		}
		if err := db.ReadDB().QueryRow(q, args...).Scan(dest...); err != nil {
			return nil, err
		}
		for j, col := range chunk {
			sample := samples[j].String
			if len(sample) > 60 {
				sample = sample[:60] + "..."
			}
			result[col] = &FieldStats{NonNull: counts[j], Total: total, Sample: sample}
		}
	}
	for _, col := range missingCols {
		result[col] = &FieldStats{Total: total}
	}

	return result, nil
}

// FieldMapByJiraID returns a map of jiraID → FieldMapping.
func (db *DB) FieldMapByJiraID() (map[string]*FieldMapping, error) {
	fields, err := db.AllFieldMappings()
	if err != nil {
		return nil, err
	}
	m := make(map[string]*FieldMapping, len(fields))
	for _, f := range fields {
		m[f.JiraID] = f
	}
	return m, nil
}
