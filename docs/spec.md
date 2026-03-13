# Technical Spec: jai

## Overview

jai is a Go CLI tool that syncs Jira Cloud data to a local SQLite database and exposes it via SQL queries, a hybrid CLI, and a full-screen TUI. It targets AI agents (compact structured output, schema introspection) and humans (interactive TUI, saved views) equally.

---

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                      jai binary                      │
│                                                      │
│  ┌──────────┐  ┌──────────┐  ┌───────────────────┐  │
│  │  CLI     │  │  TUI     │  │  Agent Output     │  │
│  │  (cobra) │  │  (btea)  │  │  (--json)         │  │
│  └────┬─────┘  └────┬─────┘  └────────┬──────────┘  │
│       │              │                 │              │
│       └──────────────┼─────────────────┘              │
│                      │                                │
│              ┌───────┴────────┐                       │
│              │   Query Engine │                       │
│              │   (SQL + FTS5) │                       │
│              └───────┬────────┘                       │
│                      │                                │
│              ┌───────┴────────┐                       │
│              │   SQLite DB    │                       │
│              │   (WAL mode)   │                       │
│              └───────┬────────┘                       │
│                      │                                │
│              ┌───────┴────────┐                       │
│              │   Sync Engine  │                       │
│              └───────┬────────┘                       │
│                      │                                │
└──────────────────────┼──────────────────────────────┘
                       │
               ┌───────┴────────┐
               │ Jira Cloud API │
               │    (v3)        │
               └────────────────┘
