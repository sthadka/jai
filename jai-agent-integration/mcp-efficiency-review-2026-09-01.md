# Review: jai MCP Token Efficiency Appendix — Validation Against Current Code

**Reviewer**: automated code validation
**Date**: 2026-09-01
**Reviewed doc**: `~/idea/acs-planning-service/appendix-jai-mcp-efficiency.md` (summary of `jai-agent-integration/mcp-efficiency-recommendations.md`, dated **2026-08-24**)
**Code validated against**: `~/work/code/jai` @ `main` / **v4.0.0** (latest release tag; releases v3.0.0, v3.1.0, v4.0.0 all landed *after* the source doc was written)

## Verdict

**The appendix is substantially outdated.** It reads as a fix plan to hand to an agent, but the agent already did the work. **All of Phase A (P0) and all of Phase B (P1) have landed** in the ~week between the source doc (2026-08-24) and now (2026-09-01). Both "critical bugs" are fixed. What remains is **Phase C/D composability polish (R10–R17)** — and several of those items are low value or already partially covered by other landed changes.

Net: the doc's *diagnosis* was sound and was acted on. Its *"current waste" numbers and mitigation advice now describe a version of jai that no longer exists.* It should be revised or retired, not handed to an agent as-is.

## Validation matrix

Legend: ✅ done · 🟡 partial · ❌ open

### Bugs

| ID | Claim | Status | Evidence |
|----|-------|--------|----------|
| **B1** | `jai_get` API fallback returns no fields (`{"_source":"api","key":...}` only) | ✅ **FIXED** | `tools_read.go:266` now calls `issueFieldsToMap(issue.Key, issue.Fields)`; helper ported to `helpers.go:130-271`, extracts ~20 fields incl. ADF→markdown description. |
| **B2** | No JQL via MCP; agents fall back to Bash | ✅ **FIXED** | `jai_query` has a `jql` param mutually exclusive with `sql` (`tools_read.go:33-36`, `handleJQLQuery` `:199-240`). Same columnar output as SQL. |

### P0 — critical

