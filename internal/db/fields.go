package db

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
