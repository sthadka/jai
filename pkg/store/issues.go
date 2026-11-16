package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Issue represents a row from the issues table.
type Issue struct {
	ID                string
	Key               string
	Project           string
	Type              string
	Summary           string
	Description       string
	Status            string
	StatusCategory    string
	Priority          string
	Assignee          string
	AssigneeEmail     string
	Reporter          string
	Created           string
	Updated           string
	Resolved          string
	Labels            string
	Components        string
	FixVersion        string
	ParentKey         string
	EpicKey           string
	StoryPoints       sql.NullFloat64
	CommentsText      string
	RawJSON           string
	SyncedAt          string
	Resolution        string
	DueDate           string
	OriginalEstimate  sql.NullInt64
	TimeSpent         sql.NullInt64
	RemainingEstimate sql.NullInt64
	SubtaskKeys       string
	SprintID          sql.NullInt64
	SprintName        string
}

// IssueLink represents a row from the issue_links table.
type IssueLink struct {
	ID            string
	IssueKey      string
	LinkType      string
	Direction     string // "inward" or "outward"
	LinkedKey     string
	LinkedSummary string
	LinkedStatus  string
	LinkedProject string
}

// UpsertIssue inserts a new issue row or updates an existing one. Extra contains
// additional dynamic columns.
//
// It upserts on the stable numeric id (ON CONFLICT(id)) rather than the mutable
// key: when an issue moves project or a project is renamed its key changes, so
// matching on key would insert a duplicate and orphan the old row's relations.
// Matching on id updates the existing row in place and lets the key-rename
// trigger cascade the new key to child tables and record a historical alias.
// A second ON CONFLICT(key) arm keeps id-less rows (no Jira id yet) idempotent.
// INSERT ... ON CONFLICT (not INSERT OR REPLACE) preserves columns maintained
// outside this function — notably changelog_synced_at — that REPLACE would wipe.
func (db *DB) UpsertIssue(issue *Issue, extra map[string]interface{}) error {
	cols := []string{
		"id", "key", "project", "type", "summary", "description",
		"status", "status_category", "priority",
		"assignee", "assignee_email", "reporter",
		"created", "updated", "resolved",
		"labels", "components", "fix_version",
		"parent_key", "epic_key", "story_points",
		"comments_text", "raw_json", "synced_at",
		"resolution", "due_date",
		"original_estimate", "time_spent", "remaining_estimate",
		"subtask_keys", "sprint_id", "sprint_name",
	}
	vals := []interface{}{
		issue.ID, issue.Key, issue.Project, issue.Type, issue.Summary, issue.Description,
		issue.Status, issue.StatusCategory, issue.Priority,
		issue.Assignee, issue.AssigneeEmail, issue.Reporter,
		issue.Created, issue.Updated, issue.Resolved,
		issue.Labels, issue.Components, issue.FixVersion,
		issue.ParentKey, issue.EpicKey, issue.StoryPoints,
		issue.CommentsText, issue.RawJSON,
		time.Now().UTC().Format(time.RFC3339),
		issue.Resolution, issue.DueDate,
		issue.OriginalEstimate, issue.TimeSpent, issue.RemainingEstimate,
		issue.SubtaskKeys, issue.SprintID, issue.SprintName,
	}

	for k, v := range extra {
		cols = append(cols, k)
		vals = append(vals, v)
	}

	placeholders := make([]string, len(cols))
	// Two SET lists: the id-conflict arm overwrites every column except id (so a
	// moved issue's new key lands on the existing row); the key-conflict arm
	// overwrites every column except key (idempotent re-sync of an id-less row).
	idUpdates := make([]string, 0, len(cols))
	keyUpdates := make([]string, 0, len(cols))
	for i, c := range cols {
		placeholders[i] = "?"
		if c != "id" {
			idUpdates = append(idUpdates, fmt.Sprintf("%s = excluded.%s", c, c))
		}
		if c != "key" {
			keyUpdates = append(keyUpdates, fmt.Sprintf("%s = excluded.%s", c, c))
		}
	}

	query := fmt.Sprintf(
		"INSERT INTO issues (%s) VALUES (%s) "+
			"ON CONFLICT(id) WHERE id <> '' DO UPDATE SET %s "+
			"ON CONFLICT(key) DO UPDATE SET %s",
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
		strings.Join(idUpdates, ", "),
		strings.Join(keyUpdates, ", "),
	)

	_, err := db.Exec(query, vals...)
	return err
}