```

### Component Responsibilities

| Component | Responsibility |
|-----------|---------------|
| **CLI** | Command parsing, flag handling, human-formatted output |
| **TUI** | Full-screen interactive views, keyboard handling, live refresh |
| **Agent Output** | Compact JSON serialization, schema introspection |
| **Query Engine** | SQL execution, field name resolution, FTS5 search, result formatting |
| **SQLite DB** | Data storage, FTS5 indexing, write queue, field mapping, sync metadata |
| **Sync Engine** | Jira API communication, incremental/full sync, write queue processing, field discovery |

---

## Project Structure

```
jai/
├── cmd/
│   └── jai/
│       └── main.go                 # Entry point
├── internal/
│   ├── cli/
│   │   ├── root.go                 # Root cobra command, global flags
│   │   ├── get.go                  # jai get
│   │   ├── query.go                # jai query
│   │   ├── search.go               # jai search
│   │   ├── view.go                 # jai view
│   │   ├── sync.go                 # jai sync
│   │   ├── push.go                 # jai push
│   │   ├── set.go                  # jai set
│   │   ├── comment.go              # jai comment
│   │   ├── fields.go               # jai fields
│   │   ├── schema.go               # jai schema
│   │   ├── status.go               # jai status
│   │   ├── init.go                 # jai init (wizard)
│   │   └── tui.go                  # jai tui (launches TUI)
│   ├── config/
│   │   ├── config.go               # Config loading, env var substitution
│   │   ├── views.go                # View definition parsing
│   │   └── defaults.go             # Default views and config values
│   ├── db/
│   │   ├── db.go                   # Connection management, WAL setup, migrations
│   │   ├── schema.go               # Table creation, FTS5 setup, triggers
│   │   ├── issues.go               # Issue CRUD, upsert logic
│   │   ├── comments.go             # Comment storage
│   │   ├── changelog.go            # Changelog storage (optional)
│   │   ├── fields.go               # Field map operations
│   │   ├── pending.go              # Write queue operations
│   │   ├── sync_meta.go            # Sync metadata operations
│   │   └── migrations.go           # Schema version tracking, migrations
│   ├── jira/
│   │   ├── client.go               # HTTP client, auth, rate limiting, pagination
│   │   ├── issues.go               # Issue fetching, field expansion
│   │   ├── fields.go               # Field metadata discovery
│   │   ├── write.go                # Issue updates, comments, transitions
│   │   └── types.go                # Jira API response types
│   ├── sync/
│   │   ├── engine.go               # Sync orchestration (incremental, full)
│   │   ├── denormalize.go          # Raw JSON → DB columns extraction
│   │   ├── deletions.go            # Deletion detection
│   │   └── writer.go               # Write queue → Jira API processor
│   ├── query/
│   │   ├── engine.go               # SQL execution, field name resolution
│   │   ├── formatter.go            # Result → table / JSON formatting
│   │   └── template.go             # {{me}}, {{team}} variable resolution
│   ├── tui/
│   │   ├── app.go                  # Root bubbletea model, layout
│   │   ├── table.go                # Table view component
│   │   ├── detail.go               # Issue detail split pane
│   │   ├── filter.go               # Inline filter component
│   │   ├── tabs.go                 # View tab bar
│   │   ├── grouping.go             # Group by logic and rendering
│   │   ├── hierarchy.go            # Epic → child expansion
│   │   ├── colors.go               # Color rule evaluation
│   │   ├── statusbar.go            # Status summary bar
│   │   ├── editor.go               # Inline field editing
│   │   ├── keys.go                 # Keybinding definitions
│   │   └── sync.go                 # Background sync integration
│   └── output/
│       ├── json.go                 # Compact JSON output (agent mode)
│       ├── table.go                # Human-readable table output
│       └── schema.go               # Schema introspection output
├── config.example.yaml
├── go.mod
├── go.sum
├── Makefile
├── README.md
└── .goreleaser.yaml
```

---

## Data Model

### SQLite Configuration

```go
// Connection setup
pragmas := []string{
    "PRAGMA journal_mode=WAL",       // Concurrent reads during writes
    "PRAGMA busy_timeout=30000",     // 30s wait on locks
    "PRAGMA foreign_keys=ON",
    "PRAGMA synchronous=NORMAL",     // Faster writes, safe with WAL
    "PRAGMA cache_size=-64000",      // 64MB cache
}
```

Build tag: `go build -tags fts5`

### Schema

#### `issues` Table

Dynamic columns based on field_map. The schema is generated at sync time.

**Fixed columns** (always present):

| Column | Type | Description |
|--------|------|-------------|
| `key` | TEXT PRIMARY KEY | Issue key (ROX-123) |
| `project` | TEXT NOT NULL | Project key |
| `type` | TEXT | Issue type name |
| `summary` | TEXT NOT NULL | Issue summary |
| `description` | TEXT | Description (plaintext, stripped from ADF) |
| `status` | TEXT | Status name |
| `status_category` | TEXT | statusCategory.name (To Do, In Progress, Done) |
| `priority` | TEXT | Priority name |
| `assignee` | TEXT | Assignee display name |
| `assignee_email` | TEXT | Assignee email |
| `reporter` | TEXT | Reporter display name |
| `created` | DATETIME | Created timestamp (ISO 8601) |
| `updated` | DATETIME | Updated timestamp (ISO 8601) |
| `resolved` | DATETIME | Resolution timestamp |
| `labels` | TEXT | Comma-separated labels |
| `components` | TEXT | Comma-separated component names |
| `fix_version` | TEXT | Fix version name |
| `parent_key` | TEXT | Parent issue key (for hierarchy) |
| `epic_key` | TEXT | Epic link key |
| `story_points` | REAL | Story points (if standard field) |
| `raw_json` | TEXT | Complete Jira API JSON response |
| `synced_at` | DATETIME | Last sync timestamp |

**Dynamic columns** (added from field_map):

Custom fields get columns named by their mapped readable name. Column type is determined by Jira field schema:
- `string`, `option`, `user` → `TEXT`
- `number` → `REAL`
- `date`, `datetime` → `DATETIME`
- `array` → `TEXT` (comma-separated)

When a new custom field is discovered during sync, jai runs `ALTER TABLE issues ADD COLUMN` and backfills from `raw_json`.

#### `comments` Table

| Column | Type | Description |
|--------|------|-------------|
| `id` | TEXT PRIMARY KEY | Comment ID |
| `issue_key` | TEXT NOT NULL | FK → issues.key |
| `author` | TEXT | Author display name |
| `author_email` | TEXT | Author email |
| `body` | TEXT | Comment body (plaintext from ADF) |
| `created` | DATETIME | Created timestamp |
| `updated` | DATETIME | Updated timestamp |

Index: `CREATE INDEX idx_comments_issue ON comments(issue_key)`

#### `issues_fts` Virtual Table

```sql
CREATE VIRTUAL TABLE issues_fts USING fts5(
    key UNINDEXED,
    summary,
    description,
    comments_text,    -- concatenated comments for this issue
    labels,
    -- additional fields from config search.fts_fields
    content='issues',
    content_rowid='rowid',
    tokenize='porter unicode61'
);
```

Kept in sync via triggers on the `issues` table:

```sql
CREATE TRIGGER issues_fts_insert AFTER INSERT ON issues BEGIN
    INSERT INTO issues_fts(rowid, key, summary, description, comments_text, labels)
    VALUES (new.rowid, new.key, new.summary, new.description, new.comments_text, new.labels);
