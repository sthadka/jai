---
name: jai
trigger: /jai
keywords: jira, sprint, backlog, ticket, issue tracker, standup, sprint planning, bug triage, release readiness
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

## Workflow Recipes

### 1. Sprint Planning / Grooming

**Step 1: Current sprint state**
```bash
jai query "SELECT key, summary, status, priority, assignee, story_points
  FROM issues
  WHERE sprint IS NOT NULL AND status NOT IN ('Done', 'Closed')
  ORDER BY priority, story_points DESC" --json --fields key,summary,status,priority,assignee,story_points
```

**Step 2: Capacity check**
```bash
jai query "SELECT assignee, COUNT(*) as issues, SUM(story_points) as points
  FROM issues
  WHERE sprint IS NOT NULL AND status NOT IN ('Done', 'Closed')
  GROUP BY assignee
  ORDER BY points DESC" --json
```

**Step 3: Unestimated items**
```bash
jai query "SELECT key, summary, issue_type, priority
  FROM issues
  WHERE sprint IS NOT NULL AND (story_points IS NULL OR story_points = 0)
  AND status NOT IN ('Done', 'Closed')" --json
```

**Step 4: Backlog candidates (ready, unassigned, prioritized)**
```bash
jai query "SELECT key, summary, priority, story_points, issue_type
  FROM issues
  WHERE sprint IS NULL AND status IN ('To Do', 'Open', 'Backlog')
  AND priority IN ('Highest', 'High', 'Medium')
  ORDER BY CASE priority
    WHEN 'Highest' THEN 1 WHEN 'High' THEN 2 WHEN 'Medium' THEN 3 ELSE 4 END,
    created ASC
  LIMIT 20" --json
```

### 2. Status Reporting / Standup Prep

**Yesterday's completions**
```bash
jai query "SELECT key, summary, assignee, status
  FROM issues
  WHERE status IN ('Done', 'Closed') AND updated >= '{{yesterday}}'
  ORDER BY assignee" --json
```

**In-progress work**
```bash
jai query "SELECT key, summary, assignee, status, updated
  FROM issues
  WHERE status IN ('In Progress', 'In Review', 'In QA')
  AND assignee IN ({{team}})
  ORDER BY assignee, updated DESC" --json
```

**Blockers (stale in-progress)**
```bash
jai query "SELECT key, summary, assignee, status, updated,
    CAST(julianday('now') - julianday(updated) AS INTEGER) as days_stale
  FROM issues
  WHERE status = 'In Progress'
  AND updated < '{{week_ago}}'
  ORDER BY updated ASC" --json
```

**Newly created (incoming work)**
```bash
jai query "SELECT key, summary, priority, reporter, created
  FROM issues
  WHERE created >= '{{yesterday}}'
  ORDER BY priority, created DESC" --json
```

### 3. Bug Triage with Duplicate Detection

**Step 1: Search for potential duplicates**
```bash
jai search "<bug summary keywords>" --json --limit 10
```

**Step 2: If FTS finds candidates, check details**
```bash
jai query "SELECT key, summary, status, resolution, description
  FROM issues
  WHERE key IN ('PROJ-1', 'PROJ-2')
  AND issue_type = 'Bug'" --json
```

**Step 3: Get open bugs by component/area**
```bash
jai query "SELECT key, summary, status, priority, components, labels
  FROM issues
  WHERE issue_type = 'Bug' AND status NOT IN ('Done', 'Closed', 'Resolved')
  ORDER BY priority, created DESC" --json
```

**Step 4: If not a duplicate, create the bug**
```bash
jai create PROJ --type=Bug --summary="<summary>" --description="<description>" \
  --priority=High --labels=triaged --components=<component>
```

**Step 5: If duplicate found, link and comment**
```bash
jai link NEW-KEY EXISTING-KEY --type=Duplicate
jai comment EXISTING-KEY "Duplicate report received. Additional context: ..."
```

### 4. Spec-to-Tickets Breakdown

**Step 1: Discover project conventions**
```bash
jai schema values issue_type --json
jai schema values priority --json
jai schema templates --json
jai fields --json --filter "required"
```

**Step 2: Check for existing epic/feature**
```bash
jai query "SELECT key, summary, status FROM issues
  WHERE issue_type = 'Epic' AND summary LIKE '%<feature name>%'" --json
```

**Step 3: Create the epic (if not exists)**
```bash
jai create PROJ --type=Epic --summary="<epic title>" --description="<spec summary>"
```

**Step 4: Create child tickets (one per spec section/requirement)**
```bash
jai create PROJ --type=Story --summary="<story 1>" --description="..." --parent=PROJ-100
jai create PROJ --type=Story --summary="<story 2>" --description="..." --parent=PROJ-100
# ... repeat for each story
```

**Step 5: Add dependencies between tickets**
```bash
jai link PROJ-102 PROJ-101 --type="Blocks"
```

**Step 6: Verify the structure**
```bash
jai query "SELECT key, summary, issue_type, status, parent_key
  FROM issues WHERE parent_key = 'PROJ-100'
  ORDER BY key" --json
```

### 5. Stale Issue Cleanup

