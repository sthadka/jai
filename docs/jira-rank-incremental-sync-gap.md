# Finding: jai incremental sync misses Jira Rank (reorder) changes

**Status:** FIXED (2026-09-21) — see "Resolution" below
**Date:** 2026-09-21
**Component:** jai `internal/sync` (root cause); surfaced via team-map Projects page ordering
**Severity:** medium — sort/order correctness; data silently drifts, no error
**Fix location:** the jai repo (`~/work/code/jai`).

---

## Resolution

Implemented a **field reconcile pass** (`internal/sync/reconcile.go`) that runs on
every incremental `jai sync`:

- Re-fetches only `key` + `sync.reconcile_fields` (default `[rank]`) for the
  working set, **with no `updated >=` filter**, and diffs each field against the
  stored `raw_json`. Only issues that actually drifted are re-fetched in full and
  upserted (capped at 500/run; above that it recommends `--full`).
- Changelog history is now synced on **every** sync (the `--changelogs` flag was
  removed; `--force` still forces a full changelog re-fetch).
- `last_full_sync` is now recorded for **all** sources (previously only
  project-keyed ones, which is why it was NULL everywhere). Sync warns when a full
  sync is older than `sync.full_sync_warning_age` (default 24h;
  `sync.full_sync_warning` to disable).

Empirical note: `rank` is the guaranteed structural case, but the same
`updated`-blind-spot affects a small number of other fields (e.g. an
integration-written `MDM Account Reference`, occasional issue-link edits). Those
are covered by configuring `reconcile_fields` and/or a periodic full sync.

The original investigation follows unchanged.

---

## Problem statement

team-map's Projects page sorts by Jira **Rank** (`customfield_10019`, a LexoRank
string) so the list matches the priority order users see in Jira. The chain is:
Jira → jai (mirrors the field into its local SQLite `issues.rank`) → team-map
sync → `store.ListProjects` `ORDER BY (rank IS NULL) ASC, rank ASC, name ASC`.

The order is wrong. jai's stored `rank` drifts out of date and only partially
matches Jira's live order (~80–90%). A **live** query
(`jai query --jql "project = ROX and type = 'project' ORDER BY rank ASC"`)
matches the Jira Plan order; the **local** SQL query (`... --sql "... ORDER BY
rank ASC"`) does not.

Root cause: **jai's incremental sync is gated entirely on the issue `updated`
timestamp, and re-ranking an issue in Jira does not bump `updated`.** So a
rank-only change is invisible to incremental sync and the local `rank` value is
never refreshed until the issue is next updated for some *other* reason.

team-map's own code is correct — this is purely upstream (jai) data staleness.

---

## Evidence

Same issue, identical `updated` timestamp locally and live, but stale rank:

| Issue | jai stored `updated` | live `updated` | jai stored `rank` | position (jai SQL) | position (live JQL) |
|---|---|---|---|---|---|
| ROX-36454 | `2026-09-21T13:47:25Z` | `2026-09-21T13:47:25Z` (identical) | `1\|hy3ggf:` | #21 | **#1** |

jai holds the current version of the issue (identical `updated`), yet its `rank`
sorts it at #21 while live Jira sorts it at #1. The rank must have changed in
Jira *after* the last `updated`-bumping event, without moving `updated`.

Corroborating facts gathered from the live instance via jai:

- Only one rank field exists in the whole instance: `field_map` →
  `customfield_10019` "Rank" (text). There is **no** Plan-scoped rank field.
- `sync_metadata.last_full_sync` is **NULL** for every project — jai has never
  run a full sync, so the drift is never reconciled.
- The changelog *does* record rank changes: 66,100 `field = 'Rank'` rows. Example:
  `ROX-36454 | Rank | to_string="Ranked higher" | changed_at=2026-09-15T15:08:10Z`.
  But `from_value`/`to_value`/`from_string` are **empty** — the changelog records
  *that* and *when* an issue was reranked, **not the new LexoRank value**.

---

## Root cause (jai code)

`internal/sync/engine.go`

- Incremental JQL is `updated`-gated (≈ line 298):
  ```
  (<base>) AND updated >= "<high-water-mark − lookback>" ORDER BY updated DESC
  ```
  A rank-only change doesn't move `updated`, so the issue falls below the mark and
  is never returned.
- Even when an issue is returned (via lookback overlap) it is skipped when
  `updated` is unchanged (≈ lines 354–356):
  ```go
  } else if existingUpdated == issue.Updated {
      // Issue unchanged since last sync — skip upsert.
      continue
  }
  ```

`internal/db/changelog.go` — the changelog sync has the **same** blind spot
(≈ lines 89–91):
```sql
SELECT key FROM issues
WHERE changelog_synced_at IS NULL
   OR updated > changelog_synced_at
```
A rank-only change never makes an issue a changelog candidate, so its new `Rank`
changelog entry is not even pulled. (`engine.go:385` only syncs changelogs for
issues upserted in the current page — i.e. those that already passed the
`updated` gate.)

