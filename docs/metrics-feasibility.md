# Flow Metrics Feasibility — jai local DB

What metrics the current `jai` dataset can realistically support, grounded in direct
queries against the local SQLite store (`~/.local/share/jai/jai.db`, 4.3 GB).

_Snapshot: 2026-11-11. 43,331 issues across ROX (37,751), CMP (4,549), HPSTRAT (772), RFE (259)._

## TL;DR

The **changelog is the engine** — 875,526 field-change events over 41,898 issues
(~97% of all issues carry history), spanning **2018-01 → 2026-11**. Status, assignee,
priority, resolution, Sprint, labels, and Story Points changes are all time-stamped and
attributable to an author. This unlocks the majority of flow metrics.

**Fully feasible now (8/8 requested, with caveats on 2):**

| Metric | Verdict | Primary source |
|---|---|---|
| Cycle time distribution | ✅ Strong | `changelog` status transitions |
| Lead time trends | ✅ Strong | `created` → `resolved` |
| Work-in-progress (WIP) trends | ✅ Strong | reconstructed from `changelog` status |
| Issues completed over time | ✅ Strong | `resolved` / status→Done events |
| Blocker ageing | ⚠️ Partial | `is blocked by` links + `Flagged`/`Hold` |
| Sprint completion | ⚠️ Partial | `Sprint` changelog (no sprint dates) |
| Workload by assignee | ✅ Strong | `assignee` + `changelog` |
| Bug-to-feature ratio | ✅ Strong | `type` column |

**Hard limits:** story-point velocity (points field ~empty), true sprint boundaries
(start/end dates not synced), and `status_category` *history* (changelog stores status
name, not category) are the only real gaps — all have workarounds below.

>> Recommendation: Replace “fully feasible” and “only real gaps” with separate verdicts for current snapshots, historical estimates, and validated metrics. Having any changelog on 97% of issues does not prove complete status history; report per-project coverage, sync freshness, excluded cohorts, and reconciliation against current state before calling historical results strong.

---

## Data inventory (verified)

### Tables and coverage

| Table | Rows | Notes |
|---|---|---|
| `issues` | 43,331 | 687 columns (core + custom Jira fields) |
| `changelog` | 875,526 | 41,898 distinct issues (~97% coverage) |
| `issue_links` | 151,358 | typed, directional links |
| `comments` | populated | per-issue discussion |
| `attachments` | populated | — |
| `sprints` | **0** | ❌ empty — not synced |
| `boards` | **0** | ❌ empty — not synced |
| `dev_links` | **0** | ❌ empty — PR/commit links not synced |

### Changelog field coverage (top fields)

| Field | Events | Issues | Enables |
|---|---|---|---|
| `status` | 129,854 | 39,195 | cycle/lead time, WIP, throughput |
| `Workflow` | 68,668 | 30,007 | workflow step history |
| `Rank` | 66,451 | 37,110 | backlog reprioritisation |
| `resolution` | 47,847 | 37,236 | genuine-done filtering |
| `Sprint` | 40,240 | 12,427 | sprint membership history |
| `assignee` | 30,209 | 23,170 | reassignment / workload churn |
| `labels` | 27,055 | 12,805 | tag-based cohorts |
| `priority` | 18,667 | 16,298 | priority escalation ageing |
| `Team` | 18,175 | 15,643 | team attribution |
| `Story Points` | 3,857 | 3,238 | ⚠️ sparse — not velocity-grade |
| `Flagged` | 151 | 99 | ⚠️ impediment flag — very sparse |

### Status → category map (current)

`Done`: Closed (37,882), Release Pending (247) · `In Progress`: In Progress (495),
Review (127), Approved (34), Dev Complete (5), Testing (2) · `To Do`: New (3,163),
Backlog (535), To Do (458), Refinement (235), Waiting (85), Planning (63).

### Resolution distribution

`Done` 26,619 · _(empty/unresolved)_ 5,100 · `Won't Do` 4,459 · `Obsolete` 2,962 ·
`Duplicate` 1,851 · `Not a Bug` 1,153 · `Cannot Reproduce` 935.
→ Filter to genuine completions with `resolution IN ('Done','Done-Errata')`.

---

## Per-metric findings

