---
name: jira-query
description: Queries and manages Jira issues via local SQLite database using the jai CLI. Syncs Jira Cloud data locally for instant SQL queries, full-text search, field updates, transitions, and bulk operations. Use when working with Jira issues, sprint planning, bug triage, release checks, or standup preparation.
---

# jai — Jira via SQL

You have access to `jai`, a CLI that syncs Jira Cloud to a local SQLite database.
All reads are instant SQL queries against a local DB. Writes push to Jira immediately.

## Activation Check

Before proceeding, verify jai is installed and configured:

```bash
command -v jai && jai status --json
```

If jai is not found, instruct the user to install it. If `jai status` shows no sync sources or auth errors, guide them through `jai init`.

## Core Commands

### Reading Data (use these first — they're free, fast, local)
- `jai query "<SQL>" --json` — Run SQL against local issues table. ALWAYS use --json.
- `jai query --jql "<JQL>" --json` — Live query against Jira API (slower, use only when local data is stale)
- `jai search "<text>" --json` — FTS5 full-text search across summary, description, comments, labels
- `jai get <KEY> --json` — Fetch single issue details
- `jai view <name> --json` — Run a named view (pre-defined SQL query)

### Schema Discovery (run BEFORE writing queries)
- `jai schema db --json` — Column names, types, which are custom fields
- `jai schema values <column> --json` — Distinct values for any column (up to 200)
- `jai schema snippets --json` — Reusable SQL fragments
- `jai schema templates --json` — Issue creation templates
- `jai fields --json` — All fields with Jira mappings and population stats

### Writing Data
- `jai set <KEY> <field> <value>` — Set a field (supports --add/--remove for arrays)
- `jai set --query "<SQL>" <field> <value>` — Bulk set via SQL-driven key selection
- `jai comment <KEY> "<text>"` — Add a comment
- `jai transition <KEY> <status>` — Move issue to a new status
- `jai create <PROJECT> --type=<type> --summary="<summary>" [flags]` — Create issue
- `jai clone <KEY> [--summary="..."] [--set key=value]` — Clone an issue
- `jai link <KEY> <TARGET> [title]` — Link issues or add URL links

### Utilities
- `jai sync` — Refresh local data from Jira (usually automatic)
- `jai status --json` — Auth, sync metadata, DB stats
- `jai open <KEY>` — Open issue in browser

## Template Variables (use in SQL strings)
{{me}}, {{team}}, {{today}}, {{yesterday}}, {{week_ago}}, {{month_ago}},
{{quarter_ago}}, {{this_week}}, {{this_month}}, {{this_quarter}}, {{projects}},
{{days_ago:N}}, {{weeks_ago:N}}, {{months_ago:N}}

## SQL Tips
- The main table is `issues`. Comments are in `comments`. Changelog in `changelog`.
- Use `--fields` to limit output columns (saves tokens)
- Custom fields are auto-named as snake_case columns (e.g., "Story Points" → `story_points`)
- FTS table is `issues_fts` — use `issues_fts MATCH 'term'` for full-text search in SQL
- JOINs work: `SELECT i.key, c.body FROM issues i JOIN comments c ON i.key = c.issue_key`

## Protocol
1. ALWAYS run `jai schema db --json` first if you haven't seen the schema yet
2. ALWAYS use `--json` flag on read commands
3. ALWAYS use `--fields` to limit output columns when you don't need everything
4. Prefer SQL queries over multiple `jai get` calls — one query replaces N API calls
5. Use template variables ({{me}}, {{today}}) instead of hardcoding values
6. For bulk operations, use `jai set --query` instead of looping over keys
7. Check `jai schema values <col>` before filtering on a column you haven't seen

## Schema Discovery Protocol

Follow a progressive discovery protocol:

**Step 1: Bootstrap (first interaction only)**
```bash
jai schema db --json --fields name,type,is_custom
```
→ Learn column names and types

**Step 2: Explore values (when filtering on unfamiliar columns)**
```bash
jai schema values status --json
jai schema values priority --json
jai schema values issue_type --json
```
→ Learn the vocabulary of each column

**Step 3: Learn snippets (before writing complex queries)**
```bash
jai schema snippets --json
```
→ Discover reusable SQL fragments the user has defined

**Step 4: Learn views (before suggesting pre-built queries)**
```bash
jai view --json
```
→ List available named views

**Step 5: Cache and reuse**
→ After discovery, remember the schema for the session
→ Only re-discover if a query fails with "no such column"

## Workflows
9 ready-made recipes for common Jira tasks. See [workflows.md](workflows.md).

## Error Recovery  
Common error patterns and fixes. See [errors.md](errors.md).

## Token Efficiency & Config
Optimization strategies and config management rules. See [tokens.md](tokens.md).
