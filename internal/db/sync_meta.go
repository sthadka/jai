package db

import (
	"database/sql"
	"time"
)

// SyncMeta holds sync metadata for a project.
type SyncMeta struct {
	Project           string
	LastSyncTime      sql.NullString
	LastFullSync      sql.NullString
	IssuesTotal       sql.NullInt64
	IssuesSynced      sql.NullInt64
	LastSyncDuration  sql.NullFloat64
	LastSyncError     sql.NullString
}

// GetSyncMeta retrieves sync metadata for a project. Returns zero value if not found.
func (db *DB) GetSyncMeta(project string) (*SyncMeta, error) {
	m := &SyncMeta{Project: project}
	err := db.QueryRow(
		`SELECT project, last_sync_time, last_full_sync, issues_total, issues_synced, last_sync_duration, last_sync_error
		 FROM sync_metadata WHERE project = ?`,
		project,
	).Scan(&m.Project, &m.LastSyncTime, &m.LastFullSync, &m.IssuesTotal, &m.IssuesSynced, &m.LastSyncDuration, &m.LastSyncError)
	if err == sql.ErrNoRows {
		return m, nil
	}
	return m, err
}

// UpdateSyncMeta upserts sync metadata for a project.
func (db *DB) UpdateSyncMeta(project string, duration float64, total, synced int, syncErr string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var errVal interface{}
	if syncErr != "" {
		errVal = syncErr
	}
	_, err := db.Exec(`
		INSERT INTO sync_metadata (project, last_sync_time, issues_total, issues_synced, last_sync_duration, last_sync_error)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project) DO UPDATE SET
			last_sync_time = excluded.last_sync_time,
			issues_total = excluded.issues_total,
			issues_synced = excluded.issues_synced,
			last_sync_duration = excluded.last_sync_duration,
			last_sync_error = excluded.last_sync_error`,
		project, now, total, synced, duration, errVal,
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

// AllSyncMeta returns sync metadata for all projects.
func (db *DB) AllSyncMeta() ([]*SyncMeta, error) {
	rows, err := db.Query(
		`SELECT project, last_sync_time, last_full_sync, issues_total, issues_synced, last_sync_duration, last_sync_error
		 FROM sync_metadata ORDER BY project`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metas []*SyncMeta
	for rows.Next() {
		m := &SyncMeta{}
		if err := rows.Scan(&m.Project, &m.LastSyncTime, &m.LastFullSync, &m.IssuesTotal, &m.IssuesSynced, &m.LastSyncDuration, &m.LastSyncError); err != nil {
			return nil, err
		}
		metas = append(metas, m)
	}
	return metas, rows.Err()
}