### 1. Cycle time distribution — ✅ Strong

Cycle time = first entry into an active status → `resolved`. The `changelog` records
every status transition, so we take `MIN(changed_at)` where `to_string` is an active
status (In Progress / Review / Dev Complete / Testing / Approved) and subtract from
`resolved`.

**Proven:** ROX, resolved since 2024, 7,777 issues, avg **62.2 days** start→done.

```sql
WITH ip AS (
  SELECT issue_key, MIN(changed_at) started FROM changelog
  WHERE field='status' AND to_string IN ('In Progress','Review','Dev Complete','Testing')
  GROUP BY issue_key)
SELECT i.type, COUNT(*) n,
       ROUND(AVG(julianday(i.resolved)-julianday(ip.started)),1) avg_days
FROM issues i JOIN ip ON ip.issue_key=i.key
WHERE i.resolved>'2024-01-01' AND julianday(i.resolved)>=julianday(ip.started)
GROUP BY i.type;
```

>> Recommendation: Make this query reproduce the stated ROX result: add `project='ROX'`, the agreed genuine-completion filter, and `Approved` (listed in the definition but omitted from SQL). Define first-ever start versus latest work episode and final resolution versus first completion for reopened issues; these produce different cycle times. Include the exact cohort and exclusions with the reported 7,777 / 62.2-day result.

Distribution (p50/p85/p95), percentiles, per-type/priority/component/team splits all
computable. Caveat: issues that skipped straight to Closed with no active-status event
are excluded (fall back to `created→resolved`, i.e. lead time).

>> Recommendation: Keep missing-start cycle times unknown and report their count; do not mix `created→resolved` into the cycle-time distribution. Show lead time separately so missing history does not silently inflate cycle-time percentiles.

### 2. Lead time trends — ✅ Strong

Lead time = `created → resolved`, both first-class columns, 100% / ~88% populated
(38,129 Done). Trend by resolution month, rolling percentiles, per project/type.

```sql
SELECT strftime('%Y-%m', resolved) m, COUNT(*) n,
       ROUND(AVG(julianday(resolved)-julianday(created)),1) avg_lead_days
FROM issues WHERE resolved IS NOT NULL GROUP BY m ORDER BY m;
```

>> Recommendation: Do not equate Done-category membership with a populated resolution timestamp without a cross-check. Apply the same completion policy across lead time and throughput, validate timestamp order, and publish the eligible denominator. Current `resolved` values describe a current snapshot, not necessarily every historical completion after reopening; label snapshot-based trends accordingly or derive completion events from history.

### 3. Work-in-progress (WIP) trends — ✅ Strong

No daily snapshots exist, but WIP is **reconstructable** from the status changelog:
replay transitions to count, for any date, how many issues sat in an active status.
Per-issue active interval = [first→active event, first→done event]; WIP(t) = intervals
covering t. Buildable as a daily/weekly time series per project/team. More expensive
(interval math over 129k status events) but entirely feasible offline.

>> Recommendation: Reconstruct every consecutive status interval as `[entered_at, exited_at)`, not one first-active→first-done span. Count only active intervals, including reopened episodes and still-open work; exclude returns to backlog/waiting. Seed creation→first-change from the earliest `from_value`/`from_string` only when history is complete, handle unchanged issues, and mark unknown periods explicitly. Define timezone and daily sampling boundary so transitions are not double-counted.

### 4. Issues completed over time (throughput) — ✅ Strong

**Proven** — ROX monthly resolved counts, e.g. 2025-03: 624, 2025-09: 496, 2026-05: 806.

```sql
SELECT strftime('%Y-%m', resolved) m, COUNT(*) n
FROM issues WHERE project='ROX' AND resolved IS NOT NULL GROUP BY m ORDER BY m;
```

Split by type/priority/assignee/team; filter `resolution IN ('Done','Done-Errata')` to
exclude Won't-Do/Duplicate noise.

### 5. Blocker ageing — ⚠️ Partial (workable, multi-signal)

No single clean "blocked" state. Three signals, strongest first:

1. **Dependency links** — `issue_links` `is blocked by` (937) / `blocks` (820) /
   `depends on` (915). Age a blocked issue while its blocker is still open (join
   `linked_status`). Best structural signal.
