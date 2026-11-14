# Incremental changelog sync: avoid re-fetching unchanged issues

## Problem

After the initial changelog sync, running `jai sync --changelogs` re-fetches changelogs for **all** 39k+ issues — even those that haven't changed since the last sync. This happens because `GetChangelogSyncCandidates` uses a LEFT JOIN against the `changelog` table: issues with zero changelog history in Jira always appear as candidates (the LEFT JOIN returns NULL), and there's no way to distinguish "no history exists" from "not yet synced."

Result: incremental `--changelogs` is as slow as the first run.

## Solution

Two complementary changes:

1. **Auto-sync changelogs during `jai sync`** — piggyback on the issue sync loop. We already know exactly which issues were created/updated. Fetch changelogs for just those issues immediately after upserting them. No separate `--changelogs` pass needed for day-to-day use.

2. **Track "last changelog checked" per issue** — add a `changelog_synced_at` column to the `issues` table. After checking an issue's changelog (even if it returns 0 entries), stamp the time. Then `GetChangelogSyncCandidates` can skip issues where `updated <= changelog_synced_at`. The `--changelogs` flag becomes a catch-up/backfill tool that only re-checks issues that have been updated since their last changelog fetch.

---

## Existing code context

### Issue sync loop (`internal/sync/engine.go:244-308`)

```go
for page, err := range e.client.SearchAll(ctx, jql, fields) {
    // ... error handling ...

    for _, apiIssue := range page {
        total++
        // ... Denormalize, skip-if-unchanged ...

        if err := e.db.UpsertIssue(issue, extra); err != nil { continue }

        // Comments and links extracted here per-issue
        comments, err := ExtractComments(apiIssue.Key, rawBytes)
        // ...
        links := ExtractIssueLinks(apiIssue.Key, rawBytes)
        // ...
    }

    // Progress emitted after each page
    ch <- Progress{Project: src.Name, New: newCount, Updated: updatedCount, Total: total}
}
```

Each page from `SearchAll` returns up to 100 issues. After processing a page, an intermediate `Progress` event is emitted.

### Bulk changelog fetch (`internal/jira/client.go:192-216`)

```go
func (c *Client) BulkFetchChangelogs(ctx context.Context, keys []string) ([]BulkChangelogEntry, error)
```

Accepts up to 100 keys, returns all changelog entries (handles pagination internally).

### Changelog extraction (`internal/sync/denormalize.go:275-304`)

```go
func ExtractBulkChangelog(entries []jira.BulkChangelogEntry, issueIDToKey map[string]string) []*db.ChangelogEntry
```

Converts bulk API response entries to DB entries. Requires an `issueID→issueKey` mapping.

### ID-to-key mapping (`internal/db/changelog.go:63-80`)

```go
func (db *DB) GetIssueIDToKeyMap() (map[string]string, error)
```

Returns a map of all Jira numeric IDs → issue keys. Used by `ExtractBulkChangelog`.

### Current candidate query (`internal/db/changelog.go:97-135`)

```go
func (db *DB) GetChangelogSyncCandidates(projectFilter []string) ([]string, error) {
    query := `
        SELECT i.key FROM issues i
        LEFT JOIN (
            SELECT issue_key, MAX(changed_at) as max_changed
            FROM changelog GROUP BY issue_key
        ) c ON i.key = c.issue_key
        WHERE c.issue_key IS NULL OR i.updated > c.max_changed`
    // ...
}
```

### Standalone changelog sync (`internal/sync/engine.go:349-477`)

`SyncChangelogs` gets candidates, splits into bulk/fallback, fetches in batches of 100, inserts via `InsertChangelogBatch`.

### CLI wiring (`internal/cli/sync.go:43-49`)

```go
if syncChangelogs {
    clCh, err := g.sync.SyncChangelogs(ctx, syncSourceFlag)
    // ...
    displayChangelogProgress(clCh)
}
```

---

## Part 1: Track "last changelog checked" per issue

### 1a. Migration 9 — add `changelog_synced_at` column

**File**: `internal/db/migrations.go`

Add a new migration after migration 8:

```go
{
    version:     9,
    description: "add changelog_synced_at to issues for incremental changelog sync",
    up: func(tx *sql.Tx) error {
        _, err := tx.Exec(`ALTER TABLE issues ADD COLUMN changelog_synced_at DATETIME`)
        return err
    },
},
```

### 1b. Update `GetChangelogSyncCandidates`

**File**: `internal/db/changelog.go`

Change the WHERE clause to use `changelog_synced_at` instead of the LEFT JOIN approach:

```go
func (db *DB) GetChangelogSyncCandidates(projectFilter []string) ([]string, error) {
    query := `
        SELECT key FROM issues
        WHERE changelog_synced_at IS NULL
           OR updated > changelog_synced_at`

    var args []any
    if len(projectFilter) > 0 {
        placeholders := make([]string, len(projectFilter))
        for i, p := range projectFilter {
            placeholders[i] = "?"
            args = append(args, p)
        }
        query += fmt.Sprintf(` AND project IN (%s)`, strings.Join(placeholders, ", "))
    }
    query += ` ORDER BY key`

    sqlRows, err := db.Query(query, args...)
    // ... same scanning logic ...
}
```

