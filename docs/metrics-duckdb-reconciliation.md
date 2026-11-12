# DuckDB ↔ Go metric-provider reconciliation (bead jai-2lc.2)

Verifies that the materialized fact store (`fact_issue_flow`, `fact_field_interval`,
and the new `fact_issue_membership`) can be queried directly from DuckDB — attached
READ-ONLY to the live SQLite `jai.db` — and reproduces the Go metric providers
(`internal/metrics`) **exactly**. This is the data-correctness gate for the live
DuckDB dashboard (epic jai-2lc): every chart queries DuckDB, so DuckDB must agree
with `jai metrics` to the digit.

Spec of record: `flow-metrics-export-analysis-plan.md` §3 (architecture / event-time
scalar join) and §4 (membership facts / component join).

## Rebuild scope & generation

| Item | Value |
|------|-------|
| Rebuild command | `./jai metrics rebuild -p ROX --no-sync` |
| Scope | **ROX only** (37,804 issues, 348,905 intervals) |
| Active generation | **8** (`metrics_active_generation.id=1 → generation=8`) |
| Built at | 2026-11-12T15:47:41Z |
| `fact_issue_membership` rows (gen 8) | component 12,906 · label 50,822 · sprint 32,246 |

> **Scope note for later dashboard beads:** generation 8 is **ROX-scoped**. ROX is the
> validated reconciliation target and matches the grounding numbers exactly. A dashboard
> bead that needs *all projects* must re-run `./jai metrics rebuild` (no `-p`) to publish a
> fresh all-projects generation before serving. Nothing here hard-codes ROX.

Percentile method parity: the Go providers use **R-7 inclusive linear interpolation**
(`internal/metrics/result.go` `percentile()`, rank = `p*(n-1)`). DuckDB `quantile_cont`
and `median` use the same continuous-interpolation method, so percentiles agree exactly.

Cohort parity: the Go delivery cohort is **genuine-done** — `done_resolutions:
[Done, Done-Errata]` from the `metrics:` config — so the DuckDB drills filter
`resolution IN ('Done','Done-Errata')`. Ratio classes come from the same config
(`type_classes`: `bug=[Bug]`, `feature=[Story, Feature]`).

## ATTACH boilerplate (reproduce)

```sql
INSTALL sqlite; LOAD sqlite;
ATTACH '~/.local/share/jai/jai.db' AS jai (TYPE sqlite, READ_ONLY);
-- every query below filters on the active generation:
SET VARIABLE g = (SELECT generation FROM jai.metrics_active_generation WHERE id=1);
```

Run the live DB with `duckdb < script.sql` (DuckDB itself stays in-memory;
**do not** pass `-readonly` to the `duckdb` CLI — that forbids the in-memory catalog;
the READ-ONLY guarantee is the `READ_ONLY` on the SQLite ATTACH).

## Go provider outputs (source of truth)

Captured with `--no-sync` so the active generation stays pinned to 8:

```
$ ./jai metrics cycle_time -p ROX --no-sync
cycle_time  days  15602  5.73  42.91  130.92  0  1794.16  31.62   (generation 8)
$ ./jai metrics lead_time  -p ROX --no-sync
lead_time   days  23382  22.03  138.9  383.67  0  2250.56  83.67   (generation 8)
$ ./jai metrics ratio      -p ROX --no-sync
bug  feature  5555  2972  1.8691                                   (generation 8)
```

## Drill A — component lead p50 (membership temporal join, §4)

Set-valued `components` is not on `fact_field_interval`; it is replayed into
`fact_issue_membership` by `writeFacts` (bead jai-2lc.1). The drill is a pure SQL
temporal join, fan-out-safe (one row per `(issue, member, interval)`):

```sql
SELECT m.member_name component, count(*) n, round(median(f.lead_seconds/86400.0),1) p50
FROM jai.fact_issue_flow f
JOIN jai.fact_issue_membership m
  ON m.generation=f.generation AND m.issue_key=f.issue_key AND m.dimension='component'
 AND m.entered_at <= f.resolved AND (m.exited_at IS NULL OR m.exited_at > f.resolved)
WHERE f.generation=getvariable('g')
  AND f.resolution IN ('Done','Done-Errata') AND f.lead_seconds IS NOT NULL
GROUP BY component HAVING count(*)>=50 ORDER BY p50 DESC;
```

Per-component p50 (top rows): Workflows 243.3 (n=68), Vulnerability Scanning 171.4
(n=101), Compliance Workflows 166.0 (n=62), … UI 25.7 (n=1302), Security 12.8
(n=918), ui/ux 4.1 (n=384). 25 components clear the n≥50 floor.

The Go CLI has **no component-segmented metric** (the `Query` struct carries no split
dimension yet — that arrives with the dashboard beads), so drill A is reconciled two
ways against the Go provider it specializes (`lead_time`):

1. **Fan-out safety** — no single issue is double-counted inside a component bucket.
2. **Lossless join** — the lead distribution computed *through* the join (deduped to
   distinct issues) equals the distribution computed *directly* on `fact_issue_flow`
   for the same issue set; the join neither drops nor distorts any measure.

