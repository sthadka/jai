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
			('description',  'Description',     'description',    'text',     0, 1, 1),
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
