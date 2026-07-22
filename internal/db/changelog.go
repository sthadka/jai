package db

import (
	"fmt"
	"strings"
)

// ChangelogEntry represents a row from the changelog table.
type ChangelogEntry struct {
	ID         string
	IssueKey   string
	Author     string
	Field      string
	FieldType  string
	FromValue  string
	FromString string
	ToValue    string
	ToString   string
	ChangedAt  string
}

// InsertChangelog inserts a changelog entry, skipping duplicates by primary key.
func (db *DB) InsertChangelog(e *ChangelogEntry) error {
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
// projectFilter limits to the given projects; an empty slice means all projects.
func (db *DB) GetChangelogSyncCandidates(projectFilter []string) ([]string, error) {
	query := `
		SELECT i.key FROM issues i
		LEFT JOIN (
			SELECT issue_key, MAX(changed_at) as max_changed
			FROM changelog GROUP BY issue_key
		) c ON i.key = c.issue_key
		WHERE c.issue_key IS NULL OR i.updated > c.max_changed`

	var args []any
	if len(projectFilter) > 0 {
		placeholders := make([]string, len(projectFilter))
		for i, p := range projectFilter {
			placeholders[i] = "?"
			args = append(args, p)
		}
		query += fmt.Sprintf(` AND i.project IN (%s)`, strings.Join(placeholders, ", "))
	}
	query += ` ORDER BY i.key`

	sqlRows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer sqlRows.Close()

	var keys []string
	for sqlRows.Next() {
		var key string
		if sqlRows.Scan(&key) == nil {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
