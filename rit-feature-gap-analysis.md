# RIT Feature Gap Analysis: jai CLI

Comparison of what the [ACS RIT idea](~/idea/acs-rit/) expects from `jai` versus what `jai` supports today.

## Summary

jai covers **~80%** of what the RIT system needs. The core read-query-write loop (`sync → query → set → push`) is solid. The gaps are mostly in **array field handling**, **workflow transitions**, **changelog/history**, and **some convenience commands** that the RIT skills would rely on heavily.

---

## Fully Supported (no changes needed)

| RIT Requirement | jai Feature | Notes |
|---|---|---|
| Query ticket metadata (type, priority, team, labels, fix_version, assignee) | `jai query <sql>` | All fields are synced and queryable via SQL |
| Fetch single issue details | `jai get <key>` | Returns full issue with `--comments` flag; JSON output via `--json` |
| Set fields (priority, assignee) on tickets | `jai set <key> <field> <value>` | Queues locally, pushed via `jai push`. Works for scalar fields |
| Add comments to tickets | `jai comment <key> <text>` | Queues locally + optimistic local insert. ADF conversion on push |
| Push queued changes to Jira Cloud | `jai push` | Processes set_field, add_comment, and transition operations with retry |
| Refresh local snapshot (incremental) | `jai sync` | High-water-mark incremental sync; `--full` for complete resync; `--resume` for crash recovery |
| Create new tickets | `jai create <project>` | Direct API call (not queued). Supports `--type`, `--summary`, `--parent`, `--labels`, `--priority`, `--assignee`, `--field key=value` |
| Create sub-tasks (daily log pattern) | `jai create --parent <key>` | `--parent` flag exists, enables the RIT daily log parent+sub-task pattern |
| Full-text search | `jai search <text>` | FTS5 with porter stemmer over summary, description, comments_text, labels |
| Schema introspection for agents | `jai schema`, `jai schema db`, `jai schema values` | Command schemas, DB column listing, distinct value enumeration |
| Field discovery | `jai fields` | Lists all fields with `--stats` for population counts, `--filter` for substring match |
| Agent-friendly JSON output | `--json` global flag | Consistent `{"ok":true,"data":...}` / `{"ok":false,"error":...}` envelope on all commands |
| Column filtering | `--fields` global flag | Comma-separated field names to restrict output |
| Named views / saved queries | `jai view [name]` | Config-defined queries with columns, grouping, sorting, color rules |
| Auto-sync on stale data | PersistentPreRunE auto-sync | Triggers sync if any source is older than configured interval (default 15m) |
| Template variables in queries | `{{me}}`, `{{team}}`, `{{today}}`, `{{week_ago}}` | Resolved before SQL execution |
| Live JQL queries | `jai query --jql <jql>` | Bypasses local DB, paginates through Jira API directly |
| Auth status and health check | `jai status` | Shows auth, sync metadata, issue counts, pending changes, DB size |
| Interactive setup | `jai init` | Wizard for credentials, connection test, source config, initial sync |
| TUI for human use | `jai tui` | Bubbletea full-screen app with views, sorting, detail pane, inline editing |
| Resolution time queries | SQL on `created`/`resolved` columns | Can compute `julianday(resolved) - julianday(created)` for resolution time analysis |
| Query by pscomponent labels | SQL on `labels` column | Labels stored as JSON arrays, queryable with `json_each()` or `LIKE` |
| Batch CVE status lookups | `jai query` | SQL queries can filter by type='Vulnerability' + any other criteria |

---

## Gaps: Must Fix for RIT

### Gap 1: Array field operations (add/remove labels)

**RIT need:** `/rit-escalate` must ADD the `rit-escalated` label without replacing existing labels. `/rit-triage` must ADD `rit-sig:scanner` labels. CVE auto-routing adds labels. Nearly every RIT skill touches labels.

**Current state:** `jai set` replaces the entire field value. For labels (now stored as JSON arrays after migration 6), `jai set ROX-123 labels '["new-label"]'` would **overwrite** all existing labels. There is no add/remove semantic.

**What's needed:**
- `jai set --add <key> labels <value>` — append to an array field
- `jai set --remove <key> labels <value>` — remove from an array field
- Or a dedicated `jai label add/remove` subcommand
- The Jira API supports `{"update":{"labels":[{"add":"new-label"}]}}` — the writer needs to support this payload shape alongside the current `{"fields":{"labels":"value"}}` shape

