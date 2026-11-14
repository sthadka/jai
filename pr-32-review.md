# Code Review: PR #32 — `feat!: reconcile rank and other silently-changed fields on every sync`

**Author:** Sukumar Yethadka (@sthadka)
**Branch:** `feat/reconcile-silent-fields` → `main`
**Changes:** +966 / −28 across 11 files
**CI:** ✅ Build and Test passing; `make check` reproduces clean locally
**Reviewed:** 2026-09-21

## Verdict

The core mechanism is sound and well-tested for the common case: a cheap
key+field scan (no `updated` filter) diffs against stored `raw_json` and only
issues that actually drifted get a full re-fetch, capped at 500/run. The
`last_full_sync` fix is a straightforward, correctly-scoped bug fix. Docs
(README, spec.md, user-guide.md) are updated accurately and the breaking
change is properly flagged for release-please.

The findings below are gaps in error visibility and two design trade-offs
worth confirming with the author before merge — nothing here indicates the
happy path is broken.

---

## Findings (ranked by severity)

### 🟠 1. Refetch failures are silently swallowed — no error surfaces anywhere

`internal/sync/reconcile.go:183-193` (`refetchAndUpsert`):

```go
for page, err := range e.client.SearchAll(ctx, jql, fields) {
    if err != nil {
        break
    }
    ...
}
```