END;

CREATE TRIGGER issues_fts_update AFTER UPDATE ON issues BEGIN
    INSERT INTO issues_fts(issues_fts, rowid, key, summary, description, comments_text, labels)
    VALUES ('delete', old.rowid, old.key, old.summary, old.description, old.comments_text, old.labels);
    INSERT INTO issues_fts(rowid, key, summary, description, comments_text, labels)
    VALUES (new.rowid, new.key, new.summary, new.description, new.comments_text, new.labels);
END;

CREATE TRIGGER issues_fts_delete AFTER DELETE ON issues BEGIN
    INSERT INTO issues_fts(issues_fts, rowid, key, summary, description, comments_text, labels)
    VALUES ('delete', old.rowid, old.key, old.summary, old.description, old.comments_text, old.labels);
END;
```

#### `changelog` Table (optional)

| Column | Type | Description |
|--------|------|-------------|
| `id` | TEXT PRIMARY KEY | Changelog entry ID |
| `issue_key` | TEXT NOT NULL | FK → issues.key |
| `author` | TEXT | Who made the change |
| `field` | TEXT | Field that changed |
| `field_type` | TEXT | Field type |
| `from_value` | TEXT | Previous value |
| `from_string` | TEXT | Previous value (display) |
| `to_value` | TEXT | New value |
| `to_string` | TEXT | New value (display) |
| `changed_at` | DATETIME | When the change was made |

Index: `CREATE INDEX idx_changelog_issue ON changelog(issue_key)`
Index: `CREATE INDEX idx_changelog_time ON changelog(changed_at)`

#### `pending_changes` Table

| Column | Type | Description |
|--------|------|-------------|
| `id` | INTEGER PRIMARY KEY | Auto-increment |
| `issue_key` | TEXT NOT NULL | Target issue |
| `operation` | TEXT NOT NULL | `set_field`, `add_comment`, `transition` |
| `payload` | TEXT NOT NULL | JSON operation details |
| `created_at` | DATETIME | When queued |
| `synced_at` | DATETIME | When pushed (NULL = pending) |
| `retry_count` | INTEGER DEFAULT 0 | Attempt count |
| `last_error` | TEXT | Last error message |

Operations payload format:

```json
// set_field
{"field": "status", "value": "In Progress"}

// add_comment
{"body": "Fixed in PR #42"}

