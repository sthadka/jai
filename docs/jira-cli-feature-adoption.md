# Feature Adoption Recommendations

> Which jira-cli features jai should adopt — and which it should deliberately skip — while
> maintaining its database-first, agent-optimized stance.

## Guiding Principles

jai's opinionated stance rests on three pillars:

1. **Database-first**: Sync once, query instantly. SQL > JQL. Offline reads.
2. **Agent-optimized**: Compact JSON, schema introspection, token efficiency.
3. **Config-driven views**: YAML + SQL views shared between CLI and TUI.

Any adopted feature must strengthen (or at least not weaken) these pillars. Features that pull
jai toward "Jira API wrapper" territory are rejected — that's jira-cli's domain, and it does it
well.

---

## Adopt: High Value

### 1. Issue Cloning (`jai clone`)

**What jira-cli does**: `jira issue clone KEY` copies all fields into a new issue, with overrides
(`-s`, `-y`, `-a`, `-l`) and find/replace in summary/description (`-H find:replace`).

**Why jai should adopt**: Cloning is a common workflow (duplicate bug, create similar story).
jai already has `jai create` — cloning is `jai get KEY --json` + `jai create` with prefilled
fields. A dedicated command makes this a single operation.

**How to implement**: Read issue from local DB, apply overrides, call Jira create API directly
(same as `jai create`). No queue — returns the new key immediately.

**Suggested API**:
```
jai clone PROJ-123 [--summary "..."] [--set field=value] [--replace find:replace]
```

### 2. Sprint and Epic Batch Operations

**What jira-cli does**: `jira epic add EPIC-KEY ISSUE-1 ISSUE-2 ...` and
`jira sprint add SPRINT-ID ISSUE-1 ...` — batch up to 50 issues per call.

**Why jai should adopt**: jai already has `jai set` with `--query` for SQL-driven bulk updates.
Sprint and epic assignment via SQL query is a natural extension:
```
jai sprint add 42 --query "SELECT key FROM issues WHERE status = 'To Do' AND sprint IS NULL"
```

**How to implement**: Immediate API call (like `jai transition`), not queued. Uses Agile v1 API.
Fits jai's pattern of SQL-driven bulk operations.

### 3. Watching / Watchers

**What jira-cli does**: `jira issue watch KEY [USER]` — add user as watcher.

**Why jai should adopt**: Watching is lightweight and useful. Sync already has the data (`watchers`
field). Adding a watcher is a simple write operation.

**Suggested API**:
```
jai watch PROJ-123 [user@email.com]    # add watcher (default: self)
jai unwatch PROJ-123                    # remove self
```

### 4. Template Variables and User-Defined SQL Snippets

**What jira-cli does**: Smart shortcuts — `~` for negation, `x` for empty, date shortcuts
(`week`, `-7d`), `--history`, `--watching`.

**Why jai should adopt — via templates**: jai's SQL approach is more powerful than JQL, but raw
SQL for common patterns is verbose. Template variables and user-defined snippets can give SQL
the ergonomics of jira-cli's filter flags without adding a parallel query language.

#### Current state

jai has four built-in template variables today:
- `{{me}}` → config `me` value (email/username)
- `{{team}}` → config `team` value
- `{{today}}` → `2024-01-22` (current date)
- `{{week_ago}}` → `2024-01-15` (7 days ago)

#### Proposed: More built-in time variables

| Variable | Expands to | Use case |
|----------|-----------|----------|
| `{{yesterday}}` | `date('now', '-1 day')` | Daily standups, "what changed yesterday" |
| `{{month_ago}}` | `date('now', '-30 days')` | Staleness detection |
| `{{quarter_ago}}` | `date('now', '-90 days')` | Quarterly reviews |
| `{{this_week}}` | Monday of current week | "Issues created this week" |
| `{{this_month}}` | 1st of current month | "Issues created this month" |
| `{{this_quarter}}` | 1st of current quarter | Quarterly metrics |

