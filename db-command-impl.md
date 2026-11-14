# Add `jai db` command for database management

## Problem

There's no way to manage the SQLite database from the CLI. Users who hit corruption, stale WAL files, or want a clean slate have to manually find and delete `~/.local/share/jai/jai.db*` files. A `jai db` command group would make this self-service.

## Commands

### `jai db reset`

Deletes the database and all associated files (`.db`, `.db-shm`, `.db-wal`), then recreates an empty database with all migrations applied.

```
$ jai db reset
This will delete all local data and require a full re-sync.
Continue? [y/N] y
✓ Database reset. Run 'jai sync' to re-populate.
```

**Flags**:
- `--force` / `-f` — skip the confirmation prompt

**Behavior**:
1. Resolve DB path from config (`g.cfg.DB.Path`) or `--db` flag
2. Close the current DB connection (if open)
3. Remove `path`, `path-shm`, `path-wal` (ignore "not found" errors)
4. Re-open the DB via `db.Open(path)` — this recreates the directory, file, applies pragmas and migrations
5. Close and print success message

### `jai db path`

Prints the resolved database file path. Useful for scripting.

```
$ jai db path
/Users/you/.local/share/jai/jai.db
```

### `jai db info`

Prints database stats: file size, issue count, last sync time, migration version.

```
$ jai db info
Path:       /Users/you/.local/share/jai/jai.db
Size:       142 MB
Issues:     157,223
Changelogs: 482,109
Migration:  v8
Last sync:  2026-07-22T10:30:00Z (12 minutes ago)
```

With `--json`:
```json
{"ok":true,"data":{"path":"/Users/you/.local/share/jai/jai.db","size_bytes":148897792,"issues":157223,"changelogs":482109,"migration_version":8,"last_sync":"2026-07-22T10:30:00Z"}}
```

---

## Existing code context

### Command registration pattern

All commands are registered in their own file under `internal/cli/`. Each file:
1. Defines a cobra command with `&cobra.Command{...}`
2. Registers it via `func init() { rootCmd.AddCommand(cmd) }`
3. Uses the `g` global for shared state (`g.db`, `g.cfg`, `g.jira`, etc.)

Example from `internal/cli/sync.go`:
```go
func init() {
    rootCmd.AddCommand(syncCmd)
    syncCmd.Flags().BoolVar(&syncFull, "full", false, "...")
}
```

### Auto-sync skip list (`internal/cli/root.go:41-48`)