// transition
{"transition_id": "31", "transition_name": "Start Progress"}
```

#### `sync_metadata` Table

| Column | Type | Description |
|--------|------|-------------|
| `project` | TEXT PRIMARY KEY | Project key |
| `last_sync_time` | DATETIME | Last incremental sync |
| `last_full_sync` | DATETIME | Last full sync |
| `issues_total` | INTEGER | Total issues in project (from Jira) |
| `issues_synced` | INTEGER | Issues in local DB |
| `last_sync_duration` | REAL | Seconds taken |
| `last_sync_error` | TEXT | Error from last sync (NULL = success) |

#### `field_map` Table

| Column | Type | Description |
|--------|------|-------------|
| `jira_id` | TEXT PRIMARY KEY | `customfield_12345` or standard field ID |
| `jira_name` | TEXT | Name from Jira API (`Custom Team Field`) |
| `name` | TEXT NOT NULL UNIQUE | Resolved column name (`team`) |
| `type` | TEXT NOT NULL | `text`, `number`, `date`, `datetime`, `array`, `option`, `user` |
| `is_custom` | BOOLEAN | True for custom fields |
| `is_column` | BOOLEAN DEFAULT 0 | True if a column exists in `issues` table |
| `user_override` | BOOLEAN DEFAULT 0 | True if name was overridden in config |
| `searchable` | BOOLEAN DEFAULT 1 | Include in FTS5 index |

#### `schema_version` Table

| Column | Type | Description |
|--------|------|-------------|
| `version` | INTEGER PRIMARY KEY | Current schema version |
| `applied_at` | DATETIME | When migration was applied |
| `description` | TEXT | Migration description |

### Schema Migrations

Migrations are embedded Go files, run sequentially on startup:

```go
var migrations = []Migration{
    {Version: 1, Description: "initial schema", Up: migrateV1},
    {Version: 2, Description: "add changelog table", Up: migrateV2},
    // ...
}
```

Dynamic column additions (new custom fields) are handled separately from schema migrations — they're part of the sync process, not the migration process.

### Indexes

```sql
CREATE INDEX idx_issues_project ON issues(project);
CREATE INDEX idx_issues_status ON issues(status);
CREATE INDEX idx_issues_assignee ON issues(assignee);
CREATE INDEX idx_issues_updated ON issues(updated);
CREATE INDEX idx_issues_type ON issues(type);
CREATE INDEX idx_issues_priority ON issues(priority);
CREATE INDEX idx_issues_parent ON issues(parent_key);
CREATE INDEX idx_issues_epic ON issues(epic_key);
CREATE INDEX idx_issues_synced ON issues(synced_at);
```

---

## Jira Cloud API Integration

### Authentication

```
Authorization: Basic base64(email:api_token)
Accept: application/json
Content-Type: application/json
```

### Client Design

```go
type Client struct {
    baseURL    string
    email      string
    token      string
    httpClient *http.Client
    limiter    *rate.Limiter  // golang.org/x/time/rate
}
```

**Rate limiting**: Use `golang.org/x/time/rate` token bucket. Default: 10 req/s (Jira Cloud allows ~10 req/s per user). Respect `Retry-After` header on 429 responses.

**Retry strategy**: Exponential backoff with jitter on 429 and 5xx responses. Max 3 retries. No retry on 4xx (except 429).

**Timeout**: 30 seconds per request.

### Endpoints Used

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/rest/api/3/myself` | GET | Connection test |
| `/rest/api/3/search` | GET | Issue search (paginated) |
| `/rest/api/3/issue/{key}` | GET | Single issue fetch |
| `/rest/api/3/issue/{key}` | PUT | Update issue fields |
| `/rest/api/3/issue/{key}/comment` | POST | Add comment |
| `/rest/api/3/issue/{key}/transitions` | GET/POST | Get/execute transitions |
| `/rest/api/3/field` | GET | Field metadata discovery |
| `/rest/api/3/project` | GET | List projects (for init wizard) |

### Pagination

```go
func (c *Client) SearchAll(jql string, fields []string) iter.Seq2[*Issue, error] {
    // Returns a Go 1.23 iterator over all matching issues
    // Internally handles startAt/maxResults pagination
    // Page size: 100 (Jira Cloud max)
}
```

### ADF (Atlassian Document Format) Handling

Jira Cloud v3 returns rich text as ADF JSON. jai converts to plaintext for storage:

```go
func ADFToPlaintext(adf json.RawMessage) string {
    // Walk the ADF tree, extract text nodes
    // Preserve paragraph breaks as newlines
    // Strip formatting, links, mentions
}
```

