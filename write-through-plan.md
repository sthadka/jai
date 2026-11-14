# Write-Through: Design Plan

Change jai's default write behavior from "queue locally, push later" to "write-through to Jira API immediately." The old queue behavior becomes opt-in via a `--queue` flag.

## Current Architecture

### How writes work today

Every write command falls into one of three categories:

| Category | Commands | Behavior |
|---|---|---|
| **Queue-only** | `set`, `comment` | Writes to `pending_changes` table + optimistic local update. Never hits Jira until `jai push`. |
| **Queue-then-push** | `transition` | Writes to `pending_changes`, then immediately calls `ProcessQueue()` which pushes ALL pending changes. |
| **Direct API** | `create`, `clone`, `link`, `watch`, `unwatch` | Calls Jira API directly. Never touches `pending_changes`. Upserts result into local DB after success. |

The TUI inline field editor (`tui/app.go:520`) also queues to `pending_changes` without pushing.

### Data flow for `jai set` (current)

```
User runs: jai set ROX-123 priority High

1. Validate field exists in field_map              (cli/set.go:112-133)
2. Build payload: {"field":"priority","value":{"name":"High"}}
3. INSERT INTO pending_changes (...)               (cli/set.go:226)
4. UPDATE issues SET priority = 'High' WHERE key = 'ROX-123'  (cli/set.go:230-232)
5. Print: "ROX-123: priority → "High" (pending sync)"
                                    ↑ NOT sent to Jira yet

User runs: jai push

6. Read all pending_changes WHERE synced_at IS NULL (sync/writer.go:38)
7. For each: PUT /rest/api/3/issue/{key}            (jira/write.go:32)
8. On success: UPDATE pending_changes SET synced_at = now()
9. On failure: INCREMENT retry_count, SET last_error
```

### Data flow for `jai comment` (current)

```
User runs: jai comment ROX-123 "Fixed in PR #42"

1. Build payload: {"body":"Fixed in PR #42"}
2. INSERT INTO pending_changes (...)               (cli/comment.go:27)
3. INSERT INTO comments (...) with id='local_<nanos>' (cli/comment.go:32-41)
4. Update issues.comments_text for FTS             (cli/comment.go:42)
5. Print: status "pending"
                                    ↑ NOT sent to Jira yet
```

### Data flow for `jai transition` (current — already write-through)

```
User runs: jai transition ROX-123 "In Review"

1. GET /rest/api/3/issue/{key}/transitions          (cli/transition.go:49)
2. Resolve transition name → ID                     (cli/transition.go:83)
3. INSERT INTO pending_changes (...)                (cli/transition.go:111)
4. ProcessQueue() → pushes ALL pending changes      (cli/transition.go:115-116)
5. Re-fetch issue from Jira, upsert locally         (cli/transition.go:132-141)
```

### Key files

| File | Lines | Role |
|---|---|---|
| `internal/db/pending.go` | 96 | `pending_changes` CRUD: Insert, List, MarkSynced, RecordRetryError, Count |
| `internal/sync/writer.go` | 132 | Queue processor: reads pending, dispatches to Jira client, retry logic |
| `internal/jira/write.go` | 190 | All Jira write API methods: UpdateField, AddComment, ExecuteTransition, etc. |
| `internal/cli/set.go` | 496 | `jai set` — queues `set_field`/`update_field` + optimistic local update |
| `internal/cli/comment.go` | 59 | `jai comment` — queues `add_comment` + local comment insert |
| `internal/cli/transition.go` | 162 | `jai transition` — queues then immediately pushes |
| `internal/cli/push.go` | 56 | `jai push` — drains the queue |
| `internal/cli/status.go` | 154 | Shows pending change count |
| `internal/tui/app.go` | ~520 | TUI inline edit — queues `set_field` |

---

## Proposed Change

### New default: write-through

```
jai set ROX-123 priority High
  → PUT /rest/api/3/issue/ROX-123   (immediate)
  → UPDATE local issues table        (on success)
  → Print: "ROX-123: priority → "High" ✓"

jai set ROX-123 priority High --queue
  → INSERT INTO pending_changes      (deferred, current behavior)
  → UPDATE local issues table        (optimistic)
  → Print: "ROX-123: priority → "High" (queued)"
```

