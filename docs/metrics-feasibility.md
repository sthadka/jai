# Flow Metrics Feasibility — jai local DB

What metrics the `jai` dataset can realistically support, and how to implement them in a
way that works for **any** Jira project, not just the instance measured here.

> **All counts below are from one instance** (Red Hat tenant, projects ROX/CMP/HPSTRAT/RFE,
> snapshot 2026-11-11). Every status name, resolution, issue type, link type, custom field,
> and sprint-name format is **tenant-specific**. Treat the numbers as an existence proof of
> the *data shape*, and treat all taxonomy as **configuration** (see
> [Metric configuration](#metric-configuration)). No metric definition should hard-code a
> literal that only holds for this tenant.

## How to read the verdicts

A metric's trustworthiness depends on its **basis**, not just whether a query returns rows:

- **Snapshot** — derived from current `issues` columns. Reflects state *now*; cannot
  describe history after reopening/field changes.
- **Reconstructed** — replayed from `changelog`. Only as good as history completeness; must
  be reconciled against current state before it is trusted.
- **Config-dependent** — correctness hinges on tenant taxonomy being configured correctly.

"Changelog exists on 97% of issues" does **not** prove complete status history. Before
calling any reconstructed metric strong, report per-project coverage, sync freshness,
excluded/unknown cohorts, and a reconciliation of reconstructed current-state vs the live
`issues` snapshot.

## TL;DR

| Metric | Basis | Verdict | Primary source |
|---|---|---|---|
| Cycle time distribution | Reconstructed | ✅ with explicit cohort | `changelog` status intervals |
| Lead time trends | Snapshot / event | ✅ | `created` → completion event |
| Work-in-progress (WIP) | Reconstructed | ✅ after reconciliation | `changelog` status intervals |
| Issues completed over time | Snapshot / event | ✅ | completion events |
| Blocker ageing | Snapshot + proxy | ⚠️ Partial | dependency links + status dwell |
| Sprint completion | Reconstructed | ⚠️ Partial | `Sprint` changelog membership |
| Workload by assignee | Snapshot + reconstructed | ✅ | `assignee` + `changelog` |
| Bug-to-feature ratio | Snapshot | ✅ config-dependent | `type` + config taxonomy |

**Known limits:** story-point velocity (points field ~empty at source), true sprint
boundaries (boards/sprints not populated under the current config — see
[Gaps](#gaps-and-diagnosis)), and `status_category` *history* (changelog stores the status
*name*, not the category — the category must be mapped from configuration, including
renamed/historical statuses).

---

## Data inventory (verified)

### Tables and coverage

| Table | Rows | Notes |
|---|---|---|
| `issues` | 43,331 | 687 columns (core + custom Jira fields) |
| `changelog` | 875,526 | 41,898 distinct issues; retains `from_value`/`to_value` **and** `*_string` |
| `issue_links` | 151,358 | typed, directional; `linked_status` is a captured string, **not** history |
| `comments` | populated | per-issue discussion |
| `attachments` | populated | — |
| `sprints` | **0** | empty under current config — not a missing feature (see Gaps) |
| `boards` | **0** | empty under current config |
| `dev_links` | **0** | empty under current config (`sync.dev_info` off) |

### Per-project changelog coverage (why "97%" is not "complete")

Two distinct things, not to be conflated:

| Project | Issues | With changelog rows | Coverage | Pending-history backlog |
|---|---:|---:|---:|---:|
| CMP | 4,549 | 4,549 | 100% | 0 |
| HPSTRAT | 772 | 770 | 99.7% | 1 |
| RFE | 259 | 259 | 100% | 0 |
| ROX | 37,751 | 36,323 | 96.2% | 0 |

- **Pending-history backlog** = issues whose changelog has never been checked or whose
  `updated > changelog_synced_at` (the sync predicate, `internal/db/changelog.go:87-91`).
  It is ~0 here, so the local history is essentially caught up to the local issue snapshot.
- A **stale per-issue `changelog_synced_at`** does **not** imply missing history: an
  unchanged issue legitimately keeps an old check timestamp. Do not infer incompleteness
  from an old timestamp alone.
- Coverage is "an issue has *some* changelog rows" (~1,400 ROX issues have none), not "every
  transition is captured". Report this excluded cohort separately.

Neither a zero backlog nor current-state reconciliation *proves* full historical
completeness (Jira can omit very old or permission-filtered history); publish the excluded
cohort, the backlog, and the source-sync freshness as three separate figures rather than one
"freshness" claim.

### Status names observed (map to stages via config, not hard-coding)

Observed name → current `status_category`: `Closed`, `Release Pending` → Done ·
`In Progress`, `Review`, `Approved`, `Dev Complete`, `Testing` → In Progress ·
`New`, `Backlog`, `To Do`, `Refinement`, `Waiting`, `Planning` → To Do. This map is
**tenant data**, is incomplete (historical/renamed statuses may not appear in current
issues), and is the reason stage mapping must be versioned configuration with an explicit
`unknown` bucket — never inferred once from current issues and assumed stable.

### Resolution distribution (genuine-done policy is configurable)

`Done` 26,619 · _(empty)_ 5,100 · `Won't Do` 4,459 · `Obsolete` 2,962 · `Duplicate` 1,851 ·
`Not a Bug` 1,153 · `Cannot Reproduce` 935. A "genuine completion" filter
(`metrics.done_resolutions`, here `['Done','Done-Errata']`) must be applied **identically**
across cycle time, lead time, and throughput so the three stay comparable.

---

## Per-metric findings

Each SQL below is illustrative with this tenant's literals inlined; in the real
implementation those literals come from config (see [Metric configuration](#metric-configuration)).

### 1. Cycle time distribution — Reconstructed, ✅ with explicit cohort

Cycle time = first entry into an **active** stage → completion. Decisions that change the
number, which must be stated with any result:

- **Start** = first-ever active entry *(default)* vs latest work episode after the last
  backlog return.
- **End** = final `resolved` *(default)* vs first completion for reopened issues.
- **Cohort** = genuine-done only, timestamp order validated, project scoped.

**Reproducible (ROX, genuine-done, first active-entry → `resolved`, resolved since 2024):
7,171 issues, avg 44.0 days.** A separate **3,424** genuine-done issues in the same cohort
have *no* active-status event → their cycle time is **unknown**, reported as a count, and
**not** folded into the distribution (do not substitute `created→resolved`; that is lead
time and would silently inflate percentiles).

```sql
WITH active AS (                    -- metrics.status_stages.active
  SELECT issue_key, MIN(changed_at) started FROM changelog
  WHERE field='status' AND to_string IN ('In Progress','Review','Dev Complete','Testing','Approved')
  GROUP BY issue_key)
SELECT i.type, COUNT(*) n,
       ROUND(AVG(julianday(i.resolved)-julianday(a.started)),1) avg_days
FROM issues i JOIN active a ON a.issue_key=i.key
WHERE i.project='ROX'                         -- scope
  AND i.resolution IN ('Done','Done-Errata')  -- metrics.done_resolutions
  AND i.resolved > '2024-01-01'
  AND julianday(i.resolved) >= julianday(a.started)   -- order sanity
GROUP BY i.type;
```

Distribution (p50/p85/p95) and per-type/priority/component/team splits all follow.

### 2. Lead time trends — Snapshot (Phase 1); event model deferred

**Completion-model decision (applies to lead time, throughput, and `fact_issue_flow`).**
`issues` carries only the *latest* `resolved`/`resolution`, so one row per issue is a
**snapshot** and cannot represent repeated completions of a reopened issue. Decision:

- **Phase 1 — snapshot-only, explicitly labelled.** Lead time and throughput count each
  issue once by its current `resolved`, filtered by the *current* `resolution`. A
  point-in-time view, not a historical completion log.
- **Phase 2 — event model (optional).** Derive completion *events* from the `status`
  changelog (entry into a `done` stage) joined to the `resolution` changelog (47,847 events).
  Where history records a resolution at the close, the event carries it; where history is
  incomplete, the event retains **unknown** resolution-at-completion rather than borrowing
  today's value. So a reopened-and-recompleted issue contributes one event per close with the
  resolution it actually had then — or `unknown` when that is unrecoverable.

Do **not** let the current `resolution` retroactively qualify or disqualify past completion
events — that is the Phase-2 model's job, using historical resolution. Lead time also
requires `resolved >= created` and a published eligible denominator.

```sql
SELECT strftime('%Y-%m', resolved) m, COUNT(*) n,
       ROUND(AVG(julianday(resolved)-julianday(created)),1) avg_lead_days
FROM issues
WHERE resolved IS NOT NULL AND resolution IN ('Done','Done-Errata')
  AND julianday(resolved) >= julianday(created)
GROUP BY m ORDER BY m;
```

### 3. Work-in-progress (WIP) trends — Reconstructed, ✅ after reconciliation

No daily snapshots exist; WIP is reconstructed by replaying status history. Correctly:

- Build **every consecutive** status interval `[entered_at, exited_at)` per issue, not one
  first-active→first-done span. Include reopened episodes and still-open work.
- Count only stages configured as WIP (`metrics.wip_stages`); exclude returns to
  backlog/waiting.
- Seed the creation→first-change interval from the earliest `from_value`/`from_string`
  **only when history is complete**; handle never-changed issues; mark periods with no/
  stale history as **unknown**, not zero.
- Fix a timezone and a daily sampling boundary so a transition is counted on exactly one
  day.
- **Reconcile** reconstructed current WIP against the live `issues` snapshot before trusting
  the series.

### 4. Issues completed over time (throughput) — Snapshot (Phase 1), ✅

Monthly completion counts under the shared completion-model decision (§2): Phase-1 snapshot
by current `resolved`/`resolution`; Phase-2 counts `status`→done transitions as events.
Split by type/priority/assignee/team, same `done_resolutions` policy as lead time.

### 5. Blocker ageing — Snapshot + proxy, ⚠️ Partial

No clean "blocked" state. Honest framing per signal:

1. **Dependency links** — `issue_links` of configured blocker types
   (`metrics.blocker_link_types`, e.g. `is blocked by` 937, `depends on` 915). These
   identify a *relationship that exists now*, not *when blocking began*, and `linked_status`
   is a captured string snapshot (`internal/sync/denormalize.go`), not historical state or
   category. So this yields **"age of currently dependency-linked issues"**, not true
   blocked-time, until link add/remove history is available. Normalize direction,
   deduplicate issues with multiple blockers, and prefer the linked issue's fresh state;
   flag stale/unknown blockers.
2. **Status dwell** — time in `Waiting`/`Hold`/`Planning` via intervals (§3), explicitly
   labelled a **proxy** for blockage, not proven blocked time.
3. **`Flagged` changelog** — 151 events / 99 issues; `blocked_reason` is 99% literal
   `"None"`. Too sparse to rely on.

### 6. Sprint completion — Reconstructed, ⚠️ Partial

- `boards`/`sprints` are empty **under the current config**, not because jai lacks the
  feature (see [Gaps](#gaps-and-diagnosis)). Without sprint start/close dates there is no
  commitment-at-close and no point velocity.
- `changelog` field `Sprint` has 40,240 membership events / 12,427 issues; `to_string` is a
  sprint name (e.g. `2023-6`, `4.3A`). **Parse membership as sets keyed by sprint ID**, not
  name — names need not be unique.

What this supports is **"eventual resolution of ever-members"** (share of issues ever in
sprint X that are now resolved), *not* sprint completion. Membership removal/re-addition
alone does not establish carry-over. True commitment/completion needs membership **at start
and at close** plus **status at close**, not today's `resolved`; velocity additionally needs
estimates captured at a defined baseline.

### 7. Workload by assignee — Snapshot + reconstructed, ✅

`assignee` history (30,209 events / 23,170 issues) gives reassignment, time-on-owner, and
per-owner throughput. Guardrails:

- Normalize empty/sentinel assignees into an explicit **Unassigned** bucket; distinguish
  "stored non-null" from "genuinely assigned".
- Define throughput attribution: **owner-at-completion** vs current owner; use stable
  account IDs where available (display names churn).
- Counts measure **item load**, not effort/capacity or individual productivity.
- Handle unknown/NULL status categories explicitly rather than dropping them.

```sql
SELECT COALESCE(NULLIF(assignee,''),'(Unassigned)') owner, COUNT(*) open_items
FROM issues
WHERE status_category IS NULL OR status_category != 'Done'   -- NULL (unknown) kept, not dropped
GROUP BY owner ORDER BY open_items DESC;
```

### 8. Bug-to-feature ratio — Snapshot, ✅ config-dependent

Ratio is only meaningful with a **configured, named** taxonomy (`metrics.type_classes`).
Here: bug=`Bug`; feature=`Story`/`Feature`/`Feature Request` (**Task excluded** — it is not
inherently feature work). Caveats baked into the config/output:

- `Vulnerability`/`Weakness` are a separate security class — never in the denominator.
- State the cohort: **created** vs **completed**. The figures below are **all-current-issue
  counts** (every issue regardless of state), the simplest cohort.
- Zero denominator is **undefined**, not `0`.
- Always present **counts alongside the ratio** (mix differs by taxonomy, not just behavior).

```sql
-- all-current-issue cohort; bug / (Story+Feature+Feature Request), Task excluded
SELECT project,
       SUM(type='Bug') bugs,
       SUM(type IN ('Story','Feature','Feature Request')) features,
       ROUND(1.0*SUM(type='Bug')
             /NULLIF(SUM(type IN ('Story','Feature','Feature Request')),0),2) ratio
FROM issues GROUP BY project;
-- ROX 10,259/4,986 = 2.06 · CMP 352/2,045 = 0.17 · HPSTRAT 0/36 = 0.0 · RFE 0/259 = 0.0
```

---

## Additional metrics unlocked by this data

All inherit the basis/caveat discipline above.

- **Flow efficiency** — active ÷ total lead time. Only valid if statuses actually
  distinguish hands-on work from queueing; otherwise label it an **active-status occupancy
  proxy**.
- **Reopen rate** — Done→non-Done transitions (quality signal).
- **Status dwell / aging-in-stage** — bottleneck detection.
- **Priority escalation ageing** — `priority` changelog (18,667 events).
- **Backlog churn / rank volatility** — `Rank` changelog (66,451 events; include `Rank` in
  `tracked_fields`).
- **Created vs resolved (net-flow)** — arrivals vs departures per month (`issues` only).
- **Aged open items** — open-issue age buckets (`issues` only).
- **Reassignment thrash** — assignee-change count per issue.
- **Cohort cuts** — by project/type/team/component/label (see historical-label caveat in
  Implementation).
- **Resolution-type mix** — Done vs Won't-Do vs Duplicate trends (`issues` only).
- **First-response / triage latency** — `created` → first assignment/first comment. Note:
  first reassignment/status move is **not** proven first response and misses assignment at
  creation; combine `assignee` events with the `comments` table and state the definition.
- **Vulnerability / security SLA** — `Vulnerability`/`Weakness` issues with `cve_id`/
  `cvss_score`. CVE/CVSS + resolution time supports **remediation timing**, not
  **compliance**: a real SLA needs an explicit policy (clock start, deadline by severity,
  allowed pauses, treatment of unresolved breaches) — all in `metrics.sla` config, since
  severity fields and thresholds are tenant-specific.

---

## Gaps and diagnosis

| Gap | Status | Action |
|---|---|---|
| `boards`/`sprints` empty | **Config, not missing feature.** `SyncSprints` exists (`internal/sync/sprint.go`) but is gated on `sync.sprints && full` (`internal/sync/engine.go:506`) **and returns early for JQL-only sources** (`sprint.go:23`). This tenant's sources are all JQL (`PROJECT = ROX`) and `sync.sprints` is unset. | Set `sync.sprints: true` **and** convert sources to `projects: [ROX]`, or extend `SyncSprints` to resolve projects from JQL. Verify API permissions and watch stderr warnings before concluding endpoints are unsupported. |
| `dev_links` empty | Config. `SyncDevInfo` gated on `sync.dev_info && full` (needs extra API perms). | Enable `sync.dev_info: true` if permitted. |
| Story points ~empty | Source-side sparsity, **separate** from sprint dates. | Treat as its own gap; prefer count-based throughput. Revisit only after sprint dates populate. |
| `status_category` not in changelog | By design. | Map name→stage via versioned `metrics.status_stages` (ID override + name fallback), with an `unknown` bucket. |

---

## Metric configuration

The core portability requirement: **no tenant literal in code**. Metric taxonomy lives in
the config file (alongside existing `views`, `snippets`, `fields.overrides`,
`sync_sources`), so a user points jai at their own Jira by editing YAML — not patching SQL.
Proposed `metrics:` block (mirrors the `config.Config` struct conventions; field names
reference the *readable* columns produced by `fields.overrides`):

```yaml
metrics:
  # Status -> flow stage. Precedence: status_stage_ids (by Jira status ID) wins,
  # then status_stages (by name) as fallback, else `unknown`. IDs survive renames.
  # Versioned: changing this restates reconstructed history (see Build/refresh semantics).
  status_stage_ids:        # optional, authoritative; key = Jira status ID -> stage
    # "10001": done
  status_stages:           # name fallback; names not listed -> `unknown`
    todo:    [New, Backlog, To Do, Refinement, Planning]
    active:  [In Progress, Review, Dev Complete, Testing, Approved]
    waiting: [Waiting, Hold]            # proxy for blocked/queue, not active
    done:    [Closed, Release Pending]
  wip_stages: [active]                  # stages counted as WIP / cycle-time active
  done_resolutions: [Done, Done-Errata] # REQUIRED for delivery metrics (see note below)

  # Issue-type classes for ratios/cohorts. Types not listed -> `other`.
  type_classes:
    bug:      [Bug]
    feature:  [Story, Feature, "Feature Request"]
    task:     [Task, Sub-task]
    security: [Vulnerability, Weakness]
  ratio:                               # named, not "bug:feature" by assumption
    numerator: bug
    denominator: feature
    cohort: completed                  # created | completed

  # Fields replayed into interval/event facts (must appear in changelog.field).
  tracked_fields: [status, priority, assignee, Team, Rank]

  blocker_link_types: [is blocked by, depends on]   # direction normalized at build

  sla:                                 # optional; drives security/SLA metrics
    security:
      # Choose ONE severity source (a numeric score cannot index categorical keys):
      severity_field: priority         # (a) categorical field with per-value deadlines
      deadlines_days: { Critical: 7, Major: 30, Minor: 90 }
      # severity_bands:                # (b) numeric cvss_score -> label, [ge, next ge)
      #   - { ge: 9.0, label: Critical }
      #   - { ge: 7.0, label: Major }
      #   - { ge: 0.0, label: Minor }
      clock_start: created
      on_missing_severity: exclude     # exclude | treat_as: <label> — never silently drop
```

Consumers:

- `status_stage_ids` / `status_stages` / `wip_stages` → cycle time, WIP, dwell, flow
  efficiency, and the status→stage table. Mapping precedence: **ID override → name →
  `unknown`**; `unknown` is a real bucket, surfaced in output, never dropped.
- `done_resolutions` → the shared genuine-completion filter (cycle/lead/throughput/ratio).
- `type_classes` + `ratio` → bug-to-feature and typed cohorts; explicit names/denominator,
  zero-denominator undefined.
- `tracked_fields` → fields the fact builder replays.
- `blocker_link_types` → blocker-ageing link set.
- `sla` → security/SLA metrics with per-severity deadlines.

**Default mapping.** Ship a default derived from each issue's Jira `statusCategory`
(To Do / In Progress / Done) as reported at sync — a safe out-of-the-box fallback,
overridable per tenant. Statuses seen only in old history (never in the current issue set)
have no category to borrow and therefore stay `unknown` until mapped explicitly.

**`done_resolutions` omitted.** Jira's Done *category* cannot identify *genuine delivery*:
`Won't Do`/`Duplicate`/`Obsolete` are also Done-category. If `done_resolutions` is unset,
jai must **not** guess — it computes lead/throughput over *all* resolved issues, labelled
"includes non-delivery resolutions", and disables the genuine-completion variant rather than
silently treating every closure as delivery.

**Validation, two tiers.** (1) *Structural*, at config load (`config.Validate`): malformed
YAML, a metric referencing an undefined stage/class, or a duplicate mapping **rejects that
metric** (fail closed — no misleading output), not a soft warning. (2) *Data-dependent*, at
run: fields absent from `changelog`, statuses with no stage, or empty cohorts are reported as
coverage diagnostics alongside results, not fatal.

---

# Implementation & Visualization

How to turn the above into a maintained suite inside `jai` (Go CLI + SQLite + `view`/
`snippet` + `--format json|csv|markdown`, MCP, TUI). **Sequence matters:** prove a narrow,
reconciled status pipeline before generalizing or materializing everything.

## Start narrow: a status replay contract

Before building a generic fact framework:

1. Agree metric/cohort **definitions** (the config above).
2. Verify **history coverage** per project and publish exclusions.
3. Build **status-only** interval replay with deterministic fixtures (reopened,
   initially-active, never-changed, equal-timestamp, partial-history).
4. **Reconcile** reconstructed current WIP against the live snapshot.
5. Benchmark one end-to-end CLI/view result.

Only then generalize. Note several metrics (net-flow, aged items, resolution mix, ratio)
run directly off `issues` and need **no** fact table; and interval percentiles/overlaps are
**not** a simple `GROUP BY` — budget real query work.

### Phase-0 replay contract (locked for M00–M04)

The decisions below are the implementation contract the Phase-0 code (status replay,
reconciliation) and its deterministic fixtures
(`internal/metrics/testdata/flow_cases.json`) are built against. They turn the proposal's
choices into exact, testable boundaries; later phases extend, not reinterpret, them.

- **Taxonomy is config, never code.** Every stage/class/resolution mapping is loaded from
  the `metrics:` YAML (or the fixture's `config` block in tests). No tenant status, type, or
  resolution literal appears in Go. The fixture carries its own taxonomy so no example count
  becomes a universal expectation.
- **Stage precedence: `status_stage_ids` (Jira status ID) → `status_stages` (name) →
  `statusCategory`-derived default → `unknown`.** IDs survive renames. The category default
  (`To Do`→todo, `In Progress`→active, `Done`→done) applies **only where a current
  `statusCategory` is available** (the live snapshot / open interval) — historical changelog
  rows carry no category, so an unmapped historical status stays `unknown`. `unknown` is a
  real, surfaced interval/cohort state, never dropped and never counted as zero WIP.
- **Deterministic ordering.** Events are ordered by `(changed_at, stable changelog id)`.
  Equal-timestamp transitions are retained (they may produce zero-length, never negative,
  intervals) and ordered by id; the result flags that an equal-timestamp tie was resolved so
  genuinely unrecoverable order is surfaced rather than hidden.
- **No-op suppression.** Changes whose `from`/`to` status are identical are dropped before
  intervals are built, so a no-op never splits a dwell.
- **Interval construction.** One half-open `[entered_at, exited_at)` interval per consecutive
  status; the final interval is open (`exited_at = NULL`) and its duration is computed against
  an explicit `as_of`, never a sync-frozen `now`. Reopened issues and returns to
  backlog/waiting are preserved as their own intervals; WIP counts only `wip_stages`.
- **History-seeding evidence.** The `created → first change` leading interval is seeded as a
  **known** status (from the first event's `from` value) **only when history is complete**.
  With partial/stale history the leading period is an explicit `unknown` interval — we never
  extend unverified history as known. A provably never-changed issue (complete history, no
  events) is one open interval in its current status; that is distinct from
  unavailable-history.
- **Cycle-time denominator.** A genuine-done issue with no active-stage interval has
  **unknown** cycle time: it is reported as a count, not folded in, and never backfilled from
  `created→resolved` (that is lead time).
- **Reconciliation semantics.** Reconcile compares the reconstructed **current** stage to the
  local `issues` snapshot stage under the same scope/config. Outcomes: `match`, `mismatch`
  (reconstructed stage ≠ snapshot stage), or `unknown` (either side is `unknown`). A single
  mismatch stays visible and forbids a "trusted series" claim; unknown cohorts keep their
  counts and caveats. A reconciled endpoint still does **not** prove complete history, and
  snapshot freshness is **not** live-Jira freshness.
- **Validation is two-tier.** Structural config errors (malformed YAML, undefined
  stage/class reference, duplicate mapping) **fail closed on the affected metric only** — the
  metric is rejected, the rest of the YAML and unrelated commands keep working. Data-dependent
  gaps (absent tracked fields, unmapped statuses, empty cohorts, stale history) are run-time
  coverage diagnostics reported alongside results, never fatal.

## Fact tables (after the narrow pipeline proves out)

### `fact_field_interval` — value-over-time (one row per dwell)

Replay `changelog` ordered by `(issue_key, changed_at)` per `tracked_field`.

| col | source / rule |
|---|---|
| `issue_key` | changelog |
| `field` | changelog `field` |
| `value_id, value` | changelog `to_value` **and** `to_string` — keep the ID for stable identity (names churn); strings for display |
| `stage` | for `status`: mapped via `metrics.status_stages`, else `unknown` |
| `entered_at` | this change's `changed_at` |
| `exited_at` | next change's `changed_at`, else **NULL** (open) |
| `author` | changelog `author` |

Rules: deterministic interval identity/order; define tie-handling for equal timestamps,
drop no-op changes, mark incomplete-history gaps `unknown`. Scalar replay is **not** valid
for set-valued fields (`Sprint`, `labels`, `components`) — model those as membership
intervals separately.

- **Open intervals store `exited_at = NULL`.** Compute duration against an explicit query
  `as_of`, never a `now` frozen at sync time — otherwise ageing/open-WIP go stale with no
  new Jira activity. Record a build watermark + source freshness separately from `as_of`.
- **Dimensions are event-time vs current.** Do not copy today's assignee/team/type onto old
  intervals — that rewrites historical attribution. Historical slices need temporal joins
  or event-time dimension capture. `components` is multi-valued: use an explicit
  membership/counting policy to avoid join fan-out inflating totals.

### `fact_field_event` — only if it adds semantics

The count-based metrics (rank volatility, reassignment thrash, triage latency) are
`COUNT`/`MIN` over `changelog` and may **not** warrant a materialized copy. Default to a
**partial index** on `changelog` and query it directly; introduce `fact_field_event` only
if profiling shows the view is too slow:

```sql
CREATE INDEX idx_cl_tracked ON changelog(field, issue_key, changed_at)
  WHERE field IN ('status','priority','assignee','Team','Rank');
```

### Rollups

- **`fact_issue_flow`** (1 row/issue, **Phase-1 snapshot** — §2): `created, first_active_at,
  resolved, lead_days, cycle_days, reopen_count, resolution, type`. Carries the *current*
  completion only; repeated completions of reopened issues are the Phase-2 event model's job
  (completion events from `status`→done + `resolution` changelog), not extra rows here.
- **`fact_wip_daily`** — pick the **supported** dimensions deliberately: a
  `date × project × stage` grid cannot answer team/type/assignee cuts without a
  combinatorial cube. Either restrict rollup dimensions or compute cohort WIP on demand
  from intervals. Advance open intervals through `as_of` even when no sync occurred.

### Build/refresh semantics

- Stage mapping is **versioned config**; include historical/renamed statuses, keep an
  `unknown` bucket. Editing the map **restates** past facts — so a map change triggers a
  full rebuild, not an incremental patch.
- **Publication = staged generations with atomic activation.** Each build writes rows tagged
  with a `generation` id; a single `active_generation` pointer (one-row table) is swapped in
  one transaction once facts **and** their rollups for that generation are complete. Readers
  always filter on the active generation, so an interrupted or partial build is never visible
  and generations never mix. Obsolete generation rows are **deleted** after the swap (plain
  `DELETE`, not a VACUUM on every activation). In WAL mode the activation write does not block
  readers, so reads stay live during a multi-thousand-issue rebuild; a whole-build single
  transaction is the simpler alternative if that liveness is not needed.
- **Lost-update invariant.** An incremental sync must not update the active generation after
  the staged rebuild has already read that issue — activation would then publish *older* facts
  over the newer incremental ones. First implementation: **serialize** rebuilds against
  incremental fact updates. Alternative: capture a source watermark at build start and catch
  up any changes past it before activation. Cover this interleaving (incremental update lands
  mid-rebuild) with a **regression test that uses two separate processes**, not just
  goroutines in one process — only a multi-process test exercises the cross-process path.
- **Serialization must span every metric writer to the DB, cross-process.** jai runs as
  multiple processes against one SQLite file — `jai serve` (MCP), CLI commands with auto-sync,
  and the TUI — so a process-local `sync.Mutex` (as used in `internal/sync`) is **not**
  sufficient. WAL + `busy_timeout=30000` (`internal/db/db.go:58-59`) serialize individual
  write *transactions* but not the multi-transaction read→activate window this invariant
  guards. Use cross-process coordination. **Prefer an OS advisory lock (e.g. `flock` on a
  sidecar file in the DB directory) over a database lease** for the first implementation — it
  releases automatically on process exit and avoids lease expiry, renewal, and fencing-token
  complexity. A DB-row lease under `BEGIN IMMEDIATE` remains a valid alternative if a
  file lock is impractical. The lock must be held by the rebuilder **and** every incremental
  metric writer. A process-local mutex is acceptable **only** if metric writes are confined to
  a single owning process (e.g. the background sync in `serve`) and all other entry points are
  readers.
- **Incremental** normal syncs append/replace only touched issues *within* the active
  generation and rebuild their rollup slices in the same transaction, so facts and daily
  rollups advance together. A **config/mapping change** forces a new full generation.
- Rebuilds are idempotent and per-issue on ingestion (including standalone changelog sync);
  handle backfill and deletions.

## Metric coverage from this substrate

| Metric | Source |
|---|---|
| Cycle/lead time, distributions | `fact_issue_flow` + `fact_field_interval` |
| WIP / CFD | intervals (and optional `fact_wip_daily` for chosen dims) |
| Completed over time | `fact_issue_flow` / `issues` |
| Blocker ageing | `fact_field_interval` waiting-dwell + `issue_links` (relationship-age) |
| Sprint (eventual resolution) | `Sprint` membership intervals |
| Workload by assignee | `assignee` intervals + `issues` |
| Bug-to-feature ratio | `issues` + `metrics.type_classes` |
| Flow efficiency / reopen / dwell | `fact_field_interval` / `fact_issue_flow` |
| Priority escalation | `fact_field_interval` field=priority |
| Rank volatility / reassignment thrash | `changelog` index (or `fact_field_event`) |
| First-response / triage latency | `changelog` + `comments` |
| Net-flow / aged items / resolution mix | `issues` views (no fact table) |
| Cohort cuts | event-time dimensions (current-attribute cuts off `issues`) |

Historical **label** cohorts are the caveat: labels are multi-valued and time-varying;
current-label cohorts work off `issues`, historical ones need the `labels` changelog (27k
events) modeled as membership intervals.

## Where definitions live

- **Shared fragments → `jai snippet`**: `active_stages`, `genuine_done`,
  `status_stage_map` — generated from `metrics:` config so SQL stays DRY and tenant-neutral.
- **Simple metrics → `jai view`** (`views:`, supports `{{me}}`): `jai view cycle-time
  --format csv` is the data API.
- **Interval/materialized metrics → `internal/metrics` + `jai metrics <name>`**, reusing
  `internal/output` formatters; fact build hooks into `internal/sync`.

One definition → three consumers (CLI · MCP · TUI); never fork the SQL.

## Visualization — pick and validate one path

Do not assume either BI option is "free". **Validate one deployment path:**

- **BI (Metabase / Grafana on SQLite)** — confirm the SQLite driver/plugin, **read-only**
  access, WAL/live-file consistency against the active sync DB, and that Jira data-access
  controls are respected. Best for team/management dashboards.
- **Static `dashboard.html`** — if offline use is required, **embed** data and viz assets
  (Vega-Lite/Observable Plot); browser file-fetch or CDN assets reintroduce runtime
  dependencies. Fed by `jai metrics … --format json`.
- **TUI pane** — sparklines/bars for the engineer at the terminal.

Chart mapping:

| Metric | Chart |
|---|---|
| Cycle time distribution | histogram + box/violin; percentile bands over time |
| Lead time trends | line of p50/p85/p95 by month |
| WIP trends | cumulative flow diagram (stacked area by stage) |
| Completed over time | bar per month, stacked by type |
| Blocker ageing | aging histogram + scatter of linked-issue age |
| Sprint (eventual resolution) | stacked bar: members vs resolved per sprint |
| Workload by assignee | horizontal bar (open load) + throughput |
| Bug:feature, net-flow | line / stacked-area over time |
| Rank volatility, reassignment thrash | bar (count per period) |
| Priority escalation, triage latency | percentile line over time |

The **cumulative flow diagram** is high value but has scope rules: it needs a consistent
population across **all** workflow stages (not just active WIP), and must document scope
changes and reopened work. It shows flow patterns but does **not** recover the individual
cycle/lead-time *distribution* from stage counts — keep the percentile charts as separate
computed metrics.

## Phased plan

0. **Correctness gate** — agreed definitions (`metrics:` config), verified per-project
   history coverage, deterministic replay fixtures, reconciled current WIP. Diagnose the
   existing sprint-sync config and story-point coverage as **separate** prerequisites.
1. **Narrow pipeline** — status-only `fact_field_interval` + `fact_issue_flow`; benchmark
   one end-to-end `jai view`/`jai metrics` result. Ship `issues`-only metrics (net-flow,
   aged items, resolution mix, ratio) that need no facts.
2. **Generalize** — extend replay to `tracked_fields`; add `changelog` index (defer
   `fact_field_event` until profiled); choose `fact_wip_daily` dimensions deliberately.
3. **Visualize** — implement and validate one path (BI or static) end-to-end before adding
   others.
4. **Close data gaps** — enable `sync.sprints`/project-keyed sources (real sprint
   completion/velocity) and `sync.dev_info` (PR cycle time) once permissions are confirmed.

## How to query

```bash
jai query "SELECT ..." --format csv     # CLI; also json, tsv, markdown, table
sqlite3 ~/.local/share/jai/jai.db "..." # direct; no MCP timeout
```

`jai query` is the recommended entry point. The `jai_query` MCP tool suits light queries
but has a 30 s timeout. Removing that timeout does **not** make a heavy query safe: prefer
bounded date ranges, explicit keyed joins (never `FROM a, b` Cartesian products), and check
the query plan; establish a representative runtime before pointing any tool at the live sync
database.