If the refetch phase's `SearchAll` call fails (network blip, auth expiry,
rate limit) after drift was already detected, the loop just `break`s. Nothing
is logged, and `ReconcileProgress.Err` is never set for this path (only the
*scan* phase sets `Err`). The only externally visible symptom is
`Refetched < Changed` in the final progress event — a discrepancy neither the
CLI nor the MCP consumer checks for (see #2). A user (or agent) has no way to
learn that known-drifted issues (e.g. rank) were *not* actually corrected
this run.

**Suggested fix:** propagate the error onto `ReconcileProgress.Err` (or a new
field distinguishing "N of M refetched") instead of dropping it.

### 🟠 2. CLI shows nothing when a reconcile pass detects drift but fixes none of it

`internal/cli/sync.go:229-243` (`displayReconcileProgress`):

```go
switch {
case p.Err != nil:
    ...
case p.Skipped:
    ...
case p.Refetched > 0:
    ...
}
```

There's no `default` branch. If `Changed > 0` and `Refetched == 0` — which
happens whenever finding #1 occurs, or every issue in the batch fails to
upsert — the switch matches nothing and prints *no output at all*. The sync
looks identical to a clean run with zero drift. Given the whole point of this
feature is to stop silent drift, a reconcile pass that itself fails silently
undercuts the fix. The MCP handler (`internal/mcp/tools_sync.go:143-151`) has
the same blind spot: it sums `p.Refetched` and drops `p.Err` entirely.

**Suggested fix:** add a branch (or fold into #1's fix) that reports
"`N drifted, 0 fixed`" distinctly from "`0 drifted`".

### 🟡 3. Full, unfiltered issue scan runs on every incremental sync — cost not discussed

`internal/sync/reconcile.go:112`: `jql := sourceJQL(src)` with **no**
`updated >=` filter, paginated 100 issues/request via `SearchAll`. This means
every `jai sync` now does a full table scan of every issue in every
configured source (not just what changed), in addition to the normal
incremental fetch. For a project/backlog with, say, 10k+ issues, that's ~100+
extra Jira API requests *every sync cycle* — and with the default
`sync.interval: 15m`, that recurs indefinitely.

The 500-issue cap bounds the *refetch* cost, but not the *scan* cost, which
scales with total backlog size regardless of how much actually changed. This
isn't discussed in the PR description or in
`docs/jira-rank-incremental-sync-gap.md`. It may well be an accepted
trade-off (the alternative — missing rank drift — is the bug being fixed),
but it's worth confirming this was sized against the largest expected
project/org before merge, since it changes the steady-state Jira API call
volume for every user, not just ones affected by rank drift.

### 🟡 4. Large reconcile scans show no progress feedback

`internal/cli/sync.go:230-232`:

```go
for p := range ch {
    if !p.Done {
        continue
    }
    ...
```

`reconcileSource` (`internal/sync/reconcile.go:150`) emits an intermediate
`ReconcileProgress` per page with running `Scanned`/`Changed` counts, but the
CLI discards every non-`Done` event. Combined with #3, a large project's
reconcile pass can take a noticeable amount of time with zero visible
output — no spinner, no counter — unlike the main sync and changelog phases,
which both render live progress. Minor UX inconsistency, easy fix (mirror the
spinner pattern already used by `displaySyncProgress`/`displayChangelogProgress`).

### 🟡 5. `full_sync_overdue` logic duplicated verbatim between CLI and MCP

`internal/cli/sync.go:249-283` (`warnIfFullSyncOverdue`) and
`internal/mcp/tools_sync.go` (`fullSyncOverdue`) implement the same
age-threshold-over-`AllSyncMeta()` logic independently. Neither is unit
tested. Low risk today since they're identical, but any future tweak to one
(e.g. how `sourceFilter` matching works, or the RFC3339 parse fallback) risks
silently diverging from the other. Consider hoisting this onto `*Engine` as a
single method both callers use.

### 🟢 6. `--force` on `jai sync` has narrower scope than its name suggests

`internal/cli/sync.go:290-291`: `jai sync --force` only forces a changelog
re-fetch (resets `changelog_synced_at`). Given `jai sync --full` already
exists and reconciles *all* fields, a bare `--force` flag reads like it might
force a fuller resync of issues too. The flag's `--help` text is clear
(`"re-fetch all changelog history from scratch..."`), so this is a minor
naming/discoverability nit rather than a functional issue.

### 🟢 7. MCP `jai_sync` doesn't expose the changelog `force` knob

`internal/mcp/tools_sync.go`: `SyncChangelogs(ctx, source, false)` hardcodes
`force=false` — there's no way for an MCP client to trigger the equivalent of
CLI's `--force`. The PR description claims "MCP `jai_sync` parity" for
reconcile + changelog + overdue behavior; this one knob is the exception.
Likely fine (MCP is meant for lightweight agent-triggered syncs), just noting
the gap.

### 🟢 8. Object-valued reconcile fields could false-positive on key order

`internal/sync/reconcile.go:257-271` (`normalizeRaw`) uses `json.Compact`,
which removes whitespace but does **not** canonicalize object key order.
`rawFieldEqual` will report drift for a field whose JSON value is an object
(e.g., a picklist/user field) if the two responses serialize keys in a
different order, even though the value is semantically unchanged. In
practice this fails safe (an unnecessary refetch, not a missed change), and
`rank`'s value is a plain string so it's unaffected — but the mechanism is
explicitly designed to be reused for arbitrary fields (the PR mentions an
integration-written `MDM Account Reference` field), so it's worth a
`json.Marshal(sortedMap)`-style canonicalization if/when an object-typed
field is added to `reconcile_fields`. Not blocking.

---

## Test coverage gaps

`internal/sync/reconcile_test.go` covers the core detect/refresh and
no-drift/no-refetch paths well, plus the `normalizeRaw`/`rawFieldEqual` unit
cases. Missing:

- The `maxReconcileRefetch` (500) cap / `Skipped` path.
- An unknown/unmapped `reconcile_fields` entry (the "warn and skip" branch in
  `ReconcileFields`).
- A refetch-phase failure (would have caught #1/#2).
- Multi-page or multi-source reconcile runs.
- `warnIfFullSyncOverdue` / `fullSyncOverdue` (untested in both places — see #5).

---

## What's solid

- `normalizeRaw`/`rawFieldEqual` null-vs-missing collapsing is correct and
  well-tested.
- The `last_full_sync` fix (`internal/sync/engine.go`) correctly moves
  `UpdateFullSyncMeta` out of the `project-keyed-only` block — a real,
  narrowly-scoped bug fix.
- Scan/refetch batch sizes (100) stay safely under SQLite's `?` placeholder
  limit for `GetIssuesRawJSON`'s `IN (...)` query.
- Docs (README "Keeping rank fresh" section, `docs/spec.md` Field
  Reconciliation section, `docs/user-guide.md`, command table) are
  consistent with the actual behavior.
- Breaking change (`--changelogs` removal) is clearly called out with
  `feat!:` / `BREAKING CHANGE:` for release-please version bumping.
