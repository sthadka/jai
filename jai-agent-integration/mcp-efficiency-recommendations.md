# MCP Token Efficiency & Composability — Recommendations

**Date**: 2026-08-24
**Context**: Real-world testing revealed that jai's MCP tools are functional but not token-efficient. The core philosophy — "select exactly the fields you need" — is undermined by MCP tools that return bloated responses by default. This document diagnoses every inefficiency and proposes a composable, hyper-efficient tool design.

---

## Executive Summary

| Operation | Current Cost | Ideal Cost | Waste Factor |
|-----------|-------------|------------|--------------|
| `jai_get` (single issue, no fields) | ~91KB | ~1-2KB | **45-90x** |
| `jai_schema(mode='db')` | ~62KB | ~2-3KB | **20-30x** |
| `jai_get` API fallback | ~100B (broken) | ~1KB | **broken** |
| First-use bootstrap (4 calls) | ~170KB | ~3KB | **55x** |
| Sprint review (5 issues) | ~517KB | ~2KB | **258x** |

The tools return everything by default, don't strip nulls, include internal columns (`raw_json`, `comments_text`), and offer no progressive disclosure. The CLI is 45x more efficient because it curates output — the MCP layer must do the same.

---

## 1. Critical Bugs

### B1: `jai_get` API fallback returns no field data

**File**: `tools_read.go:196-207`

When an issue isn't in the local DB, the MCP tool falls back to the Jira API but returns only `{"_source":"api","key":"..."}` — no fields at all. The CLI's `issueFieldsToMap()` properly parses the API response into ~20 useful fields.

```go
// Current — broken
data = make(map[string]interface{})
data["key"] = issue.Key
data["_source"] = "api"
// issue.Fields is completely ignored
```

**Fix**: Port `issueFieldsToMap()` from `cli/get.go` to a shared function and call it in the MCP handler.

### B2: No JQL support via MCP

The CLI supports `jai query --jql "..."` for live Jira queries — the only way to query non-synced projects. The MCP tools have no equivalent. Agents must fall back to Bash, defeating the purpose of the MCP server.

**Fix**: Add a `jql` parameter to `jai_query` (mutually exclusive with `sql`).

---

## 2. Token Efficiency Problems

### E1: `jai_get` returns everything (91KB per issue)

**File**: `tools_read.go:188`

```go
results, err := s.query.Execute("SELECT * FROM issues WHERE key = ?", key)
```

Then builds a map of ALL columns including:
- `raw_json`: The entire Jira API response blob (~50KB alone)
- `comments_text`: FTS concatenated text (~1-20KB)
- ~60 null custom fields (~5-10KB of `"field": null`)
- `synced_at`, `id`: internal metadata

**CLI comparison**: The CLI's human output renders only ~25 curated fields via `frontMatterEntries` in ~2KB. The MCP tool returns 45x more data.

### E2: `jai_schema(mode='db')` dumps 62KB

**File**: `tools_schema.go:107-169`

Returns every column from `PRAGMA table_info(issues)` plus every entry in `field_map`. A typical instance has 90+ columns and 100+ field mappings. This is the agent's "hello world" call — it runs before any useful work.

### E3: Null values never stripped

```go
data = make(map[string]interface{}, len(results.Columns))
for i, col := range results.Columns {
    data[col] = results.Rows[0][i]  // includes nulls
}
```

An issue with 90 columns where 60 are null sends 60 `"field": null` entries (~5-10KB waste).

### E4: Snippets return both raw and expanded SQL

**File**: `tools_schema.go:242-282`

Every snippet returns both the raw template and the expanded form with variables substituted. Agents use snippet names (`{{my_open}}`), not expanded SQL. The `expanded` field is wasted tokens.

### E5: Default limits are too high

- `jai_query`: LIMIT 100 default — agents exploring data need 20, not 100
- `jai_schema(mode='values')`: Up to 200 values — top 20 with counts is sufficient
- Bulk `jai_set` response includes full keys array even for hundreds of keys

### E6: Resources have no limits

**File**: `resources.go`

- `jira://issue/{key}`: Returns ~25 fields PLUS the entire parsed `raw_json` blob
- `jira://schema/db`: Same 62KB problem as `jai_schema`
- `jira://query/{sql}`: No LIMIT enforcement — unbounded results

---

## 3. Recommended Changes

### P0 — Critical (43-258x token reduction)

#### R1: Default compact field set for `jai_get`

When `fields` is omitted, return only a curated set matching the CLI's `frontMatterEntries`:

```
key, summary, status, status_category, type, priority, assignee, reporter,
labels, components, fix_version, parent_key, created, updated, sprint_name
```

Add `fields: "all"` sentinel for the rare case when every column is needed. Update the tool description to say: _"Fields to include (default: key, summary, status, priority, assignee, type). Use 'all' for every column."_ — this guides agents toward efficiency without requiring the skill file.

