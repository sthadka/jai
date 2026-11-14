# Code Review: PR #22 — `fix: use Jira user timezone for incremental sync cursors`

**Author:** Feng Pan (@fepan)
**Changes:** +56 / −8 across 4 files
**Reviewed:** 2026-08-31

## Verdict

The core fix is sound. Converting the UTC cursor instant into the authenticated
user's timezone before formatting the JQL literal is the correct fix for Jira
Cloud's user-timezone JQL interpretation, and the `America/New_York` test
confirms the behavior. The findings below are robustness gaps around that fix,
not objections to it.

---

## Findings (ranked by severity)

### ⛔ 0. BLOCKING — CI is red: PR breaks `TestSyncSource_IncludesChangelogs`

`internal/sync/changelog_engine_test.go:102` — Reproduced locally (`make test`),
identical to CI run 33287871690:

```
--- FAIL: TestSyncSource_IncludesChangelogs (6.80s)
    changelog_engine_test.go:102: unexpected request to /rest/api/3/myself
FAIL	github.com/sthadka/jai/internal/sync
```

**Cause:** `Sync` now calls `e.loadJiraTZ(ctx)` (`engine.go:216`) which issues a
`MySelf` request to `/rest/api/3/myself`. The test's mock server only handles
`/search/jql` and `/changelog/bulkfetch` and `t.Fatalf`s on any other path
(`changelog_engine_test.go:101-102`), so the new call fails the test.

This is the same root cause as finding #5 (redundant `/myself` round-trip) but
manifesting as a hard CI failure — it must be fixed before merge. Two options:

1. Update the test mock to handle `/rest/api/3/myself` (returns a `MySelf` with a
   `timeZone`), plus any other affected sync tests.
2. Better — fold the timezone lookup into the existing `VerifyAuth`/`MySelf` call
   so `Sync` doesn't make a second request (see #5), then update tests once.

**Note:** the author's testing (`go test ./internal/sync -run
'^TestCursorToJQL_ConvertsToJiraTimezone$'` + `go test ./internal/jira`) ran only
the single new test, never the full `internal/sync` suite, which is why this was
missed.

### 🔴 1. Missing `time/tzdata` — fix silently no-ops on minimal deployments

`internal/sync/engine.go:189` — `loadJiraTZ` relies on system tzdata (no
`import _ "time/tzdata"`). On a scratch/Alpine container or any host lacking
`/usr/share/zoneinfo`, `time.LoadLocation("America/New_York")` errors,
`loadJiraTZ` falls back to `time.UTC`, and `cursorToJQL` formats in UTC again —
**recreating the exact multi-hour blind window this PR fixes.** CI passes because
dev machines have tzdata, so the regression is invisible.

**Fix:** add `import _ "time/tzdata"`.

### 🟠 2. Silent fallback to UTC — no log or diagnostic

`internal/sync/engine.go:186` — Both the `MySelf` error and the `LoadLocation`
error are swallowed, degrading to UTC with no signal. A transient `/myself`
failure, missing `timeZone` field, or unresolvable name causes cursors to keep
syncing "successfully" while quietly missing up to an offset's worth of updated
issues. Also brushes the repo's `wrap, don't swallow` rule. At minimum, log a
warning so the degradation is visible.

### 🟠 3. Untested fallback + malformed-cursor branches

`internal/sync/engine_test.go:58` — The new test covers only the happy path.
`cursorToJQL(cursor, nil)` must return a UTC-formatted string (the tzdata-missing
/ lookup-failed path that production depends on), and a malformed cursor must
pass through unchanged — neither is exercised. A future refactor could break the
nil guard with green tests.

### 🟡 4. Cached timezone never refreshed + unsynchronized write

`internal/sync/engine.go:184` — `e.jiraTZ` is cached for the Engine's lifetime.
In a long-lived engine (TUI / background worker), a user changing their Jira
timezone isn't picked up until restart. The first-time write also races
concurrent `Sync` callers on a shared Engine (e.g. TUI manual refresh overlapping
the background ticker) — `go test -race` on an overlapping-Sync test would flag
it.

### 🟡 5. Redundant `/myself` round-trip

`internal/sync/engine.go:215` — `VerifyAuth` (`sync.go:28`, `root.go:152`)
already calls `MySelf`, then `Sync → loadJiraTZ` calls it again. `VerifyAuth`
discards its `*MySelf` at `engine.go:54`; it could return/cache it (with
`TimeZone`) so `loadJiraTZ` reuses it. Efficiency, not correctness.

### ⚪ 6. CHANGELOG cosmetics + likely merge conflict

`CHANGELOG.md:3` — Double blank line after `## [Unreleased]`. More importantly,
the branch's CHANGELOG is based on 3.1.0 while `main` already released 4.0.0, so
this section will likely conflict on merge — worth a rebase.

---

## Recommendation

Finding #1 is the one to fix before merge — without `time/tzdata` the fix is
fragile precisely where it matters (containerized deployments). #2 and #3 are
quick, high-value follow-ups. #4–#6 are worth a comment but not blocking.
