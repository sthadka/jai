package db

import (
	"database/sql"
	"time"
)

// PendingChange represents a queued write operation.
type PendingChange struct {
	ID         int64
	IssueKey   string
	Operation  string // set_field, add_comment, transition
	Payload    string // JSON
	CreatedAt  string
	SyncedAt   sql.NullString
	RetryCount int
	LastError  sql.NullString
}

// createPendingChangesTable is called by migration v2 (or can be ensured here).
// For now we create the table inline when needed.
func (db *DB) EnsurePendingChangesTable() error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS pending_changes (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		issue_key   TEXT NOT NULL,
		operation   TEXT NOT NULL,
		payload     TEXT NOT NULL,
		created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
		synced_at   DATETIME,
		retry_count INTEGER NOT NULL DEFAULT 0,
		last_error  TEXT
	)`)
	return err
}

// InsertPendingChange queues a new write operation.
func (db *DB) InsertPendingChange(issueKey, operation, payload string) error {
	_, err := db.Exec(
		`INSERT INTO pending_changes (issue_key, operation, payload, created_at) VALUES (?, ?, ?, ?)`,
		issueKey, operation, payload, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// ListPendingChanges returns all unsynced pending changes.
func (db *DB) ListPendingChanges() ([]*PendingChange, error) {
	rows, err := db.Query(`
		SELECT id, issue_key, operation, payload, created_at, synced_at, retry_count, last_error
		FROM pending_changes
		WHERE synced_at IS NULL
		ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var changes []*PendingChange
	for rows.Next() {
		c := &PendingChange{}
		if err := rows.Scan(&c.ID, &c.IssueKey, &c.Operation, &c.Payload, &c.CreatedAt, &c.SyncedAt, &c.RetryCount, &c.LastError); err != nil {
			return nil, err
		}
		changes = append(changes, c)
	}
	return changes, rows.Err()
}

// MarkSynced marks a pending change as synced.
func (db *DB) MarkSynced(id int64) error {
	_, err := db.Exec(
		`UPDATE pending_changes SET synced_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

// RecordRetryError increments the retry count and records the last error.
func (db *DB) RecordRetryError(id int64, errMsg string) error {
	_, err := db.Exec(
		`UPDATE pending_changes SET retry_count = retry_count + 1, last_error = ? WHERE id = ?`,
		errMsg, id,
	)
	return err
}

// CountPendingChanges returns the number of unsynced pending changes.
func (db *DB) CountPendingChanges() (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pending_changes WHERE synced_at IS NULL`).Scan(&count)
	return count, err
}