#### R2: Strip null values from all MCP responses

Add a `stripNulls()` filter before JSON serialization. Never include fields with null or empty values. For `jai_get` alone, this cuts response size by 60-80% even without field filtering.

#### R3: Exclude internal columns

Never return these in MCP responses unless explicitly requested via `fields: "all"`:
- `raw_json` — the raw Jira API response (50KB+)
- `comments_text` — FTS concatenated text
- `synced_at` — internal sync timestamp
- `id` — internal row ID

#### R4: Fix `jai_get` API fallback

Parse `issue.Fields` using the same logic as the CLI. Extract the shared `issueFieldsToMap()` function.

### P1 — Important (5-10x additional reduction)

#### R5: Tiered schema discovery

Replace the 62KB `jai_schema(mode='db')` dump with tiers:

| Tier | Returns | When |
|------|---------|------|
| `core` (default) | ~20 standard Jira fields + custom fields with >50% population | 90% of calls |
| `custom` | Custom fields only, with Jira names and population stats | When exploring custom fields |
| `all` | Current behavior (full dump) | Never, in practice |

Add a `filter` parameter for targeted lookup: `{"mode": "db", "filter": "sprint"}`.

#### R6: Add JQL support to `jai_query`

```json
{"jql": "project = SUPPORTEX AND type = Bug", "fields": "key,summary,status", "limit": 20}
```

Mutually exclusive with `sql`. Returns the same columnar format as SQL queries.

#### R7: Lower default limits

| Tool | Current | Proposed |
|------|---------|----------|
| `jai_query` | 100 | 20 |
| `jai_schema(mode='values')` | 200 | 20 |
| `jai_search` | 20 | 20 (keep) |

Add `truncated: true` and `total: N` when results are limited so agents know there's more.

#### R8: Snippets — return names and raw only

Drop the `expanded` field from default snippet responses. Add `expand: true` parameter for the rare case it's needed.

#### R9: Tool descriptions that prevent waste

Current descriptions say "Omit for all fields" — this encourages bloated responses.

**Better**: _"Fields to include (default: key, summary, status, priority, assignee, type, created, updated). Use 'all' for every column including custom fields."_

The tool description itself is the first line of defense against token waste.

### P2 — Composability Improvements

#### R10: Merge `jai_schema` + `jai_fields` into `jai_discover`

Reduce overlapping tools into a single discovery endpoint:

| Mode | Returns | Replaces |
|------|---------|----------|
| `columns` | Core column list with types | `jai_schema(mode='db')` |
| `values` | Distinct values for a column | `jai_schema(mode='values')` |
| `field_map` | Field mappings with Jira IDs | `jai_fields` |
| `snippets` | SQL fragment names | `jai_schema(mode='snippets')` |
| `templates` | Issue template names | `jai_schema(mode='templates')` |
| `bootstrap` | Core columns + snippet names + view names + sync age in one call | New — replaces 4 calls with 1 |

The `bootstrap` mode is the key composability win: an agent's first interaction costs ~2KB instead of ~170KB across 4 separate calls.

#### R11: Collapse write tools from 7 to 2

Current: `jai_set`, `jai_comment`, `jai_transition`, `jai_create`, `jai_clone`, `jai_link`, `jai_update` (7 tools).

Proposed:
- **`jai_update`** — All mutations on existing issues. Already supports set + transition + comment. Add link and watch actions.
- **`jai_create`** — All creation. Merge clone into create with a `clone_from` parameter.

Each tool in the MCP tool list costs ~200 tokens in tool-selection overhead. Cutting from 7 to 2 saves ~1000 tokens per turn on tool listing alone.

#### R12: Add `context` parameter for progressive responses

```json
// Scanning — 3 fields per issue
{"action": "get", "key": "ROX-123", "context": "list"}
// → {"key":"ROX-123","summary":"Fix bug","status":"In Progress"}

// Inspecting — 15 curated fields
{"action": "get", "key": "ROX-123", "context": "detail"}
// → key, summary, status, priority, assignee, type, labels, components, ...

// Everything — custom fields, description, comments
{"action": "get", "key": "ROX-123", "context": "full"}
// → all non-null fields
```

This is "progressive disclosure" applied to tool responses. Agents start with `list`, drill into `detail` for interesting issues, and rarely need `full`.

#### R13: Add `count_only` to `jai_query`

Let agents check "how many issues match?" before fetching rows. Costs 1 small call instead of accidentally loading 500 issues.

#### R14: Batch operations

Accept an array of operations in one call:
```json
{"operations": [
  {"action": "set", "key": "ROX-1", "field": "priority", "value": "High"},
  {"action": "transition", "key": "ROX-2", "status": "Done"},
  {"action": "comment", "key": "ROX-3", "text": "Resolved in v2.1"}
]}
```

