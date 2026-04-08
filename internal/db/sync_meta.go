package db

import (
	"database/sql"
	"time"
)

// SyncMeta holds sync metadata for a project.
type SyncMeta struct {
	Project            string
	LastSyncTime       sql.NullString
	LastFullSync       sql.NullString
	IssuesTotal        sql.NullInt64
	IssuesSynced       sql.NullInt64
	LastSyncDuration   sql.NullFloat64
	LastSyncError      sql.NullString
	LastIssueUpdated   sql.NullString
}

// GetSyncMeta retrieves sync metadata for a project. Returns zero value if not found.
func (db *DB) GetSyncMeta(project string) (*SyncMeta, error) {
	m := &SyncMeta{Project: project}
	err := db.QueryRow(
		`SELECT project, last_sync_time, last_full_sync, issues_total, issues_synced, last_sync_duration, last_sync_error, last_issue_updated
		 FROM sync_metadata WHERE project = ?`,
		project,
	).Scan(&m.Project, &m.LastSyncTime, &m.LastFullSync, &m.IssuesTotal, &m.IssuesSynced, &m.LastSyncDuration, &m.LastSyncError, &m.LastIssueUpdated)
	if err == sql.ErrNoRows {
		return m, nil
	}
	return m, err
}

// UpdateSyncMeta upserts sync metadata for a project.
// hwm is the RFC3339 timestamp of the most recently updated issue seen in this sync run (empty to leave unchanged).
func (db *DB) UpdateSyncMeta(project string, duration float64, total, synced int, syncErr, hwm string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var errVal interface{}
	if syncErr != "" {
		errVal = syncErr
	}
	var hwmVal interface{}
	if hwm != "" {
		hwmVal = hwm
	}
	_, err := db.Exec(`
		INSERT INTO sync_metadata (project, last_sync_time, issues_total, issues_synced, last_sync_duration, last_sync_error, last_issue_updated)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project) DO UPDATE SET
			last_sync_time = excluded.last_sync_time,
			issues_total = excluded.issues_total,
			issues_synced = excluded.issues_synced,
			last_sync_duration = excluded.last_sync_duration,
			last_sync_error = excluded.last_sync_error,
			last_issue_updated = CASE WHEN excluded.last_issue_updated IS NOT NULL THEN excluded.last_issue_updated ELSE last_issue_updated END`,
		project, now, total, synced, duration, errVal, hwmVal,
	)
	return err
}

// UpdateFullSyncMeta records the last full sync time.
func (db *DB) UpdateFullSyncMeta(project string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO sync_metadata (project, last_full_sync)
		VALUES (?, ?)
		ON CONFLICT(project) DO UPDATE SET last_full_sync = excluded.last_full_sync`,
		project, now,
	)
	return err
}

// GetResumeCursor returns the stored resume cursor for a sync source (empty if none).
func (db *DB) GetResumeCursor(source string) (string, error) {
	var cursor sql.NullString
	err := db.QueryRow(`SELECT resume_cursor FROM sync_metadata WHERE project = ?`, source).Scan(&cursor)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cursor.String, nil
}

// SetResumeCursor stores a resume cursor for a sync source.
func (db *DB) SetResumeCursor(source, cursor string) error {
	_, err := db.Exec(`
		INSERT INTO sync_metadata (project, resume_cursor)
		VALUES (?, ?)
		ON CONFLICT(project) DO UPDATE SET resume_cursor = excluded.resume_cursor`,
		source, cursor,
	)
	return err
}

// ClearResumeCursor removes the resume cursor for a sync source.
func (db *DB) ClearResumeCursor(source string) error {
	_, err := db.Exec(`UPDATE sync_metadata SET resume_cursor = NULL WHERE project = ?`, source)
	return err
}

// IssueCountByProject returns a map of Jira project key → issue count.
func (db *DB) IssueCountByProject() (map[string]int, error) {
	rows, err := db.Query(`SELECT project, COUNT(*) FROM issues GROUP BY project`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var proj string
		var n int
		if err := rows.Scan(&proj, &n); err != nil {
			return nil, err
		}
		counts[proj] = n
	}
	return counts, rows.Err()
}

// TotalIssueCount returns the total number of issues in the database.
func (db *DB) TotalIssueCount() (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&n)
	return n, err
}

// AllSyncMeta returns sync metadata for all projects.
func (db *DB) AllSyncMeta() ([]*SyncMeta, error) {
	rows, err := db.Query(
		`SELECT project, last_sync_time, last_full_sync, issues_total, issues_synced, last_sync_duration, last_sync_error, last_issue_updated
		 FROM sync_metadata ORDER BY project`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metas []*SyncMeta
	for rows.Next() {
		m := &SyncMeta{}
		if err := rows.Scan(&m.Project, &m.LastSyncTime, &m.LastFullSync, &m.IssuesTotal, &m.IssuesSynced, &m.LastSyncDuration, &m.LastSyncError, &m.LastIssueUpdated); err != nil {
			return nil, err
		}
		metas = append(metas, m)
	}
	return metas, rows.Err()
}