2. **Status dwell** — time spent in `Waiting` / `Hold` / `Planning` via changelog
   intervals (same technique as WIP).
3. **`Flagged` changelog** (Flagged→Impediment) — only 151 events / 99 issues, and the
   `blocked_reason` column is 99% literal `"None"`. Too sparse to rely on alone.

Verdict: blocker *ageing* via link-based and status-dwell methods is solid; the explicit
Jira impediment flag is not.

>> Recommendation: Downgrade link-based “blocker ageing” to “age of currently dependency-linked issues” until link creation/removal history is available: current links identify relationships, not when blocking began. `linked_status` is a captured status string (`internal/sync/denormalize.go:334-374`), not a historical state or category; prefer the linked issue's fresh state where available and expose unknown/stale blockers. Normalize direction, deduplicate blocked issues with multiple blockers, and keep Waiting/Planning dwell explicitly labeled as a proxy rather than proven blocked time.

### 6. Sprint completion — ⚠️ Partial (membership yes, boundaries no)

- `sprints`/`boards` tables are **empty** → no sprint start/end/complete dates, no
  committed-vs-completed-at-sprint-close, no true velocity.
- Current `sprint_name`/`sprint_id` columns on `issues` are effectively empty (88 rows).
- **But** `changelog` field `Sprint` has 40,240 membership events / 12,427 issues
  (`to_string` = sprint name, e.g. `2023-6`, `4.3A`).

So we **can** derive: which issues were ever in sprint X, carry-over (sprint added then
removed / re-added), and completion = share of sprint-X members resolved. We **cannot**
derive point-accurate velocity or commitment measured at the official sprint close
without syncing the Jira Agile sprint/board endpoints. Recommend adding board/sprint sync
to make this first-class.

>> Recommendation: Call this “eventual resolution of ever-members,” not sprint completion. Parse membership as sets keyed by sprint ID (names need not be unique); removal/re-addition alone does not establish carry-over. True commitment/completion requires membership at start/close plus status at close, not today's `resolved`; point velocity additionally needs estimates at a defined baseline.

### 7. Workload by assignee — ✅ Strong

`assignee` is 100% populated; `changelog.assignee` (30,209 events) gives reassignment
history and churn. Supports: open load per assignee, throughput per assignee, reassignment
count (ownership thrash), and time-on-each-assignee intervals.

```sql
SELECT assignee, COUNT(*) open_items
FROM issues WHERE status_category!='Done' GROUP BY assignee ORDER BY open_items DESC;
```

>> Recommendation: Distinguish non-null assignee storage from genuinely assigned work; normalize empty/sentinel values into an Unassigned bucket. Define throughput attribution as owner-at-completion versus current owner, using stable account IDs where available. Issue counts measure item load, not effort/capacity or individual productivity; handle unknown status categories explicitly rather than silently dropping SQL NULLs.

### 8. Bug-to-feature ratio — ✅ Strong

**Proven** — bugs vs (Story/Feature/Feature Request/Task):
ROX **0.63**, CMP **0.14**, HPSTRAT 0.0, RFE 0.0.
15 issue types available (Bug 10,607; Task 11,824; Story 5,919; Feature 1,148;
Vulnerability 2,766; Epic 2,862 …). Trendable over time by created or resolved month.

>> Recommendation: Make the denominator configurable and name it accurately: Task is not necessarily feature work, while Vulnerability/Weakness are excluded here. Specify created versus completed cohorts, genuine-resolution policy, and zero-denominator behavior (undefined, not zero); present counts alongside ratios so cross-project comparisons remain interpretable.

---

## Additional metrics unlocked by this data

- **Flow efficiency** — active time ÷ total lead time (active vs waiting status intervals).
- **Reopen rate** — count Done→non-Done transitions in `changelog` (quality signal).
- **Status dwell / aging-in-stage** — avg time in each status; find bottleneck stages.
- **Priority escalation ageing** — `priority` changelog (18,667 events): how long until
  criticals move, how often priority is bumped.