Replaces N tool calls with 1. Return per-operation results.

#### R15: Add `_sync_age` to read responses

Include `"_sync_age_seconds": N` in read responses so agents can judge data freshness without a separate `jai_status` call. One field saves one tool call.

#### R16: Query-level column selection

Build `SELECT a, b, c FROM issues WHERE ...` from the `fields` parameter instead of `SELECT * ... + post-filter`. This reduces DB I/O and avoids deserializing `raw_json` entirely.

#### R17: Resources should match tool limits

Strip `raw_json` from `jira://issue/{key}` resources. Enforce LIMIT on `jira://query/{sql}`. Apply the same null-stripping and default field sets as tools.

---

## 4. Design Principles

### P1: Default to minimal, opt into verbose

Every tool returns the minimum useful response by default. More data requires explicit opt-in (`fields: "all"`, `tier: "all"`, `context: "full"`). This is the opposite of the current approach.

**Analogy**: The filesystem MCP server returns file listings (names + sizes), not file contents. You ask for content when you need it. jai should return issue summaries, not full issue dumps.

### P2: One call per intent

An agent wanting to "update priority and move to In Progress" makes 1 `jai_update` call, not 3 separate calls. Composite tools with multi-action support are more efficient than atomic CRUD tools.

### P3: Schema discovery should be incremental

Don't dump the entire schema on first call:
1. Core columns (~20 fields) via `jai_discover mode=columns` or `bootstrap`
2. Custom columns on request (`tier: "custom"`)
3. Values for a specific column on request (`mode: "values"`)

This matches how humans learn a database.

### P4: Tool descriptions are the first line of defense

The tool description itself should guide agents toward efficient usage. "Omit for all fields" encourages waste. "Default: key, summary, status, priority, assignee" encourages efficiency. Agents read tool descriptions before the skill file.

### P5: Count before fetch

Always support `count_only: true`. Let agents pre-flight expensive queries. The pattern is: count → decide → fetch.

---

## 5. Before/After Scenarios

### Sprint review: inspect 5 issues

**Current** (517KB, ~129K tokens):
```
jai_schema mode=db           → 62KB
jai_get ROX-1                → 91KB
jai_get ROX-2                → 91KB
jai_get ROX-3                → 91KB
jai_get ROX-4                → 91KB
jai_get ROX-5                → 91KB
```

**After R1-R4** (12KB, ~3K tokens):
```
jai_discover mode=bootstrap  → 2KB
jai_get ROX-1 (defaults)     → 2KB  (15 fields, nulls stripped)
jai_get ROX-2 (defaults)     → 2KB
jai_get ROX-3 (defaults)     → 2KB
jai_get ROX-4 (defaults)     → 2KB
jai_get ROX-5 (defaults)     → 2KB
```

**Optimal** (2KB, ~500 tokens):
```
jai_query "SELECT key, summary, status, priority, assignee
           FROM issues WHERE sprint_name = 'Sprint 42'
           AND status != 'Done'" limit=20  → 2KB
```

**Reduction: 43-258x**

### First-time schema discovery

**Current** (170KB, ~42K tokens):
```
jai_schema mode=db          → 62KB
jai_fields                  → 50KB
jai_schema mode=snippets    → 5KB
jai_schema mode=values col=status → 3KB
```

**After R5+R10** (3KB, ~750 tokens):
```
jai_discover mode=bootstrap → 3KB
  (core columns + snippet names + view names + sync age)
```

**Reduction: 55x**

---

## 6. Implementation Sequence

```
Phase A: Quick wins (S effort, P0 impact)
  ├─ R2: stripNulls() on all responses
  ├─ R3: Exclude raw_json/comments_text/synced_at/id
  ├─ R1: Default field set for jai_get (15 curated fields)
  ├─ R4: Fix API fallback (port issueFieldsToMap)
  └─ R9: Update tool descriptions

Phase B: Schema & query improvements (M effort, P1 impact)
  ├─ R5: Tiered schema discovery
  ├─ R6: JQL support in jai_query
  ├─ R7: Lower default limits
  └─ R8: Snippets name-only default

Phase C: Composability (M-L effort, P2 impact)
  ├─ R10: Merge into jai_discover
  ├─ R11: Collapse write tools to 2
  ├─ R12: Progressive context parameter
  └─ R16: Query-level column selection

Phase D: Polish (S effort, P3 impact)
  ├─ R13: count_only support
  ├─ R14: Batch operations
  ├─ R15: _sync_age in responses
  └─ R17: Resource limits
```

Phase A alone achieves a **43x reduction** in typical usage. Phases A+B achieve **55x+**. The full plan approaches **258x** for common workflows.
