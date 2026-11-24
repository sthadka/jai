package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
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

// IssueUpsert pairs an issue row with its dynamic extra columns for a batched,
// transactional upsert via UpsertIssuesTx.
type IssueUpsert struct {
	Issue *Issue
	Extra map[string]interface{}
}

// IssueBundle pairs an issue upsert with its child rows (comments, links,
// attachments) so a full Jira sync page can commit atomically in one tx.
type IssueBundle struct {
	Issue              *Issue
	Extra              map[string]interface{}
	Comments           []*Comment
	UpdateCommentsText bool
	Links              []IssueLink
	UpdateLinks        bool
	Attachments        []*Attachment
	UpdateAttachments  bool
}

// buildUpsertIssue returns the INSERT ... ON CONFLICT statement and bound args
// that upsert one issue row. It is shared by UpsertIssue (single autocommit)
// and UpsertIssuesTx (many rows in one transaction) so both paths stay in sync.
//
// It upserts on the stable numeric id (ON CONFLICT(id)) rather than the mutable
// key: when an issue moves project or a project is renamed its key changes, so
// matching on key would insert a duplicate and orphan the old row's relations.
// Matching on id updates the existing row in place and lets the key-rename
// trigger cascade the new key to child tables and record a historical alias.
// A second ON CONFLICT(key) arm keeps id-less rows (no Jira id yet) idempotent.
// INSERT ... ON CONFLICT (not INSERT OR REPLACE) preserves columns maintained
// outside this function — notably changelog_synced_at — that REPLACE would wipe.
func buildUpsertIssue(issue *Issue, extra map[string]interface{}) (string, []interface{}) {
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
	var idVal any = issue.ID
	if issue.ID == "" {
		idVal = nil
	}
	vals := []interface{}{
		idVal, issue.Key, issue.Project, issue.Type, issue.Summary, issue.Description,
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

	if len(extra) > 0 {
		extraKeys := make([]string, 0, len(extra))
		for k := range extra {
			extraKeys = append(extraKeys, k)
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			cols = append(cols, k)
			vals = append(vals, extra[k])
		}
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
			"ON CONFLICT(id) DO UPDATE SET %s "+
			"ON CONFLICT(key) DO UPDATE SET %s",
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
		strings.Join(idUpdates, ", "),
		strings.Join(keyUpdates, ", "),
	)
	return query, vals
}

// UpsertIssue inserts a new issue row or updates an existing one. Extra contains
// additional dynamic columns.
func (db *DB) UpsertIssue(issue *Issue, extra map[string]interface{}) error {
	query, vals := buildUpsertIssue(issue, extra)
	_, err := db.Exec(query, vals...)
	return err
}

// UpsertIssuesTx upserts every issue in a single transaction: either all rows
// land or none do. The rank/field reconcile pass uses it so an interrupted or
// failed refresh never exposes a half-updated ordering — on any error the
// transaction rolls back and the prior (last-known-good) snapshot stays intact.
// An empty slice is a no-op.
func (db *DB) UpsertIssuesTx(items []*IssueUpsert) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin issue upsert tx: %w", err)
	}
	stmts := make(map[string]*sql.Stmt, 4)
	closeStmts := func() {
		for _, s := range stmts {
			_ = s.Close()
		}
	}
	for _, it := range items {
		query, vals := buildUpsertIssue(it.Issue, it.Extra)
		stmt, ok := stmts[query]
		if !ok {
			stmt, err = tx.Prepare(query)
			if err != nil {
				closeStmts()
				_ = tx.Rollback()
				return fmt.Errorf("preparing upsert for issue %s in tx: %w", it.Issue.Key, err)
			}
			stmts[query] = stmt
		}
		if _, err := stmt.Exec(vals...); err != nil {
			closeStmts()
			_ = tx.Rollback()
			return fmt.Errorf("upserting issue %s in tx: %w", it.Issue.Key, err)
		}
	}
	closeStmts()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit issue upsert tx: %w", err)
	}
	return nil
}

