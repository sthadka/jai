package db

import "time"

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

// UpsertAttachments replaces all attachments for an issue and updates attachment_count.
func (db *DB) UpsertAttachments(issueKey string, attachments []*Attachment) error {
	// Delete existing attachments for this issue.
	if _, err := db.Exec(`DELETE FROM attachments WHERE issue_key = ?`, issueKey); err != nil {
		return err
	}

	// Insert new attachments.
	for _, a := range attachments {
		_, err := db.Exec(
			`INSERT INTO attachments (id, issue_key, filename, size, mime_type, author, created, url, synced_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.IssueKey, a.Filename, a.Size, a.MimeType, a.Author, a.Created, a.URL,
			time.Now().UTC().Format(time.RFC3339),
		)
		if err != nil {
			return err
		}
	}

	// Update attachment_count on the issue.
	_, err := db.Exec(`UPDATE issues SET attachment_count = ? WHERE key = ?`, len(attachments), issueKey)
	return err
}
