package db

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