- **Backlog churn / rank volatility** — `Rank` changelog (66,451 events).
- **Created vs resolved (net-flow / backlog growth)** — arrivals vs departures per month.
- **Aged open items** — `created` age for still-open issues, bucketed.
- **Reassignment thrash** — assignee-change count per issue (23,170 issues have history).
- **Label / component / team cohort cuts** — all the above sliced by `labels`,
  `components`, `Team` changelog.
- **Resolution-type mix** — Done vs Won't-Do vs Duplicate vs Cannot-Reproduce trends.
- **First-response / triage latency** — `created` → first `assignee` or first status move.
- **Vulnerability SLA** — 2,766 `Vulnerability` + 28 `Weakness` issues with CVE/CVSS
  custom fields (`cve_id`, `cvss_score`) for security remediation timing.

>> Recommendation: Label flow efficiency as an active-status occupancy proxy unless statuses actually distinguish hands-on work from queueing. First reassignment/status movement is likewise not proven first response and misses assignment at creation. A vulnerability SLA needs an explicit policy (clock start, deadline by severity, pauses, and unresolved breaches); CVE/CVSS plus resolution time alone supports remediation timing, not compliance.

## Gaps and recommended sync additions

| Gap | Impact | Fix |
|---|---|---|
| `sprints`/`boards` empty | No true sprint velocity / commitment-at-close | Sync Jira Agile board + sprint endpoints |
| `dev_links` empty | No PR/commit lead time, no dev→merge cycle | Sync development-panel links |
| Story Points ~empty (fields) + sparse in changelog | No point-based velocity/burndown | Use count-based throughput; backfill points sync |
| `status_category` not in changelog | Category-level WIP needs name→category map | Map via current `status`→`status_category` lookup (stable) |

>> Recommendation: Diagnose/backfill existing Agile sync before proposing a new implementation: `internal/sync/sprint.go:21-73` already persists boards and sprint dates; `internal/sync/engine.go:506-507` gates it on `Sync.Sprints` and full sync, and JQL-only sources are skipped. Verify configuration, source type, API permissions, and warnings; empty tables do not prove missing endpoint support. Keep story-point completeness as a separate gap after sprint dates are populated.

## How to query

Three options, in order of ergonomics for metric work:

```bash
jai query "SELECT ..." --format csv     # CLI; also json, tsv, markdown, table
sqlite3 ~/.local/share/jai/jai.db "..." # direct; fastest for heavy scans
```

`jai query` is the recommended entry point (consistent output formats, respects config).
The `jai_query` MCP tool works for light queries but **avoid cross-joins** (`FROM a, b`)
— they cartesian-explode and hit the 30 s MCP timeout; direct `sqlite3` has no such limit.

>> Recommendation: Removing the MCP timeout does not make a Cartesian product safe. Prefer bounded date ranges, explicit keyed joins, and query-plan checks on either entry point; establish a representative runtime before recommending heavy scans against the live sync database.

---

# Implementation & Visualization

How to turn the above into a maintained metric suite inside `jai`, grounded in its
existing design (Go CLI + SQLite + `view`/`snippet` + `--format json|csv|markdown`, MCP,
TUI).

## Keystone: generalized interval + event fact tables

Every requested and additional metric decomposes into **field-change history replayed
over time**. Rather than hand-write heavy SQL per metric, build the substrate **once at
sync time** (in a new `internal/metrics` builder hooked into `internal/sync`) and make
every metric a cheap `GROUP BY`.

Do **not** hard-wire the builder to `status`. Generalize it to any changelog field,
driven by a configurable field list (`status, priority, assignee, Team`). Same replay
code, 20+ metrics covered.

>> Recommendation: Start with a status replay contract and measured queries before generalizing/materializing every field. Many listed metrics already use `issues` directly, and interval percentiles/overlaps do not reduce to a simple `GROUP BY`. Define whether `fact_field_event` adds semantics beyond `changelog`; use a view if it merely duplicates the source. The proposed tracked-field list also omits Rank even though rank churn is advertised.

### `fact_field_interval` — value-over-time (one row per dwell)

Built by replaying `changelog` ordered by `issue_key, changed_at`, per tracked `field`.

| col | source |
|---|---|
| `issue_key, project, type, assignee, team, component` | `issues` (denormalized for slicing) |
| `field` | changelog `field` (e.g. `status`, `priority`, `assignee`, `Team`) |
| `value` | changelog `to_string` |
| `value_category` | for `status`: mapped via `status_category_map` snippet |
| `entered_at, exited_at, duration_days` | consecutive `changed_at`; open interval → `now` |
| `author` | changelog `author` |