Example:
```sql
SELECT key, summary, status FROM issues
WHERE updated < '{{month_ago}}' AND status NOT IN ('Done', 'Closed')
```

#### Proposed: Parameterized time variables

Instead of a fixed set, support `{{days_ago:N}}` and `{{weeks_ago:N}}`:

| Variable | Expands to | Example |
|----------|-----------|---------|
| `{{days_ago:N}}` | N days before today | `{{days_ago:14}}` → two weeks ago |
| `{{weeks_ago:N}}` | N weeks before today | `{{weeks_ago:4}}` → four weeks ago |
| `{{months_ago:N}}` | N months before today | `{{months_ago:3}}` → quarter ago |

This replaces an infinite set of fixed variables with three parameterized ones. A user writes:
```sql
SELECT key, summary FROM issues WHERE created >= '{{days_ago:5}}'
```

#### Proposed: User-defined SQL snippets in config

The most powerful extension. Let users define reusable SQL fragments in their config:

```yaml
snippets:
  active: "status NOT IN ('Done', 'Closed', 'Resolved')"
  stale: "julianday('now') - julianday(updated) > 28"
  my_open: "assignee = '{{me}}' AND {{active}}"
  high_pri: "priority IN ('Highest', 'High')"
  unassigned: "assignee IS NULL OR assignee = ''"
  has_epic: "epic_key IS NOT NULL AND epic_key != ''"
  recent: "updated >= '{{week_ago}}'"
  blocked: "status = 'Blocked'"
  bugs: "type = 'Bug'"
  no_sprint: "sprint IS NULL OR sprint = ''"
```

Usage — snippets compose with each other and with built-in variables:
```sql
-- "My open high-priority bugs"
SELECT key, summary, priority FROM issues
WHERE {{my_open}} AND {{high_pri}} AND {{bugs}}

-- expands to:
-- WHERE assignee = 'user@email.com'
--   AND status NOT IN ('Done', 'Closed', 'Resolved')
--   AND priority IN ('Highest', 'High')
--   AND type = 'Bug'
```

Snippets are resolved recursively (a snippet can reference another snippet, like `{{my_open}}`
referencing `{{active}}`), with a depth limit to prevent cycles.

**Why this is powerful**:
- Fits jai's config-driven philosophy — snippets live in YAML alongside views
- Composable — combine snippets in any WHERE clause
- Shareable — team members share the same config, same vocabulary
- Agent-friendly — an AI agent can discover available snippets via `jai schema snippets`
- SQL-native — no new query language, just reusable fragments

**Why this is better than jira-cli's approach**: jira-cli's filter flags (`-s`, `-a`, `-l`)
are hard-coded into the CLI. Users can't define their own. Snippets are user-extensible and
work in any SQL context — views, ad-hoc queries, even `jai set --query`.

#### Proposed: Project-aware variables

| Variable | Expands to | Use case |
|----------|-----------|----------|
| `{{projects}}` | `'PROJ1','PROJ2','PROJ3'` (from sync sources) | `WHERE project IN ({{projects}})` |

Useful when sync sources span multiple projects and you want to query across all of them
without hardcoding project keys.

#### Implementation approach

1. **Built-in time variables** — extend `resolveTemplates()` in `internal/query/engine.go` (trivial)
2. **Parameterized variables** — add regex matching for `{{days_ago:N}}` pattern (small)
3. **User-defined snippets** — add `Snippets map[string]string` to `config.Config`, resolve
   in `resolveTemplates()` with recursive expansion (medium)
4. **Schema introspection** — add `jai schema snippets` to list available snippets for agents

---

## Adopt: Medium Value

### 5. Remote Links

**What jira-cli does**: `jira issue link remote KEY URL [TITLE]` — attach a web URL to an issue.

**Why**: Useful for linking PRs, docs, dashboards. jai already has `jai link` for issue-to-issue
links. Extending to remote links is a small surface area addition.

