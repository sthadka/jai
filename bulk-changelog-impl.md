# Optimize changelog sync: switch to bulk fetch API + batch DB inserts

## Problem

`SyncChangelogs` in `internal/sync/engine.go:349-407` fetches changelogs **one issue at a time** via `GET /rest/api/3/issue/{key}?expand=changelog`. For 180 issues, that's 180 sequential HTTP round-trips. Each also inserts changelog entries into SQLite one row at a time with no transaction batching. Result: minutes for what should take seconds.

## Solution

Replace per-issue `GetIssueChangelog` calls with Jira's bulk changelog API (`POST /rest/api/3/changelog/bulkfetch`) and batch all DB writes in transactions.

---

## Jira Bulk Changelog API

**Endpoint**: `POST /rest/api/3/changelog/bulkfetch`

**Request body**:
```json
{
  "issueIdsOrKeys": ["ROX-100", "ROX-101", "...up to 100"],
  "fromDate": "2026-07-01T00:00:00.000+0000"
}
```
- `issueIdsOrKeys`: array of issue keys or numeric IDs. **Max 100 per request.**
- `fromDate`: optional ISO 8601 filter — only return changelog entries created after this timestamp.

**Response** (offset-paginated):
```json
{
  "startAt": 0,
  "maxResults": 1000,
  "total": 2500,
  "values": [
    {
      "id": "12345",
      "issueId": "10042",
      "author": { "displayName": "Jane Doe" },
      "created": "2026-06-10T14:30:00.000+0000",
      "items": [
        {
          "field": "status",
          "fieldtype": "jira",
          "from": "10001",
          "fromString": "In Progress",
          "to": "10002",
          "toString": "Release Pending"
        }
      ]
    }
  ]
}
```

**Key differences from the per-issue `?expand=changelog` response**:
- Response is a flat paginated list of changelog histories, NOT nested under an issue object.
- Each value has `issueId` (numeric) but may NOT have `issueKey`. You'll likely need to map issue IDs to keys.
- Pagination is offset-based (`startAt`/`maxResults`/`total`), not cursor-based.

**Caveat**: This endpoint was flagged experimental in late 2024. The actual response shape may differ slightly — test against a real Jira Cloud instance and adjust struct tags if needed.

---

## Existing code context

### Current types (`internal/jira/types.go:100-129`)
```go
type ChangelogResponse struct {
    Key       string     `json:"key"`
    Changelog *Changelog `json:"changelog"`
}

type Changelog struct {
    Histories []ChangelogHistory `json:"histories"`
}

type ChangelogHistory struct {
    ID      string `json:"id"`
    Author  *struct {
        DisplayName string `json:"displayName"`
    } `json:"author"`
    Created string          `json:"created"`
    Items   []ChangelogItem `json:"items"`
}

type ChangelogItem struct {
    Field      string `json:"field"`
    FieldType  string `json:"fieldtype"`
    From       string `json:"from"`
    FromString string `json:"fromString"`
    To         string `json:"to"`
    ToString   string `json:"toString"`
}
```

### Current per-issue fetch (`internal/jira/client.go:182-188`)
```go
func (c *Client) GetIssueChangelog(ctx context.Context, key string) (*ChangelogResponse, error) {
    var resp ChangelogResponse
    if err := c.get(ctx, "/rest/api/3/issue/"+key+"?expand=changelog&fields=key", &resp); err != nil {
        return nil, err
    }
    return &resp, nil
}
```

### Current sync loop (`internal/sync/engine.go:377-401`)
```go
for _, key := range candidates {
    // ... context check ...
    resp, err := e.client.GetIssueChangelog(ctx, key)  // 1 HTTP call per issue
    if err != nil {
        skipped++
        continue
    }
    entries := ExtractChangelog(key, resp)
    for _, entry := range entries {
        _ = e.db.InsertChangelog(entry)  // 1 INSERT per entry, no transaction
    }
    synced++
}
```

### Current extraction (`internal/sync/denormalize.go:240-270`)
```go
func ExtractChangelog(issueKey string, resp *jira.ChangelogResponse) []*db.ChangelogEntry {
    // Iterates resp.Changelog.Histories, builds entries with ID = "historyID_itemIndex"
}
```

