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

// InsertChangelogBatch inserts multiple changelog entries in a single transaction.
func (db *DB) InsertChangelogBatch(entries []*ChangelogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO changelog
			(id, issue_key, author, field, field_type, from_value, from_string, to_value, to_string, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		if _, err := stmt.Exec(e.ID, e.IssueKey, e.Author, e.Field, e.FieldType,
			e.FromValue, e.FromString, e.ToValue, e.ToString, e.ChangedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetIssueIDToKeyMap returns a map of Jira numeric issue ID to issue key.
// Issues synced before the id column was populated will have NULL and are skipped.
func (db *DB) GetIssueIDToKeyMap(keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		args[i] = k
	}
	query := fmt.Sprintf(
		`SELECT id, key FROM issues WHERE key IN (%s) AND id IS NOT NULL`,
		strings.Join(placeholders, ", "),
	)

	sqlRows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer sqlRows.Close()

	m := make(map[string]string)
	for sqlRows.Next() {
		var id, key *string
		if sqlRows.Scan(&id, &key) == nil && id != nil && key != nil && *id != "" {
			m[*id] = *key
		}
	}
	return m, nil
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
