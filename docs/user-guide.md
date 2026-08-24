# jai User Guide

This guide covers day-to-day usage of jai. For installation and initial setup, see the [README](../README.md).

---

## Getting started

```sh
export JAI_TOKEN=your-jira-api-token
jai init                # interactive setup wizard
jai sync                # sync issues from Jira
jai query "SELECT key, summary, status FROM issues LIMIT 10"
```

### Multiple environments

Use `--config` to point to a different config file (e.g., staging vs production):

```sh
jai --config ~/.config/jai/staging.yaml sync
jai --config ~/.config/jai/staging.yaml query "SELECT key, status FROM issues LIMIT 5"
```

The init wizard respects `--config` too — it reads/writes the specified file and auto-derives the database path from the config filename (e.g., `staging.yaml` uses `staging.db`):

```sh
jai init --config ~/.config/jai/staging.yaml
```

---

## Reading data

### Single issue

```sh
jai get ROX-123
jai get ROX-123 --json --fields key,summary,status,labels
```

### SQL queries

```sh
jai query "SELECT key, summary, status FROM issues WHERE assignee_email = '{{me}}'"
```

The `{{me}}` template variable is replaced with the `me:` value from your config.

### Template variables

Queries support built-in and parameterized template variables:

```sh
# Built-in time variables
jai query "SELECT key, summary FROM issues WHERE created >= '{{this_week}}'"
jai query "SELECT key, summary FROM issues WHERE updated < '{{month_ago}}'"

# Parameterized — pass any number
jai query "SELECT key, summary FROM issues WHERE created >= '{{days_ago:14}}'"
jai query "SELECT key, summary FROM issues WHERE created >= '{{weeks_ago:4}}'"
jai query "SELECT key, summary FROM issues WHERE created >= '{{months_ago:3}}'"

# Project list from all sync sources
jai query "SELECT key FROM issues WHERE project IN ({{projects}})"
```

Available: `{{today}}`, `{{yesterday}}`, `{{week_ago}}`, `{{month_ago}}`, `{{quarter_ago}}`, `{{this_week}}`, `{{this_month}}`, `{{this_quarter}}`, `{{projects}}`, `{{days_ago:N}}`, `{{weeks_ago:N}}`, `{{months_ago:N}}`.

### User-defined snippets

Define reusable SQL fragments in your config:

```yaml
snippets:
  active: "status NOT IN ('Done', 'Closed', 'Resolved')"
  stale: "julianday('now') - julianday(updated) > 28"
  my_open: "assignee_email = '{{me}}' AND {{active}}"
```

Use them in any query:

```sh
jai query "SELECT key, summary FROM issues WHERE {{my_open}} AND {{stale}}"
```

Snippets can reference other snippets and built-in variables (recursive expansion). Circular references produce an error. List available snippets with `jai schema snippets`.

### Full-text search

```sh
jai search "authentication token expired"
```

### Named views

```sh
jai view my-work
```

Views are defined in your config YAML. See the README for examples.

### Changelog history

```sh
jai changelog ROX-123                  # formatted timeline of all field changes
jai changelog ROX-123 --field status   # filter to specific field
jai changelog ROX-123 --json           # structured output
```

Requires `jai sync --changelogs` to fetch changelog data first.

### Data export formats

All read commands support `--format` for different output formats:

```sh
jai query "SELECT key, summary, status FROM issues LIMIT 10" --format csv
jai query "SELECT key, summary, status FROM issues LIMIT 10" --format tsv
jai query "SELECT key, summary, status FROM issues LIMIT 10" --format markdown
jai view my-work --format json
jai search "authentication" --format csv
```

Available formats: `table` (default), `json`, `csv`, `tsv`, `markdown`. The `--json` flag takes precedence over `--format`.

### Field discovery

```sh
jai fields                      # list all fields with Jira IDs and types
jai fields --search keyword     # LIKE match on field names
jai fields --suggest ROX-123    # show which custom fields have values for an issue
jai schema get                  # command schema for agents
```

---

## Write operations

Write commands push changes to Jira immediately by default. Use `--queue` / `-q` to defer changes locally until you run `jai push`.

### Set a field

```sh
jai set ROX-123 priority High
jai set ROX-123 summary "Updated title"
```

### Array fields (labels, components, fixVersions)

Add or remove individual values without replacing the entire array:

```sh
jai set ROX-123 labels --add backend
jai set ROX-123 labels --add security --add urgent
jai set ROX-123 labels --remove backend
```

Replace all values at once with comma-separated syntax:

```sh
jai set ROX-123 labels "bug,security,backend"
```

Using `--add`/`--remove` on a non-array field produces an error:

```sh
jai set ROX-123 priority --add High
# Error: priority is not an array field
```

### Bulk set

Set the same field on multiple issues at once:

```sh
# Comma-separated keys
jai set ROX-1,ROX-2,ROX-3 priority Major

# SQL query — any query returning a 'key' column
jai set --query "SELECT key FROM issues WHERE type = 'Bug' AND status = 'To Do'" priority Major
```

### Transitions

Transition an issue to a new workflow status. Like all write commands, transitions push to Jira immediately. Use `--queue` to defer.

```sh
# List available transitions
jai transition ROX-123 --list

# Execute a transition (case-insensitive)
jai transition ROX-123 "In Progress"
jai transition ROX-123 "done"
```

If the transition name doesn't match, jai lists the available options:

```sh
jai transition ROX-123 "NotAStatus"
# Error: unknown transition "NotAStatus" for ROX-123
# Available transitions:
#   - To Do (id: 41)
#   - In Progress (id: 51)
#   - Done (id: 91)
```

**Bulk transitions** — transition multiple issues at once:

```sh
# Comma-separated keys
jai transition ROX-1,ROX-2,ROX-3 "Done"

# SQL query — any query returning a 'key' column
jai transition --query "SELECT key FROM issues WHERE type = 'Bug' AND status = 'In Progress'" "Done"
```

### Composite updates

Update multiple aspects of an issue in one command:

```sh
# Set fields, transition, and comment together
jai update ROX-123 --set priority=High --set assignee=alice --transition "In Progress" --comment "Starting work"

# Multiple --set flags allowed
jai update ROX-456 --set priority=Major --set labels=urgent,security --comment "Escalating"

# Use --queue to defer all operations
jai update ROX-789 --set priority=High --transition "Done" --queue
```

Operations execute sequentially. If one fails, subsequent operations are skipped.

### Issue links

Create links between issues. Links push to Jira immediately.

```sh
# Issue-to-issue link
jai link ROX-1 ROX-2 --type "Blocks"

# Remote URL link (detected automatically)
jai link ROX-1 https://github.com/org/repo/pull/42 "PR #42"
jai link ROX-1 https://github.com/org/repo/pull/42    # URL used as title

# List available link types
jai link --list-types
```

### Watch / Unwatch

Add or remove watchers on issues. Pushes to Jira immediately.

```sh
jai watch ROX-123                        # add yourself as watcher
jai watch ROX-123 user@example.com       # add another user
jai unwatch ROX-123                      # remove yourself
```

### Comments

```sh
jai comment ROX-123 "Fixed in PR #4892"
```

Comments push to Jira immediately. Use `--queue` / `-q` to defer until `jai push`.

### Create issues

```sh
jai create ROX --type Bug --summary "Login fails on SSO" --priority High --labels backend,auth

# Use a template (defined in config under `templates:`)
jai create ROX --type Bug --template bug-report --summary "Login fails on SSO"

# Read description from stdin
echo "Detailed description" | jai create ROX --type Bug --summary "Login fails" --body -

# Inline description
jai create ROX --type Bug --summary "Login fails" --body "Short description here"
```

### Clone issues

Create a copy of an existing issue with optional overrides:

```sh
jai clone ROX-123                                  # exact copy
jai clone ROX-123 --summary "New title"            # override summary
jai clone ROX-123 --set priority=High              # override fields
jai clone ROX-123 --replace "production:staging"   # find/replace in summary + description
```

### Open in browser

```sh
jai open ROX-123                  # open in default browser
jai open ROX-123 --url-only       # print URL to stdout
```

### Push

Push is only needed for changes that were deferred with `--queue`. It syncs all queued changes to Jira.

```sh
jai push
```

### Shell completions

Generate shell completion scripts:

```sh
jai completion bash > /etc/bash_completion.d/jai
jai completion zsh > "${fpath[1]}/_jai"
jai completion fish > ~/.config/fish/completions/jai.fish
```

### Snippets

Manage SQL query snippets for common patterns:

```sh
# List all snippets (built-in + user)
jai snippet list

# Show snippet SQL
jai snippet show bug

# Add a user snippet to config
jai snippet add my_open "assignee_email = '{{me}}' AND status != 'Done'"
```

Built-in snippets: `bug`, `story`, `task`, `spike`, `epic`.

### Dependencies

Visualize issue dependencies and cross-project relationships:

```sh
# Tree view of dependencies
jai deps ROX-123

# Limit traversal depth (default: 1, max: 5)
jai deps ROX-123 --depth 3

# Cross-project dependency summary
jai deps --project ROX
```

Detects unsynced projects referenced in links.

---

## Sync

```sh
jai sync                # incremental sync
jai sync --full         # full resync with deletion detection
jai sync --changelogs   # sync status transition history
```

By default, sync runs in the background — commands return immediately with cached data. JSON output includes `sync_age_seconds`. Human output shows "(data from N minutes ago)" when stale.

Use `--wait-sync` to block until sync completes:

```sh
jai query "SELECT count(*) FROM issues" --wait-sync
```

Use `--no-sync` to skip auto-sync entirely:

```sh
jai query "SELECT count(*) FROM issues" --no-sync
```

### Sync status

```sh
jai status
```

### Sprint and board sync

Sprints and boards are synced automatically from the Jira Agile API (enabled by default):

```yaml
sync:
  sprints: true  # default
```

Issues get `sprint_id` and `sprint_name` columns.

### Dev info sync

Optionally sync PR count, open PR status, and branch names from Jira dev-status API:

```yaml
sync:
  dev_info: true  # default: false (opt-in)
```

Updates `pr_count`, `has_open_pr`, `branch_name` on issues.

---

## Agent mode

Every command supports `--json` for structured output and `--fields` to select columns:

```sh
jai get ROX-123 --json --fields key,summary,status
# {"ok":true,"data":{"key":"ROX-123","summary":"...","status":"In Progress"}}

jai set ROX-1,ROX-2 priority Major --json
# {"ok":true,"data":{"count":2,"keys":["ROX-1","ROX-2"]}}

jai transition ROX-123 --list --json
# {"ok":true,"data":{"issue_key":"ROX-123","transitions":[...]}}

jai link --list-types --json
# {"ok":true,"data":{"link_types":[...]}}
```

Errors are structured:
```json
{"ok":false,"error":{"type":"QueryError","message":"no such column: statuss"}}
```

---

## MCP Server

Start an MCP (Model Context Protocol) server to expose jai functionality to AI agents:

```sh
# Start with stdio transport (default) — for Claude Code integration
jai serve

# HTTP transport on custom port
jai serve --transport http --port 9000

# SSE transport
jai serve --transport sse --port 8080

# Read-only mode — blocks all write operations
jai serve --read-only

# Enable specific toolsets only
jai serve --toolsets read,schema,sync
```

**Environment variable overrides:**
- `JAI_MCP_TOOLSETS` — comma-separated toolsets to enable
- `JAI_MCP_READ_ONLY` — set to `true` for read-only mode
- `JAI_MCP_TRANSPORT` — `stdio`, `http`, or `sse`
- `JAI_MCP_PORT` — port for HTTP/SSE (default: 8947)

**Available toolsets:**
- `read` — get, query, search, view
- `schema` — fields, schema introspection
- `write` — set, create, clone, comment
- `sync` — sync, status
- `browser` — open issues in browser
- `config` — read/write config (excludes credentials)

The server provides 17 tools, 8 MCP resources (jira://issue/{key}, jira://schema/db, etc.), and 6 MCP prompts (standup-report, sprint-review, bug-triage, etc.).

Background sync runs automatically on the configured interval (default: 15m).

**Claude Code integration:** Add jai to `.claude/mcp_servers.json` or use the `/jai` skill from `.claude/skills/jai.md`.

---

## Global flags

| Flag | Description |
|------|-------------|
| `--json` | Structured JSON output |
| `--fields` | Comma-separated field names to include |
| `--format` | Output format: table (default), json, csv, tsv, markdown |
| `--no-sync` | Skip auto-sync before the command |
| `--wait-sync` | Wait for sync to complete before returning results |
| `--config` | Path to config file (default: `~/.config/jai/config.yaml`) |
| `--db` | Path to database file |
