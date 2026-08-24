# Error Recovery

Common error patterns and fixes.

## Contents
- ["no such column" → Schema drift](#no-such-column--schema-drift)
- ["no such table: issues_fts" → FTS not built](#no-such-table-issues_fts--fts-not-built)
- ["UNIQUE constraint failed" → Duplicate key](#unique-constraint-failed--duplicate-key)
- [Auth/connection errors → Check status](#authconnection-errors--check-status)
- [Write fails → Use queue](#write-fails--use-queue)
- [Empty results → Check sync freshness](#empty-results--check-sync-freshness)
- [Custom field not found → Check field_map](#custom-field-not-found--check-field_map)

## "no such column" → Schema drift
If a query fails with "no such column: <name>":
1. Run `jai schema db --json` to refresh your schema knowledge
2. Run `jai fields --json --filter "<name>"` to find the actual column name
3. Retry the query with the correct column name

## "no such table: issues_fts" → FTS not built
Run `jai sync` to trigger FTS index rebuild, then retry search.

## "UNIQUE constraint failed" → Duplicate key
The issue already exists locally. Use `jai get <KEY> --json` to check its state.

## Auth/connection errors → Check status
Run `jai status --json` to diagnose. Look at auth_ok and last_sync fields.

## Write fails → Use queue
If a write-through operation fails (network, permissions):
1. Retry with `--queue` to save locally
2. Fix the issue (network, permissions)
3. Run `jai push` to flush the queue

## Empty results → Check sync freshness
If a query returns empty but you expect results:
1. Check `jai status --json` → last_sync timestamp
2. Run `jai sync` to refresh
3. Retry the query

## Custom field not found → Check field_map
Run `jai fields --json --filter "<partial name>"` to find the actual mapped column name.
Custom fields are slugified: "Story Points" → story_points, "Target Release" → target_release.
