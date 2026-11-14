# Moved Issues: Design Plan

When a Jira issue is moved between projects, its key changes (e.g., ROX-16993 → RFE-9570). jai currently has no awareness of this, leading to stale data, ghost rows, orphaned relations, and confusing "not found" errors.

## Current Behavior

### What happens today

1. **`jai get ROX-16993`** — queries `WHERE key = 'ROX-16993'`. Returns stale data if the old row still exists, or errors with `"issue ROX-16993 not found in local database (try: jai sync)"`. The API fallback in `get` actually succeeds (Jira resolves old keys), but the result is displayed with the new key and never persisted locally.

2. **`jai sync` (incremental)** — JQL returns the issue under its new key (RFE-9570). A new row is inserted. The old ROX-16993 row persists as a ghost, inflating counts and appearing in queries.

3. **`jai sync --full`** — `DetectDeletions` (`sync/deletions.go:13`) queries `project = "ROX"` and hard-deletes ROX-16993 since it no longer exists in ROX. But orphaned `issue_links` and `changelog` rows referencing the old key are not cleaned up (no CASCADE on those tables). If RFE is not in any sync source, the issue disappears entirely.

4. **`jai query "SELECT * FROM issues WHERE key = 'ROX-16993'"`** — returns the ghost row (stale data) or zero rows. No hint that the issue was moved.

5. **`jai clone ROX-16993`** — calls `db.GetIssue(sourceKey)`, returns nil, prints the same "not found" error with no API fallback.

6. **`jai set ROX-16993 priority High` → `jai push`** — queues a pending change with `issue_key = "ROX-16993"`. The push sends `PUT /rest/api/3/issue/ROX-16993`. Jira may accept this (it often resolves old keys for writes), but behavior is undefined from jai's perspective.

### Root cause

- `key TEXT PRIMARY KEY` is the sole identifier. The stable Jira numeric `id` (added in migration v8) exists but is only used for changelog bulk fetching.
- Sync is scoped by JQL/project — a moved issue leaves one project's scope and may not enter another's.
- No lookup path compares requested key vs. returned key.
- No alias, redirect, or history mechanism exists.

### Affected tables

| Table | Key Column | CASCADE on delete? |
|---|---|---|
| `issues` | `key TEXT PRIMARY KEY` | N/A |
| `comments` | `issue_key TEXT NOT NULL` | Yes |
| `issue_links` | `issue_key TEXT NOT NULL` | No |
| `changelog` | `issue_key TEXT NOT NULL` | No |
| `pending_changes` | `issue_key TEXT NOT NULL` | No |

When an issue moves, `issue_links`, `changelog`, and `pending_changes` rows referencing the old key become permanently orphaned.

---

## How jira-cli Handles This