>> Recommendation: Preserve raw value IDs as well as display strings and give each interval a deterministic identity/order. Changelog already retains `from_value`/`to_value` (`internal/db/changelog.go:9-20`); use those for status/account identity rather than names alone. Specify tie handling for equal timestamps, no-op changes, incomplete history, and fields that contain sets; do not assume one scalar replay rule correctly models Sprint/labels/components.

>> Recommendation: Store open `exited_at` as NULL and calculate its duration against an explicit query `as_of`, rather than freezing `now` at sync. Otherwise ageing and open WIP go stale without new Jira changes. Record a build watermark and source freshness separately from the observation time.

>> Recommendation: Specify whether denormalized dimensions mean current attributes or attributes at event time. Copying today's assignee/team/type onto old intervals rewrites historical attribution; historical slices need temporal joins or event-time dimensions. Components are multi-valued, so use an explicit membership/counting policy to prevent join fan-out from inflating totals.

Powers everything interval-based: cycle time, lead time, WIP/CFD, status dwell, blocker
ageing, flow efficiency, reopen rate, **priority-escalation ageing**, **time-on-owner**.
Status metrics are just `WHERE field='status'`.

### `fact_field_event` — raw change grain (one row per change)

The un-collapsed source of the intervals; kept for **count-based churn** that needs no
durations: **rank volatility**, **reassignment thrash**, **triage latency**
(`MIN(changed_at) WHERE field='assignee'` − `created`).

### Rollups derived from the above

- **`fact_issue_flow`** (1 row/issue): `created, first_active_at, resolved, lead_days,
  cycle_days, reopen_count, resolution, type` — distributions, ratios, Vulnerability SLA.
- **`fact_wip_daily`** (date × project × value_category count) — materialized so WIP/CFD
  trends don't re-walk intervals on every render.

Why materialize: changelog stores status *name* not category, and WIP needs interval
overlap math that is ugly in one SQL statement. Compute once at sync, query trivially.
The `status`→`status_category` map is stable — pin it as a snippet/table.

>> Recommendation: Treat category mapping as versioned metric configuration, not a stable inference from currently observed issue statuses. Include historical-only/renamed statuses, preserve an Unknown bucket, and state whether mapping edits restate history. Define atomic, idempotent per-issue rebuilds after successful history ingestion (including standalone changelog sync), backfill/deletion handling, and rebuilds when definitions change. Publish facts and rollups together so interrupted/partial sync cannot expose mixed generations.

>> Recommendation: Define daily rollup grain and refresh semantics before storing it. Date × project × category cannot answer the promised team/type/assignee cuts; choose supported dimensions without a combinatorial cube. Advance open intervals through an explicit `as_of` even when no sync occurs. Validate replay with reopened, initially-active, unchanged, equal-timestamp, and partial-history fixtures, plus a current-state WIP reconciliation before visualization.

## Metric coverage from this substrate

| Metric | Source |
|---|---|
| Cycle time / lead time / distributions | `fact_issue_flow` + `fact_field_interval` |
| WIP trends / CFD | `fact_wip_daily` |
| Issues completed over time | `fact_issue_flow` (or `issues`) |
| Blocker ageing | `fact_field_interval` dwell in Waiting/Hold + `issue_links` |
| Sprint completion | `Sprint` changelog membership (⚠️ no sprint dates until boards synced) |
| Workload by assignee | `fact_field_interval` `field='assignee'` + `issues` |
| Bug-to-feature ratio | `issues` view |
| Flow efficiency / reopen rate / status dwell | `fact_field_interval` / `fact_issue_flow` |
| Priority escalation ageing | `fact_field_interval` `field='priority'` |
| Rank volatility / reassignment thrash | `fact_field_event` counts |
| First-response / triage latency | `fact_field_event` + `comments` |
| Net-flow / aged open items / resolution mix | `issues` views (no fact table) |
| Cohort cuts (project/type/team/component) | denormalized fact dimensions |

