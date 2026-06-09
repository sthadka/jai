package db

import "regexp"

var safeColumnRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// FieldMapping represents a row from the field_map table.
type FieldMapping struct {
	JiraID       string
	JiraName     string
	Name         string
	Type         string
	IsCustom     bool
	IsColumn     bool
	UserOverride bool
	Searchable   bool
}

// UpsertFieldMapping inserts or replaces a field mapping.
func (db *DB) UpsertFieldMapping(f *FieldMapping) error {
	_, err := db.Exec(`
		INSERT INTO field_map (jira_id, jira_name, name, type, is_custom, is_column, user_override, searchable)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(jira_id) DO UPDATE SET
			jira_name = excluded.jira_name,
			name = CASE WHEN field_map.user_override THEN field_map.name ELSE excluded.name END,
			type = excluded.type,
			is_custom = excluded.is_custom,
			is_column = excluded.is_column`,
		f.JiraID, f.JiraName, f.Name, f.Type, f.IsCustom, f.IsColumn, f.UserOverride, f.Searchable,
	)
	return err
}

// MarkFieldAsColumn marks a field as having a column in the issues table.
func (db *DB) MarkFieldAsColumn(jiraID string) error {
	_, err := db.Exec(`UPDATE field_map SET is_column = 1 WHERE jira_id = ?`, jiraID)
	return err
}

// AllFieldMappings returns all field mappings.
func (db *DB) AllFieldMappings() ([]*FieldMapping, error) {
	rows, err := db.Query(`
		SELECT jira_id, jira_name, name, type, is_custom, is_column, user_override, searchable
		FROM field_map ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fields []*FieldMapping
	for rows.Next() {
		f := &FieldMapping{}
		if err := rows.Scan(&f.JiraID, &f.JiraName, &f.Name, &f.Type, &f.IsCustom, &f.IsColumn, &f.UserOverride, &f.Searchable); err != nil {
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

// FieldPopulationStats returns non-null counts for the given columns in the issues table.
// If project is non-empty, counts are scoped to that project.
func (db *DB) FieldPopulationStats(columns []string, project string) (map[string]*FieldStats, error) {
	where := ""
	var totalArgs []interface{}
	if project != "" {
		where = " WHERE project = ?"
		totalArgs = append(totalArgs, project)
	}

	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM issues"+where, totalArgs...).Scan(&total); err != nil {
		return nil, err
	}

	result := make(map[string]*FieldStats, len(columns))
	for _, col := range columns {
		if !safeColumnRe.MatchString(col) {
			continue
		}

		cond := `"` + col + `" IS NOT NULL AND "` + col + `" != ''`
		if project != "" {
			cond += " AND project = ?"
		}

		var args []interface{}
		if project != "" {
			args = append(args, project)
		}

		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM issues WHERE `+cond, args...).Scan(&count); err != nil {
			result[col] = &FieldStats{Total: total}
			continue
		}

		var sample string
		row := db.QueryRow(`SELECT "`+col+`" FROM issues WHERE `+cond+` LIMIT 1`, args...)
		_ = row.Scan(&sample)
		if len(sample) > 60 {
			sample = sample[:60] + "..."
		}

		result[col] = &FieldStats{NonNull: count, Total: total, Sample: sample}
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