Commands that should NOT trigger auto-sync are listed in `noAutoSync`. Add `"db"` to this map (it's actually already there — listed as `"db": true` for the schema sub-command, but it will also match the new `jai db` command).

### DB open/close

The `PersistentPreRunE` in `root.go:54-96` opens the DB before any command runs. For `jai db reset`, this is a problem — we need to close the DB before deleting it, then reopen. Options:

1. **Skip DB open for `db reset`**: Add `"reset"` to a skip list or check `cmd.Parent().Name() == "db"` in `PersistentPreRunE`
2. **Close and reopen in the command**: Call `g.db.Close()`, delete files, call `db.Open()` again

Option 2 is simpler and matches the existing pattern. The `reset` subcommand just needs the config loaded (for the DB path), not an open DB connection.

### DB path resolution (`internal/cli/root.go:73-75`)

```go
if g.dbPath != "" {
    g.cfg.DB.Path = g.dbPath
}
```

The `--db` flag overrides the config path. The `db` command should respect this.

---

## Files to create/change

### 1. Create `internal/cli/db.go`

```go
package cli

import (
    "fmt"
    "os"
    "time"

    "github.com/spf13/cobra"
    "github.com/sthadka/jai/internal/db"
)

var dbResetForce bool

var dbCmd = &cobra.Command{
    Use:   "db",
    Short: "Manage the local database",
}

var dbResetCmd = &cobra.Command{
    Use:   "reset",
    Short: "Delete and recreate the database",
    Long:  "Removes the SQLite database and all WAL files, then creates a fresh empty database.",
    RunE: func(cmd *cobra.Command, args []string) error {
        dbPath := g.cfg.DB.Path

        if !dbResetForce {
            fmt.Println("This will delete all local data and require a full re-sync.")
            fmt.Print("Continue? [y/N] ")
            var answer string
            fmt.Scanln(&answer)
            if answer != "y" && answer != "Y" {
                fmt.Println("Aborted.")
                return nil
            }
        }

        // Close current connection.
        if g.db != nil {
            g.db.Close()
            g.db = nil
        }

        // Remove DB and WAL/SHM files.
        for _, suffix := range []string{"", "-shm", "-wal"} {
            os.Remove(dbPath + suffix) // ignore errors (file may not exist)
        }

        // Recreate.
        database, err := db.Open(dbPath)
        if err != nil {
            return fmt.Errorf("recreating database: %w", err)
        }
        database.Close()

        fmt.Println("✓ Database reset. Run 'jai sync' to re-populate.")
        return nil
    },
}

var dbPathCmd = &cobra.Command{
    Use:   "path",
    Short: "Print the database file path",
    RunE: func(cmd *cobra.Command, args []string) error {
        fmt.Println(g.cfg.DB.Path)
        return nil
    },
}

var dbInfoCmd = &cobra.Command{
    Use:   "info",
    Short: "Show database statistics",
    RunE: func(cmd *cobra.Command, args []string) error {
        dbPath := g.cfg.DB.Path

        // File size.
        info, err := os.Stat(dbPath)
        if err != nil {
            return fmt.Errorf("stat database: %w", err)
        }

        // Issue count.
        var issueCount int
        row := g.db.QueryRow("SELECT count(*) FROM issues")
        row.Scan(&issueCount)

        // Changelog count.
        var changelogCount int
        row = g.db.QueryRow("SELECT count(*) FROM changelog")
        row.Scan(&changelogCount)

        // Migration version — use db.currentVersion() or query schema_version table.
        var version int
        row = g.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version")
        row.Scan(&version)

        // Last sync time.
        var lastSync string
        row = g.db.QueryRow("SELECT COALESCE(MAX(last_sync), '') FROM sync_meta")
        row.Scan(&lastSync)

        if g.jsonOut {
            // Use output.OK() pattern
            fmt.Printf(`{"ok":true,"data":{"path":%q,"size_bytes":%d,"issues":%d,"changelogs":%d,"migration_version":%d,"last_sync":%q}}`,
                dbPath, info.Size(), issueCount, changelogCount, version, lastSync)
            fmt.Println()
            return nil
        }

        fmt.Printf("Path:       %s\n", dbPath)
        fmt.Printf("Size:       %s\n", humanSize(info.Size()))
        fmt.Printf("Issues:     %d\n", issueCount)
        fmt.Printf("Changelogs: %d\n", changelogCount)
        fmt.Printf("Migration:  v%d\n", version)
        if lastSync != "" {
            if t, err := time.Parse(time.RFC3339, lastSync); err == nil {
                fmt.Printf("Last sync:  %s (%s ago)\n", lastSync, time.Since(t).Round(time.Second))
            } else {
                fmt.Printf("Last sync:  %s\n", lastSync)
            }
        } else {
            fmt.Printf("Last sync:  never\n")
        }
        return nil
    },
}

func humanSize(bytes int64) string {
    const unit = 1024
    if bytes < unit {
        return fmt.Sprintf("%d B", bytes)
    }
    div, exp := int64(unit), 0
    for n := bytes / unit; n >= unit; n /= unit {
        div *= unit
        exp++
    }
    return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func init() {
    dbCmd.AddCommand(dbResetCmd, dbPathCmd, dbInfoCmd)
    dbResetCmd.Flags().BoolVarP(&dbResetForce, "force", "f", false, "skip confirmation prompt")
    rootCmd.AddCommand(dbCmd)
}
```

### 2. Update `internal/cli/root.go`

The `noAutoSync` map already has `"db": true`, so no change needed there.

However, `PersistentPreRunE` opens the DB for all commands. For `db reset` and `db path`, we don't need a working DB. Two options:

**Option A** (simple): Let the DB open normally. If it fails (corrupt DB), `db reset` can't run — which is the exact scenario where users need it. To fix this, add `"reset"` and `"path"` to a list that skips DB opening:

```go
// In PersistentPreRunE, after config loading:
var noDBRequired = map[string]bool{
    "init": true, "help": true, "completion": true,
    "reset": true, "path": true,
}
if noDBRequired[cmd.Name()] {
    return nil
}
// ... existing db.Open code ...
```

**Option B** (targeted): Have `db reset` handle the corrupt-DB case by catching the open error in `PersistentPreRunE` and storing it, then letting the reset command proceed anyway. This is more complex.

**Recommendation**: Option A. It's the simplest and handles the "DB is corrupt, need to reset" case correctly.

---

## Edge cases to handle

1. **DB doesn't exist yet**: `db reset` should still work (just create a fresh DB). `os.Remove` errors on missing files should be ignored.
2. **DB is corrupt/locked**: `db reset` must work even if the DB can't be opened — this is why skipping DB open in `PersistentPreRunE` matters.
3. **`--db` flag**: All sub-commands should respect the `--db` override from the root persistent flag.
4. **Concurrent access**: If another `jai` process is using the DB, the reset will fail on Windows (locked files). On macOS/Linux, the delete succeeds but the other process keeps its file descriptor. Print a warning if the `-wal` file is non-empty.

## Tests

### `internal/cli/db_test.go`
- **`TestDBReset`**: Create a DB with some data, run reset, verify DB exists but is empty (0 issues, migrations applied).
- **`TestDBPath`**: Verify output matches config path.
- **`TestDBInfo`**: Insert some issues, verify counts in output.
- **`TestDBResetNoConfirm`**: Without `--force`, pipe "n" to stdin, verify DB still has data.

## Build & test

```bash
go build -tags fts5 -o jai ./cmd/jai
go test -tags fts5 ./internal/cli/
./jai db info
./jai db path
./jai db reset --force && ./jai sync
```