### Current DB insert (`internal/db/changelog.go:23-32`)
```go
func (db *DB) InsertChangelog(e *ChangelogEntry) error {
    _, err := db.Exec(`INSERT OR IGNORE INTO changelog (...) VALUES (?, ?, ...)`, ...)
    return err
}
```

### Existing `postDecode` helper (`internal/jira/client.go:137-152`)
```go
func (c *Client) postDecode(ctx context.Context, path string, in, out interface{}) error {
    // Marshals `in` to JSON, POSTs, decodes response into `out`
}
```

---

## Files to change

### 1. `internal/jira/types.go` — add bulk response types

Add new types. Keep all existing types unchanged.

```go
// BulkChangelogRequest is the request body for POST /rest/api/3/changelog/bulkfetch.
type BulkChangelogRequest struct {
    IssueIdsOrKeys []string `json:"issueIdsOrKeys"`
    FromDate       string   `json:"fromDate,omitempty"`
}

// BulkChangelogResponse is the paginated response from the bulk changelog endpoint.
type BulkChangelogResponse struct {
    StartAt    int                  `json:"startAt"`
    MaxResults int                  `json:"maxResults"`
    Total      int                  `json:"total"`
    Values     []BulkChangelogEntry `json:"values"`
}

// BulkChangelogEntry is a single changelog history from the bulk response.
type BulkChangelogEntry struct {
    ID      string `json:"id"`
    IssueID string `json:"issueId"`
    Author  *struct {
        DisplayName string `json:"displayName"`
    } `json:"author"`
    Created string          `json:"created"`
    Items   []ChangelogItem `json:"items"` // reuse existing ChangelogItem
}
```

**Note**: If testing reveals the bulk response includes an `issueKey` field, add it to `BulkChangelogEntry` and skip the ID-to-key mapping in step 4.

### 2. `internal/jira/client.go` — add `BulkFetchChangelogs` method

```go
// BulkFetchChangelogs fetches changelogs for up to 100 issues in one API call.
// Handles pagination internally — accumulates all values across pages.
func (c *Client) BulkFetchChangelogs(ctx context.Context, issueKeys []string) ([]BulkChangelogEntry, error) {
    // 1. Build BulkChangelogRequest{IssueIdsOrKeys: issueKeys}
    // 2. POST to /rest/api/3/changelog/bulkfetch using c.postDecode
    // 3. Pagination loop: while startAt + len(values) < total, re-request with updated startAt
    // 4. Return accumulated []BulkChangelogEntry
}
```

Use `postDecode` for each page request. To paginate, you'll need to either:
- Add `startAt` to the request body (check if the API accepts it there), or
- Append `?startAt=N` as a query param

### 3. `internal/db/changelog.go` — add `InsertChangelogBatch`

