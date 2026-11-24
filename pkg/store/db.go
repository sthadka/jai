package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	sqlite3 "github.com/mattn/go-sqlite3"
)

const readDriverName = "sqlite3_jai_read"

func init() {
	sql.Register(readDriverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			for _, p := range []string{
				"PRAGMA temp_store=MEMORY",
				"PRAGMA mmap_size=268435456",
			} {
				if _, err := conn.Exec(p, nil); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

// readPoolMaxConns scales concurrent read-only WAL connections with CPU count,
// clamped to [4, 8].
func readPoolMaxConns() int {
	return min(8, max(4, runtime.NumCPU()))
}

// DB wraps a SQLite database connection with a single-writer pool (the
// embedded *sql.DB) and a separate multi-connection read-only pool for
// concurrent WAL readers.
type DB struct {
	*sql.DB
	readDB *sql.DB
}

// Open opens (or creates) the SQLite database at path, applies WAL pragmas, and runs migrations.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db: %w", err)
	}

	// Limit to one connection to avoid WAL write conflicts.
	sqlDB.SetMaxOpenConns(1)

	db := &DB{DB: sqlDB}

	if err := db.applyPragmas(); err != nil {
		sqlDB.Close()
		return nil, err
	}

	if err := db.Migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	// Ensure DB file permissions are 0600.
	_ = os.Chmod(path, 0600)

	if path == ":memory:" {
		db.readDB = sqlDB
	} else {
		readDB, err := openReadPool(path)
		if err != nil {
			sqlDB.Close()
			return nil, err
		}
		db.readDB = readDB
	}

	return db, nil
}

// ReadDB returns the read-only SQLite connection pool for concurrent WAL reads.
// Falls back to the primary *sql.DB if no separate read pool was initialized.
func (db *DB) ReadDB() *sql.DB {
	if db.readDB != nil {
		return db.readDB
	}
	return db.DB
}

// Close closes the read-only pool (if distinct), runs PRAGMA optimize, and
// closes the writer connection so SQLite can cleanly checkpoint WAL on shutdown.
func (db *DB) Close() error {
	var readErr error
	if db.readDB != nil && db.readDB != db.DB {
		readErr = db.readDB.Close()
	}
	_ = db.Optimize()
	writeErr := db.DB.Close()
	return errors.Join(readErr, writeErr)
}

// Optimize runs SQLite's PRAGMA optimize to refresh query planner statistics
// for tables whose statistics have grown stale.
func (db *DB) Optimize() error {
	_, err := db.Exec(`PRAGMA optimize`)
	return err
}

func openReadPool(path string) (*sql.DB, error) {
	readDB, err := sql.Open(readDriverName, readOnlyDSN(path))
	if err != nil {
		return nil, fmt.Errorf("opening read-only sqlite pool: %w", err)
	}
	maxConns := readPoolMaxConns()
	readDB.SetMaxOpenConns(maxConns)
	readDB.SetMaxIdleConns(maxConns)
	if err := readDB.Ping(); err != nil {
		readDB.Close()
		return nil, fmt.Errorf("pinging read-only sqlite pool: %w", err)
	}
	return readDB, nil
}

func readOnlyDSN(path string) string {
	// SQLite URI filenames require a "file:" prefix so sqlite3_open_v2 parses
	// mode=ro (otherwise go-sqlite3 strips the query string before open).
	// Escape the three URI-significant characters per https://www.sqlite.org/uri.html.
	escaped := filepath.ToSlash(path)
	escaped = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(escaped)
	return "file:" + escaped + "?mode=ro&_query_only=1&_busy_timeout=30000&_cache_size=-16000"
}

// RebuildFTS rebuilds the issues_fts index from the current issues and issue_blobs tables.
func (db *DB) RebuildFTS() error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM issues_fts`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO issues_fts(rowid, key, summary, description, comments_text, labels)
		SELECT i.rowid, i.key, i.summary, i.description, b.comments_text, i.labels
		FROM issues i
		LEFT JOIN issue_blobs b ON b.issue_key = i.key`); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) applyPragmas() error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=30000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA mmap_size=268435456",
		"PRAGMA wal_autocheckpoint=2000",
		"PRAGMA journal_size_limit=67108864",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("applying pragma %q: %w", p, err)
		}
	}
	return nil
}