Store plaintext in `description` and `comments.body` columns. Raw ADF is preserved in `raw_json`.

---

## Sync Engine

### Incremental Sync Flow

```
1. Lock: acquire sync lock (file lock or DB advisory lock)
2. Read sync_metadata.last_sync_time for each project
3. For each project:
   a. JQL: "project = {key} AND updated >= '{last_sync_time}' ORDER BY updated ASC"
   b. Paginate through all results
   c. For each issue:
      - Upsert raw_json
      - Denormalize fields → columns (using field_map for name resolution)
      - Upsert comments (if changed)
      - Upsert changelog entries (if sync.history: true)
      - Concatenate comments into comments_text for FTS
   d. Update sync_metadata
4. Release sync lock
```

### Denormalization

```go
func Denormalize(rawJSON []byte, fieldMap map[string]FieldMapping) map[string]interface{} {
    // Parse raw JSON
    // For each field in fieldMap:
    //   - Extract value from JSON using jira_id path
    //   - Convert to appropriate Go type
    //   - Map to readable column name
    // Return column name → value map for INSERT/UPDATE
}
```

**Type conversion rules**:

| Jira Schema Type | Go Type | SQLite Type | Extraction |
|------------------|---------|-------------|------------|
| `string` | `string` | TEXT | Direct |
| `number` | `float64` | REAL | Direct |
| `date` | `string` | DATETIME | Direct (ISO 8601) |
| `datetime` | `string` | DATETIME | Direct (ISO 8601) |
| `option` | `string` | TEXT | `.value` |
| `user` | `string` | TEXT | `.displayName` |
| `array[string]` | `string` | TEXT | Join with `,` |
| `array[option]` | `string` | TEXT | Map `.value`, join with `,` |
| `array[user]` | `string` | TEXT | Map `.displayName`, join with `,` |
| `any` (ADF) | `string` | TEXT | ADFToPlaintext |

### Deletion Detection

Run periodically (not every incremental sync — too expensive):

```
1. For each project, count issues via Jira API: GET /search?jql=project={key}&maxResults=0 → total
2. Compare with local count
3. If local > remote, run a full key reconciliation:
   a. Fetch all keys from Jira (SELECT key only, paginated)
   b. Compare with local keys
   c. Mark missing keys as is_deleted=1, deleted_at=NOW()
```

This runs during `jai sync --full` and optionally on a configurable schedule.

### Write Queue Processing

```go
func (w *Writer) ProcessQueue(ctx context.Context) error {
    // 1. SELECT from pending_changes WHERE synced_at IS NULL ORDER BY created_at
    // 2. For each change:
    //    a. Build Jira API request from operation + payload
    //    b. Call API
    //    c. On success: UPDATE synced_at = NOW()
    //    d. On failure: UPDATE retry_count++, last_error = err
    //    e. Skip if retry_count > 5 (log permanent failure)
    // 3. Return summary of processed/failed
}
```

### Field Discovery

During first sync or `jai sync --discover-fields`:

```go
func (c *Client) DiscoverFields() ([]JiraField, error) {
    // GET /rest/api/3/field
    // Returns all fields (standard + custom)
}

func InferColumnName(jiraField JiraField) string {
    // Standard fields: use known mapping (summary, status, priority, etc.)
    // Custom fields:
    //   1. Use config override if present
    //   2. Slugify jiraField.Name: "Custom Team Field" → "custom_team_field"
    //   3. Ensure uniqueness (append _2, _3 if collision)
}
```

---

## Query Engine

### Field Name Resolution

All SQL queries pass through a resolution layer that maps readable names to actual column names:

```go
func (e *Engine) Execute(sql string, args ...interface{}) (*Results, error) {
    // 1. Resolve template variables: {{me}}, {{team}}, etc.
    // 2. Execute SQL against DB
    // 3. Map result column names back to readable names
    // 4. Return structured results
}
```

Users write SQL using readable field names. Since those ARE the actual column names in the DB, no SQL rewriting is needed — field resolution happens at the schema level, not the query level.

