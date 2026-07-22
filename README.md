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
brew install sthadka/tap/jai
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

### Template variables

Queries support template variables that are replaced before execution:

| Variable | Expands to |
|----------|-----------|
| `{{me}}` | Your email (from config `me:`) |
| `{{team}}` | Your team (from config `team:`) |
| `{{today}}` | Today's date |
| `{{yesterday}}` | Yesterday's date |
| `{{week_ago}}` | 7 days ago |
| `{{month_ago}}` | 30 days ago |
| `{{quarter_ago}}` | 90 days ago |
| `{{this_week}}` | Monday of current week |
| `{{this_month}}` | 1st of current month |
| `{{this_quarter}}` | 1st of current quarter |
| `{{projects}}` | Quoted project keys from all sync sources |
| `{{days_ago:N}}` | N days ago |
| `{{weeks_ago:N}}` | N weeks ago |
| `{{months_ago:N}}` | N months ago |

```sql
jai query "SELECT key, summary FROM issues WHERE created >= '{{days_ago:14}}'"
jai query "SELECT key, summary FROM issues WHERE project IN ({{projects}}) AND updated < '{{month_ago}}'"
```

### User-defined snippets

Define reusable SQL fragments in your config and reference them as `{{name}}` in queries:

```yaml
snippets:
  active: "status NOT IN ('Done', 'Closed', 'Resolved')"
  stale: "julianday('now') - julianday(updated) > 28"
  my_open: "assignee_email = '{{me}}' AND {{active}}"
```

```sql
jai query "SELECT key, summary FROM issues WHERE {{my_open}} AND {{stale}}"
```

Snippets can reference other snippets and built-in variables. Circular references are detected and produce an error. Use `jai schema snippets` to list available snippets.

---

## Status transition history

`jai sync --changelogs` fetches the full changelog for each issue from the Jira API and stores status transitions in a `changelog` table. This enables time-series analysis of when features moved between statuses.

```sh
jai sync --changelogs                                    # sync changelogs for all sources
jai sync --changelogs --source "project = OCPSTRAT"      # sync changelogs for one source
```

The changelog sync is incremental — it only fetches issues that are missing changelog data or have been updated since the last sync. It uses per-issue API calls (`?expand=changelog`), so it's slower than the regular issue sync.

```sql
-- When did each 5.0 feature enter Release Pending?
jai query "SELECT issue_key, changed_at FROM changelog
  WHERE field='status' AND to_string='Release Pending'
  AND issue_key IN (SELECT key FROM issues WHERE target_version LIKE '%5.0%')"

-- Compare completion curves across releases
jai query "SELECT substr(changed_at, 1, 7) as month, COUNT(*) as completed
  FROM changelog c JOIN issues i ON c.issue_key = i.key
  WHERE c.field='status' AND c.to_string='Release Pending'
  AND i.target_version LIKE '%4.22%'
  GROUP BY month ORDER BY month"
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
# Create a new issue (hits Jira API directly, returns key immediately)
jai create ROX --type Bug --summary "Login fails on SSO" --priority High --labels backend,auth
# → ✓ Created ROX-4901: Login fails on SSO

# Create from a template (defined in config YAML)
jai create ROX --type Bug --template bug-report --summary "Login fails on SSO"

# Create with description from stdin
echo "Detailed description here" | jai create ROX --type Bug --summary "Login fails" --body -

# Create with all the bells and whistles
jai create ROX --type Story \
  --summary "Add search" \
  --description "Implement full-text search across all fields" \
  --parent ROX-100 \
  --labels backend,search \
  --components Platform \
  --priority Medium \
  --assignee user@example.com \
  --fix-version v4.6 \
  --due-date 2025-03-15 \
  --field customfield_10001='{"value":"Team Alpha"}' \
  --json
# → {"ok":true,"data":{"key":"ROX-4902","id":"12345","project":"ROX","status":"created"}}

# Clone an existing issue
jai clone ROX-4821 --summary "Similar bug in staging" --set priority=High
# → ✓ Created ROX-4903: Similar bug in staging
jai clone ROX-4821 --replace "production:staging"
# → ✓ Created ROX-4904 (summary/description text replaced)

# Update a field
jai set ROX-4821 priority High
# → ROX-4821: priority → "High" (pending sync)

# Array fields — add/remove individual values
jai set ROX-4821 labels --add backend --add auth
# → ROX-4821: labels += [backend auth] (pending sync)
jai set ROX-4821 labels --remove backend
# → ROX-4821: labels -= [backend] (pending sync)

# Bulk set — comma-separated keys or SQL query
jai set ROX-1,ROX-2,ROX-3 priority Major
# → queued 3 changes (pending sync)
jai set --query "SELECT key FROM issues WHERE type = 'Bug' LIMIT 5" priority Major
# → queued 5 changes (pending sync)

# Transition an issue (pushes immediately)
jai transition ROX-4821 "In Progress"
# → ROX-4821: transitioned to "In Progress"
jai transition ROX-4821 --list
# → Available transitions: New, Backlog, In Progress, Done, ...

# Link two issues
jai link ROX-4821 ROX-4756 --type "Blocks"
# → ROX-4821 -> ROX-4756: linked (Blocks)
jai link --list-types
# → Available link types: Blocks, Related, Duplicate, ...

# Add a remote link (URL detected automatically)
jai link ROX-4821 https://github.com/org/repo/pull/42 "PR #42"
# → ROX-4821: remote link added

# Watch/unwatch issues
jai watch ROX-4821                         # watch as yourself
jai watch ROX-4821 user@example.com        # add another watcher
jai unwatch ROX-4821                       # stop watching

# Open in browser
jai open ROX-4821                          # opens in default browser
jai open ROX-4821 --url-only               # print URL only

# Add a comment
jai comment ROX-4821 "Fixed in PR #4892, deploying to staging"
# → ROX-4821: comment added (pending sync)

# Push all pending changes
jai push
# → ✓ ROX-4821: priority → "High"
# → ✓ ROX-4821: labels updated
# → ✓ ROX-4821: comment added
# → 3 succeeded, 0 failed
```