This is much faster (no LEFT JOIN, no subquery) and correctly handles the "no changelog history" case: once `changelog_synced_at` is set, the issue won't be a candidate again until `updated` advances past it.

### 1c. Add `MarkChangelogSynced` DB method

**File**: `internal/db/changelog.go`

```go
// MarkChangelogSynced stamps changelog_synced_at for the given issue keys.
func (db *DB) MarkChangelogSynced(keys []string) error {
    if len(keys) == 0 {
        return nil
    }
    now := time.Now().UTC().Format(time.RFC3339)

    // Batch in chunks of 500 to stay under SQLite variable limit.
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

        _, err := db.Exec(
            fmt.Sprintf(`UPDATE issues SET changelog_synced_at = ? WHERE key IN (%s)`,
                strings.Join(placeholders, ", ")),
            args...,
        )
        if err != nil {
            return err
        }
    }
    return nil
}
```

### 1d. Call `MarkChangelogSynced` after each batch in `SyncChangelogs`

**File**: `internal/sync/engine.go`, inside the bulk fetch loop (after `InsertChangelogBatch`)

```go
dbEntries := ExtractBulkChangelog(entries, idToKey)
if err := e.db.InsertChangelogBatch(dbEntries); err != nil {
    skipped += len(batch)
    ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped}
    continue
}

// Mark these issues as changelog-synced (even if they had 0 entries).
_ = e.db.MarkChangelogSynced(batch)

synced += len(batch)
```

Also mark in the per-issue fallback loop, after processing each issue:

```go
dbEntries := ExtractChangelog(key, resp)
for _, entry := range dbEntries {
    _ = e.db.InsertChangelog(entry)
}
_ = e.db.MarkChangelogSynced([]string{key})
synced++
```

---

## Part 2: Auto-sync changelogs during `jai sync`

### 2a. Collect upserted keys per page

**File**: `internal/sync/engine.go`, inside `syncSource`

After each page of issues is processed, we know which keys were actually upserted (new or updated). Collect them:

```go
// Before the page loop:
var pageUpsertedKeys []string

// Inside the per-issue loop, after successful UpsertIssue:
if err := e.db.UpsertIssue(issue, extra); err != nil {
    continue
}
pageUpsertedKeys = append(pageUpsertedKeys, apiIssue.Key)

// ... existing comments/links extraction ...
```

### 2b. Fetch changelogs for upserted issues after each page

**File**: `internal/sync/engine.go`, inside `syncSource`, after the per-issue loop but before emitting progress

After the inner `for _, apiIssue := range page` loop completes, fetch changelogs for the upserted keys:

```go
// After processing all issues in this page, sync their changelogs.
if len(pageUpsertedKeys) > 0 {
    e.syncChangelogsForKeys(ctx, pageUpsertedKeys)
}
pageUpsertedKeys = pageUpsertedKeys[:0] // reset for next page

// Emit progress (existing line)
ch <- Progress{Project: src.Name, New: newCount, Updated: updatedCount, Total: total}
```

### 2c. Add `syncChangelogsForKeys` helper method

**File**: `internal/sync/engine.go`

A private method that fetches changelogs for a small batch of keys (typically ≤100, one page worth). No progress channel needed — this is inline with the issue sync.

```go
// syncChangelogsForKeys fetches and stores changelogs for the given issue keys.
// Called inline during issue sync for newly upserted issues. Errors are non-fatal.
func (e *Engine) syncChangelogsForKeys(ctx context.Context, keys []string) {
    if len(keys) == 0 {
        return
    }

    // Build id→key map for these keys.
    // We can get the IDs from the DB since we just upserted them with IDs.
    idToKey, err := e.db.GetIssueIDToKeyMapForKeys(keys)
    if err != nil || len(idToKey) == 0 {
        // Fallback: per-issue fetch
        for _, key := range keys {
            resp, err := e.client.GetIssueChangelog(ctx, key)
            if err != nil {
                continue
            }
            entries := ExtractChangelog(key, resp)
            for _, entry := range entries {
                _ = e.db.InsertChangelog(entry)
            }
        }
        _ = e.db.MarkChangelogSynced(keys)
        return
    }

    // Bulk fetch (keys are ≤100, fits in one API call).
    entries, err := e.client.BulkFetchChangelogs(ctx, keys)
    if err != nil {
        // Fallback to per-issue on bulk failure (same as above).
        for _, key := range keys {
            resp, err := e.client.GetIssueChangelog(ctx, key)
            if err != nil {
                continue
            }
            dbEntries := ExtractChangelog(key, resp)
            for _, entry := range dbEntries {
                _ = e.db.InsertChangelog(entry)
            }
        }
        _ = e.db.MarkChangelogSynced(keys)
        return
    }

    dbEntries := ExtractBulkChangelog(entries, idToKey)
    _ = e.db.InsertChangelogBatch(dbEntries)
    _ = e.db.MarkChangelogSynced(keys)
}
```

### 2d. Add `GetIssueIDToKeyMapForKeys` for small batches

