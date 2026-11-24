package store

import (
	"database/sql"
	"time"
)

// Attachment represents a row from the attachments table.
type Attachment struct {
	ID       int
	IssueKey string
	Filename string
	Size     int
	MimeType string
	Author   string
	Created  string
	URL      string
}

// UpsertAttachments replaces all attachments for an issue and updates
// attachment_count atomically in a single transaction.
func (db *DB) UpsertAttachments(issueKey string, attachments []*Attachment) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := UpsertAttachmentsTx(tx, issueKey, attachments); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertAttachmentsTx replaces all attachments for an issue within an existing transaction.
func UpsertAttachmentsTx(tx *sql.Tx, issueKey string, attachments []*Attachment) error {
	if _, err := tx.Exec(`DELETE FROM attachments WHERE issue_key = ?`, issueKey); err != nil {
		return err
	}

	if len(attachments) > 0 {
		stmt, err := tx.Prepare(
			`INSERT INTO attachments (id, issue_key, filename, size, mime_type, author, created, url, synced_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		)
		if err != nil {
			return err
		}
		defer stmt.Close()
		now := time.Now().UTC().Format(time.RFC3339)
		for _, a := range attachments {
			if _, err := stmt.Exec(a.ID, a.IssueKey, a.Filename, a.Size, a.MimeType, a.Author, a.Created, a.URL, now); err != nil {
				return err
			}
		}
	}

	_, err := tx.Exec(`UPDATE issues SET attachment_count = ? WHERE key = ?`, len(attachments), issueKey)
	return err
}
