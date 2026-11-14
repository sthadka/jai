# MCP Server Testing Notes

**Date**: 2026-08-24  
**Session**: Testing `jai` MCP server tools for Jira issue retrieval

## Tools Tested

### `jai_get` (issue retrieval)
- Works for both synced (ROX) and non-synced (SUPPORTEX) projects
- For non-synced projects, falls back to Jira API (`_source: "api"`)
- API fallback returns minimal data — only `key` and `_source`, no fields
- `fields` parameter on API-fetched issues returns empty `{}`
- For synced projects, returns all fields (full 91KB+ response with all custom fields)
- No way to limit fields on the MCP response to avoid massive payloads for synced issues

### `jai_query` (SQL)
- Works correctly for synced projects (ROX, CMP, HPSTRAT, RFE)
- Returns empty results for non-synced projects (expected — no local rows)
- Column names match schema; `project` is the column for project key (not `project_key`)
- Field filtering via SELECT works as expected

### `jai_fields` (field discovery)
- Works well — found `acceptance_criteria` field by searching "acceptance"
- Shows jira_id, type, searchability, and custom flag
- Useful for discovering column names before writing SQL

### `jai_schema` (schema discovery)
- Returns full schema with all columns and types
- Output is very large (62KB+) — could benefit from filtering/pagination

### `jai_view` (named views)
- Not tested beyond schema load

## Issues Found

1. **`jai_get` API fallback returns no field data**: When fetching an issue not in the local DB, the MCP tool returns only `{"_source":"api","key":"..."}` with no fields. The CLI (`./jai get SUPPORTEX-32572 --json`) returns full field data for the same issue. The MCP tool should return the same data as the CLI.

2. **No JQL support via MCP**: The CLI supports `jai query --jql "..."` for live Jira queries, but there's no corresponding MCP tool or parameter. Had to fall back to Bash to use `--jql`. This is the only way to query non-synced projects.

3. **`jai_get` payload size for synced issues**: Returns every field (including hundreds of null custom fields), producing 91KB+ responses. A default field set or field filtering that actually works would reduce token consumption significantly.

## Workarounds Used

- Used `./jai get <key> --json` via Bash for full issue data from non-synced projects
- Used `./jai query --jql "key = X" --fields "key,field" --json` via Bash to query specific fields from non-synced projects
- Used `jai_query` SQL with explicit SELECT columns to get manageable responses from synced projects

## Recommendations

1. Fix `jai_get` API fallback to return full field data (match CLI behavior)
2. Add JQL support to MCP — either a `jql` parameter on `jai_query` or a dedicated `jai_jql` tool
3. Support field filtering on `jai_get` for synced issues (the `fields` parameter exists but seems ignored for large responses)
4. Consider a default compact field set for `jai_get` to avoid 91KB responses
