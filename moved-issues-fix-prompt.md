# Task: Handle Jira issues moved between projects (key changes)

## Context

When a Jira issue is moved between projects, Jira changes its key (e.g. `ROX-19661` → `RFE-9688`). Jira's REST API transparently resolves the old key — `GET /rest/api/3/issue/ROX-19661` returns HTTP 200 with the issue body under `"key": "RFE-9688"` — so visiting the old key in the Jira web UI just redirects.

`jai` (this repo) has **no awareness of this at all**. The old key's row sits in the local SQLite DB, untouched and increasingly stale, with nothing indicating it moved. This was discovered when a user found `ROX-19661` returning stale local data while Jira itself redirects that key to `RFE-9688`.

A design doc already exists at `moved-issues-plan.md` (repo root) with a full root-cause analysis and two proposed solutions (Option A: key-aliases table, Option B: redirect stub row), plus a recommendation to combine both. **Read that file in full before starting** — it has the schema, detection-point pseudocode, and comparison table. This prompt updates it with exact current file:line references (verified against the codebase on branch `feat/agent-integration` as of this writing) and adds one gap the original doc didn't cover: the MCP server's `jai_get` tool has the identical bug as the CLI's `jai get`, in a separate file.

## Verified current state (file:line references)

### Schema — `internal/db/schema.go`
- `issues` table: `key TEXT PRIMARY KEY` (line 15). No `id`-based identity, no alias/redirect columns.
- `comments.issue_key` has `ON DELETE CASCADE` to `issues(key)` (line 52).
- `issue_links`, `changelog`, `pending_changes` — `issue_key TEXT NOT NULL` with **no** CASCADE (confirm current line numbers with `tilth_search issue_links,changelog,pending_changes` before editing; the plan doc has approximate refs from when it was written).
- Migrations: `internal/db/migrations.go`. Latest is **migration version 10** (line 204, "add sprint, attachment, dev_links tables and issue columns for agent integration"). **Your new migration must be version 11.**
- `id` column (Jira's stable numeric issue ID) was added in migration v9/v8-era and is populated (`internal/db/issues.go:59-104`, `UpsertIssue` writes `issue.ID` as the first column) but is otherwise unused for identity — only consumed by `internal/db/changelog.go` (`GetIssueIDToKeyMap`) for changelog bulk-fetch.

### `UpsertIssue` — `internal/db/issues.go:59-104`
Does `INSERT OR REPLACE INTO issues (id, key, ...)`. Since the table's `PRIMARY KEY` is `key` (not `id`), an incoming row with the **same `id` but a new `key`** does not replace the old row — it inserts a second, independent row. Both old and new key rows coexist under the same `id` with nothing linking them.

`GetIssueUpdated(key string) (string, error)` — `internal/db/issues.go:107-117` — used by sync to decide new-vs-updated-vs-unchanged. Looked up **by key**, so it never notices two rows share an `id`.

### Sync engine — `internal/sync/engine.go`, function `syncSource` (line 193)
- JQL is scoped per source, e.g. `project in ("ROX")` (see `sourceJQL`, called at line 208). A moved-out issue simply stops matching and stops appearing in the paginated results — nothing here reacts to "this key disappeared."
- Per-page upsert loop: `existingUpdated, _ := e.db.GetIssueUpdated(apiIssue.Key)` (line 284) → `e.db.UpsertIssue(issue, extra)` (line 294). **This is the exact spot to add move detection**: after computing `issue.ID`, look up whether a *different* key already exists locally with the same `id`, and if so, treat it as a move (migrate relations, remove/stub the old key) instead of just inserting a fresh row.
- Deletion detection call site: lines 338-347, guarded by `if full && src.JQL == ""`, iterating `src.Projects`:
  ```go
  340: if full && src.JQL == "" {
  341:     for _, project := range src.Projects {
  342:         if _, err := DetectDeletions(ctx, e.db, e.client, project); err != nil {
  343:             _ = err // non-fatal
  344:         }
  345:     }
  346:     _ = e.db.UpdateFullSyncMeta(src.Name)
  347: }
  ```
  Only runs on `--full` sync, only for `Projects:`-configured sources (raw `JQL:` sources are skipped — "no reliable scope to diff against").

### Deletion detection — `internal/sync/deletions.go` (59 lines total)
```go
13: func DetectDeletions(ctx context.Context, database *db.DB, client *jira.Client, project string) (int, error) {
15:     jql := fmt.Sprintf(`project = "%s" ORDER BY key ASC`, project)
...
48:     for _, key := range localKeys {
49:         if !remoteKeys[key] {
50:             // Mark as deleted by removing from DB (or could add is_deleted column).
51:             if _, err := database.Exec(`DELETE FROM issues WHERE key = ?`, key); err != nil {
...
54:             deleted++
```
Hard-deletes any locally-known key that the project's JQL no longer returns — with **zero distinction** between "genuinely deleted in Jira," "moved to another project," and "fell out of a narrower JQL filter." Line 50's own comment is a leftover TODO acknowledging there's no soft-delete concept. Existing test: `internal/sync/deletions_test.go` (asserts hard-delete behavior only; no move-detection test).

### Single-issue Jira API fetch — `internal/jira/client.go:198-205`
```go
func (c *Client) GetIssue(ctx context.Context, key string) (*Issue, error) {
    var issue Issue
    if err := c.get(ctx, "/rest/api/3/issue/"+key, &issue); err != nil {
        return nil, err
    }
    return &issue, nil
}
```
`Issue{ID, Key, Fields}` (`internal/jira/types.go:18-22`). This call will silently return the issue under its **new** key when Jira resolves a moved old key — `issue.Key != key` (the requested key) is the tell, but **nothing anywhere compares them.**

### CLI `jai get` — `internal/cli/get.go`
```go
24: results, err := g.query.Execute("SELECT * FROM issues WHERE key = ?", key)
...
33: if len(results.Rows) == 0 {
34:     issue, apiErr := g.jira.GetIssue(cmd.Context(), key)
...
45:     fields := issueFieldsToMap(issue.Key, &f)   // uses issue.Key (possibly different from requested `key`), never compares or records the mismatch
```
**Critical bug**: the API fallback (line 34) only fires when the local DB has **zero rows** for `key` (line 33). For `ROX-19661`, the stale row is *never zero rows* — it's a full, if outdated, row — so this fallback path never executes and the mismatch is never discovered. This is the exact mechanism behind the originally reported symptom.

### MCP `jai_get` — `internal/mcp/tools_read.go`, function `handleGet` (line 243)
Same bug, independently implemented, same shape:
```go
250: results, err := s.query.Execute("SELECT * FROM issues WHERE key = ?", key)
...
259: if len(results.Rows) == 0 {
260:     issue, apiErr := s.jira.GetIssue(ctx, key)
...
266:     data = issueFieldsToMap(issue.Key, issue.Fields)
267:     data["_source"] = "api"
```
**This file is not mentioned in `moved-issues-plan.md`** (it predates the MCP server work). Any fix must patch both `internal/cli/get.go` and `internal/mcp/tools_read.go::handleGet` — they do not share a code path.

### Sync source config — `internal/config/config.go:64-72`
```go
type SyncSource struct {
    Name     string   `yaml:"name"`
    JQL      string   `yaml:"jql"`
    Projects []string `yaml:"projects"`
}
```
Static, user-authored list (set up via `jai init`, `internal/cli/init.go:167-212`). If the issue moves into a project not covered by any configured source, the new key (`RFE-9688`) will **never sync locally**, independent of any move-detection fix. Per the design doc, do **not** auto-expand sync sources — just surface a clear message telling the user to add it.

## What to build

Follow the "Recommendation: Combine Both" section of `moved-issues-plan.md` (Option A key-aliases table + Option B `moved_to` stub row), adapted to the verified line references above. Concretely:

1. **Migration v11** (`internal/db/migrations.go`, append after the v10 block ending ~line 305+): add `key_aliases` table and `issues.moved_to` column, matching the schema in `moved-issues-plan.md` lines 66-74 and 183-186.

2. **New DB functions** (new file `internal/db/key_aliases.go` or add to `internal/db/issues.go`):
   - `RecordKeyAlias(oldKey, newKey string) error`
   - `ResolveKey(key string) (resolved string, wasMoved bool, err error)` — flatten chains on write, don't chase at read time (see plan's "What to skip").
   - `InsertMoveStub(oldKey, newKey, oldProject string) error`
   - `MigrateRelations(oldKey, newKey string) error` — updates `issue_links`, `changelog`, `pending_changes` (`UPDATE <table> SET issue_key = ? WHERE issue_key = ?`).
   - `GetKeyByID(jiraID string) (string, error)` — needed by both the sync-upsert and deletion-detection detection points; look up by the existing `id` column.

