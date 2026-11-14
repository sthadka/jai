## Add --add and --remove flags to jai set for array fields (labels, components, fixVersions)

RIT skills (`/rit-escalate`, `/rit-triage`) must add/remove individual labels without overwriting existing values. Currently `jai set` replaces the entire field value.

Add `--add` and `--remove` flags to the `set` command. When used, queue an `update_field` operation (distinct from `set_field`) with a payload like `{"field":"labels","op":"add","value":"rit-escalated"}`. The writer processes this using the Jira update API: `{"update":{"labels":[{"add":"rit-escalated"}]}}` instead of `{"fields":{"labels":"value"}}`.

The `FieldMapping.Type` field from `field_map` table stores the Jira schema type — use it to validate that `--add`/`--remove` are only used on array-type fields. Error clearly if used on a scalar field.

Also update the optimistic local update in `set.go` — for `--add`, read the current JSON array from the DB column, append the value, write back; for `--remove`, filter it out.

Acceptance criteria:
- `jai set ROX-123 --add labels rit-escalated` queues an `update_field` op and updates local DB
- `jai set ROX-123 --remove labels old-label` queues an `update_field` op and updates local DB
- `jai set ROX-123 --add priority High` errors with "priority is not an array field"
- `jai push` correctly sends `{"update":{"labels":[{"add":"..."}]}}` to Jira API
- JSON output works with `--json` flag
- Unit tests for the new writer operation and flag validation

Files (modifies): internal/cli/set.go, internal/sync/writer.go, internal/jira/write.go
Files (new): internal/cli/set_test.go

## Fix array field serialization in set command and writer

When `jai set ROX-123 labels "label1,label2"` is used (scalar set on an array field), the value is stored as a plain string. On push, `processSetField` sends `{"fields":{"labels":"label1,label2"}}` — Jira expects `{"fields":{"labels":["label1","label2"]}}`.

Fix the `set` command to detect array fields via `FieldMapping.Type` and serialize the value as a JSON array. For comma-separated input: split on commas, trim whitespace, marshal as `[]string`. The writer's `processSetField` already uses `interface{}` for the value — it just needs the value stored correctly in the pending_changes payload.

Also fix the optimistic local update to store as a JSON array string, not a bare comma-separated string.

Acceptance criteria:
- `jai set ROX-123 labels "bug,security"` stores `["bug","security"]` in pending_changes payload
- `jai push` sends `{"fields":{"labels":["bug","security"]}}` to Jira
- Scalar fields are unaffected
- Unit tests for array detection and serialization

Files (modifies): internal/cli/set.go, internal/sync/writer.go
Dependencies: depends_on:Add --add and --remove flags to jai set for array fields (labels, components, fixVersions)

## Add jai transition CLI command

RIT engineers need to move tickets through workflows (close, start progress, etc.) via CLI. The Jira client already has `GetTransitions()` and `ExecuteTransition()` methods, and the writer already processes `transition` operations. No CLI command exposes this.

Create `internal/cli/transition.go` with:
- `jai transition <key> <status>` — resolve transition by name (case-insensitive match against available transitions), queue it in pending_changes, then auto-push immediately (transitions are time-sensitive, unlike field edits)
- `jai transition <key> --list` — list available transitions for the issue
- Name resolution: fetch transitions via `GetTransitions()`, match by name (case-insensitive). If ambiguous, show all matches and error. If no match, show available transitions.
- JSON output via `--json` flag following the standard envelope

The auto-push behavior is important: transitions are stateful and time-sensitive. Queue the pending change, then immediately call `Writer.ProcessQueue()` to push it. This differs from `jai set` which defers to `jai push`.

Acceptance criteria:
- `jai transition ROX-123 "In Progress"` queues and immediately pushes a transition
- `jai transition ROX-123 --list` shows available transitions
- `jai transition ROX-123 done` matches case-insensitively
- Ambiguous or unknown transition names produce helpful errors with available options
- JSON output works
- Unit test for name resolution logic

Files (new): internal/cli/transition.go, internal/cli/transition_test.go

## Add bulk set support with --query flag

RIT auto-assignment and priority normalization need to update many tickets at once. Currently requires running `jai set` in a loop.

Add `--query` flag to `jai set`: `jai set --query "SELECT key FROM issues WHERE assignee IS NULL" assignee "current-rit-primary"`. The flag runs the SQL query, extracts `key` column from results, and queues a pending change for each matching issue.

Also support comma-separated keys: `jai set ROX-1,ROX-2,ROX-3 priority Major` — detect commas in the key argument and expand.

Report a summary: `"queued 47 changes (pending sync)"`. With `--json`, return `{"ok":true,"data":{"count":47,"keys":["ROX-1",...]}}`.

Acceptance criteria:
- `jai set --query "SELECT key FROM issues WHERE type='Bug'" priority Major` queues changes for all matching issues
- `jai set ROX-1,ROX-2 labels bug` queues changes for both keys (works with array serialization fix)
- `--query` that returns 0 rows produces a clear message, not a silent success
- `--query` SQL errors are reported clearly
- JSON output works
- Unit tests for key expansion and query-based dispatch

Files (modifies): internal/cli/set.go

## Add jai link command for issue link creation

RIT daily log promotes sub-tasks to standalone tickets and needs to link them back. Issue links are synced from Jira (stored in `issue_links` table) but cannot be created.

Add a `CreateLink` method to the Jira client: `POST /rest/api/3/issueLink` with payload `{"type":{"name":"Relates"},"inwardIssue":{"key":"ROX-1"},"outwardIssue":{"key":"ROX-2"}}`.

Create `internal/cli/link.go`:
- `jai link <from-key> <to-key>` — create a "Relates" link (default type)
- `jai link <from-key> <to-key> --type "is blocked by"` — specify link type
- `jai link --list-types` — list available link types from Jira API
- Direct API call (not queued) since links are simple and idempotent

Acceptance criteria:
- `jai link ROX-1 ROX-2` creates a "Relates" link
- `jai link ROX-1 ROX-2 --type "blocks"` creates a typed link
- `jai link --list-types` shows available link types
- JSON output works
- Error handling for invalid keys or link types

Files (new): internal/cli/link.go, internal/cli/link_test.go
Files (modifies): internal/jira/write.go