// UpsertIssueBundleTx commits an entire batch of issues and their child rows
// (comments, comments_text, issue_links, attachments) in a single transaction,
// reusing prepared statements across all bundles.
func (db *DB) UpsertIssueBundleTx(bundles []*IssueBundle) error {
	if len(bundles) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin issue bundle tx: %w", err)
	}

	issueStmts := make(map[string]*sql.Stmt, 4)
	var commentStmt, commentsTextStmt, delLinksStmt, insLinkStmt, delAttStmt, insAttStmt, updAttCountStmt *sql.Stmt
	closeAll := func() {
		for _, s := range issueStmts {
			_ = s.Close()
		}
		for _, s := range []*sql.Stmt{commentStmt, commentsTextStmt, delLinksStmt, insLinkStmt, delAttStmt, insAttStmt, updAttCountStmt} {
			if s != nil {
				_ = s.Close()
			}
		}
	}
	fail := func(err error) error {
		closeAll()
		_ = tx.Rollback()
		return err
	}

	nowComments := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	nowRFC3339 := time.Now().UTC().Format(time.RFC3339)

	for _, b := range bundles {
		if b == nil || b.Issue == nil {
			continue
		}
		query, vals := buildUpsertIssue(b.Issue, b.Extra)
		stmt, ok := issueStmts[query]
		if !ok {
			stmt, err = tx.Prepare(query)
			if err != nil {
				return fail(fmt.Errorf("preparing upsert for issue %s: %w", b.Issue.Key, err))
			}
			issueStmts[query] = stmt
		}
		if _, err := stmt.Exec(vals...); err != nil {
			return fail(fmt.Errorf("upserting issue %s: %w", b.Issue.Key, err))
		}

		key := b.Issue.Key
		if len(b.Comments) > 0 {
			if commentStmt == nil {
				commentStmt, err = tx.Prepare(
					`INSERT OR REPLACE INTO comments (id, issue_key, author, author_email, body, created, updated)
					 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				)
				if err != nil {
					return fail(err)
				}
			}
			for _, c := range b.Comments {
				if _, err := commentStmt.Exec(c.ID, c.IssueKey, c.Author, c.AuthorEmail, c.Body, c.Created, c.Updated); err != nil {
					return fail(fmt.Errorf("upserting comment %s for %s: %w", c.ID, key, err))
				}
			}
		}
		if b.UpdateCommentsText {
			if commentsTextStmt == nil {
				commentsTextStmt, err = tx.Prepare(`
					UPDATE issues SET comments_text = (
						SELECT GROUP_CONCAT(body, ' ') FROM comments WHERE issue_key = ?
					), synced_at = ?
					WHERE key = ?`)
				if err != nil {
					return fail(err)
				}
			}
			if _, err := commentsTextStmt.Exec(key, nowComments, key); err != nil {
				return fail(fmt.Errorf("updating comments_text for %s: %w", key, err))
			}
		}
		if b.UpdateLinks {
			if delLinksStmt == nil {
				delLinksStmt, err = tx.Prepare(`DELETE FROM issue_links WHERE issue_key = ?`)
				if err != nil {
					return fail(err)
				}
			}
			if _, err := delLinksStmt.Exec(key); err != nil {
				return fail(fmt.Errorf("deleting links for %s: %w", key, err))
			}
			if len(b.Links) > 0 {
				if insLinkStmt == nil {
					insLinkStmt, err = tx.Prepare(
						`INSERT OR REPLACE INTO issue_links (id, issue_key, link_type, direction, linked_key, linked_summary, linked_status, linked_project) VALUES (?,?,?,?,?,?,?,?)`,
					)
					if err != nil {
						return fail(err)
					}
				}
				for _, l := range b.Links {
					if _, err := insLinkStmt.Exec(l.ID, l.IssueKey, l.LinkType, l.Direction, l.LinkedKey, l.LinkedSummary, l.LinkedStatus, l.LinkedProject); err != nil {
						return fail(fmt.Errorf("inserting link for %s: %w", key, err))
					}
				}
			}
		}
		if b.UpdateAttachments {
			if delAttStmt == nil {
				delAttStmt, err = tx.Prepare(`DELETE FROM attachments WHERE issue_key = ?`)
				if err != nil {
					return fail(err)
				}
			}
			if _, err := delAttStmt.Exec(key); err != nil {
				return fail(fmt.Errorf("deleting attachments for %s: %w", key, err))
			}
			if len(b.Attachments) > 0 {
				if insAttStmt == nil {
					insAttStmt, err = tx.Prepare(
						`INSERT INTO attachments (id, issue_key, filename, size, mime_type, author, created, url, synced_at)
						 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					)
					if err != nil {
						return fail(err)
					}
				}
				for _, a := range b.Attachments {
					if _, err := insAttStmt.Exec(a.ID, a.IssueKey, a.Filename, a.Size, a.MimeType, a.Author, a.Created, a.URL, nowRFC3339); err != nil {
						return fail(fmt.Errorf("inserting attachment %d for %s: %w", a.ID, key, err))
					}
				}
			}
			if updAttCountStmt == nil {
				updAttCountStmt, err = tx.Prepare(`UPDATE issues SET attachment_count = ? WHERE key = ?`)
				if err != nil {
					return fail(err)
				}
			}
			if _, err := updAttCountStmt.Exec(len(b.Attachments), key); err != nil {
				return fail(fmt.Errorf("updating attachment_count for %s: %w", key, err))
			}
		}
	}

	closeAll()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit issue bundle tx: %w", err)
	}
	return nil
}
// GetIssueUpdated returns the stored "updated" timestamp for an issue, or "" if not found.
func (db *DB) GetIssueUpdated(key string) (string, error) {
	var updated sql.NullString
	err := db.ReadDB().QueryRow("SELECT updated FROM issues WHERE key = ?", key).Scan(&updated)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return updated.String, nil
}

