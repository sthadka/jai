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

Changelog data is fetched automatically on every `jai sync`.

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

Replace all values at once with either a comma-separated string or a JSON array
(the same shape the read column emits, so a value read back can be written
straight back):

```sh
jai set ROX-123 labels "bug,security,backend"
jai set ROX-123 labels '["bug","security","backend"]'
```

**User arrays (e.g. Contributors).** For array fields whose elements are Jira
users, pass emails or accountIds; jai resolves emails to accountIds and sends
the objects Jira requires. The read column stores these as a JSON array of
emails, so it round-trips:

```sh
jai set ROX-123 contributors "jvmartin@redhat.com,ksanchet@redhat.com"
jai set ROX-123 contributors '["jvmartin@redhat.com","ksanchet@redhat.com"]'
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
jai sync                # incremental sync (reconciles rank + syncs changelogs)
jai sync --full         # full resync with deletion detection (reconciles all fields)
jai sync --force        # re-fetch all changelog history from scratch
jai sync prune          # dry run: list local data no longer covered by config
jai sync prune --yes    # delete issues/changelog/comments/metadata for removed sources
```

`jai sync` also runs a **reconcile pass** for `sync.reconcile_fields`
(default `[rank]`) to catch changes Jira makes without bumping the issue
`updated` timestamp — notably rank/LexoRank reorders. To keep it cheap the scan
is limited by `sync.reconcile_scope` (default `statusCategory != Done`, since
closed issues can't be re-ranked) and runs at most once per source per
`sync.reconcile_interval` (default `24h`; set `"0"` to run every sync). A
periodic `jai sync --full` reconciles all other fields; sync warns when one is
overdue (configurable via `sync.full_sync_warning`).

Removing a source from `sync_sources` stops future syncs but leaves its data in
the local DB, so it keeps showing in the overdue reminder. `jai sync prune`
deletes issues, changelog, comments, and sync metadata for any project or source
no longer in config (dry run by default; `--yes` applies). Issue deletion is
skipped when a `jql:` source has no parseable project key.

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
## Flow metrics

jai ships a portable flow-metrics suite computed entirely from the local SQLite
database (issues + replayed changelog history). There is a single definition of
each metric behind `internal/metrics`; the CLI (`jai metrics`), the MCP tool
(`jai_metrics`), the TUI pane (key `m`) and the offline dashboard all read it, so
equivalent requests return identical values, denominators, time basis and
taxonomy version — the SQL is never forked per consumer.

The taxonomy is tenant-neutral: no status, type or resolution literal is baked
into the code. Everything comes from the `metrics:` block in your config.

### Configuring the `metrics:` block

Add a `metrics:` block to your config YAML (a full annotated example lives in
`config.example.yaml`):

```yaml
metrics:
  status_stage_ids: {}            # optional: "10001": done (Jira status ID; overrides name, survives renames)
  status_stages:                  # name fallback; names not listed -> statusCategory default -> unknown
    todo:    [New, Backlog, To Do, Refinement, Planning]
    active:  [In Progress, Review, Dev Complete, Testing, Approved]
    waiting: [Waiting, Hold]
    done:    [Closed, Release Pending]
  wip_stages: [active]            # stages counted as WIP / cycle-time active
  done_resolutions: [Done, Done-Errata]  # omit to run delivery metrics over ALL resolved issues (labelled)
  type_classes:                   # types not listed -> `other`
    bug:      [Bug]
    feature:  [Story, Feature, "Feature Request"]
    task:     [Task, Sub-task]
    security: [Vulnerability, Weakness]
  ratio:                          # zero denominator is undefined, never 0
    numerator: bug
    denominator: feature
    cohort: completed             # created | completed
  tracked_fields: [status, priority, assignee, Team, Rank]  # must appear in changelog.field
  blocker_link_types: [is blocked by, depends on]
  sla:                            # optional; drives security/SLA metrics
    security:
      severity_field: priority    # categorical field with per-value deadlines
      deadlines_days: { Critical: 7, Major: 30, Minor: 90 }
      clock_start: created
      on_missing_severity: exclude  # exclude | treat_as:<label>
```

Key semantics:

- **Stage precedence**: `status_stage_ids` (Jira status ID) → `status_stages`
  (status name) → statusCategory default → the surfaced `unknown` bucket.
  `unknown` is a real bucket, never dropped.
- **`wip_stages`**: stages treated as work-in-progress / cycle-time active
  (default `[active]`).
- **`done_resolutions`**: required for *genuine-delivery* metrics. If unset,
  delivery metrics compute over ALL resolved issues, label the result
  "includes non-delivery resolutions", and the `--genuine` variant is disabled
  (errors rather than guessing).
- **`type_classes` / `ratio`**: named numerator/denominator classes (e.g.
  bug/feature). A zero denominator is undefined (never 0); unlisted types fall
  into `other`.
- **`tracked_fields`**: scalar fields replayed into interval facts; each must
  appear in `changelog.field`. Set-valued fields (`Sprint`, `labels`,
  `components`) are modelled as membership intervals, not scalar replay.

**Fail-closed validation.** Structural config errors (malformed YAML, an
undefined stage/class reference, a duplicate mapping) reject the *individual*
affected metric at load time — not the whole document, and not as a silent
warning. Unrelated metrics keep working; the rejected metric is surfaced as a
warning on the CLI output and as an "unavailable" card on the dashboard.
Data-dependent gaps (missing fields, empty cohorts) are runtime coverage
diagnostics, not fatal errors.

### The `jai metrics` command

```sh
jai metrics                       # list available metrics (alias: jai metrics list)
jai metrics cycle_time            # run one metric
jai metrics cycle_time --format csv
jai metrics throughput --period week --from 2024-01-01 --to 2024-07-01
jai metrics ratio --cohort completed
jai metrics cycle_time --genuine  # require the delivery-only variant
jai metrics coverage              # data-coverage diagnostics
jai metrics rebuild               # full fact-store restatement
jai metrics dashboard             # offline self-contained HTML dashboard
```

Run flags (on `jai metrics <name>`):

| Flag | Description |
|------|-------------|
| `-p`, `--project` | Limit to project(s); repeat or comma-separate. |
| `--as-of` | Compute open intervals/ages against this time (RFC3339 or `YYYY-MM-DD`; default now UTC). |
| `--from` | Range start (inclusive) for time-bounded metrics. |
| `--to` | Range end (exclusive) for time-bounded metrics. |
| `--period` | Throughput/net-flow bucket: `month` (default) or `week`. |
| `--cohort` | Cohort selector (metric-specific: `created` \| `completed` \| `all`). |
| `--basis` | Counting basis (currently `snapshot` only; other values are rejected). |
| `--genuine` | Require the genuine-done variant (errors if `done_resolutions` is unset). |
| `--timeout` | Bound the computation (default `1m`; `0` disables). |

`--project` and `--as-of` are shared (persistent) across the `coverage`,
`rebuild` and `dashboard` subcommands too. All standard output formats apply
(`--format table|json|csv|tsv|markdown`, `--json`). On the row formats the
caveats (cohort, genuine-done state, build/source freshness, exclusions,
warnings) print to stderr so the CSV/TSV data stream stays clean; the `json`
format carries the full envelope.

### Metric catalog

`jai metrics list` prints the registered ids. Current catalog:

| Metric | What it reports |
|--------|-----------------|
| `cycle_time` | Active-stage duration distribution (percentiles) for completed work. |
| `lead_time` | Create-to-done duration distribution for completed work. |
| `throughput` | Count of items completed per bucket (`--period`). |
| `net_flow` | Arrivals minus departures per bucket (issues-only). |
| `aged_open` | Age distribution of still-open issues vs `--as-of`. |
| `resolution_mix` | Breakdown of resolved issues by resolution value. |
| `ratio` | Configured numerator/denominator type-class ratio. |
| `workload_by_assignee` | Current open workload grouped by assignee. |
| `workload_throughput` | Completed-item throughput by assignee. |
| `workload_throughput_current` | Throughput attributed to the current assignee. |
| `workload_time_on_owner` | Time intervals spent on each owner. |
| `reassignment_thrash` | Assignee churn / reassignment rate signal. |
| `reassignment_count` | Count of assignee changes. |
| `blocker_ageing` | Age proxy for blocked issues (via `blocker_link_types`). |
| `security_sla` | SLA compliance for security/severity deadlines (`sla:` config). |
| `wip` | Current work-in-progress item count. |
| `stage_dwell` | Time spent per flow stage. |
| `cfd` | Cumulative flow diagram data across all stages. |
| `reopen` | Reopen counts/rate from status replay. |
| `flow_occupancy` | Share of time spent occupying each stage. |
| `sprint_resolution` | Eventual resolution outcome of sprint-scoped work. |
| `priority_changes` | Count/rate of priority field changes. |
| `rank_volatility` | Rank-change churn signal. |
| `triage_latency` | Time from creation to first triage transition. |

Every metric returns a truthful known/unknown result: empty cohorts, absent
tracked fields and unconfigured dependencies surface as warnings or
"unavailable", never as fabricated values.

### Generated views and snippets

The status→stage taxonomy, the WIP set and the genuine-delivery filter are also
compiled from the `metrics:` config into shared SQL fragments (`jai snippet`)
and simple views (`jai view`), so ad-hoc queries reuse exactly one definition of
the taxonomy. They are regenerated from the current policy on every command (a
config change never leaves a stale fragment behind):

```sh
jai snippet list          # includes: status_stage_map, active_stages, genuine_done  (generated)
jai view metrics-stage-breakdown   # Flow stage breakdown
jai view metrics-wip               # Work-in-progress item count
jai view metrics-delivered         # Genuinely delivered item count
jai view metrics-resolution-mix    # Resolution mix of resolved issues
```

```sql
-- reuse the generated fragment in your own query
jai query "SELECT {{status_stage_map}} AS stage, COUNT(*) FROM issues GROUP BY stage"
```

### Generations, rebuild and the locking model

Interval metrics are served from a materialized **fact store** (replayed
changelog intervals + rollups). Each build tags its rows with a `generation`
id and a one-row active-generation pointer is swapped in a single transaction
only after all facts and rollups for that generation are complete. Readers
always filter on the active generation, so a query never sees a half-built
generation; obsolete generations are deleted after the swap. WAL mode keeps
reads live during a build.

- `jai metrics rebuild` stages a fresh full generation (`BuildAll`) and
  atomically publishes it. Run it after changing the `metrics:` config — a map
  change restates history and requires a full rebuild.
- Normal `jai sync` refreshes facts incrementally for changed issues within the
  active generation (changelog/prune/sync paths are wired to keep facts fresh).
- Because jai runs as multiple processes (CLI, `serve`, TUI) over one SQLite
  file, the rebuilder and every incremental writer take a cross-process OS
  advisory lock (`flock` on a sidecar file in the DB dir). A rebuild launched
  from a second process can never publish stale facts over a newer incremental
  update.

If you query a delivery metric before any generation exists, you get a result
with a warning ("no active fact generation; build the fact store before
querying delivery metrics") rather than an error.

### MCP `jai_metrics`

The `metrics` toolset exposes a read-only `jai_metrics` MCP tool backed by the
same shared service as the CLI. Omit `metric` to list; supply it to run one and
get the full envelope (values plus basis, cohort, taxonomy version, policies,
exclusions, warnings, build generation and per-project source freshness). It
accepts `metric`, `project`, `as_of`, `from`, `to`, `period`, `cohort`,
`basis`, `genuine` and `timeout_seconds` (default 25s, capped at 30s). Enable
it with `jai serve --toolsets metrics` (or include it in `JAI_MCP_TOOLSETS`).

### TUI metrics pane

In `jai tui`, press `m` to open the read-only metrics pane. It renders the same
computed metrics through the shared service; scroll with the usual vim-style
keys. It never writes the fact store.

### Offline dashboard

```sh
jai metrics dashboard                               # writes ./dashboard.html
jai metrics dashboard --output flow.html --project ENG
jai metrics dashboard --from 2024-01-01 --to 2024-07-01 --as-of 2024-07-01
jai metrics dashboard --title "Platform Flow"
```

`jai metrics dashboard` exports ONE self-contained HTML file: the computed
metric data is embedded as JSON and the visualization layer is an inlined,
pure-SVG renderer. There are no CDN references, no `<script src>` and no network
or file-fetch requests at runtime — open it from disk with networking disabled
and every chart renders. Metrics that are unconfigured or have no data render as
explicit "unavailable" cards rather than fabricated charts.

Dashboard flags: `-o`/`--output` (default `dashboard.html`), `--title`
(default `Flow Metrics`), `--from`/`--to` (trend window; default `as_of − 6
months` .. `as_of`), plus the inherited `--project`/`--as-of`.

**Privacy.** The artifact embeds the selected Jira issue data. Share it only
with viewers authorized to see those issues.

### Known limitations / future work

- **Single-run sync changelog staleness** — within a single `jai sync` run the
  changelog-derived facts can lag the just-synced changelog; re-running sync or
  `jai metrics rebuild` refreshes them (tracked as `jai-xoi.1`).
- **Numeric `severity_bands` SLA config** — the SLA/security metric currently
  keys deadlines off categorical severity values; numeric severity-band
  compliance config is not yet supported (tracked as `jai-xoi.2`).
- **MCP `jai_sync` changelog refresh** — triggering sync through the MCP
  `jai_sync` tool does not yet refresh changelog-derived metric facts in the
  same call (tracked as `jai-xoi.3`).
- **Deferred signals** — true optional-completion events, true sprint/velocity
  and PR-based metrics remain separately labelled and deferred until their own
  prerequisites land; they are not reported as supported.

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