**File**: `internal/db/changelog.go`

The existing `GetIssueIDToKeyMap()` loads ALL mappings. For inline sync we only need the current page's keys (≤100), so a filtered version is safe here:

```go
// GetIssueIDToKeyMapForKeys returns id→key mappings for the given keys.
// Use for small batches (≤100 keys). For large sets, use GetIssueIDToKeyMap().
func (db *DB) GetIssueIDToKeyMapForKeys(keys []string) (map[string]string, error) {
    if len(keys) == 0 {
        return nil, nil
    }
    placeholders := make([]string, len(keys))
    args := make([]any, len(keys))
    for i, k := range keys {
        placeholders[i] = "?"
        args[i] = k
    }
    query := fmt.Sprintf(
        `SELECT id, key FROM issues WHERE key IN (%s) AND id IS NOT NULL AND id != ''`,
        strings.Join(placeholders, ", "),
    )
    sqlRows, err := db.Query(query, args...)
    if err != nil {
        return nil, err
    }
    defer sqlRows.Close()

    m := make(map[string]string)
    for sqlRows.Next() {
        var id, key string
        if sqlRows.Scan(&id, &key) == nil {
            m[id] = key
        }
    }
    return m, nil
}
```

---

## Part 3: CLI changes

### 3a. Update `--changelogs` flag description

**File**: `internal/cli/sync.go`

Change the flag description to reflect its new purpose:

```go
syncCmd.Flags().BoolVar(&syncChangelogs, "changelogs", false,
    "backfill changelog history (only needed for initial import; day-to-day syncs include changelogs automatically)")
```

### 3b. No other CLI changes needed

The `--changelogs` flag still calls `SyncChangelogs`, which now uses `GetChangelogSyncCandidates` with the improved query. After the first backfill, incremental `--changelogs` runs will find very few candidates (only issues updated since their last changelog check).

---

## Execution order

The changes should be implemented in this order:

1. **Migration 9** — add `changelog_synced_at` column
2. **`MarkChangelogSynced`** — new DB method
3. **`GetChangelogSyncCandidates`** — update to use `changelog_synced_at`
4. **`SyncChangelogs`** — call `MarkChangelogSynced` after each batch
5. **`GetIssueIDToKeyMapForKeys`** — new DB method for small batches
6. **`syncChangelogsForKeys`** — new engine helper
7. **`syncSource`** — collect upserted keys, call `syncChangelogsForKeys` per page
8. **CLI** — update flag description

Steps 1-4 make `--changelogs` incremental. Steps 5-7 make changelogs sync automatically. Step 8 is cosmetic.

---

## Expected behavior after implementation

| Scenario | Before | After |
|---|---|---|
| `jai sync` (daily use) | Syncs issues only, no changelogs | Syncs issues + changelogs for changed issues |
| `jai sync --changelogs` (first run) | Fetches all 39k issues | Same (backfill) |
| `jai sync --changelogs` (subsequent) | Re-fetches all 39k issues | Only fetches issues updated since last check |
| `jai sync --changelogs` (after `jai sync` ran) | Re-fetches all 39k issues | Near-zero candidates (already synced inline) |

---

## Tests

### `internal/db/changelog_test.go`
- **`TestMarkChangelogSynced`** — stamp 3 keys, verify `changelog_synced_at` is set
- **`TestMarkChangelogSynced_LargeBatch`** — stamp 600 keys (tests chunking at 500)
- **`TestGetChangelogSyncCandidates_SkipsChecked`** — insert issue with `changelog_synced_at` set to after `updated`, verify it's not a candidate
- **`TestGetChangelogSyncCandidates_IncludesStale`** — insert issue with `updated` after `changelog_synced_at`, verify it IS a candidate
- **`TestGetIssueIDToKeyMapForKeys`** — insert 3 issues with IDs, query 2, verify only those 2 returned

### `internal/sync/engine_test.go` (or existing test file)
- **`TestSyncChangelogsForKeys`** — mock Jira client, verify changelogs fetched and `changelog_synced_at` stamped
- **`TestSyncSource_IncludesChangelogs`** — mock full sync, verify changelogs are fetched inline for upserted issues

## Do NOT change

- `BulkFetchChangelogs` client method
- `ExtractBulkChangelog` / `ExtractChangelog` functions
- `InsertChangelogBatch` / `InsertChangelog` DB methods
- `GetIssueIDToKeyMap()` (the unfiltered version, still used by `SyncChangelogs`)
- `displayChangelogProgress` in `cli/sync.go`
- The `ChangelogProgress` struct or channel protocol

## Build & test

```bash
go build -tags fts5 -o jai ./cmd/jai
go test -tags fts5 ./internal/db/ ./internal/sync/ ./internal/jira/
```

## Verification

1. Delete DB, run `jai sync` — verify changelogs are synced inline (check `SELECT count(*) FROM changelog`)
2. Run `jai sync` again — verify no changelog re-fetch (issues unchanged, `changelog_synced_at` blocks them)
3. Run `jai sync --changelogs` — verify near-zero candidates
4. Manually update an issue in Jira, run `jai sync` — verify only that issue's changelog is re-fetched
