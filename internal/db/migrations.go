package db

import (
	"database/sql"
	"fmt"
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
