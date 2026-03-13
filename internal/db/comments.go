package db

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
	rows, err := db.Query(
		`SELECT id, issue_key, author, author_email, body, created, updated
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

// UpdateIssueCommentsText updates the comments_text column for FTS.
func (db *DB) UpdateIssueCommentsText(issueKey string) error {
	_, err := db.Exec(`
		UPDATE issues SET comments_text = (
			SELECT GROUP_CONCAT(body, ' ') FROM comments WHERE issue_key = ?
		), synced_at = ?
		WHERE key = ?`,
		issueKey, time.Now().UTC().Format("2006-01-02T15:04:05Z"), issueKey,
	)
	return err
}
