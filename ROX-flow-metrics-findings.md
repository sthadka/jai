# ROX Flow-Metrics Findings

Generated with the new `jai metrics` suite (branch `feat/flow-metrics`) against the local
synced DB (`~/.local/share/jai/jai.db`), project **ROX**, `as_of` 2026-11-11.

> **Read this first — the numbers depend on a config I inferred, not an official ROX taxonomy.**
> I mapped statuses→stages, set `done_resolutions: [Done, Done-Errata]`, `wip_stages: [active]`,
> and (to exercise the SLA metric) invented per-priority security deadlines. Change the `metrics:`
> block and these shift. All figures are **snapshot basis** (current resolution), not an event
> model. Facts: generation 2, 37,762 issues → 348,640 intervals, rebuilt in ~4s.

## Coverage / trustworthiness
| field | value |
|---|---|
| issues | 37,762 |
| with changelog | 37,753 (100% any-history) |
| pending-history backlog | 53 |
| never changelog-checked | 7 |
| updated after last check | 46 |
| unmapped statuses | none |

Caveats surfaced by the tool: sprint sources are JQL-only (no real sprint dates), `sync.dev_info`
disabled (no PR cycle time), **story points absent across the cohort** (prefer count-based
throughput). "Any-history" coverage counts issues with *some* changelog rows, not every transition.

## Headline signals

### Delivery is slow and back-loaded
Cohort = genuinely-delivered (resolution Done/Done-Errata).

| metric | n | p50 | p85 | p95 | mean | max |
|---|---|---|---|---|---|---|
| **cycle_time** (active-stage days) | 15,593 | 5.7 | 42.9 | **130.9** | 31.6 | 1,794 |
| **lead_time** (created→resolved) | 23,346 | 22.0 | 138.9 | **383.7** | 83.7 | 2,251 |
| **flow_occupancy** (active ÷ lead, *proxy*) | 23,345 | 0.00 | 0.87 | 1.00 | **0.27** | 1.00 |

- Long tails dominate: p95 cycle ~131 days, p95 lead ~1 year+.
- flow_occupancy mean **0.27** (median 0) ⇒ delivered issues spend ~¾ of their lifetime **not** in
  an active stage — time is lost to backlog/waiting, not hands-on work. (Labeled a proxy, not true
  flow efficiency.)
- `cycle_time` excludes **7,617** issues with `no_active_start` (closed without ever entering an
  active stage — i.e. triaged/declined straight from backlog), plus 10,268 non-delivery resolutions.

### ~30% of closures are not delivery
`resolution_mix` over all resolved issues:

| resolution | count | | resolution | count |
|---|---|---|---|---|
| Done | 23,215 | | Not a Bug | 1,083 |
| Won't Do | 3,766 | | Cannot Reproduce | 929 |
| Obsolete | 2,746 | | Done-Errata | 131 |
| Duplicate | 1,658 | | Can't Do / Declined | 86 |

Genuine delivery (Done + Done-Errata) ≈ **23,346 of ~33,572 resolved (~70%)**. The other ~30%
(Won't Do / Obsolete / Duplicate / Not-a-Bug / CNR) is why the suite refuses to equate "Done
category" with "delivered" — reporting lead/throughput over *all* resolved would overcount output
by ~43%.

### Large, old open backlog
`aged_open` (open = status category ≠ Done):

| age bucket | count |
|---|---|
| 0–7d | 111 |
| 8–30d | 124 |
| 31–90d | 381 |
| 91–180d | 432 |
| **>180d** | **3,212** |

Of ~4,260 open issues, **75% have been open >180 days**. Current active WIP is ~422 (reconstructed
423 vs snapshot 424 — a benign off-by-one the reconciliation gate flagged for review).

### Bug-heavy delivery mix
`ratio` bug:feature over delivered work = **1.88** (5,550 bugs : 2,953 features). Delivered class
counts: task 12,437, bug 5,550, feature 2,953, other 1,317, security 1,089. Nearly 2 bugs shipped
per feature.

### Triage is bimodal
`triage_latency` (created → first assignment or comment): p50 **0.18d (~4h)** but p85 **43d**,
p95 **203d**, mean 40.7d; **4,074 issues never got a response**. Most items are triaged fast; a long
tail is effectively abandoned at intake.

### Churn is modest
| metric | mean / issue | max |
|---|---|---|
| reopen rate | 3.6% (1,348 / 37,762) | — |
| priority_changes | 0.42 | 12 |
| reassignment (count/thrash) | 0.70 | 10 |
| rank_volatility | 1.48 | 119 |

Priority-escalation *direction* is unavailable (no priority ordering configured — the tool reports
change counts, not escalations, rather than guessing).

### Sprints & security
- `sprint_resolution`: of **31,703** sprint ever-members, **22,956 (72.4%)** eventually reached
  genuine-done; **26,867** issues were never in a sprint (much work bypasses sprint planning).
  (Event-time membership; no real sprint *dates* are synced, so this is eventual resolution, not
  velocity/commitment.)
- `security_sla` (Vulnerability/Weakness; **illustrative deadlines I set**: Blocker 3 / Critical 7 /
  Major 30 / Minor·Normal·Undefined 90 days, clock from created): 2,475 security issues, remediation
  p50 **7.7d**, p85 61d, p95 203d, mean 51.6d. Against those deadlines: **2,146 compliant, 342
  breached, 13 open-overdue, 9 open-within**. Severity spread is dominated by **Undefined (1,644)** —
  most security issues carry no real priority, so compliance is only as good as triage.
- `blocker_ageing` (proxy, 687 currently dependency-linked/waiting issues): p50 10.3d, mean 39.7d.
  Labeled a proxy — `issue_links` show a dependency that exists *now*, not when blocking began.

### Time series available
- `throughput`: monthly delivered counts from 2018-07 (ramp to a steady ~150–220/month).
- `net_flow`: created-vs-resolved per month (swings positive/negative).
- `cfd`: 3,226 daily all-stage cumulative points from 2018-01 (for a stacked cumulative-flow chart;
  it intentionally does **not** recover the cycle/lead distribution).
- `wip`: 3,226 daily WIP points.
These are best consumed via `jai metrics <name> --format csv` or the offline dashboard
(`jai metrics dashboard`).

## What I'd act on (if these were my numbers)
1. **Aging backlog**: 3,212 issues open >180d and ~30% non-delivery closures suggest a backlog-
   hygiene / triage problem more than a throughput one. Tighten intake (4,074 never-responded) and
   batch-close stale items.
2. **Low flow efficiency (0.27)**: lead time is dominated by queue/wait, not active work — look at
   WIP limits and handoffs, not individual speed.
3. **Security triage**: 1,644 security issues with Undefined priority makes any SLA meaningless until
   severity is assigned at intake.

## How to reproduce
```bash
# metrics config added to a throwaway copy (same DB), then:
jai --config /tmp/jai-rox-metrics.yaml metrics rebuild -p ROX --no-sync
jai --config /tmp/jai-rox-metrics.yaml -p ROX metrics <name>   # cycle_time, lead_time, throughput,
#   net_flow, aged_open, resolution_mix, ratio, reopen, flow_occupancy, wip, cfd, stage_dwell,
#   workload_by_assignee, triage_latency, blocker_ageing, sprint_resolution, security_sla,
#   priority_changes, rank_volatility, reassignment_count, reassignment_thrash, coverage
```
To make these official, move the `metrics:` block into `~/.config/jai/config.yaml` with a
ROX-owner-reviewed status→stage map, `done_resolutions`, and real security deadlines.