// GetIssueUpdated returns the stored "updated" timestamp for an issue, or "" if not found.
func (db *DB) GetIssueUpdated(key string) (string, error) {
	var updated sql.NullString
	err := db.QueryRow("SELECT updated FROM issues WHERE key = ?", key).Scan(&updated)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return updated.String, nil
}

// GetIssuesRawJSON returns a map of issue key → stored raw_json for the given
// keys. Keys with no stored row (or NULL raw_json) are omitted. Used by the
// reconcile pass to diff live field values against what is stored locally.
func (db *DB) GetIssuesRawJSON(keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		args[i] = k
	}
	q := "SELECT key, raw_json FROM issues WHERE key IN (" + strings.Join(placeholders, ", ") + ")"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw sql.NullString
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		if raw.Valid {
			result[key] = raw.String
		}
	}
	return result, rows.Err()
}

// GetIssue retrieves a single issue by key. If the key is not a live key it is
// resolved through the issue_key_alias table, so historical keys (from moves or
// project renames) and old URLs still return the current issue.
func (db *DB) GetIssue(key string) (map[string]interface{}, error) {
	result, err := db.getIssueByKey(key)
	if err != nil {
		return nil, err
	}
	if result == nil {
		if resolved := db.resolveKey(key); resolved != key {
			if result, err = db.getIssueByKey(resolved); err != nil {
				return nil, err
			}
		}
	}
	if result == nil {
		return nil, nil
	}
	if err := db.mergeCustomFields(result); err != nil {
		return nil, err
	}
	return result, nil
}

// mergeCustomFields expands the sparse custom_fields JSON (keyed by Jira field id)
// into name-keyed entries on the result, so callers read a custom field the same
// way whether it lives in a typed column or JSON. The raw custom_fields entry is
// dropped from the returned map.
func (db *DB) mergeCustomFields(result map[string]interface{}) error {
	raw, ok := result["custom_fields"]
	delete(result, "custom_fields")
	if !ok || raw == nil {
		return nil
	}
	var text string
	switch v := raw.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return nil
	}
	if text == "" {
		return nil
	}
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(text), &fields); err != nil {
		return nil // tolerate malformed JSON rather than failing the read
	}
	if len(fields) == 0 {
		return nil
	}
	ids := make([]string, 0, len(fields))
	for id := range fields {
		ids = append(ids, id)
	}
	names, err := db.fieldNamesByJiraID(ids)
	if err != nil {
		return err
	}
	for id, val := range fields {
		name := names[id]
		if name == "" {
			name = id
		}
		if _, exists := result[name]; !exists {
			result[name] = val
		}
	}
	return nil
}

// fieldNamesByJiraID returns jira_id → field name for the given ids.
func (db *DB) fieldNamesByJiraID(ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	rows, err := db.Query(`SELECT jira_id, name FROM field_map WHERE jira_id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]string, len(ids))
	for rows.Next() {
		var jid, name string
		if err := rows.Scan(&jid, &name); err != nil {
			return nil, err
		}
		m[jid] = name
	}
	return m, rows.Err()
}

// resolveKey maps a possibly-historical issue key to the current live key via the
// issue_key_alias table, returning the input unchanged when it is already live or
// has no alias.
func (db *DB) resolveKey(key string) string {
	var current string
	err := db.QueryRow(
		`SELECT i.key FROM issue_key_alias a JOIN issues i ON i.id = a.id WHERE a.old_key = ?`,
		key,
	).Scan(&current)
	if err == nil && current != "" {
		return current
	}
	return key
}

func (db *DB) getIssueByKey(key string) (map[string]interface{}, error) {
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
			`INSERT OR REPLACE INTO issue_links (id, issue_key, link_type, direction, linked_key, linked_summary, linked_status, linked_project) VALUES (?,?,?,?,?,?,?,?)`,
			l.ID, l.IssueKey, l.LinkType, l.Direction, l.LinkedKey, l.LinkedSummary, l.LinkedStatus, l.LinkedProject,
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