**Impact:** HIGH — blocks the most critical RIT skills (`/rit-escalate`, `/rit-triage`)

### Gap 2: No `jai transition` CLI command

**RIT need:** RIT engineers need to close tickets, move them to "In Progress", etc. The daily log pattern requires transitioning sub-tasks. `/rit-cve` needs to close tickets with VEX justification.

**Current state:** The writer CAN process transition operations, and the Jira client has `GetTransitions()` and `ExecuteTransition()` methods. But there is **no CLI command** to queue a transition. Only accessible through TUI inline editing or direct DB insertion.

**What's needed:**
- `jai transition <key> <status>` — list available transitions and execute one
- `jai transition <key> --list` — show available transitions for an issue
- Should resolve transition by name (e.g., "Done", "In Progress") not just ID

**Impact:** MEDIUM-HIGH — RIT engineers currently can't move tickets through workflows via CLI/skills

### Gap 3: Label value handling for Jira API

**RIT need:** When pushing label changes, the Jira API expects labels as an array of strings: `{"fields":{"labels":["label1","label2"]}}`. 

**Current state:** `jai set` stores the value as a string in the payload (`{"field":"labels","value":"some-value"}`). The writer passes this string directly to `UpdateField`, which wraps it as `{"fields":{"labels":"some-value"}}`. For array fields (labels, components, fixVersions), this will fail or produce incorrect results because the Jira API expects an array, not a string.

**What's needed:**
- The set command (or writer) needs to detect array fields and marshal the value correctly
- For labels: accept `jai set ROX-123 labels "label1,label2"` and send `{"fields":{"labels":["label1","label2"]}}` to the API
- Or use the `--add`/`--remove` approach from Gap 1 which uses the `update` API

**Impact:** HIGH — `jai set` for array fields likely doesn't work correctly on push today

---

## Gaps: Should Fix for RIT

### Gap 4: No changelog/history sync

**RIT need:** `/rit-handover` wants resolution time benchmarks and status change history. `/rit-triage` wants to see recent status changes. The RIT digest needs to track "what changed since yesterday."

**Current state:** The DB has a `changelog` table (`EnsureChangelogTable`, `UpsertChangelog` methods exist). The config has a `sync.history` flag. But the sync engine **never calls** these — changelog data is not fetched from Jira.

**What's needed:**
- Sync engine should fetch issue changelog via `/rest/api/3/issue/{key}/changelog` during sync
- Populate the `changelog` table with status changes, field updates, assignee changes
- A `jai changelog <key>` command to display history for an issue
- This enables queries like "show all tickets whose status changed in the last 24 hours"

**Impact:** MEDIUM — resolution time can be computed from `created`/`resolved` dates without changelog, but status change tracking and "what changed" queries require it

### Gap 5: No bulk operations

**RIT need:** Auto-assignment on intake wants to bulk-assign tickets. Priority normalization wants to bulk-set priority on many tickets. Monthly lint operations need to update many tickets.

**Current state:** Each `jai set` call is individual. To update 50 tickets, you run `jai set` 50 times.

**What's needed:**
- `jai set --query "SELECT key FROM issues WHERE assignee IS NULL AND type = 'Vulnerability'" assignee "current-rit-primary"` — set a field on all matching issues
- Or accept multiple keys: `jai set ROX-1,ROX-2,ROX-3 priority Major`
- This is a convenience — scripts can loop, but native bulk support is cleaner for skills

**Impact:** LOW-MEDIUM — workaround exists (shell loop), but native support improves skill ergonomics

### Gap 6: No link creation command

**RIT need:** `/rit-log` promotes sub-tasks to standalone tickets and wants to link them back to the daily log parent. Escalation tracking may want to link related tickets.

**Current state:** Issue links are synced FROM Jira (stored in `issue_links` table), but there is no command to CREATE links. The Jira client has no `CreateLink` method.

**What's needed:**
- `jai link <from-key> <link-type> <to-key>` — create an issue link (e.g., "relates to", "is blocked by")
- Or support linking as a `--link` flag on `jai create`