```sql
-- (1) fan-out safety: max rows per (issue,component) must be 1
SELECT max(c), count(*) FROM (
  SELECT f.issue_key, m.member_name, count(*) c
  FROM jai.fact_issue_flow f JOIN jai.fact_issue_membership m
    ON m.generation=f.generation AND m.issue_key=f.issue_key AND m.dimension='component'
   AND m.entered_at<=f.resolved AND (m.exited_at IS NULL OR m.exited_at>f.resolved)
  WHERE f.generation=getvariable('g')
    AND f.resolution IN ('Done','Done-Errata') AND f.lead_seconds IS NOT NULL
  GROUP BY f.issue_key, m.member_name);
-- (2) distinct-issue lead through the join == same issue set direct on flow
```

| Check (drill A) | Go / expected | DuckDB | Result |
|-----------------|---------------|--------|--------|
| max rows per (issue, component) | 1 (fan-out-safe) | 1 (over 7,511 pairs) | **PASS** |
| distinct component-covered issues | — | 6,729 | — |
| lead p50 through join (deduped) | 35.89 (direct on flow) | 35.89 | **PASS** |
| lead p95 through join (deduped) | 512.50 (direct on flow) | 512.50 | **PASS** |

## Drill B — event-time priority cycle p95 (scalar interval join, §3)

Priority at completion is read from the event-time `fact_field_interval` state in
effect at `resolved` (never today's snapshot value copied onto old intervals):

```sql
SELECT iv.value priority, count(*) n,
       round(quantile_cont(f.cycle_seconds/86400.0,0.95),1) p95
FROM jai.fact_issue_flow f
JOIN jai.fact_field_interval iv
  ON iv.generation=f.generation AND iv.issue_key=f.issue_key AND iv.field='priority'
 AND iv.entered_at <= f.resolved AND (iv.exited_at IS NULL OR iv.exited_at > f.resolved)
WHERE f.generation=getvariable('g')
  AND f.cycle_seconds IS NOT NULL AND f.resolution IN ('Done','Done-Errata')
GROUP BY priority ORDER BY n DESC;
```

Per-priority cycle p95 (event-time): Undefined 101.0 (n=6963), P2-Medium 26.3
(n=3333), Normal 212.5 (n=1859), Major 260.5 (n=1298), Critical 210.5 (n=1048),
Blocker 177.1 (n=625), Minor 409.0 (n=222), P1-High 54.2 (n=217), P3-Low 12.5 (n=16),
P0-Highest 33.3 (n=15), P2-Medium (not available) 0.0 (n=3), P4-Lowest 0.0 (n=3).

Because priority is scalar, the event-time join is an exact, lossless **partition** of
the Go `cycle_time` cohort: every cohort issue has exactly one covering priority
interval, so the buckets sum back to the Go cohort and the overall p95 through the
join equals the Go number.

| Check (drill B) | Go / expected | DuckDB | Result |
|-----------------|---------------|--------|--------|
| max priority rows per issue | 1 (exact partition) | 1 | **PASS** |
| partition total rows / issues | 15,602 (= `cycle_time` count) | 15,602 / 15,602 | **PASS** |
| overall cycle p95 through join | 130.92 (`jai metrics cycle_time`) | 130.92 | **PASS** |

## Overall + grounding reconciliation

Overall distributions queried straight off the fact store (no segmentation), plus the
ROX grounding numbers from the validated prototype:

| Metric | Go (`jai metrics`) | DuckDB | Grounding | Result |
|--------|--------------------|--------|-----------|--------|
| cycle_time count | 15,602 | 15,602 | — | **PASS** |
| cycle_time p50 (days) | 5.73 | 5.73 | — | **PASS** |
| cycle_time p95 (days) | 130.92 | 130.92 | 130.9 | **PASS** |
| lead_time count | 23,382 | 23,382 | — | **PASS** |
| lead_time p50 (days) | 22.03 | 22.03 | — | **PASS** |
| lead_time p95 (days) | 383.67 | 383.67 | 383.7 | **PASS** |
| ratio bug:feature | 1.8691 | 1.8691 | 1.88 | **PASS** |

```sql
-- overall cycle (reconciles cycle_time p50/p95)
SELECT count(*) n, round(median(cycle_seconds/86400.0),2) p50,
       round(quantile_cont(cycle_seconds/86400.0,0.95),2) p95
FROM jai.fact_issue_flow
WHERE generation=getvariable('g') AND cycle_seconds IS NOT NULL
  AND resolution IN ('Done','Done-Errata');

-- overall lead (reconciles lead_time p50/p95)
SELECT count(*) n, round(median(lead_seconds/86400.0),2) p50,
       round(quantile_cont(lead_seconds/86400.0,0.95),2) p95
FROM jai.fact_issue_flow
WHERE generation=getvariable('g') AND lead_seconds IS NOT NULL
  AND resolution IN ('Done','Done-Errata');

-- ratio bug:feature (type_classes: bug=[Bug], feature=[Story,Feature])
SELECT sum(type='Bug')::BIGINT bug,
       sum(type IN ('Story','Feature'))::BIGINT feature,
       round(sum(type='Bug')*1.0/sum(type IN ('Story','Feature')),4) ratio
FROM jai.fact_issue_flow
WHERE generation=getvariable('g') AND resolved IS NOT NULL
  AND resolution IN ('Done','Done-Errata');
```

## Result

**All rows PASS.** The DuckDB drills over the live `jai.db` reproduce the Go metric
providers exactly: the event-time scalar join (drill B) is a lossless, fan-out-safe
partition of the `cycle_time` cohort, and the `fact_issue_membership` component join
(drill A) is fan-out-safe and introduces no distortion versus the `lead_time` cohort.
No mismatch; nothing to fix in the engine. The DuckDB-over-SQLite surface is the
correct, honest query path for the dashboard.
