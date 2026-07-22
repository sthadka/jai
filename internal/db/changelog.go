package db

import (
	"fmt"
	"strings"
	"time"
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

// GetIssueIDToKeyMap returns a map of Jira numeric issue ID to issue key
// for all issues that have a populated id.
func (db *DB) GetIssueIDToKeyMap() (map[string]string, error) {
	sqlRows, err := db.Query(`SELECT id, key FROM issues WHERE id IS NOT NULL AND id != ''`)
	if err != nil {
		return nil, err
	}
	defer sqlRows.Close()

	m := make(map[string]string)
	for sqlRows.Next() {
		var id, key string
		if sqlRows.Scan(&id, &key) == nil {
			m[id] = key
		}
	}
	return m, nil
}

// GetChangelogSyncCandidates returns issue keys that need changelog sync.
// An issue needs sync if its changelog has never been checked, or if its
// updated timestamp is newer than the last time its changelog was checked.
// projectFilter limits to the given projects; an empty slice means all projects.
func (db *DB) GetChangelogSyncCandidates(projectFilter []string) ([]string, error) {
	query := `
		SELECT key FROM issues
		WHERE changelog_synced_at IS NULL
		   OR updated > changelog_synced_at`

	var args []any
	if len(projectFilter) > 0 {
		placeholders := make([]string, len(projectFilter))
		for i, p := range projectFilter {
			placeholders[i] = "?"
			args = append(args, p)
		}
		query += fmt.Sprintf(` AND project IN (%s)`, strings.Join(placeholders, ", "))
	}
	query += ` ORDER BY key`

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

// MarkChangelogSynced stamps changelog_synced_at for the given issue keys.
func (db *DB) MarkChangelogSynced(keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)

	// Batch in chunks of 500 to stay under SQLite variable limit.
	const chunkSize = 500
	for i := 0; i < len(keys); i += chunkSize {
		end := i + chunkSize
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[i:end]

		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk)+1)
		args[0] = now
		for j, k := range chunk {
			placeholders[j] = "?"
			args[j+1] = k
		}

		_, err := db.Exec(
			fmt.Sprintf(`UPDATE issues SET changelog_synced_at = ? WHERE key IN (%s)`,
				strings.Join(placeholders, ", ")),
			args...,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetIssueIDToKeyMapForKeys returns id→key mappings for the given keys.
// Use for small batches (≤100 keys). For large sets, use GetIssueIDToKeyMap().
func (db *DB) GetIssueIDToKeyMapForKeys(keys []string) (map[string]string, error) {
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
		`SELECT id, key FROM issues WHERE key IN (%s) AND id IS NOT NULL AND id != ''`,
		strings.Join(placeholders, ", "),
	)
	sqlRows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer sqlRows.Close()

	m := make(map[string]string)
	for sqlRows.Next() {
		var id, key string
		if sqlRows.Scan(&id, &key) == nil {
			m[id] = key
		}
	}
	return m, nil
}
