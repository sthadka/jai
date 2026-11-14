# Appendix: jai MCP Token Efficiency — Bloat Findings & Fix Plan

> **Status — updated 2026-09-01 (jai v4.0.0).** The fixes proposed here were **implemented**:
> both critical bugs (B1, B2) and **all of Phase A (P0) + Phase B (P1)** landed between the
> source doc (2026-08-24) and v4.0.0. The "current waste" numbers, the Bugs section, and R1–R9
> below are therefore **historical** — they describe pre-v3 behavior. Only Phase C/D
> composability items (R10–R17) remain open, several of them low value. Line-by-line validation:
> `~/work/code/jai/jai-agent-integration/mcp-efficiency-review-2026-09-01.md`.
> Status tags: ✅ done · 🟡 partial · ❌ open.

## Summary

The ACS Planning Service drives Jira through **jai's MCP server** (sidecar; see
[appendix-skill-abstraction.md](appendix-skill-abstraction.md) §3·MCP). As of 2026-08-24 jai's MCP
tools were functional but **token-bloated by default** — a single `jai_get` returned ~91 KB, and a
first-use bootstrap ~170 KB. Because every feature assessment issues several jai reads *inside*
the (cache-warm) agent loop, this bloat directly inflated cost and could crowd the model's context.
This appendix documented the waste and a concrete fix plan handed to an agent to improve jai
(source of truth: `~/work/code/jai/jai-agent-integration/mcp-efficiency-recommendations.md`, dated
2026-08-24). **jai v4.0.0 (2026-09) shipped the plan through Phase B**, so bare `jai_get` is now
compact (~1–2 KB) and the recommended service path is `jai_query` with an explicit field list
(marginally leaner still). The single rule still worth hard-encoding: **never `SELECT *` via
`jai_query`** — that path still returns `raw_json` (see R16 / gap G1 in the review).

## Findings

### Waste measured 2026-08-24 (pre-fix — now resolved)

*All four rows below reflect pre-v3 behavior. In v4.0.0 they land at or near the "Ideal" column:
`jai_get` defaults to ~15–20 curated, null-stripped fields; `jai_schema(mode='db')` defaults to
`tier=core` (~23 columns).*

| Operation | Then (2026-08-24) | Ideal | Waste (then) | Now (v4.0.0) |
|---|---|---|---|---|
| `jai_get` (single issue, no fields) | ~91 KB | ~1–2 KB | **45–90×** | ✅ ~1–2 KB (curated default + stripNulls) |
| `jai_schema(mode='db')` | ~62 KB | ~2–3 KB | **20–30×** | ✅ ~2–3 KB (tier=core default) |
| First-use bootstrap (4 calls) | ~170 KB | ~3 KB | **55×** | 🟡 4 small calls; no single `bootstrap` (R10 open) |
| Sprint review (5 issues) | ~517 KB | ~2 KB | **258×** | ✅ ~2 KB via `jai_query` + fields |

Root causes (then): tools returned **everything** by default; **nulls never stripped** (~60 null
custom fields per issue); **internal columns leaked** (`raw_json` ~50 KB, `comments_text`,
`synced_at`, `id`); **no progressive disclosure**; default limits too high. R1–R3 fixed these for
`jai_get`; the residual is `jai_query "SELECT *"` (G1).

### Bugs

- ✅ **B1 — `jai_get` API fallback returns no fields.** *(FIXED in v4.0.0.)* Previously, when an
  issue wasn't in the local DB, the MCP handler returned only `{"_source":"api","key":...}`. Now
  `tools_read.go:266` calls the ported `issueFieldsToMap()` (`helpers.go:130`), extracting ~20
  fields including ADF→markdown description.
- ✅ **B2 — no JQL via MCP.** *(FIXED in v4.0.0.)* `jai_query` now accepts a `jql` param mutually
  exclusive with `sql` (`tools_read.go:33`, `handleJQLQuery`), so agents no longer fall back to
  Bash for non-synced projects.

### Recommendations (grouped by impact)

**P0 — critical (43–258× reduction) — ✅ ALL DONE in v4.0.0:**
- ✅ **R1** default compact field set for `jai_get` (20 curated fields; `fields:"all"` sentinel) —
  `helpers.go:21`.
- ✅ **R2** `stripNulls()` on `jai_get` + schema responses — `helpers.go:30`.
- ✅ **R3** exclude internal columns (`raw_json`, `comments_text`, `synced_at`, `id`) —
  `helpers.go:12`. *(Not applied to raw `SELECT *` — see G1.)*