**Find stale issues (no update in 30+ days, still open)**
```bash
jai query "SELECT key, summary, status, assignee, updated,
    CAST(julianday('now') - julianday(updated) AS INTEGER) as days_stale
  FROM issues
  WHERE status NOT IN ('Done', 'Closed', 'Resolved')
  AND updated < '{{month_ago}}'
  ORDER BY updated ASC
  LIMIT 50" --json
```

**Group by assignee for accountability**
```bash
jai query "SELECT assignee, COUNT(*) as stale_count,
    MIN(updated) as oldest_update
  FROM issues
  WHERE status NOT IN ('Done', 'Closed', 'Resolved')
  AND updated < '{{month_ago}}'
  GROUP BY assignee
  ORDER BY stale_count DESC" --json
```

**For each stale issue, add a nudge comment or close**
```bash
jai comment PROJ-OLD "This issue has been inactive for 30+ days. Is it still relevant?"
# OR
jai transition PROJ-OLD "Closed"
jai comment PROJ-OLD "Auto-closed due to 30+ days of inactivity."
```

### 6. Cross-Team Dependency Mapping

**Find all issues with cross-project links**
```bash
jai query "SELECT i.key, i.summary, i.status, i.assignee, i.project
  FROM issues i
  WHERE i.key IN (
    SELECT DISTINCT key FROM issues WHERE description LIKE '%OTHERPROJ-%'
  )
  ORDER BY i.project, i.key" --json
```

**Find blocked issues**
```bash
jai query "SELECT key, summary, status, assignee, project, labels
  FROM issues
  WHERE status = 'Blocked' OR labels LIKE '%blocked%'
  ORDER BY priority, project" --json
```

**Issues assigned across teams**
```bash
jai query "SELECT project, assignee, COUNT(*) as count
  FROM issues
  WHERE status NOT IN ('Done', 'Closed')
  GROUP BY project, assignee
  HAVING count > 3
  ORDER BY project, count DESC" --json
```

### 7. Workload Analysis

**Current load by person**
```bash
jai query "SELECT assignee,
    COUNT(*) as total,
    SUM(CASE WHEN status = 'In Progress' THEN 1 ELSE 0 END) as in_progress,
    SUM(CASE WHEN priority IN ('Highest', 'High') THEN 1 ELSE 0 END) as high_pri,
    SUM(COALESCE(story_points, 0)) as total_points
  FROM issues
  WHERE status NOT IN ('Done', 'Closed', 'Resolved')
  AND assignee IS NOT NULL
  GROUP BY assignee
  ORDER BY total DESC" --json
```

**Overloaded (more than 5 in-progress items)**
```bash
jai query "SELECT assignee, COUNT(*) as wip
  FROM issues
  WHERE status = 'In Progress' AND assignee IS NOT NULL
  GROUP BY assignee
  HAVING wip > 5
  ORDER BY wip DESC" --json
```

**Completion velocity (last 2 weeks)**
```bash
jai query "SELECT assignee,
    COUNT(*) as completed,
    SUM(COALESCE(story_points, 0)) as points_completed
  FROM issues
  WHERE status IN ('Done', 'Closed')
  AND updated >= '{{weeks_ago:2}}'
  AND assignee IS NOT NULL
  GROUP BY assignee
  ORDER BY completed DESC" --json
```

### 8. Release Readiness Check

**Issues targeted for release version**
```bash
jai query "SELECT key, summary, status, priority, assignee, issue_type
  FROM issues
  WHERE fix_versions LIKE '%v2.1%'
  ORDER BY CASE status
    WHEN 'Done' THEN 3 WHEN 'In Progress' THEN 2 ELSE 1 END,
    priority" --json
```

**Completion summary**
```bash
jai query "SELECT status, COUNT(*) as count
  FROM issues
  WHERE fix_versions LIKE '%v2.1%'
  GROUP BY status
  ORDER BY count DESC" --json
```

**Open blockers for the release**
```bash
jai query "SELECT key, summary, priority, assignee, status
  FROM issues
  WHERE fix_versions LIKE '%v2.1%'
  AND status NOT IN ('Done', 'Closed')
  AND priority IN ('Highest', 'High', 'Blocker')
  ORDER BY priority" --json
```

**Unresolved bugs in release scope**
```bash
jai query "SELECT key, summary, priority, assignee, status
  FROM issues
  WHERE fix_versions LIKE '%v2.1%'
  AND issue_type = 'Bug'
  AND status NOT IN ('Done', 'Closed', 'Resolved')
  ORDER BY priority" --json
```

### 9. Retrospective Data Gathering

**Sprint velocity over time (completed in last sprint)**
```bash
jai query "SELECT issue_type, COUNT(*) as count,
    SUM(COALESCE(story_points, 0)) as points
  FROM issues
  WHERE status IN ('Done', 'Closed')
  AND updated >= '{{weeks_ago:2}}'
  GROUP BY issue_type" --json
```