// IssueHydrationState reports row-level sync timestamps for an issue so callers
// can decide whether details (worklogs/attachments/full custom fields) or
// complete unfiltered changelog history need lazy hydration.
type IssueHydrationState struct {
	Updated               string
	SyncedAt              string
	DetailsSyncedAt       string
	ChangelogSyncedAt     string
	ChangelogFullSyncedAt string
}

// GetIssueHydrationState returns the stored hydration timestamps for an issue
// key (resolving historical aliases if needed), or nil if no row exists.
func (db *DB) GetIssueHydrationState(key string) (*IssueHydrationState, error) {
	query := func(k string) (*IssueHydrationState, error) {
		var st IssueHydrationState
		var updated, synced, details, cl, clFull sql.NullString
		err := db.ReadDB().QueryRow(
			`SELECT updated, synced_at, details_synced_at, changelog_synced_at, changelog_full_synced_at
			 FROM issues WHERE key = ?`, k,
		).Scan(&updated, &synced, &details, &cl, &clFull)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		st.Updated = updated.String
		st.SyncedAt = synced.String
		st.DetailsSyncedAt = details.String
		st.ChangelogSyncedAt = cl.String
		st.ChangelogFullSyncedAt = clFull.String
		return &st, nil
	}

	st, err := query(key)
	if err != nil || st != nil {
		return st, err
	}
	if resolved := db.resolveKey(key); resolved != key {
		return query(resolved)
	}
	return nil, nil
}

// GetIssuesUpdatedByID returns a map of Jira numeric issue id → stored updated
// timestamp for the given IDs, chunked to stay under SQLite's bind-variable
// limit. Missing IDs or rows with empty/NULL updated are omitted.
func (db *DB) GetIssuesUpdatedByID(ids []string) (map[string]string, error) {
	return db.getIssuesUpdatedByColumn("id", ids)
}

// GetIssuesUpdatedByKey returns a map of issue key → stored updated timestamp
// for the given keys, chunked to stay under SQLite's bind-variable limit.
// Missing keys or rows with empty/NULL updated are omitted.
func (db *DB) GetIssuesUpdatedByKey(keys []string) (map[string]string, error) {
	return db.getIssuesUpdatedByColumn("key", keys)
}

