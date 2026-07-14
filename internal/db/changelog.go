package db

import "database/sql"

// ChangelogEntry represents a row from the changelog table.
type ChangelogEntry struct {
	ID          string
	IssueKey    string
	Author      string
	Field       string
	FieldType   string
	FromValue   string
	FromString  string
	ToValue     string
	ToString    string
	ChangedAt   string
}

// EnsureChangelogTable creates the changelog table if it doesn't exist.
func (db *DB) EnsureChangelogTable() error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS changelog (
		id          TEXT PRIMARY KEY,
		issue_key   TEXT NOT NULL,
		author      TEXT,
		field       TEXT,
		field_type  TEXT,
		from_value  TEXT,
		from_string TEXT,
		to_value    TEXT,
		to_string   TEXT,
		changed_at  DATETIME
	)`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_changelog_issue ON changelog(issue_key)`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_changelog_time ON changelog(changed_at)`)
	return err
}

// UpsertChangelog inserts or ignores a changelog entry.
func (db *DB) UpsertChangelog(e *ChangelogEntry) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO changelog
			(id, issue_key, author, field, field_type, from_value, from_string, to_value, to_string, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.IssueKey, e.Author, e.Field, e.FieldType,
		e.FromValue, e.FromString, e.ToValue, e.ToString, e.ChangedAt,
	)
	return err
}

// GetChangelogSyncCandidates returns issue keys that need changelog sync.
// An issue needs sync if it has no changelog rows, or if its updated timestamp
// is newer than the most recent changelog entry for that issue.
func (db *DB) GetChangelogSyncCandidates(projectFilter string) ([]string, error) {
	query := `
		SELECT i.key FROM issues i
		LEFT JOIN (
			SELECT issue_key, MAX(changed_at) as max_changed
			FROM changelog GROUP BY issue_key
		) c ON i.key = c.issue_key
		WHERE c.issue_key IS NULL OR i.updated > c.max_changed`
	if projectFilter != "" {
		query += ` AND i.project = ?`
	}
	query += ` ORDER BY i.key`

	var keys []string
	var sqlRows *sql.Rows
	var err error
	if projectFilter != "" {
		sqlRows, err = db.Query(query, projectFilter)
	} else {
		sqlRows, err = db.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer sqlRows.Close()
	for sqlRows.Next() {
		var key string
		if sqlRows.Scan(&key) == nil {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