```go
// InsertChangelogBatch inserts multiple changelog entries in a single transaction.
func (db *DB) InsertChangelogBatch(entries []*ChangelogEntry) error {
    tx, err := db.Begin()
    if err != nil {
        return err
    }
    defer tx.Rollback()

    stmt, err := tx.Prepare(`
        INSERT OR IGNORE INTO changelog
            (id, issue_key, author, field, field_type, from_value, from_string, to_value, to_string, changed_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
    if err != nil {
        return err
    }
    defer stmt.Close()

    for _, e := range entries {
        if _, err := stmt.Exec(e.ID, e.IssueKey, e.Author, e.Field, e.FieldType,
            e.FromValue, e.FromString, e.ToValue, e.ToString, e.ChangedAt); err != nil {
            return err
        }
    }
    return tx.Commit()
}
```

### 4. `internal/sync/denormalize.go` — add `ExtractBulkChangelog`

```go
// ExtractBulkChangelog converts bulk changelog API entries into DB entries.
// issueIDToKey maps Jira numeric issue IDs to keys (e.g. "10042" -> "ROX-123").
func ExtractBulkChangelog(entries []jira.BulkChangelogEntry, issueIDToKey map[string]string) []*db.ChangelogEntry {
    var result []*db.ChangelogEntry
    for _, e := range entries {
        issueKey, ok := issueIDToKey[e.IssueID]
        if !ok {
            continue // skip entries for unknown issues
        }
        author := ""
        if e.Author != nil {
            author = e.Author.DisplayName
        }
        changedAt := normalizeDate(e.Created)

        for i, item := range e.Items {
            result = append(result, &db.ChangelogEntry{
                ID:         fmt.Sprintf("%s_%d", e.ID, i),  // same format as ExtractChangelog
                IssueKey:   issueKey,
                Author:     author,
                Field:      item.Field,
                FieldType:  item.FieldType,
                FromValue:  item.From,
                FromString: item.FromString,
                ToValue:    item.To,
                ToString:   item.ToString,
                ChangedAt:  changedAt,
            })
        }
    }
    return result
}
```

### 5. `internal/db/` — add issue ID-to-key lookup

Check if the `issues` table has a numeric `id` column (the Jira issue ID, not the key). If so, add:

```go
// GetIssueIDToKeyMap returns a map of Jira issue ID -> issue key for the given keys.
func (db *DB) GetIssueIDToKeyMap(keys []string) (map[string]string, error) {
    // SELECT id, key FROM issues WHERE key IN (?, ?, ...)
    // Return map[id]key
}
```

If the `issues` table doesn't store the numeric Jira ID, check how issues are stored — it may be in the `id` column or embedded in the raw JSON. Adapt accordingly.

### 6. `internal/sync/engine.go` — rewrite `SyncChangelogs`

Replace the sequential loop with batched bulk fetches:

```
SyncChangelogs(ctx, sourceFilter):
    1. Get candidates via db.GetChangelogSyncCandidates (same as today)
    2. If empty, send Done progress and return
    3. Build issueIDToKey map via db.GetIssueIDToKeyMap(candidates)
    4. Chunk candidates into batches of 100
    5. For each batch:
        a. Call client.BulkFetchChangelogs(ctx, batch)
        b. If error AND this is the first batch:
           - Log warning, fall back to per-issue loop (existing logic)
           - Return (don't try bulk again)
        c. Convert via ExtractBulkChangelog(resp, issueIDToKey)
        d. Insert via db.InsertChangelogBatch(entries)
        e. synced += len(batch)
        f. Send ChangelogProgress{Total: total, Synced: synced, Skipped: skipped}
    6. Send Done progress
```

**Fallback is important**: If `BulkFetchChangelogs` returns a 404 or other error on the first batch, fall back to the existing per-issue approach. This keeps the feature working on Jira instances that don't support the bulk endpoint.

**Progress reporting**: Report after each batch of 100 instead of every 10 issues. The `ChangelogProgress` struct and `displayChangelogProgress` in `cli/sync.go` stay unchanged.

---

## Tests

### `internal/jira/client_test.go`
- **`TestBulkFetchChangelogs`** — mock server at `/rest/api/3/changelog/bulkfetch` returns a single page with 3 entries. Verify all entries returned, request body contains correct keys.
- **`TestBulkFetchChangelogs_Pagination`** — mock returns page 1 (total=5, maxResults=3, 3 entries), then page 2 (2 entries). Verify all 5 accumulated.
- **`TestBulkFetchChangelogs_Empty`** — mock returns `{"values":[],"total":0}`. Verify empty slice, no error.

### `internal/sync/denormalize_test.go`
- **`TestExtractBulkChangelog`** — 2 entries with known issueIDToKey mapping. Verify correct key assignment, ID format (`historyID_itemIndex`), date normalization.
- **`TestExtractBulkChangelog_UnknownIssueID`** — entry with issueID not in the map is skipped.

### `internal/db/changelog_test.go` (create if needed)
- **`TestInsertChangelogBatch`** — insert 5 entries in batch, verify all 5 present.
- **`TestInsertChangelogBatch_Duplicates`** — insert overlapping entries, verify INSERT OR IGNORE deduplicates without error.

---

## Do NOT change

- `GetIssueChangelog` method — keep for fallback
- `ExtractChangelog` function — keep for fallback
- `ChangelogResponse` / `Changelog` / `ChangelogHistory` / `ChangelogItem` types — still used by per-issue endpoint
- `displayChangelogProgress` in `cli/sync.go` — consumes the same `ChangelogProgress` channel
- `GetChangelogSyncCandidates` query — still determines which issues need sync
- Changelog DB schema / migration (v7)

## Build & test

```bash
go build -tags fts5 -o jai ./cmd/jai
go test -tags fts5 ./internal/jira/ ./internal/sync/ ./internal/db/
```

## Verification

After implementing, run `./jai sync --changelogs`. It should complete in a fraction of the time (2 bulk requests instead of ~180 serial ones). Progress spinner should still update. Data should be identical to what the per-issue approach produced.
