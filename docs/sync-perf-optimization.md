# Sync performance optimization

Investigation + implementation of four incremental speedups to `jai sync`, each
measured with a real `jai sync --full` against Red Hat Jira.

## Test setup

- **Primary workload:** `project = ROX AND issuetype = bug` — 9709 issues.
- **Second project (source-parallelism test):** `project = OCPSTRAT` — 3488 issues.
- **Environment:** Apple M3 Pro, `redhat.atlassian.net`, isolated DB + config in
  `/tmp/jai-perf` (live config/DB untouched). `sprints` and `dev_info` disabled
  to isolate the issue+changelog pipeline. Fresh DB before every run.
- **Metric:** wall-clock of the full `jai sync --full` command (issue phase +
  changelog phase), from `/tmp/jai-perf/run.sh`.

Each step is cumulative (step N binary contains steps 1..N).

## Pagination: what we request per API query (unchanged, already maxed)

| Call | Endpoint | Page size |
|------|----------|-----------|
| Issue search | `POST /rest/api/3/search/jql` | `maxResults: 100` (hard cap when fields are requested) |
| Changelog | `POST /rest/api/3/changelog/bulkfetch` | ≤100 keys/batch, paginated by `nextPageToken` |

Jira Cloud caps `/search/jql` at **100 issues/page** whenever any field beyond
`id`/`key` is requested; jai requests ~26 standard + all custom fields, so 100 is
already the ceiling. Higher page sizes are not available — the bottleneck was
**serial, latency-bound request chaining**, not page size.

## Results

Steps 1–2 are isolated on a single source (source count is irrelevant with one
source). Steps 3–4 are isolated on two sources, since they only bite when there
is cross-source and rate-limited work to overlap.

### Table A — single source: ROX bugs (9709 issues), rate 10

| Step | Change | Wall time | Speedup |
|------|--------|-----------|---------|
| Baseline | inline changelog, serial everything | **288.5s** | 1.00× |
| +1 | decouple changelog from search critical path | **291.9s** | 0.99× |
| +2 | parallelize changelog batch pass (8 workers) | **174.9s** | **1.65×** |

### Table B — two sources: ROX bugs + OCPSTRAT (13197 issues)

| Step | Change | Wall time | Speedup vs 2-src ref |
|------|--------|-----------|----------------------|
| Steps 1–2 ref | serial sources, rate 10 | **306.1s** | 1.00× |
| +3 | run sync sources concurrently | **256.6s** | **1.19×** |
| +4 | raise rate_limit 10→40, concurrency 8 (+ burst fix) | **231.4s** | **1.32×** |
| +4 tuned | rate 40, concurrency 16 | **197.5s** | **1.55×** |

**End to end:** the ROX-bugs full sync dropped from **288.5s to 174.9s (1.65×)**
with steps 1–2 alone; on multi-source configs steps 3–4 compound further.

### Baseline detail
- Issue phase (changelog fetched **inline** per page): **4m46.1s** (286.1s)
- Changelog post-pass: 0/0 (everything already synced inline)
- Effective rate: 9709 / 286s ≈ **34 issues/s** (matches the observed ~31/s)

### Step 1 detail — decouple changelog (time-neutral, the enabler)
- Issue phase (pure, no changelog): **2m31s** (151s) → 9709/151 ≈ **64 issues/s**
- Changelog post-pass (serial, 9709/9709): ~141s
- Total 291.9s ≈ baseline. Same total API work, just reordered — but the 141s
  changelog phase is now a separate, parallelizable target (Step 2), and the
  visible issue-sync rate nearly doubled (34→64/s).
- Code: removed the inline `syncChangelogsForKeys` call from `syncSource`;
  changelog now flows solely through the existing post-sync `SyncChangelogs`
  pass. `reconcile.go` still uses the inline helper for its incremental path.

### Step 2 detail — parallelize the changelog pass
- Issue phase (unchanged): **2m26.7s** (146.7s)
- Changelog post-pass: **141s → ~28s** (~5× on that phase; now bounded by the
  10 req/s rate limiter, not latency)