[github.com/ankitpokhrel/jira-cli](https://github.com/ankitpokhrel/jira-cli) — the popular Go-based Jira CLI.

**Short answer: it doesn't need to handle it.** jira-cli is pure API-first with zero local persistence of issue data. Every command (`view`, `edit`, `move`, `assign`, `comment`) makes a live `GET /rest/api/{2,3}/issue/{key}` call. No SQLite, no cache, no on-disk issue store.

Since the Jira REST API transparently resolves old keys (requesting `GET /rest/api/3/issue/ROX-16993` returns the issue with `key: "RFE-9570"`), moved issues "just work" in jira-cli. The only local files are config (`~/.config/.jira/.config.yml`) and temporary editor buffers — neither stores issue keys.

The TUI has an in-memory `contentsCache` keyed by issue key, but it's populated from live API data and discarded when the session ends.

**Takeaway:** The moved-issue problem is inherent to jai's DB-first architecture. API-first tools are immune because the Jira API itself handles key resolution. This validates jai's need for an explicit solution — it can't rely on the API for every read.

---

## Proposed Solutions

Two complementary approaches. They are not mutually exclusive — the recommendation is to combine them.

### Option A: Key Aliases Table

Add a separate `key_aliases` table that records old→new key mappings. Go-level lookup functions (`GetIssue`, `GetIssueUpdated`) resolve aliases before querying.

#### Schema

```sql
-- Migration v10 (or next available)
CREATE TABLE IF NOT EXISTS key_aliases (
    old_key   TEXT PRIMARY KEY,
    new_key   TEXT NOT NULL,
    moved_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_key_aliases_new ON key_aliases(new_key);
```

#### Detection points

**1. API fallback in `jai get` (`cli/get.go:34`)**

The `get` command already calls `jira.GetIssue(key)` when the local DB misses. Compare `userSuppliedKey` vs `issue.Key`:

```go
issue, err := g.jira.GetIssue(cmd.Context(), key)
if err != nil {
    return fmt.Errorf("issue %s not found locally or in Jira", key)
}
if issue.Key != key {
    // Issue was moved
    g.db.RecordKeyAlias(key, issue.Key)
    fmt.Fprintf(cmd.ErrOrStderr(), "Note: %s was moved to %s\n", key, issue.Key)
}
```

**2. During sync upsert (`sync/engine.go:~280`)**

After upserting an issue, check if a different key exists with the same Jira numeric `id`:

```go
if err := e.db.UpsertIssue(issue, extra); err != nil { ... }

// Detect moved issues by stable Jira ID
if issue["id"] != nil {
    oldKey, _ := e.db.GetKeyByID(issue["id"].(string))
    newKey := issue["key"].(string)
    if oldKey != "" && oldKey != newKey {
        e.db.RecordKeyAlias(oldKey, newKey)
        e.db.MigrateRelations(oldKey, newKey) // update issue_links, changelog, pending_changes
        e.db.DeleteIssue(oldKey)               // remove ghost row
    }
}
```

**3. During deletion detection (`sync/deletions.go:49`)**

Before hard-deleting a local key that's missing from the remote project, check if the same `id` exists under a new key:

```go
if !remoteKeys[key] {
    // Check if this issue was moved (same id, different key)
    newKey, _ := database.GetKeyByID(idForKey)
    if newKey != "" && newKey != key {
        database.RecordKeyAlias(key, newKey)
        database.MigrateRelations(key, newKey)
    }
    database.Exec(`DELETE FROM issues WHERE key = ?`, key)
    deleted++
}
```

#### Resolution function

```go
// ResolveKey follows the alias chain (flattened — no chasing).
func (db *DB) ResolveKey(key string) (resolvedKey string, wasMoved bool, err error) {
    var newKey string
    err = db.QueryRow("SELECT new_key FROM key_aliases WHERE old_key = ?", key).Scan(&newKey)
    if err == sql.ErrNoRows {
        return key, false, nil
    }
    if err != nil {
        return key, false, err
    }
    return newKey, true, nil
}
```

Wire into: `GetIssue`, `GetIssueUpdated`, and all CLI commands that accept a key argument (`get`, `view`, `clone`, `set`, `comment`, `transition`).

#### Relation migration

When a move is detected, update orphan-prone tables:

```go
func (db *DB) MigrateRelations(oldKey, newKey string) error {
    for _, table := range []string{"issue_links", "changelog", "pending_changes"} {
        db.Exec(fmt.Sprintf("UPDATE %s SET issue_key = ? WHERE issue_key = ?", table), newKey, oldKey)
    }
    return nil
}
```

#### Pros

- Clean separation: alias table is a lookup index, not a data hack
- Go-level commands (`get`, `view`, `clone`, `set`) resolve transparently
- Alias chain is explicit and queryable (`SELECT * FROM key_aliases`)
- `MigrateRelations` fixes orphaned child rows

#### Cons

- **Does not help raw SQL queries** — `jai query "SELECT * FROM issues WHERE key = 'ROX-16993'"` bypasses Go-level resolution entirely, returns zero rows
- Requires wiring `ResolveKey` into every command that accepts a key
- Alias table can grow unboundedly (minor — moved issues are rare)

---

### Option B: Redirect Stub Row

When a move is detected, keep a lightweight stub row in the `issues` table itself under the old key. This makes the old key visible to raw SQL queries.

#### Schema change

```sql
-- Migration v10: add moved_to column
ALTER TABLE issues ADD COLUMN moved_to TEXT;
```

#### Stub row

When a move is detected (same detection points as Option A), instead of just recording an alias, upsert a stub row:

```go
func (db *DB) InsertMoveStub(oldKey, newKey, oldProject string) error {
    _, err := db.Exec(`
        INSERT OR REPLACE INTO issues (key, project, summary, moved_to, status, updated)
        VALUES (?, ?, ?, ?, 'Moved', datetime('now'))
    `, oldKey, oldProject, fmt.Sprintf("[Moved to %s]", newKey), newKey)
    return err
}
```

#### How it works

- `jai query "SELECT * FROM issues WHERE key = 'ROX-16993'"` → returns a row with `summary = "[Moved to RFE-9570]"` and `moved_to = "RFE-9570"`. The user sees a breadcrumb instead of zero rows.
- `jai get ROX-16993` → fetches the stub row, sees `moved_to` is set, follows it to RFE-9570, and displays the real issue with a notice.
- `jai query "SELECT count(*) FROM issues WHERE project = 'ROX'"` → the stub row is counted. This may or may not be desired — can be filtered with `WHERE moved_to IS NULL`.

#### Pros

- **Works with raw SQL queries** — the main advantage over Option A. Users don't need to change queries or know about aliases.
- Simple to implement — single column addition, no new table.
- The stub row is self-documenting (summary says where it moved).

#### Cons

- Pollutes the `issues` table with non-real data. Queries like `SELECT count(*)` or `WHERE status = 'Moved'` need awareness of stubs.
- The stub row has mostly-null columns (no description, assignee, etc.), which could surprise queries that assume all rows are real issues.
- `moved_to` on the issues table mixes concerns — the table is for issue data, not routing.
- During full sync, deletion detection would need to preserve stub rows (special-case logic).
- Stub rows don't solve the orphaned-relations problem for `issue_links`/`changelog`/`pending_changes`.

---

## Comparison

| Concern | Option A (Aliases Table) | Option B (Stub Row) |
|---|---|---|
| `jai get ROX-16993` | Resolves via alias, shows real issue | Follows `moved_to`, shows real issue |
| `jai query "WHERE key = 'ROX-16993'"` | Zero rows (no interception) | Returns stub row with breadcrumb |
| `jai query "SELECT count(*)"` | Accurate count | Inflated by stubs (filterable) |
| Orphaned relations | `MigrateRelations` fixes them | Not addressed |
| Schema cleanliness | Separate table, clean separation | Column on issues table, mixed concerns |
| Deletion detection | Check-before-delete with ID lookup | Must preserve stubs (special case) |
| Implementation complexity | Moderate (new table + wiring) | Low (column + stub insert) |
| Raw SQL transparency | No (alias is invisible to SQL) | Yes (stub is a real row) |

---

## Recommendation: Combine Both

Use **Option A (aliases table) as the primary mechanism** for Go-level commands, and **Option B (stub row) as a complementary UX enhancement** for raw SQL queries.

### Combined flow

1. **Add both** `key_aliases` table and `moved_to` column in one migration.

2. **On move detection** (any of the three detection points):
   - Insert into `key_aliases(old_key, new_key)`
   - Upsert a stub row in `issues` with `moved_to = new_key`
   - Call `MigrateRelations(oldKey, newKey)` to fix orphaned child rows
   - Delete the old full row (the stub replaces it)

3. **In Go-level commands** (`get`, `view`, `clone`, `set`, `comment`):
   - Call `ResolveKey(key)` first
   - If resolved to a different key, print: `"Note: ROX-16993 was moved to RFE-9570"`
   - Operate on the resolved key

4. **In `jai query`** (no code change needed):
   - Users see stub rows with `summary = "[Moved to RFE-9570]"` instead of zero rows
   - Power users can filter: `WHERE moved_to IS NULL`

5. **In deletion detection**:
   - Before deleting a key, check if the same `id` exists under a different key
   - If moved: create stub + alias instead of deleting
   - Never delete stub rows (they have `moved_to IS NOT NULL`)

6. **Improved error message** when both local DB and API fail:
   ```
   Error: issue ROX-16993 not found locally or in Jira.
   If it was moved to another project, try looking it up by its new key.
   ```

7. **When API resolves an old key** and the new project is not in sync sources:
   ```
   Note: ROX-16993 was moved to RFE-9570 (project RFE).
   RFE is not in your sync sources. Add it to receive future updates:
     sync_sources:
       - name: rfe
         jql: project = RFE
   ```

### Implementation order

1. **Migration** — add `key_aliases` table and `moved_to` column
2. **`db.RecordKeyAlias`**, **`db.ResolveKey`**, **`db.InsertMoveStub`**, **`db.MigrateRelations`** — core DB functions
3. **`cli/get.go`** — detect move on API fallback, record alias + stub, print notice
4. **`sync/engine.go`** — detect move during upsert via `id` comparison
5. **`sync/deletions.go`** — check-before-delete, create stub instead of hard-delete
6. **Other CLI commands** — wire `ResolveKey` into `view`, `clone`, `set`, `comment`, `transition`
7. **Tests** — unit tests for ResolveKey, MigrateRelations, move detection in sync; integration test for the full get→sync→query flow

### What to skip

- **Alias chains** (A→B→C) — flatten on discovery. If B→C is recorded later, update A→C.
- **Automatic sync source expansion** — too magical. Inform the user, let them decide.
- **Using `id` as primary key** — massive refactor for marginal benefit. The alias table solves the problem with minimal schema impact.
- **Intercepting raw SQL in the query engine** — fragile, would require SQL parsing. Stub rows solve this more naturally.