### Commands affected

| Command | Current | New default | With `--queue` |
|---|---|---|---|
| `jai set` | Queue only | Write-through | Queue (old behavior) |
| `jai comment` | Queue only | Write-through | Queue (old behavior) |
| `jai transition` | Queue then push all | Write-through (just this op) | Queue (old behavior) |
| `jai create` | Direct API | No change | N/A (already direct) |
| `jai clone` | Direct API | No change | N/A (already direct) |
| `jai link` | Direct API | No change | N/A (already direct) |
| `jai watch/unwatch` | Direct API | No change | N/A (already direct) |
| `jai push` | Drain queue | No change (still needed for `--queue`'d items) | N/A |
| TUI inline edit | Queue only | See TUI section below | N/A |

---

## Detailed Design

### 1. `jai set` — write-through

**Current flow** (`cli/set.go`):

`setScalarField` (line 191) and `setArrayField` (line 394) both call `InsertPendingChange` then do an optimistic local `UPDATE issues`.

**New flow:**

```go
func setScalarField(cmd *cobra.Command, issueKey, fieldName, jiraID, value, fieldType string) error {
    // ... existing validation and payload building (lines 192-223) ...

    if queue {
        // OLD behavior: queue for later push
        if err := g.db.InsertPendingChange(issueKey, "set_field", string(payload)); err != nil {
            return err
        }
        // Optimistic local update
        g.db.Exec(fmt.Sprintf("UPDATE issues SET %s = ? WHERE key = ?", fieldName), localVal, issueKey)
        fmt.Printf("%s: %s → %q (queued)\n", issueKey, fieldName, localVal)
    } else {
        // NEW default: write-through
        if err := g.jira.UpdateField(cmd.Context(), issueKey, jiraID, payloadVal); err != nil {
            return fmt.Errorf("setting %s on %s: %w", fieldName, issueKey, err)
        }
        // Update local DB only on success
        g.db.Exec(fmt.Sprintf("UPDATE issues SET %s = ? WHERE key = ?", fieldName), localVal, issueKey)
        fmt.Printf("%s: %s → %q ✓\n", issueKey, fieldName, localVal)
    }
    return nil
}
```

**Bulk mode** (`setBulk`, line 293): Currently iterates keys and queues a change for each. In write-through mode, each key gets an immediate API call. This means `jai set --query "SELECT key FROM issues WHERE project = 'ROX'" priority High` fires N API calls sequentially. This is acceptable — Jira's rate limit is ~10 req/s and bulk operations are inherently slow. No parallelization needed for v1; can add `--parallel N` later if needed.

**Array ops** (`setArrayField`, line 394): Currently queues `update_field` with `"op": "add"` or `"op": "remove"`. In write-through mode, calls `g.jira.UpdateFieldOp()` directly. Multiple `--add` / `--remove` flags on the same command produce multiple API calls (one per operation), same as the current push behavior.

### 2. `jai comment` — write-through

**Current flow** (`cli/comment.go`):

Lines 21-42: Inserts into `pending_changes`, then creates a local comment with synthetic ID `local_<nanos>`.

**New flow:**

```go
if queue {
    // OLD behavior
    g.db.InsertPendingChange(issueKey, "add_comment", string(payload))
    // Insert local comment with synthetic ID
    localComment := db.Comment{ID: fmt.Sprintf("local_%d", time.Now().UnixNano()), ...}
    g.db.InsertComment(localComment)
} else {
    // NEW default: write-through
    commentID, err := g.jira.AddComment(cmd.Context(), issueKey, body)
    if err != nil {
        return fmt.Errorf("adding comment to %s: %w", issueKey, err)
    }
    // Insert local comment with REAL Jira ID
    realComment := db.Comment{ID: commentID, ...}
    g.db.InsertComment(realComment)
}
g.db.UpdateIssueCommentsText(issueKey)
```

**Implication:** In write-through mode, the local comment gets the real Jira comment ID instead of a synthetic `local_*` one. This eliminates the duplicate-comment problem that can occur when a sync runs after a push (the synced comment has the real ID, the local one has a synthetic ID → two rows for the same comment).

**Requires:** `AddComment` in `jira/write.go` currently returns only `error`. It needs to also return the comment ID from the Jira response. This is a small change — the response body contains `{"id": "12345", ...}`.

### 3. `jai transition` — simplify

**Current flow** (`cli/transition.go`):

Lines 106-141: Inserts into `pending_changes`, then calls `ProcessQueue()` which pushes ALL pending changes (not just the transition), then re-fetches the issue.

**New flow:**

```go
if queue {
    g.db.InsertPendingChange(issueKey, "transition", string(payload))
    fmt.Printf("%s: transition to %q (queued)\n", issueKey, statusName)
} else {
    // Direct API call — no queue involvement
    if err := g.jira.ExecuteTransition(cmd.Context(), issueKey, transitionID); err != nil {
        return fmt.Errorf("transitioning %s: %w", issueKey, err)
    }
    // Re-fetch and upsert locally (existing logic, lines 132-141)
    refreshIssueLocally(cmd.Context(), issueKey)
    fmt.Printf("%s: → %s ✓\n", issueKey, statusName)
}
```

**Key fix:** The current `transition` command has a surprising side effect — it pushes ALL pending changes, not just the transition. A user who queued 5 field edits and then runs `jai transition ROX-123 "Done"` gets those 5 edits pushed too. The new write-through default eliminates this because the transition goes directly to the API without touching `ProcessQueue`.

### 4. TUI inline edit

**Current flow** (`tui/app.go:520`):

```go
_ = a.database.InsertPendingChange(issueKey, "set_field", payload)
```

No push, no error handling (error is silently discarded).

**Options for TUI:**

**Option A: Write-through in TUI (recommended)**

Make the TUI also write-through by default. When the user presses Enter to confirm a field edit:

1. Show a brief "saving..." indicator
2. Call `jira.UpdateField()` in a background goroutine (bubbletea `tea.Cmd`)
3. On success: update local DB, show "✓ saved"
4. On failure: show error message, offer to queue instead

This matches user expectations — when you edit a field in a UI, you expect it to save immediately. The current "silently queued, push later" behavior is surprising.

**Latency concern:** A Jira API call takes 200-800ms. In TUI mode, the user is already looking at a full-screen interface and can continue browsing while the save completes in the background. This is similar to how web-based Jira works (inline edits save asynchronously with a spinner).

**Option B: Keep TUI as queue-only**

TUI always queues, never writes through. Add a visual indicator showing queued changes count and a keybinding (e.g., `P`) to push.

Simpler to implement but inconsistent with the CLI default and with user expectations for a UI.

**Option C: TUI respects a config setting**

Add `tui.write_through: true|false` to the config. Default `true` for consistency.

Over-engineered for v1. Start with Option A, add config later if users complain about latency.

**Recommendation: Option A.** Consistent behavior across CLI and TUI. Background `tea.Cmd` handles latency gracefully.

### 5. The `--queue` flag

**Scope:** Add `--queue` (short: `-q`) to `set`, `comment`, and `transition`.

```go
var queueFlag bool

func init() {
    setCmd.Flags().BoolVarP(&queueFlag, "queue", "q", false, "Queue change locally instead of pushing to Jira immediately")
    commentCmd.Flags().BoolVarP(&queueFlag, "queue", "q", false, "Queue change locally instead of pushing to Jira immediately")
    transitionCmd.Flags().BoolVarP(&queueFlag, "queue", "q", false, "Queue change locally instead of pushing to Jira immediately")
}
```

**Persistent flag alternative:** Could add `--queue` as a persistent flag on `rootCmd` so it applies to all commands. But `create`, `clone`, `link`, `watch` are already direct-API and adding queue support for them is unnecessary complexity. Better to add it per-command.

**Config override:** Consider adding `default_write_mode: "direct" | "queue"` to the YAML config for users who prefer the old behavior globally. This would make `--queue` the default without passing the flag every time. The flag then acts as an override in either direction — a `--direct` flag could force write-through when the config says queue. But for v1, the flag alone is sufficient. Add config later if there's demand.

### 6. `jai push` — still needed

`jai push` remains unchanged. It drains `pending_changes` for any `--queue`'d items. If no items are queued, it prints "No pending changes." as today.

### 7. `jai status` — minor update

`status.go:33` shows `pendingCount`. Still relevant — shows queued items. Could add a note: "Use 'jai push' to sync queued changes."

---

## Implications and Impact

### Breaking changes

1. **`jai set` and `jai comment` now require network.** Previously these commands worked offline (queued locally). Now they fail if Jira is unreachable. Users who relied on offline queuing must add `--queue`.

2. **Error surfaces shift.** Currently, validation errors from Jira (invalid field value, permission denied) surface at `jai push` time. Now they surface immediately at `jai set`/`jai comment` time. This is actually better UX — you learn about errors immediately rather than discovering them later.

3. **`jai transition` no longer side-pushes queued changes.** Currently, `jai transition` pushes ALL pending changes (not just the transition). Users who relied on this implicit push-all behavior will need to run `jai push` separately. This is arguably a bug fix — the side-push was surprising.

4. **Output format changes.** `"(pending sync)"` → `"✓"` for successful writes. `--json` output changes `"status": "pending"` → `"status": "synced"`. Scripts parsing this output will break.

5. **TUI saves immediately.** Users who relied on "edit in TUI, review queued changes, push when ready" workflow must adapt.

### Behavioral changes (non-breaking)

1. **Local comments get real Jira IDs.** No more `local_*` synthetic IDs for write-through comments. Eliminates potential duplicates after sync.

2. **Local DB is always consistent with Jira.** No more "edited locally but not yet in Jira" state (unless `--queue` is used). `jai query` results match Jira state.

3. **Slightly slower commands.** `jai set` goes from ~5ms (local write) to ~300-800ms (API call + local write). Acceptable for interactive use. Bulk `jai set --query` is proportionally slower.

4. **Bulk operations are sequential.** `jai set --query "..." field value` fires one API call per matched issue. For 100 issues at ~500ms each, that's ~50 seconds. Current queue+push is also sequential (writer.go processes one at a time), so this isn't worse — it's just visible during the command instead of during push.

### What stays the same

- `jai create`, `jai clone`, `jai link`, `jai watch`, `jai unwatch` — already direct API, no change.
- `jai push` — still works for `--queue`'d items.
- `jai sync` — read path, completely unaffected.
- `jai query` — unaffected (though results are now more likely to match Jira state).
- `pending_changes` table — still exists, still used by `--queue` mode.
- Retry logic in `sync/writer.go` — still used by `jai push` for `--queue`'d items.
- `jai status` — still shows pending count.

### Risk assessment

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Users rely on offline queuing | Low | Medium | `--queue` flag preserves old behavior. Document in changelog. |
| Bulk `jai set` with large result sets is slow | Medium | Low | Sequential is fine for v1. Add `--parallel N` later. |
| TUI latency on slow networks | Medium | Low | Background tea.Cmd with spinner. Falls back to queue on error. |
| Scripts parse `"status": "pending"` | Low | Medium | Document JSON output changes in changelog. |
| Jira rate limiting on rapid successive commands | Low | Low | Jira Cloud allows ~10 req/s. Only bulk ops approach this. |

---

## Implementation Plan

### Phase 1: Core write-through (3 commands)

**1a. Refactor `jai set` write path** (`cli/set.go`)

- Add `--queue` / `-q` flag
- Extract write logic into a helper: `executeFieldChange(ctx, issueKey, jiraID, payload, fieldName, localVal, queue bool)`
- In default mode: call `g.jira.UpdateField()` or `g.jira.UpdateFieldOp()`, update local DB on success
- In queue mode: existing `InsertPendingChange` + optimistic local update
- Update output: `"✓"` for write-through, `"(queued)"` for queue mode
- Handle all three sub-flows: `setScalarField`, `setArrayField`, `setBulk`

**1b. Refactor `jai comment` write path** (`cli/comment.go`)

- Add `--queue` / `-q` flag
- In default mode: call `g.jira.AddComment()`, insert local comment with real ID
- Requires: update `AddComment` in `jira/write.go` to return the comment ID (currently returns only error)
- In queue mode: existing behavior with synthetic `local_*` ID

**1c. Simplify `jai transition`** (`cli/transition.go`)

- Add `--queue` / `-q` flag
- In default mode: call `g.jira.ExecuteTransition()` directly (no ProcessQueue)
- Keep the post-transition refresh logic (re-fetch issue, upsert locally)
- In queue mode: just insert into `pending_changes`, do not auto-push

### Phase 2: TUI write-through

**2a. Background save command** (`tui/app.go`)

- Replace `InsertPendingChange` at line 520 with a `tea.Cmd` that calls `jira.UpdateField()`
- Add save state to the TUI model: `saving bool`, `saveErr error`
- Show "saving..." during the API call, "✓" on success, error message on failure
- On failure: fall back to queuing with a message "Queued — push with 'jai push'"

**2b. Save indicator in status bar**

- Show queued change count in the TUI status bar (if any `--queue`'d items exist)
- Add `P` keybinding to push queued changes from within the TUI

### Phase 3: JSON output and status updates

**3a. Update JSON output** (`cli/set.go`, `cli/comment.go`, `cli/transition.go`)

- Write-through success: `{"ok":true,"data":{"issue_key":"ROX-123","field":"priority","value":"High","status":"synced"}}`
- Queue mode: `{"ok":true,"data":{...,"status":"queued"}}`
- Write-through failure: `{"ok":false,"error":{"type":"JiraError","message":"..."}}`

**3b. Update `jai status`** (`cli/status.go`)

- Clarify pending count with context: `"Queued changes: 3 (push with 'jai push')"`

### Phase 4: Tests and documentation

**4a. Unit tests**

- Test write-through path for `set`, `comment`, `transition` (mock Jira client)
- Test `--queue` flag preserves old behavior
- Test error handling: API failure in write-through mode returns error, does not update local DB
- Test bulk `set` in write-through mode
- Update existing `pending_test.go` tests

**4b. Integration tests**

- End-to-end: `jai set` in write-through mode → verify Jira API called → verify local DB updated
- End-to-end: `jai set --queue` → verify pending_changes row → `jai push` → verify Jira API called

**4c. Documentation**

- Update `docs/spec.md` write path section
- Add migration guide in release notes: "Write-through is now the default. Use `--queue` for offline queuing."
- Update `jai set --help`, `jai comment --help`, `jai transition --help`

### Phase 5: Release

- Bump major version (breaking change)
- Changelog entry explaining the behavioral change
- Migration guide for users who relied on queue-first behavior

---

## Open Questions

1. **Should `--queue` be a global persistent flag or per-command?** Per-command is cleaner (not all commands support queuing). But a global flag with config override (`default_write_mode`) would let users opt back into the old behavior entirely. Recommend: per-command flag for v1, add config override in a follow-up if requested.

2. **Should write-through re-fetch the issue after success?** `jai transition` already does this (lines 132-141). For `jai set`, the optimistic local update is likely sufficient — the value you just set is what you'd get back. But a re-fetch guarantees consistency (Jira may transform values, e.g., trimming whitespace). Recommend: don't re-fetch for `set`/`comment` in v1. The optimistic update is good enough and avoids a second API call.

3. **Should bulk `jai set --query` continue on error?** Currently (via ProcessQueue), each change is independent — failures don't stop processing. In write-through mode, should `jai set --query "..." priority High` stop on first error or continue? Recommend: continue and report per-issue results (same as current push behavior). Add `--fail-fast` flag if needed later.

4. **What about `pending_changes` cleanup?** After write-through, the change is not recorded in `pending_changes` at all (no audit trail). Should we still record it with `synced_at` set immediately? Recommend: no. Write-through means "don't queue." The Jira API response is the source of truth. Adding audit logging is a separate concern.

5. **TUI: should edits auto-save or require explicit confirm?** Currently, pressing Enter on a field edit queues immediately. In write-through mode, Enter would fire an API call. Should we add a "Save? [y/n]" confirmation? Recommend: no confirmation. The current UX is "edit and press Enter to save" — changing to "edit and press Enter, then confirm" is a regression. The API call is fast enough (~500ms) that it feels like a save, not a destructive action.
