package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type migration struct {
	version     int
	description string
	up          func(*sql.Tx) error
}

var migrations = []migration{
	{
		version:     1,
		description: "initial schema",
		up:          createSchema,
	},
	{
		version:     2,
		description: "add projects table",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS projects (
				key        TEXT PRIMARY KEY,
				name       TEXT NOT NULL,
				synced_at  DATETIME NOT NULL DEFAULT (datetime('now'))
			)`)
			return err
		},
	},
	{
		version:     3,
		description: "add resume_cursor to sync_metadata",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE sync_metadata ADD COLUMN resume_cursor TEXT`)
			return err
		},
	},
	{
		version:     4,
		description: "add issue_links table and new issue columns (resolution, due_date, time tracking, subtask_keys)",
		up: func(tx *sql.Tx) error {
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS issue_links (
					id          TEXT PRIMARY KEY,
					issue_key   TEXT NOT NULL,
					link_type   TEXT NOT NULL,
					direction   TEXT NOT NULL,
					linked_key  TEXT NOT NULL
				)`,
				`CREATE INDEX IF NOT EXISTS idx_issue_links_issue  ON issue_links(issue_key)`,
				`CREATE INDEX IF NOT EXISTS idx_issue_links_linked ON issue_links(linked_key)`,
				`ALTER TABLE issues ADD COLUMN resolution         TEXT`,
				`ALTER TABLE issues ADD COLUMN due_date           DATETIME`,
				`ALTER TABLE issues ADD COLUMN original_estimate  INTEGER`,
				`ALTER TABLE issues ADD COLUMN time_spent         INTEGER`,
				`ALTER TABLE issues ADD COLUMN remaining_estimate INTEGER`,
				`ALTER TABLE issues ADD COLUMN subtask_keys       TEXT`,
			}
			for _, s := range stmts {
				if _, err := tx.Exec(s); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version:     5,
		description: "add last_issue_updated high-water mark to sync_metadata",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE sync_metadata ADD COLUMN last_issue_updated DATETIME`)
			return err
		},
	},
	{
		version:     6,
		description: "convert comma-separated array columns to JSON arrays",
		up: func(tx *sql.Tx) error {
			// Drop FTS triggers to avoid O(rows×columns) FTS rebuilds during bulk update.
			for _, name := range []string{"issues_fts_insert", "issues_fts_update", "issues_fts_delete"} {
				if _, err := tx.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s", name)); err != nil {
					return fmt.Errorf("dropping trigger %s: %w", name, err)
				}
			}

			builtinArrayCols := []string{"labels", "components", "fix_version", "subtask_keys"}
			for _, col := range builtinArrayCols {
				if err := convertCSVToJSON(tx, col); err != nil {
					return fmt.Errorf("converting %s: %w", col, err)
				}
			}

			rows, err := tx.Query(`SELECT name FROM field_map WHERE type = 'array' AND is_column = 1`)
			if err != nil {
				return err
			}
			var customCols []string
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					customCols = append(customCols, n)
				}
			}
			rows.Close()

			for _, col := range customCols {
				if err := convertCSVToJSON(tx, col); err != nil {
					return fmt.Errorf("converting custom column %s: %w", col, err)
				}
			}

			// Recreate FTS triggers.
			triggers := []string{
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
			for _, t := range triggers {
				if _, err := tx.Exec(t); err != nil {
					return fmt.Errorf("recreating FTS trigger: %w", err)
				}
			}

			// Rebuild FTS index once to pick up the new labels format.
			if _, err := tx.Exec(`INSERT INTO issues_fts(issues_fts) VALUES('rebuild')`); err != nil {
				return fmt.Errorf("rebuilding FTS index: %w", err)
			}

			return nil
		},
	},
	{
		version:     7,
		description: "add changelog table for status transition history",
		up: func(tx *sql.Tx) error {
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS changelog (
					id          TEXT PRIMARY KEY,
					issue_key   TEXT NOT NULL,
					author      TEXT,
					field       TEXT,
					field_type  TEXT,
					from_value  TEXT,
					from_string TEXT,
					to_value    TEXT,
					to_string   TEXT,
					changed_at  DATETIME
				)`,
				`CREATE INDEX IF NOT EXISTS idx_changelog_issue ON changelog(issue_key)`,
				`CREATE INDEX IF NOT EXISTS idx_changelog_time ON changelog(changed_at)`,
				`CREATE INDEX IF NOT EXISTS idx_changelog_field_to ON changelog(field, to_string)`,
			}
			for _, s := range stmts {
				if _, err := tx.Exec(s); err != nil {
					return err
				}
			}
			return nil
		},
	},
}

func convertCSVToJSON(tx *sql.Tx, column string) error {
	if !safeColumnRe.MatchString(column) {
		return fmt.Errorf("invalid column name: %s", column)
	}

	q := fmt.Sprintf(
		`SELECT rowid, "%s" FROM issues WHERE "%s" IS NOT NULL AND "%s" != '' AND "%s" NOT LIKE '[%%'`,
		column, column, column, column,
	)
	rows, err := tx.Query(q)
	if err != nil {
		return err
	}

	type row struct {
		rowid int64
		val   string
	}
	var updates []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.rowid, &r.val) == nil {
			updates = append(updates, r)
		}
	}
	rows.Close()

	if len(updates) == 0 {
		return nil
	}

	stmt, err := tx.Prepare(fmt.Sprintf(`UPDATE issues SET "%s" = ? WHERE rowid = ?`, column))
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, u := range updates {
		parts := strings.Split(u.val, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		b, err := json.Marshal(parts)
		if err != nil {
			continue
		}
		if _, err := stmt.Exec(string(b), u.rowid); err != nil {
			return err
		}
	}
	return nil
}

// Migrate applies any pending schema migrations in order.
func (db *DB) Migrate() error {
	// Ensure schema_version table exists before querying it.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version     INTEGER PRIMARY KEY,
		applied_at  DATETIME NOT NULL DEFAULT (datetime('now')),
		description TEXT
	)`); err != nil {
		return fmt.Errorf("creating schema_version: %w", err)
	}

	current, err := db.currentVersion()
	if err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("beginning migration %d: %w", m.version, err)
		}

		if err := m.up(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d (%s): %w", m.version, m.description, err)
		}

		if _, err := tx.Exec(
			`INSERT INTO schema_version (version, applied_at, description) VALUES (?, ?, ?)`,
			m.version, time.Now().UTC().Format(time.RFC3339), m.description,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %d: %w", m.version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", m.version, err)
		}
	}

	return nil
}

func (db *DB) currentVersion() (int, error) {
	var version int
	err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
	return version, err
}