3. **Sync-upsert move detection** (`internal/sync/engine.go`, right around line 294, after `e.db.UpsertIssue(issue, extra)`): before/around the upsert, if `issue.ID` already exists locally under a different key, treat as a move — call `RecordKeyAlias` + `MigrateRelations` + delete/stub the old row instead of leaving two rows.

4. **Deletion-detection move check** (`internal/sync/deletions.go`, replace the hard-delete at lines 50-53): before `DELETE FROM issues WHERE key = ?`, check `GetKeyByID` for the local key's `id` against the *global* set of currently-known keys (not just this project) — if the same `id` now lives under a different key, it was moved, not deleted: record alias + stub instead of hard-deleting. If truly absent, keep the existing hard delete.

5. **`jai get` fix** (`internal/cli/get.go`): call `ResolveKey` (or check for a stub row) *before* the "zero rows" branch at line 33, not only after. If the local row itself has `moved_to` set (stub), or `ResolveKey` returns a different key, resolve and re-query, printing a notice (`"Note: ROX-19661 was moved to RFE-9688"`) per the plan's step 3. Also keep the existing zero-rows→API-fallback path, but add the `issue.Key != key` comparison there too (record the alias if discovered this way) — this covers issues that were moved but never had a stale local row to begin with.

6. **MCP `jai_get` fix** (`internal/mcp/tools_read.go::handleGet`, lines 243-274): mirror the same fix as #5. This is a **separate code path** from the CLI — must be patched independently, and should behave consistently (e.g. same notice text or a `data["_moved_from"]` / `data["_moved_to"]` field so agents can detect the redirect programmatically, since MCP callers are code, not humans reading stderr).

