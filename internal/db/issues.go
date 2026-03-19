package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Issue represents a row from the issues table.
type Issue struct {
	Key              string
	Project          string
	Type             string
	Summary          string
	Description      string
	Status           string
	StatusCategory   string
	Priority         string
	Assignee         string
	AssigneeEmail    string
	Reporter         string
	Created          string
	Updated          string
	Resolved         string
	Labels           string
	Components       string
	FixVersion       string
	ParentKey        string
	EpicKey          string
	StoryPoints      sql.NullFloat64
	CommentsText     string
	RawJSON          string
	SyncedAt         string
	Resolution       string
	DueDate          string
	OriginalEstimate sql.NullInt64
	TimeSpent        sql.NullInt64
	RemainingEstimate sql.NullInt64
	SubtaskKeys      string
}

// IssueLink represents a row from the issue_links table.
type IssueLink struct {
	ID        string
	IssueKey  string
	LinkType  string
	Direction string // "inward" or "outward"
	LinkedKey string
}

// UpsertIssue inserts or replaces an issue row. Extra contains additional dynamic columns.
func (db *DB) UpsertIssue(issue *Issue, extra map[string]interface{}) error {
	cols := []string{
		"key", "project", "type", "summary", "description",
		"status", "status_category", "priority",
		"assignee", "assignee_email", "reporter",
		"created", "updated", "resolved",
		"labels", "components", "fix_version",
		"parent_key", "epic_key", "story_points",
		"comments_text", "raw_json", "synced_at",
		"resolution", "due_date",
		"original_estimate", "time_spent", "remaining_estimate",
		"subtask_keys",
	}
	vals := []interface{}{
		issue.Key, issue.Project, issue.Type, issue.Summary, issue.Description,
		issue.Status, issue.StatusCategory, issue.Priority,
		issue.Assignee, issue.AssigneeEmail, issue.Reporter,
		issue.Created, issue.Updated, issue.Resolved,
		issue.Labels, issue.Components, issue.FixVersion,
		issue.ParentKey, issue.EpicKey, issue.StoryPoints,
		issue.CommentsText, issue.RawJSON,
		time.Now().UTC().Format(time.RFC3339),
		issue.Resolution, issue.DueDate,
		issue.OriginalEstimate, issue.TimeSpent, issue.RemainingEstimate,
		issue.SubtaskKeys,
	}

	for k, v := range extra {
		cols = append(cols, k)
		vals = append(vals, v)
	}

	placeholders := make([]string, len(cols))
	for i := range cols {
		placeholders[i] = "?"
	}

	query := fmt.Sprintf(
		"INSERT OR REPLACE INTO issues (%s) VALUES (%s)",
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
	)

	_, err := db.Exec(query, vals...)
	return err
}

// GetIssue retrieves a single issue by key.
func (db *DB) GetIssue(key string) (map[string]interface{}, error) {
	rows, err := db.Query("SELECT * FROM issues WHERE key = ?", key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	if !rows.Next() {
		return nil, nil
	}

	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}

	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}

	result := make(map[string]interface{}, len(cols))
	for i, col := range cols {
		result[col] = vals[i]
	}
	return result, nil
}

// UpsertIssueLinks replaces all link rows for an issue.
func (db *DB) UpsertIssueLinks(issueKey string, links []IssueLink) error {
	if _, err := db.Exec(`DELETE FROM issue_links WHERE issue_key = ?`, issueKey); err != nil {
		return err
	}
	for _, l := range links {
		if _, err := db.Exec(
			`INSERT OR REPLACE INTO issue_links (id, issue_key, link_type, direction, linked_key) VALUES (?,?,?,?,?)`,
			l.ID, l.IssueKey, l.LinkType, l.Direction, l.LinkedKey,
		); err != nil {
			return err
		}
	}
	return nil
}

// EnsureColumn adds a column to the issues table if it doesn't exist.
func (db *DB) EnsureColumn(name, colType string) error {
	// Check if column already exists.
	rows, err := db.Query("PRAGMA table_info(issues)")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid     int
			colName string
			colT    string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &colName, &colT, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if colName == name {
			return nil // already exists
		}
	}

	_, err = db.Exec(fmt.Sprintf(`ALTER TABLE issues ADD COLUMN "%s" %s`, name, colType))
	return err
}