---

## Configuration

```yaml
# ~/.config/jai/config.yaml
jira:
  url: https://mycompany.atlassian.net
  email: me@company.com
  token: ${JAI_TOKEN}        # never store tokens in plaintext

sync:
  interval: 15m              # auto-sync interval
  rate_limit: 10             # requests/second (Jira Cloud limit)

me: me@company.com           # used in {{me}} template variable

sync_sources:
  - name: my-project
    jql: project = MYPROJ ORDER BY updated DESC
  - name: another-team
    jql: project = OTHER AND team = "Platform"

fields:
  overrides:
    customfield_12345: team  # override auto-inferred field names

snippets:                    # reusable SQL fragments for queries
  active: "status NOT IN ('Done', 'Closed', 'Resolved')"
  my_open: "assignee_email = '{{me}}' AND {{active}}"

templates:                   # issue description templates for jai create --template
  bug-report: |
    ## Steps to Reproduce
    1.
    ## Expected Behavior
    ## Actual Behavior
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
| `jai sync --changelogs` | Sync status transition history from Jira changelogs |
| `jai query <sql>` | Execute SQL against local DB |
| `jai get <key>` | Fetch a single issue |
| `jai search <text>` | FTS5 full-text search |
| `jai view <name>` | Run a named view |
| `jai fields` | List available fields and mappings |
| `jai schema <command>` | Command schema for agents |
| `jai status` | Sync status and pending changes |
| `jai open <key>` | Open issue in browser (`--url-only` to print URL) |
| `jai clone <key>` | Clone an issue with optional overrides |
| `jai create <project>` | Create a new issue (`--template`, `--body`) |
| `jai set <key> <field> <value>` | Update an issue field |
| `jai set <key> <field> --add <val>` | Add a value to an array field |
| `jai set <key> <field> --remove <val>` | Remove a value from an array field |
| `jai set K1,K2,K3 <field> <value>` | Bulk set on comma-separated keys |
| `jai set --query <sql> <field> <value>` | Bulk set via SQL query |
| `jai transition <key> <status>` | Transition an issue to a new status |
| `jai link <from> <to>` | Link two issues or add a remote URL link |
| `jai watch <key>` | Add yourself (or a user) as watcher |
| `jai unwatch <key>` | Remove yourself as watcher |
| `jai comment <key> <text>` | Add a comment |
| `jai push` | Push pending changes to Jira |
| `jai tui` | Launch full-screen TUI |
| `jai completion <shell>` | Generate shell completions (bash/zsh/fish) |

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