---

## Why "just use the changelog" is not a clean fix

Rank changes do create changelog history, but syncing off it has two blockers:

1. **The changelog omits the value.** Rank entries are `"Ranked higher"` /
   `"Ranked lower"` with empty from/to. Detecting a rerank from the changelog
   still forces a re-fetch of the issue's current `rank` field to get the actual
   LexoRank. The changelog gives you *who/when*, not *what*.
2. **No global "changelog since T" feed.** Jira changelog is per-issue (bulk-by-id
   at best), so you must already know which issues to inspect. And jai's changelog
   candidate query is itself `updated`-gated (above), so today it can't discover
   rank-only changes on its own.

Net: any changelog-driven flow ends in "…now re-fetch the issue's rank anyway",
so a direct rank re-fetch is simpler and strictly less work.

---

## Jira API options to mitigate (research)

### 1. Snapshot + diff on `ORDER BY Rank` (recommended for a poller)
Because `updated` is unreliable for reorders, the robust documented pattern is to
periodically re-fetch the **ordered key list** (`ORDER BY Rank ASC`) and diff it
against the stored order — not to depend on modified timestamps. For jai this
means a **lightweight rank-reconciliation pass**:
- Re-fetch only `key` + `rank` (2 fields, tiny payload) for the working set via
  JQL `(<scope>) ORDER BY rank ASC`, **with no `updated >=` filter**.
- Upsert only rows whose `rank` changed.
- Cheap (rank is a short string, hundreds per page), gets real values, bypasses
  the `updated` gate. Best fit for jai's current poll architecture; run on a
  schedule and/or on demand.

### 2. Webhooks — `jira:issue_updated` (real-time, but needs a receiver)
Re-ranking **does** fire a `jira:issue_updated` webhook for the moved issue, with
the **Rank** field present in the webhook `changelog` payload — even though the
persisted `updated` field (as seen by REST/JQL polling) may not move. So webhooks
catch reorders that polling misses.
- Filter with the webhook `changelog` (look for `field = "Rank"`) or register
  with a `fieldIdsFilter` scoped to `customfield_10019` to receive *only* rank
  events.
- Caveat: delivery order is not guaranteed (use `timestamp`/changelog to
  sequence), and jai would need a persistent HTTP endpoint to receive webhooks —
  an architectural addition beyond today's pull-based sync (jai already has an
  MCP-HTTP mode, but a Jira-webhook receiver is separate). Even on a webhook,
  the Rank changelog lacks the LexoRank value, so a `rank` re-fetch (or re-sort)
  is still needed to persist ordering.

### 3. Agile rank endpoint (write-side, not useful for read)
`PUT /rest/agile/1.0/issue/rank` (`rankBeforeIssue` / `rankAfterIssue`) is for
*changing* rank programmatically; it does not help detect external reorders.

### Advanced Roadmaps / Plans note
The remaining ~10–20% gap between the Plan CSV and `ORDER BY Rank` is Plan-local
manual ordering that is **not** written back to the Rank field. Reproducing it
exactly would require Jira's Advanced Roadmaps/Plans API (internal/GraphQL, not
standard issue REST) — out of scope. Committing the Plan's ordering back to Jira
("Review changes" → save) writes it into the Rank field and closes the gap, but
changes the global rank for everyone.

---

## Recommendations

| Option | Effort | Notes |
|---|---|---|
| **Targeted `key,rank` reconcile pass** (no `updated` gate) | small–medium | Best fit; cheap, gets real values, poll-friendly. |
| Scheduled periodic **full sync** | trivial | Correct but heavy; `last_full_sync` is currently NULL — none has ever run. |
| **Webhook receiver** (`jira:issue_updated`, `fieldIdsFilter`) | medium+ | Real-time; needs a persistent endpoint + delivery-order handling; still re-fetch rank for the value. |

## Immediate workaround
```
jai sync --full        # (or MCP jai_sync with full=true) — re-fetches all issues, rewrites rank
```
Then re-sync the downstream consumer (team-map: Refresh). Expect the ~80–90%
match the live JQL gives; the rest is Plan-local overrides.

---

## References
- Jira Cloud webhooks (events, `fieldIdsFilter`, changelog payload):
  https://developer.atlassian.com/cloud/jira/platform/webhooks/
- "Two webhooks on board transition" (rank change fires its own `issue_updated`):
  https://support.atlassian.com/jira/kb/how-to-handle-two-web-hooks-triggered-when-a-issue-is-transitioned-from-one-status-to-another-via-board/
- Understanding/Managing LexoRank:
  https://support.atlassian.com/jira/kb/understanding-and-managing-lexorank-in-jira-server/
- Ordering issues by rank via REST/JQL (community):
  https://community.developer.atlassian.com/t/how-to-best-order-issues-using-rank-field-and-rest-api/53309
- Reading the rank field via API (community):
  https://community.atlassian.com/forums/Jira-questions/How-to-get-the-rank-with-Jira-API/qaq-p/583700
