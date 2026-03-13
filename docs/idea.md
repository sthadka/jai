# jai — Query Jira with SQL

## Problem

Jira is everywhere, but it's painful to work with programmatically. The REST API returns 50KB+ per issue. JQL can't do JOINs, aggregations, or full-text ranking. Existing CLI tools mirror the web UI's complexity and return unstructured, token-heavy output.

AI agents waste thousands of tokens parsing bloated responses when they need three fields. Engineers context-switch to a slow web UI for operations that should take seconds. There's no terminal equivalent of Jira Plan.

## Context

**Target users**: AI agents, engineers, engineering managers, DevOps teams
**Target platform**: Jira Cloud (API v3)
**Language**: Go
**Distribution**: Homebrew, single binary
**License**: Open source

**Existing work**: A production Python implementation ([jira-search](https://github.com/sthadka/jira-search)) proves the SQLite/FTS5 offline pattern works at scale (~1GB DB, sub-second queries). jai is the Go successor — faster, more portable, agent-optimized, with a full-screen TUI.

## Core Idea

**jai syncs your Jira data to a local SQLite database and lets you query it with SQL.**

This single design choice unlocks everything:

- **For agents**: Select exactly the fields you need. One SQL query replaces five API round trips. Token usage drops by 10-50x.
- **For humans**: Full-screen TUI powered by instant local queries. Custom views defined in YAML. No web browser needed.
- **For everyone**: JOINs, aggregations, CTEs, window functions, full-text search — all the things JQL can't do.

### How It Works

```
1. jai sync          # Download all issues to local SQLite DB
2. jai query "..."   # Query with SQL
3. jai tui           # Full-screen interactive view
```

Sync is incremental by default. Runs automatically in TUI mode and opportunistically during CLI commands (disable with `--no-sync`). All fields are downloaded as raw JSON and denormalized into queryable columns — no resync needed when you want a new field.

Write operations go to a local queue table and sync back to Jira asynchronously. This means jai works offline, persists updates even when the API is unreachable, and can highlight stale data in the TUI.

Users can configure which fields are included in the FTS5 full-text search index (e.g., summary, description, comments) via config.

---

## Architecture

### DB-First Design

The local SQLite database is the single source of truth for all operations. No command ever hits the Jira API directly for reads.

```
Jira Cloud API v3
    ↕ (sync)
SQLite DB (FTS5, WAL mode)
    ↕ (query)
CLI commands / TUI / Agent output
```

**Storage**: Raw JSON blob per issue + denormalized columns for all standard and custom fields. FTS5 virtual table for configurable full-text search fields. WAL mode for concurrent read/write.

**Custom field mapping**: During first sync, jai fetches Jira's field metadata and auto-infers readable names (e.g., `customfield_12345` → `Team`). These mappings are stored in the `field_map` DB table. Users can view them with `jai fields` and override inferred names in config. The DB is the source of truth; config overrides are layered on top.

**Write queue**: Updates written to a `pending_changes` table. A background goroutine (in TUI) or explicit `jai push` command syncs them to Jira. Next incremental sync confirms the changes landed.

### Tech Stack

| Component | Choice |
|-----------|--------|
| CLI framework | cobra |
| TUI framework | bubbletea (lipgloss, bubbles) |
| SQLite | mattn/go-sqlite3 (CGO, FTS5 via build tag) |
| Cross-compilation | zig cc |
| Config | YAML + environment variable substitution |
| Auth | Jira Cloud API Token + email |
| Testing | stdlib + httptest mocks |

---

## CLI Design

### Hybrid Interface

Every command works for both humans and agents. Human-readable output is default; `--json` switches to compact, structured output. `--fields` selects specific fields.

```bash
# Human
jai get ROX-123

# Agent
jai get ROX-123 --json --fields key,summary,status,assignee
```

### Core Commands

**Data Access**

| Command | Description | Example |
|---------|-------------|---------|
| `jai get <key>` | Get a single issue | `jai get ROX-123` |
| `jai query <sql>` | Run SQL against local DB | `jai query "SELECT key, summary FROM issues WHERE status = 'Open'"` |
| `jai search <text>` | Full-text search (FTS5) | `jai search "memory leak"` |
| `jai view <name>` | Run a saved view (from config) | `jai view my-issues` |
| `jai fields` | List available fields with mappings | `jai fields` |
| `jai schema <command>` | Show command schema (for agents) | `jai schema get` |

**Sync**

| Command | Description | Example |
|---------|-------------|---------|
| `jai sync` | Incremental sync (default) | `jai sync` |
| `jai sync --full` | Full resync | `jai sync --full` |
| `jai push` | Push pending writes to Jira | `jai push` |
| `jai status` | Show sync status, pending changes | `jai status` |

**Mutate**

| Command | Description | Example |
|---------|-------------|---------|
| `jai set <key> <field> <value>` | Update a field | `jai set ROX-123 status "In Progress"` |
| `jai comment <key> <text>` | Add a comment | `jai comment ROX-123 "Fixed in PR #42"` |

**TUI**

| Command | Description | Example |
|---------|-------------|---------|
| `jai tui` | Launch full-screen TUI | `jai tui` |
| `jai tui --view <name>` | Launch with a specific view | `jai tui --view my-bugs` |

**Setup**

| Command | Description | Example |
|---------|-------------|---------|
| `jai init` | Interactive setup wizard | `jai init` |
| `jai config` | Show/edit configuration | `jai config` |

### Agent Output

All commands with `--json` return compact JSON (no pretty-printing to save tokens):

```json
{"ok":true,"data":{"key":"ROX-123","summary":"Fix auth bug","status":"Open"}}
```

Errors are also structured:

```json
{"ok":false,"error":{"type":"NotFound","message":"Issue ROX-123 not found"}}
```

### Schema Introspection

Agents discover how to use jai without documentation:

```bash
jai schema get
```

```json
{"command":"get","params":{"key":{"type":"string","required":true}},"flags":{"fields":{"type":"string","description":"Comma-separated field names"},"json":{"type":"bool"}},"output_fields":["key","summary","status","assignee","priority","created","updated","..."]}
```

The `output_fields` list is derived directly from the DB schema — always accurate, always up to date.

---

## The `jai query` Command

This is the headline feature. Raw SQL against your Jira data.

To help users transition from JQL, jai will include:
- A **JQL-to-SQL reference** in documentation showing common JQL patterns and their SQL equivalents
- The `jai fields` command showing field name mappings (e.g., `team` → `customfield_12345`) so users know what column names to use in queries

### Saved Views

Frequently used queries can be saved in config and run by name. jai ships with sensible defaults:

```bash
jai view my-issues          # Issues assigned to me
jai view search-comments    # Search within comments (prompts for term)
jai view team-status        # Issue count by team and status
jai view stale-issues       # Issues not updated in 4+ weeks
```

Users define their own views in the same YAML config used by the TUI (see Views section below). `jai view` runs them as CLI table output; `jai tui` renders them as interactive tabs.

### Examples

**Simple**: Issues assigned to me, sorted by priority
```sql
jai query "SELECT key, summary, priority, status FROM issues WHERE assignee = 'me@co.com' ORDER BY priority"
```

**JOINs**: Issues with comments mentioning "security"
```sql
jai query "SELECT i.key, i.summary, c.body FROM issues i JOIN comments c ON i.key = c.issue_key WHERE c.body LIKE '%security%'"
```

**Aggregation**: Issue count by team and status
```sql
jai query "SELECT team, status, COUNT(*) as cnt FROM issues GROUP BY team, status ORDER BY cnt DESC"
```

**Window functions**: Ranking issues by staleness per team
```sql
jai query "SELECT key, summary, team, updated, ROW_NUMBER() OVER (PARTITION BY team ORDER BY updated ASC) as staleness_rank FROM issues WHERE status != 'Done'"
```

**CTE**: Bug velocity — bugs opened vs closed per week
```sql
jai query "
WITH opened AS (SELECT strftime('%Y-%W', created) as week, COUNT(*) as opened FROM issues WHERE type = 'Bug' GROUP BY week),
     closed AS (SELECT strftime('%Y-%W', resolved) as week, COUNT(*) as closed FROM issues WHERE type = 'Bug' AND status = 'Done' GROUP BY week)
SELECT o.week, o.opened, COALESCE(c.closed, 0) as closed, o.opened - COALESCE(c.closed, 0) as net
FROM opened o LEFT JOIN closed c ON o.week = c.week ORDER BY o.week
"
```

**Full-text search with ranking**:
```sql
jai query "SELECT key, summary, rank FROM issues_fts WHERE issues_fts MATCH 'memory leak crash' ORDER BY rank LIMIT 10"
```

For agents, add `--json` for compact output. For humans, results render as a formatted table.

---

## TUI

A full-screen terminal application (bubbletea) that replaces Jira Plan and the web UI for common workflows. Powered entirely by local SQLite — instant, smooth, works offline.

### Views

Views are defined in YAML config. They serve double duty: `jai view <name>` runs them as CLI output, `jai tui` renders them as interactive tabs.

```yaml
views:
  my-work:
    title: "My Work"
    query: "SELECT key, summary, status, priority, updated FROM issues WHERE assignee = '{{me}}' AND status != 'Done' ORDER BY updated DESC"
    columns: [key, summary, status, priority, updated]

  team-board:
    title: "Team Board"
    query: "SELECT key, summary, status, assignee, priority FROM issues WHERE team = '{{team}}'"
    group_by: status
    columns: [key, summary, assignee, priority]

  stale-bugs:
    title: "Stale Bugs"
    query: "SELECT key, summary, assignee, team, updated FROM issues WHERE type = 'Bug' AND status NOT IN ('Done', 'Closed') AND updated < date('now', '-28 days') ORDER BY updated ASC"
    columns: [key, summary, assignee, team, updated]
    color_rules:
      - field: updated
        older_than: 56d
        color: red
      - field: updated
        older_than: 28d
        color: yellow

  release-plan:
    title: "Release Plan"
    query: "SELECT key, summary, status, team, priority, story_points FROM issues WHERE fix_version = '{{version}}'"
    group_by: team
    columns: [key, summary, status, priority, story_points]
    status_summary: true
```

### TUI Features

- **Tab-based navigation** between views
- **Sortable columns** — click or keyboard shortcut to sort by any column
- **Group by** — collapse/expand groups (by team, status, priority, etc.)
- **Inline filtering** — type to filter visible rows
- **Issue detail pane** — select an issue to see full details in a split view
- **Color rules** — conditional coloring based on field values (stale items red, blockers red, etc.)
- **Status summary bar** — counts by status at the bottom (e.g., "12 To Do | 8 In Progress | 3 Done")
- **Quick actions** — press `e` to edit a field, `c` to comment, `o` to open in browser
- **Auto-sync** — incremental sync runs in background, TUI updates live. Locally modified data is highlighted as "pending" until confirmed by sync.
- **Hierarchy view** — expand epics to see child issues (like Jira Plan's Level 4 → Level 3 hierarchy)
- **Template variables** — `{{me}}`, `{{team}}`, `{{version}}` resolve from config

### Keyboard Shortcuts

Full vim-style navigation with standard key alternatives:

```
Navigation
  j / ↓             Move down
  k / ↑             Move up
  h / ←             Scroll left
  l / →             Scroll right
  gg / Home         Go to top
  G / End           Go to bottom
  Ctrl-d / PgDn     Page down
  Ctrl-u / PgUp     Page up

Views
  Tab / Shift-Tab   Switch views
  1-9               Jump to view by number

Actions
  Enter             Open issue detail
  /                 Filter
  g                 Group by field
  s                 Sort by column
  e                 Edit field
  c                 Comment
  o                 Open in browser
  r                 Refresh (manual sync)
  q                 Quit
  ?                 Help
```

---

## Configuration

```yaml
# ~/.config/jai/config.yaml

jira:
  url: https://mycompany.atlassian.net
  email: ${JAI_EMAIL}
  token: ${JAI_TOKEN}

sync:
  projects: [ROX, ACS]            # Projects to sync
  auto_sync: true                  # Incremental sync during CLI commands
  interval: 15m                    # Auto-sync interval in TUI mode
  history: false                   # Sync changelog (optional, off by default)

db:
  path: ~/.local/share/jai/jai.db

search:
  # Fields included in FTS5 full-text search index
  fts_fields: [summary, description, comments, labels]

fields:
  # Override auto-inferred custom field names
  # (jai auto-discovers field names from Jira during sync and stores them in the DB;
  #  use this section only to override names you want to customize)
  custom_12345: team
  custom_67890: story_points

me: me@company.com
team: "ACS Scanner"

views:
  # ... (as shown above)
```

### Field Name Resolution

jai handles Jira's messy custom field naming automatically:

1. **During first sync**: jai calls Jira's field metadata API and populates the `field_map` table with all fields, their Jira IDs, inferred readable names, and types
2. **`jai fields`**: Shows all available fields with their readable names, Jira IDs, and types — useful for writing SQL queries
3. **Config overrides**: Users can override any inferred name in `config.yaml` under `fields:` (e.g., if Jira calls it "Custom Team Field", you can map it to just `team`)
4. **Everywhere**: The readable name is used in SQL column names, CLI `--fields` values, JSON output keys, and TUI column headers

Environment variables: `JAI_EMAIL`, `JAI_TOKEN`, `JAI_CONFIG` (config file path).

---

## Sync

### Incremental Sync (default)

1. Read `last_sync_time` from `sync_metadata` table
2. Fetch issues where `updated >= last_sync_time` from Jira API
3. For each issue: store raw JSON, denormalize fields into columns
4. Update `last_sync_time`
5. Detect deletions: issues in DB but not returned by a project-scoped query get marked `is_deleted`

### Full Sync

1. Fetch all issues for configured projects
2. Replace all data (within a transaction)
3. Rebuild FTS5 index

### Write Sync

1. Read `pending_changes` table
2. For each change: call Jira API to apply
3. On success: mark change as synced
4. On failure: keep in queue, increment retry count, log error
5. Next incremental sync will pull the confirmed state

### Auto-Sync Behavior

- **TUI mode**: Background goroutine runs incremental sync on configured interval. TUI refreshes when new data arrives. Locally modified data that hasn't been confirmed by sync is highlighted as "pending" so the user isn't confused by stale values — especially important when offline.
- **CLI mode**: If `auto_sync: true` and last sync is older than `interval`, run incremental sync before executing the command. Skip with `--no-sync`.
- **Manual**: `jai sync` always available.

### Changelog Sync (optional)

When `sync.history: true`, jai also downloads issue change history into a `changelog` table. This enables views like:
- "What changed on my team this week"
- "Who moved this issue to Done and when"
- "How long did this issue spend in each status"

Off by default to keep sync fast and DB small.

---

## DB Schema (Conceptual)

```sql
-- Core issue table with denormalized fields
CREATE TABLE issues (
    key TEXT PRIMARY KEY,           -- ROX-123
    project TEXT NOT NULL,
    type TEXT,                      -- Bug, Story, Epic, Task
    summary TEXT NOT NULL,
    description TEXT,
    status TEXT,
    priority TEXT,
    assignee TEXT,
    reporter TEXT,
    created DATETIME,
    updated DATETIME,
    resolved DATETIME,
    labels TEXT,                    -- comma-separated
    components TEXT,                -- comma-separated
    fix_version TEXT,
    parent_key TEXT,                -- for hierarchy
    -- Custom fields (dynamic columns from field_map)
    -- e.g., team TEXT, story_points REAL, product_manager TEXT
    -- Metadata
    raw_json TEXT,                  -- full Jira API response
    synced_at DATETIME
);

-- Comments table (normalized for JOINs, also concatenated into FTS)
CREATE TABLE comments (
    id TEXT PRIMARY KEY,
    issue_key TEXT NOT NULL REFERENCES issues(key),
    author TEXT,
    body TEXT,
    created DATETIME,
    updated DATETIME
);

-- FTS5 virtual table (fields configurable via search.fts_fields)
CREATE VIRTUAL TABLE issues_fts USING fts5(
    key UNINDEXED,
    summary,
    description,
    comments,
    content='issues',
    content_rowid='rowid'
);

-- Changelog (optional, when sync.history: true)
CREATE TABLE changelog (
    id TEXT PRIMARY KEY,
    issue_key TEXT NOT NULL REFERENCES issues(key),
    author TEXT,
    field TEXT,
    from_value TEXT,
    to_value TEXT,
    changed_at DATETIME
);

-- Write queue
CREATE TABLE pending_changes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_key TEXT NOT NULL,
    operation TEXT NOT NULL,        -- set_field, add_comment, transition
    payload TEXT NOT NULL,          -- JSON
    created_at DATETIME,
    synced_at DATETIME,             -- NULL until synced
    retry_count INTEGER DEFAULT 0,
    error TEXT
);

-- Sync metadata
CREATE TABLE sync_metadata (
    project TEXT PRIMARY KEY,
    last_sync_time DATETIME,
    last_full_sync DATETIME,
    issues_total INTEGER,
    issues_synced INTEGER
);

-- Field mapping (auto-populated from Jira, overridable via config)
CREATE TABLE field_map (
    jira_id TEXT PRIMARY KEY,       -- customfield_12345
    name TEXT NOT NULL,             -- team
    display_name TEXT,              -- Team (human-readable, from Jira)
    type TEXT NOT NULL,             -- text, number, date, array
    searchable BOOLEAN DEFAULT 1,
    user_override BOOLEAN DEFAULT 0 -- true if name was overridden in config
);
```

---

## First-Run Experience

`jai init` launches a warm, interactive setup wizard (inspired by [OpenClaw's wizard](https://docs.openclaw.ai/start/wizard)):

1. **Welcome** — Brief explanation of what jai does
2. **Jira connection** — Prompt for URL, email, API token. Test connection immediately.
3. **Project selection** — List available projects, let user select which to sync
4. **First sync** — Start sync in background while the wizard continues. Show progress bar.
5. **Field discovery** — While sync runs, show discovered custom fields and ask if any names should be overridden
6. **Default views** — Create a starter set of views based on the user's projects
7. **Done** — Sync complete (or still running), launch TUI or show next steps

The goal is to go from `brew install jai` to a working TUI in under 5 minutes.

---

## What Makes jai Different

| | Jira Web | go-jira / jira-cli | jai |
|---|---|---|---|
| Query language | JQL | JQL | **SQL** |
| JOINs, aggregations | No | No | **Yes** |
| Full-text search | Basic | No | **FTS5 with ranking** |
| Token efficiency | N/A | Low (full payloads) | **High (field selection)** |
| Agent introspection | No | No | **Schema command** |
| Offline | No | No | **Yes (DB-first)** |
| Custom views | Limited | No | **YAML-defined, instant** |
| Speed | Slow (network) | Slow (network) | **Instant (local DB)** |
| TUI | No | Basic | **Full-screen, Jira Plan replacement** |

### Future Goals

- **Data lake integration**: For very large Jira installations, allow connecting to a data lake for reads instead of local SQLite. For now, we explicitly recommend keeping the synced dataset reasonably sized (project or JQL scoped) for best performance.

---

## Open Questions

None — all resolved. Ready for spec/plan escalation.