**Bug ratio**
```bash
jai query "SELECT
    SUM(CASE WHEN issue_type = 'Bug' THEN 1 ELSE 0 END) as bugs,
    SUM(CASE WHEN issue_type != 'Bug' THEN 1 ELSE 0 END) as features,
    ROUND(100.0 * SUM(CASE WHEN issue_type = 'Bug' THEN 1 ELSE 0 END) / COUNT(*), 1) as bug_pct
  FROM issues
  WHERE status IN ('Done', 'Closed')
  AND updated >= '{{weeks_ago:2}}'" --json
```

**Cycle time (created to closed, last sprint)**
```bash
jai query "SELECT key, summary,
    CAST(julianday(updated) - julianday(created) AS INTEGER) as cycle_days
  FROM issues
  WHERE status IN ('Done', 'Closed')
  AND updated >= '{{weeks_ago:2}}'
  ORDER BY cycle_days DESC" --json
```

**Carry-over (started but not finished)**
```bash
jai query "SELECT key, summary, status, assignee, story_points
  FROM issues
  WHERE status = 'In Progress'
  AND updated < '{{week_ago}}'
  ORDER BY updated ASC" --json
```

## Error Recovery

### "no such column" → Schema drift
If a query fails with "no such column: <name>":
1. Run `jai schema db --json` to refresh your schema knowledge
2. Run `jai fields --json --filter "<name>"` to find the actual column name
3. Retry the query with the correct column name

### "no such table: issues_fts" → FTS not built
Run `jai sync` to trigger FTS index rebuild, then retry search.

### "UNIQUE constraint failed" → Duplicate key
The issue already exists locally. Use `jai get <KEY> --json` to check its state.

### Auth/connection errors → Check status
Run `jai status --json` to diagnose. Look at auth_ok and last_sync fields.

### Write fails → Use queue
If a write-through operation fails (network, permissions):
1. Retry with `--queue` to save locally
2. Fix the issue (network, permissions)
3. Run `jai push` to flush the queue

### Empty results → Check sync freshness
If a query returns empty but you expect results:
1. Check `jai status --json` → last_sync timestamp
2. Run `jai sync` to refresh
3. Retry the query

### Custom field not found → Check field_map
Run `jai fields --json --filter "<partial name>"` to find the actual mapped column name.
Custom fields are slugified: "Story Points" → story_points, "Target Release" → target_release.

## Token Efficiency

### 1. Use --fields to limit columns
BAD:  `jai query "SELECT * FROM issues WHERE ..." --json`          (all 50+ columns)
GOOD: `jai query "SELECT key, summary, status FROM issues WHERE ..." --json`  (3 columns)

### 2. Use --json always
Human output is pretty-printed with box-drawing characters — much larger than JSON.

### 3. Use COUNT/GROUP BY before fetching details
First: `jai query "SELECT COUNT(*) FROM issues WHERE ..." --json`  (1 row)
Then:  `jai query "SELECT key, summary FROM issues WHERE ... LIMIT 10" --json`  (10 rows)

### 4. Use template variables
BAD:  `WHERE assignee = 'john.doe@company.com' AND updated >= '2026-08-17'`
GOOD: `WHERE assignee = '{{me}}' AND updated >= '{{week_ago}}'`

### 5. Use snippets for repeated patterns
If a user has defined snippets, use them: {{my_team_bugs}}, {{sprint_items}}, etc.

### 6. Use FTS for keyword search, not LIKE
BAD:  `WHERE summary LIKE '%login%' OR description LIKE '%login%'`
GOOD: `jai search "login" --json`

### 7. Use SQL JOINs, not multiple commands
BAD:  `jai get PROJ-1 --json; jai get PROJ-2 --json; jai get PROJ-3 --json`
GOOD: `jai query "SELECT key, summary FROM issues WHERE key IN ('PROJ-1','PROJ-2','PROJ-3')" --json`

### 8. Use bulk set, not individual sets
BAD:  `jai set PROJ-1 priority High; jai set PROJ-2 priority High; jai set PROJ-3 priority High`
GOOD: `jai set PROJ-1,PROJ-2,PROJ-3 priority High`
BEST: `jai set --query "SELECT key FROM issues WHERE ..." priority High`

### 9. Limit results
Always use LIMIT when exploring. Default to LIMIT 20 unless the user wants everything.

### 10. Cache schema knowledge
Run `jai schema db` once per session. Don't re-run it for every query.

## Config Management

You can read and modify jai's config file at `~/.config/jai/config.yaml` (or the path from `jai status --json`).

### When to modify config:
- User asks to track a new Jira project → add a sync_source, then run `jai sync`
- User repeatedly asks for the same report → create a named view
- A useful SQL pattern emerges → save it as a snippet
- User wants a standard issue template → add a template
- Cross-project dependency query reveals unsynced projects → suggest adding sync source

### Safety rules:
- NEVER modify the `jira.token` or `jira.email` fields — these contain credentials
- ALWAYS read the config first to understand the existing structure
- ALWAYS preserve existing entries — add, don't replace
- After modifying config, run `jai sync` if you added a sync source
- After modifying config, verify with `jai status --json` or `jai view <name> --json`

### Config location:
- Default: `~/.config/jai/config.yaml`
- Custom: check `JAI_CONFIG` env var or `--config` flag
- The config path is shown in `jai status --json` output
