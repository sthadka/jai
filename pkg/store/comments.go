package store

import "time"

// Comment represents a row from the comments table.
type Comment struct {
	ID          string
	IssueKey    string
	Author      string
	AuthorEmail string
	Body        string
	Created     string
	Updated     string
}

// UpsertComment inserts or replaces a comment row.
func (db *DB) UpsertComment(c *Comment) error {
	_, err := db.Exec(
		`INSERT OR REPLACE INTO comments (id, issue_key, author, author_email, body, created, updated)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.IssueKey, c.Author, c.AuthorEmail, c.Body, c.Created, c.Updated,
	)
	return err
}

// GetComments returns all comments for an issue, ordered by created time.
func (db *DB) GetComments(issueKey string) ([]*Comment, error) {
	// Use CAST(... AS TEXT) to bypass go-sqlite3's automatic DATETIME→time.Time
	// conversion. Without this, Jira's "+0000" timezone format (no colon) fails
	// go-sqlite3's timestamp parsers and the column scans as time.Time{} (zero),
	// which then formats as "0001-01-01T00:00:00Z" and displays as "Jan 01, 0001".
	rows, err := db.ReadDB().Query(
		`SELECT id, issue_key, author, author_email, body,
		 CAST(created AS TEXT), CAST(updated AS TEXT)
		 FROM comments WHERE issue_key = ? ORDER BY created`,
		issueKey,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []*Comment
	for rows.Next() {
		c := &Comment{}
		if err := rows.Scan(&c.ID, &c.IssueKey, &c.Author, &c.AuthorEmail, &c.Body, &c.Created, &c.Updated); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// UpdateIssueCommentsText updates issue_blobs.comments_text (which triggers FTS5
// update) and stamps issues.synced_at inside one transaction.
func (db *DB) UpdateIssueCommentsText(issueKey string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO issue_blobs (issue_key, comments_text)
		VALUES (?, (SELECT GROUP_CONCAT(body, ' ') FROM comments WHERE issue_key = ?))
		ON CONFLICT(issue_key) DO UPDATE SET comments_text = excluded.comments_text`,
		issueKey, issueKey,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE issues SET synced_at = ? WHERE key = ?`,
		time.Now().UTC().Format("2006-01-02T15:04:05Z"), issueKey,
	); err != nil {
		return err
	}
	return tx.Commit()
}