### Result Formatting

```go
type Results struct {
    Columns []string          // Column names
    Rows    [][]interface{}   // Row data
    Count   int               // Row count
}

// Human output: formatted table (using lipgloss)
func (r *Results) Table() string

// Agent output: compact JSON
func (r *Results) JSON() []byte
// {"ok":true,"columns":["key","summary"],"rows":[["ROX-123","Fix bug"]]}
```

### Template Variables

Resolved before SQL execution:

| Variable | Source | Example |
|----------|--------|---------|
| `{{me}}` | `config.me` | `me@company.com` |
| `{{team}}` | `config.team` | `ACS Scanner` |
| `{{version}}` | CLI flag `--version` or config default | `4.5.0` |
| `{{project}}` | CLI flag `--project` or first synced project | `ROX` |
| `{{today}}` | Current date | `2026-03-13` |
| `{{week_ago}}` | 7 days ago | `2026-03-06` |

---

## CLI Contracts

### Global Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--json` | bool | false | Compact JSON output |
| `--fields` | string | all | Comma-separated field names to include |
| `--no-sync` | bool | false | Skip auto-sync |
| `--config` | string | `~/.config/jai/config.yaml` | Config file path |
| `--db` | string | from config | Database file path |

### Command Contracts

#### `jai get <key>`

```
Input:  issue key (positional)
Flags:  --json, --fields
Output: Single issue, all fields (or selected fields)

Human output:
  ROX-123  Fix authentication bug
  Status:    In Progress
  Priority:  High
  Assignee:  Jane Doe
  Team:      ACS Scanner
  Created:   2026-03-01
  Updated:   2026-03-12
  Labels:    security, auth
  ...

Agent output (--json):
  {"ok":true,"data":{"key":"ROX-123","summary":"Fix authentication bug","status":"In Progress",...}}

Agent output (--json --fields key,summary,status):
  {"ok":true,"data":{"key":"ROX-123","summary":"Fix authentication bug","status":"In Progress"}}
```

#### `jai query <sql>`

```
Input:  SQL string (positional)
Flags:  --json, --fields (overrides SELECT columns in output)
Output: Table of results

Human output:
  KEY       | SUMMARY                  | STATUS
  ROX-123   | Fix authentication bug   | In Progress
  ROX-456   | Update docs              | To Do
  (2 rows)

Agent output (--json):
  {"ok":true,"columns":["key","summary","status"],"rows":[["ROX-123","Fix authentication bug","In Progress"],["ROX-456","Update docs","To Do"]],"count":2}
```

#### `jai search <text>`

```
Input:  search text (positional)
Flags:  --json, --fields, --limit (default 20)
Output: FTS5 ranked results

Internally executes:
  SELECT key, summary, rank FROM issues_fts
  WHERE issues_fts MATCH '{text}'
  ORDER BY rank
  LIMIT {limit}
```

#### `jai view <name>`

```
Input:  view name from config (positional)
        If omitted, lists available views
Flags:  --json, --fields, template variable overrides (--version, --team, etc.)
Output: Results of the view's SQL query
```

#### `jai fields`

```
Input:  none
Flags:  --json, --filter (filter by name pattern)
Output: All fields with their mappings

Human output:
  NAME               | JIRA ID             | TYPE    | FTS
  key                | key                 | text    |
  summary            | summary             | text    | *
  status             | status              | text    |
  team               | customfield_12345   | option  |
  story_points       | customfield_67890   | number  |
  ...
  (42 fields)

Agent output (--json):
  {"ok":true,"fields":[{"name":"key","jira_id":"key","type":"text","searchable":false},...],"count":42}
```

#### `jai schema <command>`

```
Input:  command name (positional)
        If omitted, lists all commands
Flags:  --json (always JSON, this is for agents)
Output: Command parameter schema + available output fields

  {"command":"get","params":{"key":{"type":"string","required":true,"description":"Issue key"}},"flags":{"fields":{"type":"string"},"json":{"type":"bool"}},"output_fields":["key","summary","status",...]}
```