- Total **291.9s → 174.9s = 1.65× vs baseline**
- Code: `SyncChangelogs` fans 100-key batches across a bounded worker pool
  (`sync.concurrency`, default 8). First batch is fetched synchronously to probe
  bulk-API availability, then reused (no redundant re-fetch). DB writes serialize
  on the single connection; counters guarded by a mutex.
- The serial **search phase (146s) is now the dominant cost** — cursor-chained,
  latency+DB bound, and not rate-limited. Only multiple sources (Step 3) or
  faster per-page work can shrink it.

### Step 3 detail — run sources concurrently
- Serial sources (2-src ref): rox 146.7s + ocp 59.3s = **206s** issue phase +
  ~100s changelog = 306.1s
- Parallel sources: issue phase overlaps to **~157s** (≈ max of the two, not the
  sum — near-ideal, so **not** DB-write-bound as first suspected) + ~100s
  changelog = 256.6s
- **306.1s → 256.6s = 1.19×**; the issue phase alone dropped 206s → 157s (1.31×).
- Code: `Sync` fans sources across a bounded pool (`sync.concurrency`); a schema
  mutex serializes the `ensureCustomColumns` ALTER-TABLE check-then-add so two
  sources can't race to add the same column. `displaySyncProgress` rewritten to
  track concurrent sources with an aggregate spinner.
- Remaining floor: the **~100s changelog phase is rate-limit-bound** — OCPSTRAT
  issues carry deep histories (many paginated bulkfetch calls) that saturate the
  10 req/s limiter. That is Step 4's target.

### Step 4 detail — raise rate limit + fix burst truncation
- Changelog phase, 13197 issues, as levers are lifted:
  - rate 10, concurrency 8 → **~100s** (rate-limited)
  - rate 40, concurrency 8 → **~82s** (now worker-limited: 8 workers ≈ 20 req/s,
    below the new 40 ceiling)
  - rate 40, concurrency 16 → **~49s** (both lifted; compounds)
- Issue phase unchanged (~148s) — search is cursor-serial and not rate-bound.
- Totals: 256.6s → **231.4s** (rate 40 / c8, 1.11×) → **197.5s** (rate 40 / c16,
  1.30×). Vs the 2-src ref: **1.55×**.
- Code: burst was `int(ratePerSec)`, which truncates a sub-1 rate (e.g. 0.5) to a
  **zero burst that blocks every request forever**; now `ceil`, floored at 1.
- **Key insight:** `rate_limit` and `concurrency` are co-limiting. Raising the
  rate alone yields diminishing returns once the worker count caps in-flight
  requests; raise both together. Each batch's internal `nextPageToken`
  pagination is still serial, so deep-history projects have a latency floor no
  rate bump removes.

## Conclusions

- **Biggest single win: Step 2** (parallel changelog), 1.65× on a single source,
  because changelog — not search — dominated the original wall time (the inline
  fetch was ~half of every page's cost).
- **Steps 3–4 compound on multi-source / deep-history configs**, taking the
  two-source sync 306s → 197s (1.55×).
- **Pagination was never the lever.** `/search/jql` is hard-capped at 100
  issues/page with fields; the wins came from removing serialization, not bigger
  pages.
- **Remaining bottleneck: the per-source search phase** (~148s for 9709 issues).
  It is cursor-chained (each page needs the prior page's `nextPageToken`), so it
  cannot be parallelized within a source. Further gains would need either
  Atlassian's two-phase pattern (IDs-only search at maxResults 5000, then
  parallel field bulk-fetch) or overlapping DB writes with the next page fetch.

## Recommended config

Defaults are unchanged and conservative (`rate_limit: 10`, `concurrency: 8`).
For a faster sync against a tenant that tolerates it:

```yaml
sync:
  rate_limit: 40   # Jira Cloud's real budget is far above 10/s per tenant
  concurrency: 16  # must be raised alongside rate_limit to use the headroom
```

## Reproduction

All artifacts in `/tmp/jai-perf` (binaries, configs, per-run logs). Each run:
fresh DB, `jai sync --full`, wall-clock via `run.sh`. Phase timings parsed from
the per-source `✓ … in <elapsed>` summary and the changelog line.