- ✅ **R4** fix the `jai_get` API fallback (B1).
- ✅ **R9** tool descriptions rewritten to steer toward efficiency ("Use 'all' for every column").

**P1 — important (5–10× more) — ✅ DONE except the `truncated` flag:**
- ✅ **R5** tiered schema discovery (`core` default / `custom` / `all`) + `filter` param —
  `tools_schema.go:116`.
- ✅ **R6** JQL support in `jai_query` (B2).
- 🟡 **R7** lower default limits (`jai_query` 100→20, `jai_schema(values)` 200→20 done); **still
  missing** `truncated`/`total` flags so agents can tell they hit the cap.
- ✅ **R8** snippets return names + raw only (no `expanded`) — `tools_schema.go:333`.

**P2 — composability (mostly open):**
- ❌ **R10** merge `jai_schema` + `jai_fields` into `jai_discover` with a **`bootstrap`** mode.
  Still two tools; a `commands` mode was added but there is no one-call bootstrap.
- 🟡 **R11** collapse the 7 write tools to 2. Still 7 registered (no tool-listing saving), **but**
  the composite `jai_update` (set+transition+comment) and bulk `jai_set` (via `keys`/`query`)
  already deliver the runtime win. Tool-count collapse is now cosmetic.
- ❌ **R12** progressive `context` param on reads — superseded in practice by the `fields` param.
- ❌ **R13** `count_only` on `jai_query` — agent can `SELECT COUNT(*)`; low value.
- 🟡 **R14** batch operations — bulk same-op set ✅ and composite single-issue update ✅;
  heterogeneous cross-issue batch array ❌.
- ❌ **R15** `_sync_age_seconds` in read responses — not present; low value.
- ❌ **R16** query-level column selection (`SELECT a,b,c` not `SELECT *`+filter). `jai_get` still
  `SELECT *`s then filters in memory — tokens already handled, DB-I/O perf only.
- 🟡 **R17** apply limits/stripping to MCP **resources**. `jira://issue/{key}` strips `raw_json`
  ✅; **still open:** `jira://query/{sql}` has no LIMIT (unbounded) and `jira://schema/db` still
  full-dumps.

### New gaps found during validation (2026-09-01)

- **G1 (highest residual):** `jai_query "SELECT *"` bypasses R2/R3 — it only filters when a
  `fields` param is passed (`tools_read.go:190`), so `SELECT *` still returns `raw_json` (~50 KB/row).
- **G2:** `jira://query/{sql}` resource enforces no LIMIT (unbounded).
- **G3:** bulk `jai_set` response echoes the full `keys` array even for hundreds of keys.
- **G4:** `jira://schema/values/{column}` resource uses `LIMIT 100` vs the tool's `LIMIT 20`.

### Design principles (from jai's own analysis)

1. Default to minimal, opt into verbose. 2. One call per intent (composite tools). 3. Incremental
schema discovery. 4. Tool descriptions are the first line of defense. 5. Count before fetch.

## Implications for Design

- **For the ACS Planning Service (now):** with jai ≥ v4.0.0, assume Phase A+B present and stop
  mitigating around B1/B2/R1–R8. Bare `jai_get` is already compact, so the old "avoid `jai_get`"
  rule is no longer load-bearing. Prefer **`jai_query` with explicit `fields`** as the leanest
  path, and **never `SELECT *`** (gap G1). Pin/track jai's version (the fix boundary is v4.0.0).
- **For the jai-improvement effort:** Phase A+B are done. Remaining high-value residuals: **G1**
  (strip excluded cols on the SQL path), **G2/R17** (LIMIT the query resource; tier the schema
  resource), and **R7's `truncated`/`total` flags**. R10 (`bootstrap`) is the one worthwhile
  composability item; R11/R12/R13/R15/R16 are low value.
- **Interaction with prompt caching:** the service caches the big system prompt across features;
  jai read *responses* are per-feature user-turn content (not cached), so their size is paid every
  feature. Post-v4 the per-read floor is ~2 KB (was ~91 KB), so this is no longer the dominant
  cost lever — but `SELECT *` can still reintroduce it.

## Sources

- `~/work/code/jai/jai-agent-integration/mcp-efficiency-recommendations.md` (2026-08-24) — original
  full analysis, before/after scenarios, and implementation sequence.
- `~/work/code/jai/jai-agent-integration/mcp-efficiency-review-2026-09-01.md` (2026-09-01) —
  line-by-line validation against v4.0.0 (this update's basis).
- `~/work/code/jai/internal/mcp/` — `tools_read.go`, `tools_schema.go`, `resources.go`,
  `helpers.go`, `toolsets.go`, `tools_write.go` (the changed code).