#### `jai sync`

```
Flags:  --full (full resync), --project (specific project)
Output: Sync progress and summary

  Syncing ROX... 150 issues (12 new, 138 updated) in 8.2s
  Syncing ACS... 43 issues (3 new, 40 updated) in 2.1s
  Done. 193 issues synced.
```

#### `jai set <key> <field> <value>`

```
Input:  issue key, field name, new value (positional)
Output: Confirmation + pending status

  ROX-123: status → "In Progress" (pending sync)

Writes to pending_changes table. If auto_sync is on, immediately attempts to push.
```

#### `jai comment <key> <text>`

```
Input:  issue key, comment text (positional)
Output: Confirmation + pending status

  ROX-123: comment added (pending sync)
```

#### `jai push`

```
Output: Push queue processing summary

  Pushing 3 pending changes...
  ✓ ROX-123: status → "In Progress"
  ✓ ROX-123: comment added
  ✗ ROX-456: assignee → "jane" (error: user not found)
  2 succeeded, 1 failed
```

#### `jai status`

```
Output: Sync and queue status

  Projects:
    ROX: 1,234 issues, last sync 5m ago
    ACS: 456 issues, last sync 5m ago

  Pending changes: 1
    ROX-456: assignee → "jane" (failed, 2 retries)

  DB size: 245 MB
```

#### `jai init`

Interactive wizard. No flags — fully guided.

```
  Welcome to jai! Let's get you set up.

  Jira URL: https://mycompany.atlassian.net
  Email: me@company.com
  API Token: ••••••••••••

  Testing connection... ✓ Connected as Jane Doe

  Available projects:
    [x] ROX - Red Hat OpenShift Security
    [x] ACS - Advanced Cluster Security
    [ ] PLAT - Platform
  Select projects to sync (space to toggle, enter to confirm):

  Starting sync in background...
  ████████████████████░░░░░░░░░░  68% (856/1,234 issues)

  Custom fields discovered:
    customfield_12345 → "Custom Team Field" → team
    customfield_67890 → "Story Points" → story_points
    (override names in ~/.config/jai/config.yaml)

  Config saved to ~/.config/jai/config.yaml
  Database at ~/.local/share/jai/jai.db

  Sync complete! 1,690 issues synced in 2m 15s.

  Next steps:
    jai query "SELECT key, summary, status FROM issues LIMIT 10"
    jai tui
    jai --help
```

---

## TUI Architecture

### Bubbletea Model Hierarchy

```
App (root model)
├── TabBar (view tabs)
├── ActiveView
│   ├── Table (sortable, scrollable)
│   │   ├── Header (column names, sort indicators)
│   │   ├── GroupRows (collapsible groups)
│   │   │   └── DataRows
│   │   └── StatusSummary (bottom bar)
│   └── DetailPane (split view, optional)
│       ├── IssueHeader
│       ├── FieldList
│       └── CommentList
├── FilterInput (overlay when / pressed)
├── CommandPalette (overlay when : pressed)
└── SyncIndicator (top-right, shows sync status)
```

### Data Flow

```
Config (views.yaml)
    → SQL query + template variables
    → Query Engine
    → Results
    → Table Model (sort, group, filter in memory)
    → Render
```

The TUI re-queries the DB when:
- User switches views
- Background sync completes (new data available)
- User applies a filter or changes grouping (filter is in-memory on current results, but heavy filters can re-query)

### Sync Integration

```go
type SyncMsg struct {
    Project string
    Added   int
    Updated int
    Error   error
}

// Background goroutine sends SyncMsg via bubbletea's program.Send()
// TUI refreshes current view query on receiving SyncMsg
```

### Pending Change Highlighting

Issues with pending (unsynced) changes display a visual indicator:

```
  KEY       | SUMMARY                  | STATUS
  ROX-123   | Fix authentication bug   | In Progress ⟳
  ROX-456   | Update docs              | To Do
```