7. **Other CLI commands** that accept a key argument (`view`, `clone`, `set`, `comment`, `transition` — check `internal/cli/` for the current file names) — wire in `ResolveKey` per plan step 3. Use `tilth_search` to find these commands' current query-by-key call sites rather than assuming file names/line numbers, since they weren't traced in this investigation.

8. **Sync-source gap messaging**: when a move is detected (any detection point) and the new key's project isn't in any configured `sync_sources` entry, print/log the guidance block from the plan (lines 273-280) — don't auto-add it.

## Explicitly out of scope (per the existing plan's "What to skip")
- Alias chains beyond a single flattened hop.
- Automatic sync-source expansion.
- Switching `issues`' primary key from `key` to `id` (too large a refactor for this).
- Intercepting/rewriting raw SQL in `jai query` to resolve aliases — rely on the stub row for that path instead (per plan, this is Option B's whole purpose).

## Test plan
- Unit tests: `ResolveKey`, `RecordKeyAlias`, `MigrateRelations`, `InsertMoveStub`, `GetKeyByID` (new file, e.g. `internal/db/key_aliases_test.go`).
- Sync test: extend `internal/sync/deletions_test.go` (or add `internal/sync/moves_test.go`) with a case where the mock Jira server returns an issue under a new key with an `id` matching an existing local row under an old key — assert alias recorded, relations migrated, old row stubbed/removed, no duplicate rows.
- `internal/cli/get.go` and `internal/mcp/tools_read.go::handleGet`: add test cases where a stale local row's key resolves via alias to a different key, asserting the notice/metadata is surfaced and the *current* data is returned (not the stale row's fields).
- Run `make check` (vet + `go test -tags fts5 ./...`) before considering this done — this repo requires the `fts5` build tag or tests fail with `no such module: fts5`.

## Acceptance criteria
- `jai get ROX-19661` (or equivalent MCP `jai_get`) after a move to `RFE-9688`, with `RFE-9688` synced locally, returns the **current** RFE-9688 data with a visible notice that it moved — not stale ROX-19661 data.
- `jai query "SELECT * FROM issues WHERE key = 'ROX-19661'"` returns a breadcrumb stub row, not zero rows and not stale full data.
- `issue_links`, `changelog`, `pending_changes` rows that referenced the old key now reference the new key (no orphans).
- `jai sync --full` no longer silently hard-deletes moved issues without a trace.
- If the new project isn't in `sync_sources`, the user gets a clear, actionable message — not silence.
