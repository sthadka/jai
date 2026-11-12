package db

import "database/sql"

// createSchema creates the initial schema tables.
func createSchema(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_version (
			version     INTEGER PRIMARY KEY,
			applied_at  DATETIME NOT NULL DEFAULT (datetime('now')),
			description TEXT
		)`,

		`CREATE TABLE IF NOT EXISTS issues (
			key             TEXT PRIMARY KEY,
			project         TEXT NOT NULL,
			type            TEXT,
			summary         TEXT NOT NULL,
			description     TEXT,
			status          TEXT,
			status_category TEXT,
			priority        TEXT,
			assignee        TEXT,
			assignee_email  TEXT,
			reporter        TEXT,
			created         DATETIME,
			updated         DATETIME,
			resolved        DATETIME,
			labels          TEXT,
			components      TEXT,
			fix_version     TEXT,
			parent_key      TEXT,
			epic_key        TEXT,
			story_points    REAL,
			comments_text   TEXT,
			raw_json        TEXT,
			synced_at       DATETIME
		)`,

		`CREATE INDEX IF NOT EXISTS idx_issues_project  ON issues(project)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_status   ON issues(status)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_assignee ON issues(assignee)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_updated  ON issues(updated)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_type     ON issues(type)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_priority ON issues(priority)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_parent   ON issues(parent_key)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_epic     ON issues(epic_key)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_synced   ON issues(synced_at)`,

		`CREATE TABLE IF NOT EXISTS comments (
			id          TEXT PRIMARY KEY,
			issue_key   TEXT NOT NULL REFERENCES issues(key) ON DELETE CASCADE,
			author      TEXT,
			author_email TEXT,
			body        TEXT,
			created     DATETIME,
			updated     DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_comments_issue ON comments(issue_key)`,

		`CREATE TABLE IF NOT EXISTS sync_metadata (
			project            TEXT PRIMARY KEY,
			last_sync_time     DATETIME,
			last_full_sync     DATETIME,
			issues_total       INTEGER,
			issues_synced      INTEGER,
			last_sync_duration REAL,
			last_sync_error    TEXT
		)`,

		`CREATE TABLE IF NOT EXISTS field_map (
			jira_id       TEXT PRIMARY KEY,
			jira_name     TEXT,
			name          TEXT NOT NULL UNIQUE,
			type          TEXT NOT NULL,
			is_custom     BOOLEAN NOT NULL DEFAULT 0,
			is_column     BOOLEAN NOT NULL DEFAULT 0,
			user_override BOOLEAN NOT NULL DEFAULT 0,
			searchable    BOOLEAN NOT NULL DEFAULT 1
		)`,

		// Seed standard fields into field_map.
		`INSERT OR IGNORE INTO field_map (jira_id, jira_name, name, type, is_custom, is_column, searchable) VALUES
			('key',          'Key',             'key',            'text',     0, 1, 0),
			('project',      'Project',         'project',        'text',     0, 1, 0),
			('issuetype',    'Issue Type',      'type',           'text',     0, 1, 0),
			('summary',      'Summary',         'summary',        'text',     0, 1, 1),
			('description',  'Description',     'description',    'richtext', 0, 1, 1),
			('status',       'Status',          'status',         'option',   0, 1, 0),
			('priority',     'Priority',        'priority',       'option',   0, 1, 0),
			('assignee',     'Assignee',        'assignee',       'user',     0, 1, 0),
			('reporter',     'Reporter',        'reporter',       'user',     0, 1, 0),
			('created',      'Created',         'created',        'datetime', 0, 1, 0),
			('updated',      'Updated',         'updated',        'datetime', 0, 1, 0),
			('resolutiondate','Resolution Date','resolved',       'datetime', 0, 1, 0),
			('labels',       'Labels',          'labels',         'array',    0, 1, 1),
			('components',   'Components',      'components',     'array',    0, 1, 0),
			('fixVersions',  'Fix Version',     'fix_version',    'array',    0, 1, 0),
			('parent',       'Parent',          'parent_key',     'text',     0, 1, 0),
			('story_points', 'Story Points',    'story_points',   'number',   0, 1, 0)
		`,

		`CREATE TABLE IF NOT EXISTS pending_changes (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_key   TEXT NOT NULL,
			operation   TEXT NOT NULL,
			payload     TEXT NOT NULL,
			created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
			synced_at   DATETIME,
			retry_count INTEGER NOT NULL DEFAULT 0,
			last_error  TEXT
		)`,

		`CREATE VIRTUAL TABLE IF NOT EXISTS issues_fts USING fts5(
			key UNINDEXED,
			summary,
			description,
			comments_text,
			labels,
			content='issues',
			content_rowid='rowid',
			tokenize='porter unicode61'
		)`,

		`CREATE TRIGGER IF NOT EXISTS issues_fts_insert AFTER INSERT ON issues BEGIN
			INSERT INTO issues_fts(rowid, key, summary, description, comments_text, labels)
			VALUES (new.rowid, new.key, new.summary, new.description, new.comments_text, new.labels);
		END`,

		`CREATE TRIGGER IF NOT EXISTS issues_fts_update AFTER UPDATE ON issues BEGIN
			INSERT INTO issues_fts(issues_fts, rowid, key, summary, description, comments_text, labels)
			VALUES ('delete', old.rowid, old.key, old.summary, old.description, old.comments_text, old.labels);
			INSERT INTO issues_fts(rowid, key, summary, description, comments_text, labels)
			VALUES (new.rowid, new.key, new.summary, new.description, new.comments_text, new.labels);
		END`,

		`CREATE TRIGGER IF NOT EXISTS issues_fts_delete AFTER DELETE ON issues BEGIN
			INSERT INTO issues_fts(issues_fts, rowid, key, summary, description, comments_text, labels)
			VALUES ('delete', old.rowid, old.key, old.summary, old.description, old.comments_text, old.labels);
		END`,
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// createMetricsSchema creates the flow-metrics fact store: staged generations with a
// one-row active-generation pointer, status interval facts, and the one-row-per-issue
// flow rollup. Readers always filter on the active generation so an interrupted or
// partial build is never visible; obsolete generations are plain-DELETEd after the
// atomic pointer swap. Open intervals store exited_at = NULL and are aged against an
// explicit query as_of, never a build-frozen now.
func createMetricsSchema(tx *sql.Tx) error {
	stmts := []string{
		// Generation metadata. built_at and source_watermark are recorded separately
		// from any query as_of: built_at is the build watermark, source_watermark is the
		// latest source change the build observed (used to serialize/catch up incremental
		// writers). fingerprint is the normalized Policy fingerprint; a change forces a
		// full restatement.
		`CREATE TABLE IF NOT EXISTS metrics_generation (
			generation       INTEGER PRIMARY KEY AUTOINCREMENT,
			fingerprint      TEXT NOT NULL,
			scope            TEXT,
			built_at         DATETIME NOT NULL,
			source_watermark DATETIME,
			issue_count      INTEGER NOT NULL DEFAULT 0
		)`,

		// One-row pointer to the active generation. Swapped in a single transaction once
		// all facts and rollups for the new generation are written.
		`CREATE TABLE IF NOT EXISTS metrics_active_generation (
			id         INTEGER PRIMARY KEY CHECK (id = 1),
			generation INTEGER REFERENCES metrics_generation(generation)
		)`,
		`INSERT OR IGNORE INTO metrics_active_generation (id, generation) VALUES (1, NULL)`,

		// Status-value-over-time intervals (Phase 1: field = 'status' only). value_id is
		// the stable Jira status id, value the display name; stage/provenance come from the
		// versioned config mapping. seq orders intervals deterministically within an issue.
		`CREATE TABLE IF NOT EXISTS fact_field_interval (
			generation  INTEGER NOT NULL,
			issue_key   TEXT NOT NULL,
			project     TEXT NOT NULL,
			field       TEXT NOT NULL,
			seq         INTEGER NOT NULL,
			value_id    TEXT,
			value       TEXT,
			stage       TEXT,
			provenance  TEXT,
			entered_at  DATETIME NOT NULL,
			exited_at   DATETIME,
			is_open     INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (generation, issue_key, field, seq)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ffi_gen_issue       ON fact_field_interval(generation, issue_key)`,
		`CREATE INDEX IF NOT EXISTS idx_ffi_gen_field_stage ON fact_field_interval(generation, field, stage)`,

		// One row per issue: the Phase-1 snapshot flow rollup. Carries the current
		// completion only (resolved/resolution); repeated completions of reopened issues
		// are the Phase-2 event model's job. lead/cycle seconds are NULL when inputs are
		// missing or timestamps are reversed.
		`CREATE TABLE IF NOT EXISTS fact_issue_flow (
			generation      INTEGER NOT NULL,
			issue_key       TEXT NOT NULL,
			project         TEXT NOT NULL,
			type            TEXT,
			created         DATETIME,
			first_active_at DATETIME,
			resolved        DATETIME,
			resolution      TEXT,
			lead_seconds    INTEGER,
			cycle_seconds   INTEGER,
			reopen_count    INTEGER NOT NULL DEFAULT 0,
			current_stage   TEXT,
			has_active      INTEGER NOT NULL DEFAULT 0,
			has_unknown     INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (generation, issue_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_fif_gen_project  ON fact_issue_flow(generation, project)`,
		`CREATE INDEX IF NOT EXISTS idx_fif_gen_resolved ON fact_issue_flow(generation, resolved)`,
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// createMembershipSchema creates fact_issue_membership: event-time intervals for the
// set-valued dimensions (components, labels, Sprint) that fact_field_interval cannot model.
// It mirrors fact_field_interval's generation handling exactly — rows tagged with the
// staged generation, readers filter on the active generation, obsolete generations are
// plain-DELETEd after the atomic pointer swap, and open intervals store exited_at = NULL
// aged against an explicit query as_of. One row per (issue, member, interval): a
// GROUP BY member_name over a temporal join never double-counts a single issue's measure.
func createMembershipSchema(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS fact_issue_membership (
			generation  INTEGER NOT NULL,
			issue_key   TEXT NOT NULL,
			project     TEXT NOT NULL,
			dimension   TEXT NOT NULL,
			seq         INTEGER NOT NULL,
			member_id   TEXT,
			member_name TEXT,
			entered_at  DATETIME NOT NULL,
			exited_at   DATETIME,
			is_open     INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (generation, issue_key, dimension, seq)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_fim_gen_issue      ON fact_issue_membership(generation, issue_key)`,
		`CREATE INDEX IF NOT EXISTS idx_fim_gen_dim_member ON fact_issue_membership(generation, dimension, member_name)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