The `⟳` marker (or color highlight) shows that ROX-123 has a pending status change that hasn't been confirmed by sync yet. Cleared once incremental sync returns the confirmed state.

### Color Rules Evaluation

```go
type ColorRule struct {
    Field     string        // column name
    Condition string        // "older_than", "equals", "not_equals", "contains"
    Value     string        // "28d", "Blocked", "Done"
    Color     lipgloss.Color
}

func (r ColorRule) Matches(row Row) bool {
    // Evaluate condition against row's field value
    // For "older_than": parse duration, compare with time.Now()
    // For "equals": string comparison
}
```

### View Config Schema

```go
type ViewConfig struct {
    Name          string       `yaml:"-"`           // map key
    Title         string       `yaml:"title"`
    Query         string       `yaml:"query"`       // SQL with {{template}} vars
    Columns       []string     `yaml:"columns"`     // display columns (subset of SELECT)
    GroupBy       string       `yaml:"group_by"`     // column to group by
    ColorRules    []ColorRule  `yaml:"color_rules"`
    StatusSummary bool         `yaml:"status_summary"` // show status counts
    SortBy        string       `yaml:"sort_by"`     // default sort column
    SortDesc      bool         `yaml:"sort_desc"`   // default sort direction
}
```

---

## Error Handling

### Error Types

```go
type Error struct {
    Type    string `json:"type"`
    Message string `json:"message"`
}

// Error types:
// - ConfigError: invalid config, missing required fields
// - AuthError: invalid credentials, expired token
// - ConnectionError: can't reach Jira
// - SyncError: sync failed (partial or complete)
// - QueryError: invalid SQL, missing table/column
// - NotFoundError: issue not found in local DB
// - PendingError: write queue operation failed
```

### Output

Human mode: Colored error message to stderr.
Agent mode (`--json`): Structured error to stdout.

```json
{"ok":false,"error":{"type":"QueryError","message":"no such column: teem - did you mean 'team'?"}}
```

### Typo Suggestions

For `QueryError` with unknown columns, suggest closest match from field_map using Levenshtein distance.

---

## Security

- **API tokens**: Never stored in config files directly. Use `${JAI_TOKEN}` env var substitution. `jai init` wizard prompts for token and writes `${JAI_TOKEN}` placeholder.
- **DB file permissions**: Created with `0600` (owner read/write only).
- **Config file permissions**: Warning if config file is world-readable (contains URL which could reveal org name).
- **No credential logging**: API tokens masked in all log output.
- **SQL injection**: Not applicable — users intentionally write SQL. The DB is local and read-only for its primary use case. Write operations are typed (set_field, add_comment) not raw SQL.

---

## Performance Targets

| Operation | Target |
|-----------|--------|
| `jai get <key>` | < 50ms |
| `jai query` (simple) | < 100ms |
| `jai query` (complex JOIN/CTE) | < 500ms |
| `jai search` (FTS5) | < 100ms |
| TUI view render | < 50ms |
| TUI view switch | < 100ms |
| Incremental sync (100 issues) | < 30s |
| Full sync (10K issues) | < 5m |
| Full sync (30K issues) | < 10m |
| DB size per 1K issues | ~5-10 MB |

---

## Testing Strategy

### Unit Tests
- **db/**: Schema creation, upsert logic, field mapping, FTS5 triggers, migrations
- **sync/denormalize.go**: JSON → column extraction for various Jira field types
- **query/template.go**: Template variable resolution
- **jira/types.go**: Jira API response parsing
- **output/**: JSON and table formatting

### Integration Tests
- **sync/engine.go**: Full sync flow with mocked HTTP server (httptest)
- **query/engine.go**: SQL execution against real SQLite DB with test data
- **db/migrations.go**: Migration sequence from v1 → latest

### E2E Tests
- CLI command execution via `exec.Command` against a test DB
- TUI smoke tests (bubbletea has testing utilities via `teatest`)

### Test Fixtures
- Jira API response fixtures as JSON files in `testdata/`
- Pre-populated SQLite DBs for query tests
- Config file variations for config parsing tests