**Suggested API**:
```
jai link PROJ-123 https://github.com/... "PR #42"    # detect URL → remote link
jai link PROJ-123 PROJ-456                           # detect key → issue link (existing)
```

### 6. Shell Completions

**What jira-cli does**: `jira completion bash|zsh|fish|powershell`.

**Why**: Standard CLI quality-of-life. Cobra generates these for free — it's already built in,
just needs to be exposed as a command.

**Suggested API**:
```
jai completion zsh    # print completion script
```

### 7. Issue Templates

**What jira-cli does**: `--template file.md` loads description from a file. Supports stdin via `-`.

**Why**: Standardized issue creation (bug reports, feature requests). Fits jai's config-driven
philosophy — templates could live in the config file or a `templates/` directory.

**Suggested API**:
```
jai create PROJ --template bug-report    # loads from config-defined template
jai create PROJ --body -                 # read from stdin
```

### 8. `jai open` — Open in Browser

**What jira-cli does**: `jira open KEY` opens the issue in the default browser.

**Why**: jai's TUI already has `o` to open in browser. A standalone command is useful outside the
TUI — especially for AI agents that want to give the user a clickable link.

**Suggested API**:
```
jai open PROJ-123              # open in browser
jai open PROJ-123 --url-only   # print URL (useful for agents)
```

---

## Skip: Deliberately Out of Scope

### Worklog Support

**What jira-cli does**: `jira issue worklog add KEY 2h30m`.

**Why skip**: Time tracking adds a new sync dimension (worklogs table), a new write command,
and Jira time format parsing — significant surface area for a feature that not all teams use.
Can be revisited if there's demand.

### Delete Issue

**What jira-cli does**: `jira issue delete KEY [--cascade]`.