**Impact:** LOW-MEDIUM — sub-task creation via `--parent` covers the main use case; explicit linking is secondary

---

## Gaps: Nice to Have

### Gap 7: No attachment handling

**RIT need:** `/rit-cve` and customer escalation investigation sometimes need to check attachments (diagnostic bundles, logs).

**Current state:** Attachments are not synced or accessible through jai.

**What's needed:**
- `jai attachments <key>` — list attachments on an issue
- `jai download <key> <filename>` — download an attachment to local filesystem
- Not critical for RIT — the Red Hat Support Tool handles diagnostic bundles separately

**Impact:** LOW

### Gap 8: No watcher/subscription management

**RIT need:** RIT primary may want to watch tickets they're triaging.

**Current state:** No watcher management.

**Impact:** LOW — Jira web UI is fine for this

### Gap 9: No sprint/board integration

**RIT need:** Minimal — RIT doesn't manage sprints. But knowing which sprint a ticket is in can help with context.

**Current state:** Sprint data may be synced as a custom field but there are no sprint-specific commands.

**Impact:** LOW

---

## Feature Matrix

| Feature | RIT Expects | jai Today | Status |
|---|---|---|---|
| `jai query` — SQL against local DB | Yes | Yes | SUPPORTED |
| `jai query --jql` — live Jira queries | Yes | Yes | SUPPORTED |
| `jai get` — single issue fetch | Yes | Yes | SUPPORTED |
| `jai set` — set scalar fields | Yes | Yes | SUPPORTED |
| `jai set` — add/remove array values (labels) | Yes | No | **GAP 1** |
| `jai set` — correct array field push | Yes | Broken | **GAP 3** |
| `jai comment` — add comments | Yes | Yes | SUPPORTED |
| `jai create` — new tickets | Yes | Yes | SUPPORTED |
| `jai create --parent` — sub-tasks | Yes | Yes | SUPPORTED |
| `jai push` — push changes to Jira | Yes | Yes | SUPPORTED |
| `jai sync` — incremental sync | Yes | Yes | SUPPORTED |
| `jai sync --full` — full resync | Yes | Yes | SUPPORTED |
| `jai search` — full-text search | Yes | Yes | SUPPORTED |
| `jai schema` — command + DB introspection | Yes | Yes | SUPPORTED |
| `jai fields` — field discovery + stats | Yes | Yes | SUPPORTED |
| `jai status` — health check | Yes | Yes | SUPPORTED |
| `jai init` — setup wizard | Yes | Yes | SUPPORTED |
| `jai tui` — full-screen TUI | Yes | Yes | SUPPORTED |
| `jai view` — saved queries | Yes | Yes | SUPPORTED |
| `--json` — agent-friendly output | Yes | Yes | SUPPORTED |
| `--fields` — column filtering | Yes | Yes | SUPPORTED |
| Template variables (`{{me}}`, etc.) | Yes | Yes | SUPPORTED |
| `jai transition` — workflow transitions | Yes | No CLI cmd | **GAP 2** |
| Changelog/history sync | Yes | DB exists, not synced | **GAP 4** |
| Bulk set operations | Nice to have | No | **GAP 5** |
| Issue link creation | Nice to have | Read-only | **GAP 6** |
| Attachment access | Nice to have | No | **GAP 7** |

---

## Recommended Priority for Gap Work

### Before RIT Rotation 1 (Prep Sprint)

1. **Gap 1 + Gap 3: Array field operations** — Without this, `/rit-escalate` and `/rit-triage` can't add labels, which are the most fundamental RIT automation actions. Implement `--add`/`--remove` flags on `jai set` and fix array field serialization for push.

2. **Gap 2: `jai transition` command** — Expose the existing transition infrastructure as a CLI command. The writer and Jira client already support it; this is just wiring up a cobra command.

### During Rotations 1-2 (Build While Running)

3. **Gap 4: Changelog sync** — Enable the existing `sync.history` config and implement changelog fetching. This unlocks "what changed" queries and richer handover metrics.

### Rotations 3+ (As Needed)

4. **Gap 5: Bulk operations** — Add `--query` flag or multi-key support to `jai set`.
5. **Gap 6: Link creation** — Add `jai link` command.
6. **Gap 7: Attachment access** — Low priority, Red Hat Support Tool covers most cases.
