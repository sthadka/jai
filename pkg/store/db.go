package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// readPoolMaxConns caps concurrent read-only SQLite connections in WAL mode.
const readPoolMaxConns = 4

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

// Close closes the read-only pool (if distinct) before the writer connection so
// SQLite can cleanly checkpoint WAL on shutdown.
func (db *DB) Close() error {
	var readErr error
	if db.readDB != nil && db.readDB != db.DB {
		readErr = db.readDB.Close()
	}
	writeErr := db.DB.Close()
	return errors.Join(readErr, writeErr)
}

func openReadPool(path string) (*sql.DB, error) {
	readDB, err := sql.Open("sqlite3", readOnlyDSN(path))
	if err != nil {
		return nil, fmt.Errorf("opening read-only sqlite pool: %w", err)
	}
	readDB.SetMaxOpenConns(readPoolMaxConns)
	readDB.SetMaxIdleConns(readPoolMaxConns)
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

// RebuildFTS rebuilds the issues_fts index from the current issues table.
// Call when the FTS index becomes inconsistent (e.g. "fts5: missing row" errors).
func (db *DB) RebuildFTS() error {
	_, err := db.Exec(`INSERT INTO issues_fts(issues_fts) VALUES('rebuild')`)
	return err
}

func (db *DB) applyPragmas() error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=30000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("applying pragma %q: %w", p, err)
		}
	}
	return nil
}