**Why skip**: Deletion is rare in Jira workflows (most teams transition to Done/Closed/Won't Do).
The risk/reward ratio is poor — a destructive operation that's seldom needed. Users who need it
can use the Jira web UI or API directly.

### mTLS Authentication

**What jira-cli does**: Client certificate auth with CA cert, client cert, client key.

**Why skip**: Enterprise-only edge case. jai targets Jira Cloud with API tokens. Adding mTLS
adds configuration complexity and testing burden for a tiny user base. Can be revisited if
there's demand.

### Jira Server / Data Center Support

**What jira-cli does**: Full v2 API support for on-premise Jira, with version detection and
API proxying.

**Why skip**: jai's DB-first architecture assumes Jira Cloud's API behavior and field
conventions. Supporting Server/DC would require a compatibility layer for API differences,
field naming, pagination, and auth — significant complexity for a shrinking market segment.
jai's spec explicitly targets Jira Cloud.

### Explorer View (Split-Pane TUI)

**What jira-cli does**: Sidebar + content pane for epics and sprints.

**Why skip**: jai's TUI uses a tab-based multi-view model with grouping (`g` key). Grouping by
epic or sprint achieves the same hierarchy visualization within jai's existing paradigm.
A split-pane is a different UX model that would complicate the TUI architecture without
clear benefit over group-by.

### In-TUI Transitions

**What jira-cli does**: Press `m` on an issue → modal with transitions → execute.

**Why skip for now**: jai's TUI already has quick edit (`,` key) for field updates. Transitions
are available via `jai transition KEY STATUS`. Adding a TUI modal for transitions is a nice
enhancement but not a priority — it's a UX polish item, not a capability gap.

**Revisit**: Worth adding as a TUI enhancement in a future polish phase.

### Raw JQL Passthrough for List Commands

**What jira-cli does**: Every list command accepts `--jql` to append raw JQL.

**Why skip**: jai already has `jai query --jql` for live API queries. Adding JQL to other
commands would blur the line between jai's SQL-first model and JQL — exactly the kind of
compromise that weakens an opinionated tool.

### Issue History / Recently Viewed

**What jira-cli does**: `--history` flag shows recently viewed issues (via `issueHistory()` JQL
function).

**Why skip**: This is a Jira API feature that tracks web UI views. jai's DB-first model means
users query their synced data — "recently viewed in Jira's web UI" is not a useful local
concept. If needed, `ORDER BY updated DESC LIMIT 10` serves the same purpose from local data.

### CSV Output

**What jira-cli does**: `--csv` flag for comma-separated output.

**Why skip**: jai's `--json` output can be piped through `jq` for any format transformation.
Adding CSV is a marginal convenience that doesn't serve jai's primary users (agents want JSON,
humans want the TUI or lipgloss tables). If someone needs CSV:
```
jai query "SELECT key, summary, status FROM issues" --json | jq -r '.rows[] | @csv'
```

### Man Pages

**What jira-cli does**: `jira man --generate` creates UNIX man pages.

**Why skip**: Niche. Cobra's built-in `--help` and jai's `schema` command (for agents) cover
discoverability. Man pages are a distribution-era convention; modern CLI users expect `--help`
and web docs.

### `.netrc` Support

**What jira-cli does**: Reads credentials from `~/.netrc`.

**Why skip**: jai uses YAML config with `${ENV_VAR}` substitution. Environment variables are
the standard for secret injection in modern tooling (CI/CD, Docker, agents). `.netrc` adds a
second credential path without clear benefit.

### OS Keychain Integration

**What jira-cli does**: Stores credentials in macOS Keychain / GNOME Keyring / Windows Credential
Manager via `go-keyring`.

**Why skip**: Adds a CGO dependency on some platforms and platform-specific testing burden. jai
already uses env vars for credentials (`${JIRA_API_TOKEN}` in config). For users who want
keychain integration:
```yaml
api_token: ${JIRA_API_TOKEN}
# Then: export JIRA_API_TOKEN=$(security find-generic-password -s jira -w)
```

---

## Priority Order

| Priority | Feature | Effort | Impact |
|----------|---------|--------|--------|
| 1 | Shell completions | Low | Medium — free from Cobra, just expose it |
| 2 | `jai open` | Low | Medium — useful for agents and humans |
| 3 | Built-in time variables | Low | Medium — extend `resolveTemplates()` |
| 4 | Parameterized time variables (`{{days_ago:N}}`) | Low | Medium — three patterns replace infinite fixed set |
| 5 | User-defined SQL snippets | Medium | High — composable, shareable, agent-discoverable |
| 6 | Issue cloning | Medium | Medium — common workflow |
| 7 | Remote links | Low | Low — extends existing link command |
| 8 | Sprint/epic batch ops | Medium | Medium — SQL-driven bulk is powerful |
| 9 | Issue templates | Medium | Low — config-driven standardization |
| 10 | Watching | Low | Low — nice to have |

---

## Summary

jira-cli is a **broad, comprehensive** Jira CLI that aims to replicate the full Jira web UI in
the terminal. jai is a **deep, opinionated** tool that trades API breadth for SQL power, offline
speed, and agent optimization.

The adopted features fall into three categories:

1. **SQL ergonomics** (template variables, parameterized time, user-defined snippets) — these
   make jai's SQL-first model as convenient as jira-cli's filter flags, while being more
   powerful and user-extensible.
2. **Missing write operations** (clone, watch, remote links, sprint/epic batch) — these fill
   capability gaps without compromising jai's architecture.
3. **CLI polish** (shell completions, `jai open`, templates) — these make jai more pleasant
   without changing its philosophy.

The features to skip are those that would pull jai toward being an API wrapper (JQL passthrough,
history, Server/DC support, worklog, delete) or add complexity for edge-case users (mTLS,
keychain, `.netrc`, CSV, man pages).

jai's competitive advantage is that it **doesn't try to be jira-cli**. It syncs data locally
and lets SQL do the heavy lifting. Every adopted feature should reinforce that stance.