Label-cohort *history* is the one caveat: labels are multi-valued and time-varying —
current-label cohorts work off `issues`; historical label cohorts need the `labels`
changelog (27k events) modeled separately.

## Where definitions live

```
changelog ─┐
           ├─ replay @ sync ─> fact_field_interval ─> fact_issue_flow
issues ────┘                └> fact_field_event      └> fact_wip_daily
                                      │
            shared SQL fragments (jai snippet): active_statuses,
            genuine_done, status_category_map
                                      │
            metric defs ── jai view (config) / jai metrics <name>
                                      │
            one definition, three consumers: CLI · MCP tool · TUI
```

- **Shared fragments → `jai snippet`**: `active_statuses`, `genuine_done`
  (`resolution IN ('Done','Done-Errata')`), `status_category_map`. Keeps SQL DRY.
- **Simple row/aggregate metrics → `jai view`** (config `views:`, supports `{{me}}`).
  `jai view cycle-time --format csv` becomes the data API — no new transport.
- **Interval/materialized metrics → `internal/metrics` + `jai metrics <name>`**, reusing
  `internal/output` formatters; fact-table build hooks into `internal/sync`.

## Visualization — no bespoke server

Two audiences, two low-effort paths:

1. **Team / management → point BI at the SQLite fact tables.** Metabase or Grafana
   (sqlite datasource) reads `fact_*` directly; interactive filters for free. Best ROI.
2. **Engineer at terminal → TUI metrics pane** (jai already ships a TUI) with
   sparklines/bars, or a generated static `dashboard.html` (Vega-Lite / Observable Plot
   reading `jai metrics … --format json`) — self-contained, no runtime.

>> Recommendation: Validate one deployment path rather than calling both BI options free/self-contained. Confirm SQLite driver/plugin support, read-only access, live-file/WAL consistency, and Jira-data access controls for the selected BI tool. For static HTML, embed the data and visualization assets if offline operation is required; browser file fetches or CDN assets otherwise introduce runtime dependencies.

Chart mapping:

| Metric | Chart |
|---|---|
| Cycle time distribution | histogram + box/violin; percentile bands over time |
| Lead time trends | line of p50/p85/p95 by resolution month |
| WIP trends | **cumulative flow diagram** (stacked area by category) — encodes WIP, lead time, throughput at once |
| Completed over time | bar per month, stacked by type |
| Blocker ageing | aging histogram + scatter of currently-blocked age |
| Sprint completion | stacked bar: members vs resolved per sprint |
| Workload by assignee | horizontal bar (open load) + throughput |
| Bug:feature ratio, net-flow | line / stacked-area over time |
| Rank volatility, reassignment thrash | bar (count per period) |
| Priority escalation, triage latency | percentile line over time |

Prioritize the **cumulative flow diagram** — it visually encodes three metrics at once.

>> Recommendation: A CFD requires a consistent population and all relevant workflow categories, not just active WIP intervals; document scope changes and reopened work. It shows flow patterns but does not recover individual lead-time distributions from category counts. Keep the cycle/lead percentile charts as separate computed metrics.

## Phased plan

1. **Foundation** — `status_category_map` snippet + generalized `fact_field_interval` /
   `fact_field_event` builder in sync (field list: status, priority, assignee, Team).
   Unblocks the bulk of status- and field-derived metrics.
2. **Rollups + exposure** — `fact_issue_flow`, `fact_wip_daily`; ship metrics as
   `jai view`s / `jai metrics`, plus the `issues`-only views (net-flow, aged items,
   resolution mix, bug ratio).
3. **Visualize** — wire Metabase/Grafana to the fact tables (fastest) and/or a generated
   static dashboard; add a TUI pane.
4. **Close data gaps** — sync Agile **boards/sprints** (real sprint completion/velocity)
   and **dev_links** (PR cycle time). Only data additions needed for first-class sprint
   metrics.

>> Recommendation: Add a correctness gate before the foundation phase: agreed metric/cohort definitions, verified history coverage, deterministic replay examples, and reconciled current WIP. Then benchmark status-only replay and one end-to-end CLI/view result before adding generalized facts or multiple UIs. Treat existing sprint-sync diagnosis and story-point coverage as separate prerequisites; board/sprint and dev-link additions alone do not make velocity first-class.