func (db *DB) getIssuesUpdatedByColumn(col string, vals []string) (map[string]string, error) {
	result := make(map[string]string, len(vals))
	if len(vals) == 0 {
		return result, nil
	}
	const chunkSize = 500
	for i := 0; i < len(vals); i += chunkSize {
		end := i + chunkSize
		if end > len(vals) {
			end = len(vals)
		}
		chunk := vals[i:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for j, v := range chunk {
			placeholders[j] = "?"
			args[j] = v
		}
		q := fmt.Sprintf("SELECT %s, updated FROM issues WHERE %s IN (%s)", col, col, strings.Join(placeholders, ", "))
		rows, err := db.ReadDB().Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			var updated sql.NullString
			if err := rows.Scan(&k, &updated); err != nil {
				rows.Close()
				return nil, err
			}
			if updated.Valid && updated.String != "" {
				result[k] = updated.String
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

// MarkIssueDetailsSynced stamps details_synced_at for the given issue keys in
// a single transaction.
func (db *DB) MarkIssueDetailsSynced(keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
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
		_, err := tx.Exec(
			fmt.Sprintf(`UPDATE issues SET details_synced_at = ? WHERE key IN (%s)`, strings.Join(placeholders, ", ")),
			args...,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetIssuesRawJSON returns a map of issue key → stored raw_json for the given
// keys. Keys with no stored row (or NULL raw_json) are omitted. Chunked to stay
// safely under SQLite's bind-variable limit (important for maxResults=5000 scans).
func (db *DB) GetIssuesRawJSON(keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return result, nil
	}
	const chunkSize = 500
	for i := 0; i < len(keys); i += chunkSize {
		end := i + chunkSize
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[i:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for j, k := range chunk {
			placeholders[j] = "?"
			args[j] = k
		}
		q := "SELECT key, raw_json FROM issues WHERE key IN (" + strings.Join(placeholders, ", ") + ")"
		rows, err := db.ReadDB().Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var raw sql.NullString
			if err := rows.Scan(&key, &raw); err != nil {
				rows.Close()
				return nil, err
			}
			if raw.Valid {
				result[key] = raw.String
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
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
	rows, err := db.ReadDB().Query(`SELECT jira_id, name FROM field_map WHERE jira_id IN (`+strings.Join(ph, ",")+`)`, args...)
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
	err := db.ReadDB().QueryRow(
		`SELECT i.key FROM issue_key_alias a JOIN issues i ON i.id = a.id WHERE a.old_key = ?`,
		key,
	).Scan(&current)
	if err == nil && current != "" {
		return current
	}
	return key
}

func (db *DB) getIssueByKey(key string) (map[string]interface{}, error) {
	rows, err := db.ReadDB().Query("SELECT * FROM issues WHERE key = ?", key)
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

// summaryExcludedCols lists heavy overflow-page text columns omitted from
// GetIssueSummary and GetIssuesSummaryByIDs.
var summaryExcludedCols = map[string]bool{
	"raw_json":      true,
	"description":   true,
	"environment":   true,
	"comments_text": true,
}

func (db *DB) summaryColumns() ([]string, error) {
	rows, err := db.ReadDB().Query(`PRAGMA table_info(issues)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var (
			cid     int
			name    string
			colType string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		if summaryExcludedCols[name] {
			continue
		}
		cols = append(cols, `"`+name+`"`)
	}
	return cols, rows.Err()
}

// GetIssueSummary retrieves a single issue by key (resolving aliases) without
// reading heavy blob/text columns (raw_json, description, environment, comments_text).
func (db *DB) GetIssueSummary(key string) (map[string]interface{}, error) {
	selCols, err := db.summaryColumns()
	if err != nil {
		return nil, err
	}
	fetch := func(k string) (map[string]interface{}, error) {
		rows, err := db.ReadDB().Query("SELECT "+strings.Join(selCols, ", ")+" FROM issues WHERE key = ?", k)
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
		res := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			res[c] = vals[i]
		}
		return res, nil
	}
	res, err := fetch(key)
	if err != nil {
		return nil, err
	}
	if res == nil {
		if resolved := db.resolveKey(key); resolved != key {
			if res, err = fetch(resolved); err != nil {
				return nil, err
			}
		}
	}
	if res == nil {
		return nil, nil
	}
	if err := db.mergeCustomFields(res); err != nil {
		return nil, err
	}
	return res, nil
}

// GetIssuesSummaryByIDs retrieves issue summaries keyed by Jira numeric id,
// excluding heavy blob/text columns and chunking to stay under SQLite bind limits.
func (db *DB) GetIssuesSummaryByIDs(ids []string) (map[string]map[string]interface{}, error) {
	out := make(map[string]map[string]interface{}, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	selCols, err := db.summaryColumns()
	if err != nil {
		return nil, err
	}
	selectList := strings.Join(selCols, ", ")
	const chunkSize = 500
	for i := 0; i < len(ids); i += chunkSize {
		end := i + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[i:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for j, id := range chunk {
			placeholders[j] = "?"
			args[j] = id
		}
		rows, err := db.ReadDB().Query("SELECT "+selectList+" FROM issues WHERE id IN ("+strings.Join(placeholders, ", ")+")", args...)
		if err != nil {
			return nil, err
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		for rows.Next() {
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for j := range vals {
				ptrs[j] = &vals[j]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return nil, err
			}
			res := make(map[string]interface{}, len(cols))
			for j, c := range cols {
				res[c] = vals[j]
			}
			if err := db.mergeCustomFields(res); err != nil {
				rows.Close()
				return nil, err
			}
			if idStr, ok := res["id"].(string); ok && idStr != "" {
				out[idStr] = res
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// UpsertIssueLinks replaces all link rows for an issue atomically in a transaction.
func (db *DB) UpsertIssueLinks(issueKey string, links []IssueLink) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := UpsertIssueLinksTx(tx, issueKey, links); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertIssueLinksTx replaces all link rows for an issue within an existing transaction.
func UpsertIssueLinksTx(tx *sql.Tx, issueKey string, links []IssueLink) error {
	if _, err := tx.Exec(`DELETE FROM issue_links WHERE issue_key = ?`, issueKey); err != nil {
		return err
	}
	if len(links) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO issue_links (id, issue_key, link_type, direction, linked_key, linked_summary, linked_status, linked_project) VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, l := range links {
		if _, err := stmt.Exec(
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
