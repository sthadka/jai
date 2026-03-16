# jai

**Query Jira with SQL.**

jai syncs your Jira Cloud data to a local SQLite database and exposes it through SQL queries, a full-screen TUI, and structured JSON output for AI agents.

```
$ jai sync
Synced ROX: 1,234 issues (45 new, 189 updated) in 12.3s

$ jai query "SELECT key, summary, status FROM issues WHERE assignee_email = '{{me}}' AND status_category != 'Done' ORDER BY priority"
KEY        SUMMARY                              STATUS
ROX-4821   Fix auth token expiry bug            In Progress
ROX-4756   Migrate CI to new runner pool        To Do
ROX-4712   Update RBAC docs for v4.5            To Do
(3 rows)

$ jai tui
```

---

## Why

Jira's REST API returns 50KB per issue. JQL can't join tables, aggregate, or rank search results. Existing CLI tools mirror the web UI's complexity and emit unstructured, token-heavy output that's expensive for AI agents to parse.

jai takes a different approach: sync once to a local SQLite database, then query instantly — with full SQL, no round trips, no API tokens burned per read.

- **10–50× fewer tokens** for AI agents. Select exactly the fields you need.
- **Instant queries** from local SQLite. No waiting for the API.
- **Full SQL power** — JOINs, aggregations, CTEs, window functions, FTS5. Things JQL will never do.
- **Works offline.** Writes queue locally and sync to Jira when you're back online.
- **Full-screen TUI** that replaces the Jira web UI for daily workflows.

---

## Install

```sh
brew install syethadk/tap/jai
```

Or build from source (requires Go 1.23+ and CGO):

```sh
git clone https://github.com/sthadka/jai
cd jai
make install
```

---

## Quick start

```sh
# 1. Set your API token
export JAI_TOKEN=your-jira-api-token

# 2. Run the setup wizard
jai init

# 3. Query
jai query "SELECT key, summary, status FROM issues LIMIT 10"

# 4. Launch the TUI
jai tui
```

`jai init` walks through connection setup, project selection, and runs the first sync. Takes under 5 minutes.

---

## SQL queries

jai stores every Jira issue as a row with denormalized columns. Query it like any database.

**Your open work, sorted by priority:**
```sql
jai query "
  SELECT key, summary, status, priority
  FROM issues
  WHERE assignee_email = '{{me}}'
    AND status_category != 'Done'
  ORDER BY priority"
```

**Bug velocity — opened vs closed per week:**
```sql
jai query "
  WITH opened AS (
    SELECT strftime('%Y-%W', created) AS week, COUNT(*) AS n
    FROM issues WHERE type = 'Bug' GROUP BY week
  ),
  closed AS (
    SELECT strftime('%Y-%W', resolved) AS week, COUNT(*) AS n
    FROM issues WHERE type = 'Bug' AND status = 'Done' GROUP BY week
  )
  SELECT o.week, o.n AS opened, COALESCE(c.n, 0) AS closed
  FROM opened o LEFT JOIN closed c ON o.week = c.week
  ORDER BY o.week DESC LIMIT 12"
```

**Issues with comments mentioning a topic:**
```sql
jai query "
  SELECT i.key, i.summary, c.author, c.body
  FROM issues i
  JOIN comments c ON i.key = c.issue_key
  WHERE c.body LIKE '%memory leak%'"
```

**Stale issues by team, ranked:**
```sql
jai query "
  SELECT key, summary, team, updated,
    ROW_NUMBER() OVER (PARTITION BY team ORDER BY updated ASC) AS staleness_rank
  FROM issues
  WHERE status_category = 'In Progress'"
```

**Full-text search with ranking:**
```sql
jai search "authentication token expired"
```

---

## Agent mode

Every command supports `--json` for compact, structured output and `--fields` to select specific columns.

```sh
# Get a single issue
jai get ROX-4821 --json --fields key,summary,status,assignee
```
```json
{"ok":true,"data":{"key":"ROX-4821","summary":"Fix auth token expiry bug","status":"In Progress","assignee":"Jane Doe"}}
```

```sh
# Run a query
jai query "SELECT key, summary, status FROM issues WHERE type = 'Bug' LIMIT 3" --json
```
```json
{"ok":true,"columns":["key","summary","status"],"rows":[["ROX-4821","Fix auth token expiry","In Progress"],["ROX-4756","Null pointer in scheduler","To Do"],["ROX-4701","Race condition in cache","Done"]],"count":3}
```

Errors are structured too:
```json
{"ok":false,"error":{"type":"QueryError","message":"no such column: statuss — did you mean 'status'?"}}
```

Agents can discover the full command surface without documentation:

```sh
jai schema get
```
```json
{"command":"get","params":{"key":{"type":"string","required":true,"description":"Issue key (e.g. ROX-123)"}},"flags":{"json":{"type":"bool"},"fields":{"type":"string","description":"Comma-separated field names"}}}
```

---

## TUI

`jai tui` opens a full-screen terminal UI powered by local SQLite — instant, smooth, works offline.

**Features:**
- Tab-based navigation between named views
- Sortable, scrollable, filterable table (`/` to filter, `s` to sort)
- Issue detail pane with full field list (`Enter` to open)
- Group by any column (`g`)
- Color rules — stale items, blockers, priorities highlighted automatically
- Status summary bar — "12 To Do | 8 In Progress | 3 Done"
- Background sync with live refresh indicator
- Quick actions: `e` edit field, `c` comment, `o` open in browser
- Vim-style navigation (`j`/`k`, `gg`/`G`, `Ctrl-d`/`Ctrl-u`)

Views are defined in YAML and shared between TUI tabs and `jai view <name>`:

```yaml
views:
  - name: my-work
    title: My Work
    query: |
      SELECT key, summary, status, priority
      FROM issues
      WHERE assignee_email = '{{me}}'
        AND status_category != 'Done'
      ORDER BY priority DESC, updated DESC
    columns: [key, summary, status, priority]
    status_summary: true
    color_rules:
      - field: priority
        condition: equals
        value: Blocker
        color: "#dd4444"

  - name: stale-bugs
    title: Stale Bugs
    query: |
      SELECT key, summary, assignee, updated
      FROM issues
      WHERE type = 'Bug'
        AND status_category = 'In Progress'
        AND updated < datetime('now', '-28 days')
      ORDER BY updated ASC
    columns: [key, summary, assignee, updated]
    color_rules:
      - field: updated
        condition: older_than
        value: 56d
        color: "#dd4444"
      - field: updated
        condition: older_than
        value: 28d
        color: "#e1c233"
```

---

## Write operations

Changes queue locally and sync to Jira on the next `jai push` or background sync cycle.

```sh
# Update a field
jai set ROX-4821 status "In Progress"
# → ROX-4821: status → "In Progress" (pending sync)

# Add a comment
jai comment ROX-4821 "Fixed in PR #4892, deploying to staging"
# → ROX-4821: comment added (pending sync)

# Push all pending changes
jai push
# → ✓ ROX-4821: status → "In Progress"
# → ✓ ROX-4821: comment added
# → 2 succeeded, 0 failed
```

---

## Configuration

```yaml
# ~/.config/jai/config.yaml
jira:
  url: https://mycompany.atlassian.net
  email: me@company.com
  token: ${JAI_TOKEN}        # never store tokens in plaintext
  projects: [ROX, ACS]

sync:
  interval: 15m              # auto-sync interval
  rate_limit: 10             # requests/second (Jira Cloud limit)

me: me@company.com           # used in {{me}} template variable

fields:
  overrides:
    customfield_12345: team  # override auto-inferred field names
```

`jai init` generates this file interactively.

**Data files:**

| File | Default path |
|------|-------------|
| Config | `~/.config/jai/config.yaml` |
| Database | `~/.local/share/jai/jai.db` |

Both paths can be overridden with `--config` and `--db` flags, or by setting `db.path` in the config file.

**Custom fields** are auto-discovered from Jira's field metadata API during the first sync. Run `jai fields` to see all available columns with their Jira IDs, types, and FTS flags. Override any name in config if the auto-inferred name isn't right.

---

## Commands

| Command | Description |
|---------|-------------|
| `jai init` | Interactive setup wizard |
| `jai sync` | Incremental sync from Jira |
| `jai sync --full` | Full resync with deletion detection |
| `jai query <sql>` | Execute SQL against local DB |
| `jai get <key>` | Fetch a single issue |
| `jai search <text>` | FTS5 full-text search |
| `jai view <name>` | Run a named view |
| `jai fields` | List available fields and mappings |
| `jai schema <command>` | Command schema for agents |
| `jai status` | Sync status and pending changes |
| `jai set <key> <field> <value>` | Update an issue field |
| `jai comment <key> <text>` | Add a comment |
| `jai push` | Push pending changes to Jira |
| `jai tui` | Launch full-screen TUI |

Global flags: `--json`, `--fields`, `--no-sync`, `--config`, `--db`

---

## Build from source

Requires Go 1.23+ and CGO (for SQLite FTS5 support).

```sh
git clone https://github.com/sthadka/jai
cd jai
make build     # builds ./jai
make test      # runs all tests
make install   # installs to $GOPATH/bin
```

For cross-compilation (linux from macOS), install [zig](https://ziglang.org/download/) and use goreleaser:

```sh
goreleaser build --snapshot --clean
```

---

## License

MIT — see [LICENSE](LICENSE).