| ID | Recommendation | Status | Evidence |
|----|----------------|--------|----------|
| **R1** | Default compact field set for `jai_get` + `fields:"all"` sentinel | ✅ **DONE** | `defaultGetFields` (20 curated fields) `helpers.go:21-26`; `applyResponseFilters` handles `""` / `"all"` / explicit `helpers.go:70-100`. |
| **R2** | `stripNulls()` on responses | ✅ **DONE** | `stripNulls` `helpers.go:30-51`, applied in `applyResponseFilters` and every `jai_schema` response. *(Columnar `jai_query`/`jai_search`/`jai_view` use a compact `OKQuery` shape with no per-row null-key repetition — the null problem doesn't apply there. One exception: gap G1.)* |
| **R3** | Exclude internal columns (`raw_json`, `comments_text`, `synced_at`, `id`) | ✅ **DONE** | `excludedColumns` `helpers.go:12-17`; `filterExcluded`; also skipped in schema listing `tools_schema.go:155` and stripped from the issue resource `resources.go:484`. *(Exception: raw `SELECT *` — gap G1.)* |
| **R4** | Fix `jai_get` API fallback | ✅ **DONE** | Same as B1. |
| **R9** | Tool descriptions that steer toward efficiency | ✅ **DONE** | `jai_get`: "default: key, summary, status… Use 'all' for every column" `tools_read.go:70`; `jai_query`: "Default limit 20"; `jai_schema`: documents tiers. |

### P1 — important

| ID | Recommendation | Status | Evidence |
|----|----------------|--------|----------|
| **R5** | Tiered schema discovery (`core`/`custom`/`all`) + `filter` | ✅ **DONE** | `jai_schema mode=db` defaults `tier=core` (~23 cols), `custom` adds population stats, `all` = full; `filter` param supported `tools_schema.go:95-97,116-237`. |
| **R6** | JQL support in `jai_query` | ✅ **DONE** | Same as B2. |
| **R7** | Lower default limits (`jai_query` 100→20; `values` 200→20) + `truncated`/`total` | 🟡 **MOSTLY** | Limits done: `jai_query` default 20 `tools_read.go:174`; `values` `LIMIT 20` `tools_schema.go:264`. **Missing:** no `truncated`/`total` flag — responses expose only `count` (rows returned), so an agent can't tell it hit the cap. |
| **R8** | Snippets return name + raw only (drop `expanded`) | ✅ **DONE** | `handleSchemaSnippets` returns `{name, raw}` only `tools_schema.go:333-345`. |

### P2 — composability

| ID | Recommendation | Status | Evidence / notes |
|----|----------------|--------|------------------|
| **R10** | Merge `jai_schema`+`jai_fields` into `jai_discover` w/ **`bootstrap`** mode | ❌ **OPEN** | Still two tools (`toolsets.go:11`). A `commands` mode was added to `jai_schema`, but there is **no `bootstrap`** one-call path. First-use is still 3–4 calls (each now small). |
| **R11** | Collapse 7 write tools → 2 | 🟡 **PARTIAL** | All 7 still registered (`toolsets.go:12`), so the ~1000-token tool-listing saving is **not** realized. **But** the composite `jai_update` (set+transition+comment, one call, "3-5x" per its own description, `tools_write.go:289`) and bulk `jai_set` (via `keys` or `query`, `tools_write.go:391-401`) already deliver the *runtime* win R11 was chasing. |
| **R12** | Progressive `context` param (`list`/`detail`/`full`) on `jai_get` | ❌ **OPEN** | Not implemented. Largely superseded by the `fields` param + compact default (an agent gets "detail" for free and can pass explicit fields for "list"). Low marginal value. |
| **R13** | `count_only` on `jai_query` | ❌ **OPEN** | Not implemented. Agent can already `SELECT COUNT(*)`. Low value. |
| **R14** | Batch operations (array of heterogeneous ops) | 🟡 **PARTIAL** | Bulk *same-op* set across many keys ✅; composite multi-action on *one* issue ✅ (`jai_update`). Heterogeneous cross-issue batch array ❌. |
| **R15** | `_sync_age_seconds` in read responses | ❌ **OPEN** | Not present anywhere in `internal/mcp/`. Freshness still needs a separate `jai_status` / `jira://status` call. Low value. |
| **R16** | Query-level column selection (`SELECT a,b,c` not `SELECT *`+filter) | ❌ **OPEN** | `jai_get` still `SELECT * FROM issues WHERE key=?` then filters in memory (`tools_read.go:250`). Tokens are already handled (output is compact); this is a **DB-I/O / raw_json-deserialization** perf item only. Low value. |
| **R17** | Resources match tool limits | 🟡 **PARTIAL** | Issue resource: `raw_json` stripped + null-stripped ✅ (`resources.go:484-485`, and it no longer `SELECT *`s). **Still open:** `jira://query/{sql}` enforces **no LIMIT** (`resources.go:401`, unbounded) and `jira://schema/db` still emits the full column + field-map dump (`resources.go:107-167`, no tiering). |

## Additional gaps found (not in the original doc)

- **G1 — `jai_query "SELECT *"` bypasses all filtering.** `handleSQLQuery` only strips columns when a `fields` param is passed (`tools_read.go:190-193`); it never applies `excludedColumns`/`stripNulls`. So `SELECT * FROM issues` over the MCP still returns `raw_json` (~50KB/row) — the exact bloat R3 targets, via the one path R3 doesn't cover. The tool description doesn't discourage `SELECT *`. This is the single largest *residual* token risk. (Adjacent to R16, but distinct: it's about guarding the SQL path, not `jai_get`.)
- **G2 — query resource is unbounded** (this is R17, still open). `jira://query/{sql}` runs arbitrary SQL with no LIMIT injection — a genuine footgun.
- **G3 — bulk `jai_set` echoes the full `keys` array** (`tools_write.go:579-580`) — the E5 sub-point about hundreds of keys in the response is still true. Minor.
- **G4 — limit inconsistency:** `jira://schema/values/{column}` resource uses `LIMIT 100` (`resources.go:187`) while the equivalent tool uses `LIMIT 20`. Minor.

## Implications for the appendix and the ACS Planning Service

1. **The "Current waste (measured)" table is stale.** It describes pre-v3 behavior. Post-fix, bare `jai_get` returns ~15–20 curated, null-stripped fields (not ~91KB), and `jai_schema(mode='db')` defaults to ~23 core columns (not ~62KB). The 45–258× numbers were real *then*; they no longer characterize v4.0.0.

2. **The mitigation advice softens.** "Avoid bare `jai_get`; constrain the agent to `jai_query` with explicit `fields`" is no longer load-bearing — bare `jai_get` is already compact. `jai_query` with explicit fields remains the leanest path and is still worth encoding as the default, but it's now an *optimization*, not a *workaround for a broken tool*. **The one rule still worth hard-encoding: never `SELECT *` via `jai_query` (gap G1).**

3. **"Track jai's version" was the right instinct.** Record the concrete mapping: **Phase A + Phase B landed by v4.0.0.** The service should assume A+B present for any jai ≥ v4.0.0 and stop mitigating around B1/B2/R1–R8.

4. **Prompt-caching point still holds.** jai read *responses* remain per-feature (uncached) user-turn content, so response size still matters — but the fixed defaults mean the floor is now ~2KB, not ~91KB, per read.

## Next steps

**For the appendix (do first — it's the thing being validated):**
1. Add a status banner: *"Phase A + B landed in jai v4.0.0 (2026-09). This doc's waste table and B1/B2/R1–R9 are historical."*
2. Replace the "Current waste (measured)" table with post-v4 numbers (or mark it "as of 2026-08-24, pre-fix").
3. Rewrite the mitigation to: (a) prefer `jai_query` with explicit `fields`; (b) **never `SELECT *`**; (c) treat `jai_get` defaults as safe.
4. Trim the recommendation list to what's actually open: R7 (`truncated` flag), R10, R11, R14 (heterogeneous batch), R17/G2, plus G1/G3/G4.

**For jai (small, high-value residuals — worth filing as `bd` issues):**
1. **G1 / R3-on-SQL (P1):** apply `excludedColumns` stripping to `jai_query` results even without a `fields` param, OR reject/warn on `SELECT *` in the tool description. Biggest remaining token lever.
2. **G2 / R17 (P1):** inject a default `LIMIT` into `jira://query/{sql}` and apply the same tiering/limits to `jira://schema/db`. Closes the last unbounded path.
3. **R7 tail (P2):** add `truncated: true` + `total: N` to `jai_query`/`jai_search`/`values` when the cap is hit — cheap, removes a real blind spot.
4. **R10 `bootstrap` (P2):** a single `jai_schema mode=bootstrap` (core columns + snippet/view names + sync age) collapses the first-use round-trips. Genuine composability win; moderate effort.
5. **Deprioritize:** R11 (tool-count collapse — cosmetic now that `jai_update`/bulk `jai_set` exist), R12, R13, R15, R16 — low marginal value; document as "won't do unless profiling says otherwise."
