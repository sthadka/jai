# Query-driven Explore: SQL + JQL → local charts

**Status:** Design / not yet implemented. Target branch: `feat/flow-metrics-dashboard`
(epic jai-2lc). Implement in a future session.

**Scope:** Add a query-driven mode to the live flow-metrics dashboard's *Explore*
surface so a user can paste **a SQL query** or **a JQL filter** (e.g. from an
existing Jira dashboard), have it run **against the local `jai.db`** (never the live
Jira API), and then build a chart by grouping/slicing the returned **list of
issues** — with chart type, group-by, segment and filter controls populated
dynamically from the result columns.

---

## 1. Why we need this

- **Reuse existing Jira dashboards.** Teams already have curated JQL filters that
  encode "the issues that matter" (e.g. untriaged criticals older than 90 days).
  Today that intelligence is trapped in Jira's UI. Letting users paste that JQL and
  build a local chart from it turns every existing saved filter into a dashboard.
- **Local, fast, offline, and richer than Jira.** The local fact tables add what
  Jira Insights cannot: event-time attribution, percentile distributions (p50/p85/
  p95), cohort logic, and arbitrary cross-field slicing — all over a 4 GB DB in
  milliseconds, with no API latency or rate limits.
- **One mental model.** Both SQL and JQL reduce to *"produce a list of issues, then
  group / slice / chart it."* The curated whitelisted dashboards stay the default;
  this is the power-user escape hatch that still obeys the read-only guarantees.
- **No live-API dependency (explicit user requirement).** JQL must be *translated*
  to local SQL, not forwarded to Jira. jai's current JQL path is pure passthrough
  (`internal/jira/client.go` `SearchAll` → `POST /rest/api/3/search/jql`); there is
  **no local JQL parser today**, so the translator is net-new.

---

## 2. Goals / non-goals

**Goals**
- A third Explore tab ("Query") accepting **SQL** (SELECT-only) and **JQL**.
- JQL is translated to SQL over the local DB; the live API is never called.
- The result is always an **issue list**; grouping/slicing/aggregation happens
  client-side over the returned rows (no re-query for shaping).
- Chart-type selector + X / Y(measure) / Series(segment) pickers + filter dropdowns
  **populated dynamically from the result's columns and values**.
- Issue-level drill: a table of the matched issues with Jira `/browse/<key>` links
  (same as the existing drill modal).
- Preserve today's security posture: read-only, SELECT-only, no injection, no
  filesystem/API escape.

**Non-goals (v1)**
- Historical JQL operators `WAS` / `WAS IN` / `CHANGED` / `DURING` / `BEFORE` /
  `AFTER` (these need changelog/interval joins — a later increment; see §8.6).
- JQL functions that need un-synced data: `membersOf()`, `currentUser()` groups,
  `votedIssues()`, saved `filter = "…"`, ScriptRunner `issueFunction`.
- Writing back to Jira; editing issues; anything non-read-only.
- Replacing the curated whitelisted dashboards (they remain the default).

---

## 3. Mental model

```
  SQL (SELECT-only) ─┐
                     ├─▶  SELECT … FROM <issues view> WHERE <predicate>  ──▶ issue rows
  JQL ──translate──-─┘            (local DuckDB over jai.db, no live API)        │
                                                                                 ▼
                           client: introspect columns → pick X / Y / Series / chart
                                   + group-by + aggregation (count/sum/avg)
                                   + filter dropdowns from distinct column values
                                   + issue drill table (Jira links)
```

The server's job shrinks to: *validate → run read-only → return `{columns, rows}`*.
All shaping is client-side JS over the returned JSON, which keeps the grouping/
filtering/segmenting entirely free of any injection surface.

---

## 4. Current architecture (what we build on)

Package `internal/metrics/dashboard/live`:

- **`engine.go`** — one persistent `duckdb` process (JSON mode) with `jai.db`
  ATTACHed `READ_ONLY`. `Engine.Query(sql)` serializes calls, re-binds the active
  generation variable `g` per query, reads to a sentinel marker. This is the
  read-only execution channel we reuse.
- **`query.go`** — `Whitelist` + `parts()` (shared validated FROM/JOIN/WHERE) +
  `Build()` (aggregate) + `BuildIssues()` (per-cell issue drill). Whitelist maps:
  `measures`, `dimensions` (incl. `dimIssueColumn` joining `jai.issues`), `cohorts`,
  `grains`, and the human-label maps surfaced via `/meta`.
- **`server.go`** — routes `/`, `/assets/`, `/meta`, `/q`, `/dashboards`,
  `/issues`. `/meta` emits dropdown vocab + `jira_base_url`. Localhost-only.
- **`assets/index.html`** — vanilla-JS SPA, vendored Apache ECharts + IBM Plex
  Sans (no CDN). Global filter bar, curated dashboards (tiles + charts), and the
  ad-hoc **Explore** tab. Persistent ECharts instance per panel; click → `/issues`
  drill modal with Jira links + a copy-paste `jai query`.
- **`specs.go`** (`internal/metrics/dashboard/specs`) — declarative dashboard specs
  (embedded + `./.jai/dashboards` + `~/.config/jai/dashboards`), hot-reloaded per
  `/dashboards` request. This is where a **"save query as panel"** would plug in.

Other relevant code:
- `internal/jira/client.go` — JQL passthrough to the API (no local parser).
- `internal/db` + sync — denormalizes Jira fields (incl. custom fields) into named
  `issues` columns; `raw_json` retains the full payload.
- `jai query <sql>` (`internal/cli/query.go`) — runs SQL against the local SQLite
  DB; `--jql` forwards to the API. Read-only in practice but not guarded.

---

## 5. Design overview

Add an Explore sub-mode selector: **Curated | SQL | JQL** (keep today's whitelisted
Explore as "Curated"). SQL and JQL share one pipeline:

1. User submits a query (textarea).
2. Server validates + runs it **read-only** over DuckDB, returns `{columns, rows,
   sql, truncated}` (row cap, e.g. 5,000).
3. Client introspects `columns` (name + inferred type: number / date / string),
   populates **Group by (X)**, **Measure (Y)**, **Segment**, **Chart type**, and
   **Filters** (distinct values per string/date column).
4. Client aggregates rows (count / sum / avg / p50… optional) by the chosen
   group-by (+ segment) and renders with ECharts; a table lists the raw issues with
   Jira links where a key column exists.
5. Optional: **Save as dashboard panel** → writes a spec to
   `./.jai/dashboards/<name>.yaml` (needs a `kind: query` panel type; see §9.4).

---

## 6. Component A — SQL mode (SELECT-only, "list issues")

Requirement: *the query may only list issues (SELECT only).*

**Execution**
- Run through the existing DuckDB `Engine` (already `READ_ONLY` ATTACH).
- Harden the engine session once at setup, after the ATTACH:
  - `SET enable_external_access = false;` — blocks local-file reads/writes,
    `COPY … TO`, `httpfs`, and further `ATTACH`. **Verified:** with this set,
    `read_csv_auto('/etc/hosts')` → *"file system operations are disabled"*, while
    `SELECT … FROM jai.issues` still returns rows (ATTACH happened before the lock).
  - `SET lock_configuration = true;` — prevents re-enabling the above from a query.
  - `SET GLOBAL sqlite_all_varchar = true;` — read every SQLite column as VARCHAR so
    rows with empty/odd values (e.g. `created = ''` typed TIMESTAMP) don't throw a
    conversion error on scan. The caller/translator then uses `TRY_CAST` for
    numeric/date predicates. **Verified:** without this, `issues.created` scans as
    TIMESTAMP and `''` rows raise *"invalid timestamp field format"*.

**Validation (SELECT-only guard)**
- Reject anything that isn't a single read statement: must start with `SELECT` or
  `WITH … SELECT`; no `;` (single statement); reject (case-insensitive, token-level)
  `INSERT UPDATE DELETE MERGE CREATE ALTER DROP ATTACH COPY PRAGMA INSTALL LOAD SET
  EXPORT CALL`. The `enable_external_access=false` + `READ_ONLY` attach are the hard
  backstop; the verb check is defense-in-depth and gives a clean error.
- "Only issues": offer a canonical **`issues` view** (or `issue_flow` view joining
  the facts) and either (a) require `FROM` to reference it, or (b) just require the
  result to contain a `key`/`issue_key` column so drill + links work. Recommended:
  (b) — don't over-constrain FROM; the read-only lockdown already bounds risk.

**Dialect note:** DuckDB dialect (not SQLite). Most simple SELECTs are identical;
document the difference and/or offer a "SQL (SQLite)" variant later if users want
exact `jai query` parity. v1 = DuckDB.

---

## 7. Component C — client charting from arbitrary results

(Shared by SQL and JQL; "Component B" is the translator in §8.)

- **Column introspection:** from `columns` + sampling `rows`, classify each column
  number / date / string. Numbers → candidate measures (Y); strings/dates →
  candidate group-by (X) / segment / filters.
- **Controls:** Chart type (bar / line / area / scatter / pie / table), Group-by (X),
  Measure (Y) + aggregation (count / sum / avg / min / max / p50 / p95), Segment
  (optional), and per-column **filter** dropdowns (distinct values, multi-select).
- **Aggregation is client-side** over the returned rows (JS reduce). No re-query for
  grouping/filtering — eliminates any injection surface for the shaping step and is
  instant for ≤5k rows.
- **Issue drill:** if a `key`/`issue_key` column exists, render the row table with
  `jira_base_url + "/browse/" + key` links (reuse `renderIssues`/modal styles).
- Reuse the existing ECharts option builders (`barOption`/`lineOption`/
  `scatterOption`) adapted to take `{x, y, series}` field names instead of the fixed
  `{slice, segment, p50…}` shape.

---

## 8. Component B — JQL → local SQL translator (the core)

No local JQL parser exists today; this is the main new subsystem. Suggested home:
`internal/jira/jql/` (parser + AST) and `internal/metrics/dashboard/live/jqlsql.go`
(AST → DuckDB WHERE compiler), or a self-contained `internal/jql` package.

### 8.1 Pipeline
```
JQL string → lexer → parser (AST) → field/function resolver → WHERE compiler
           → SELECT <issue cols> FROM issues WHERE <where> [ORDER BY …]
```

### 8.2 Grammar (v1 subset)
- Clauses: `field OP value`, combined with `AND` / `OR` / `NOT` and parentheses.
- Operators: `= != > >= < <= ~ !~ IN NOT IN IS EMPTY IS NOT EMPTY WAS*? CHANGED*?`
  (v1 supports all except the history ones marked `*` — see §8.6).
- Values: bare words, quoted strings (`"To Do"`), numbers, lists `(a, b, c)`,
  relative dates (`-90d`, `-2w`, `startOfDay()`, `now()`), and functions.
- Tail: optional `ORDER BY field [ASC|DESC]`.

### 8.3 Operator mapping

| JQL | SQL (DuckDB over `issues`) |
|---|---|
| `f = v` / `f != v` | `col = 'v'` / `COALESCE(col,'') NOT IN ('', 'v')` (JQL `!=` excludes EMPTY) |
| `f IN (a,b)` / `NOT IN` | `col IN ('a','b')` / `col NOT IN (...)` (+ EMPTY handling as above) |
| `f > n` (numeric custom field) | `TRY_CAST(col AS DOUBLE) > n` |
| `f ~ "text"` / `!~` | `col ILIKE '%text%'` / `NOT ILIKE` |
| `f IS EMPTY` / `IS NOT EMPTY` | `(col IS NULL OR col = '')` / negation |
| `created < -90d` | `TRY_CAST(created AS TIMESTAMP) < now() - INTERVAL 90 DAY` |
| `labels IN (x)` / `component = y` | JSON-array contains (labels stored as `["a","b"]`) — `list_contains(CAST(labels AS JSON)::VARCHAR[], 'x')` or `labels LIKE '%"x"%'` |
| `AND/OR/NOT/( )` | boolean tree → nested `AND/OR/NOT ( )` |
| `ORDER BY f` | `ORDER BY col` |

### 8.4 Field resolution (name → column)
- jai denormalizes Jira fields (incl. custom) into named `issues` columns. Build a
  **field-name → column map** from jai's existing field mapping (same source as the
  `jai fields` / MCP `jai_fields` tool) plus a small alias table for JQL's canonical
  names (`issuetype`→`type`, `statusCategory`→`status_category`, `fixVersion`→
  `fix_version`, `PX_Impact_Score`→`px_impact_score`, `Team`→`team`, …).
- Unknown field → **reject with a clear message** listing it (never guess).

### 8.5 Entity-by-ID resolution (name vs GUID)
- JQL often references entities by **ID** (`Team != ec74d716-…-1819`,
  `assignee = 5f3a…`), but the denormalized columns store **display names**
  (`team = "ACS Docs"`). The ID is present in `raw_json`.
- v1 approach: build a one-time **id → name** lookup per entity field by scanning
  `raw_json` (or the fields metadata), so `Team != <id>` compiles to
  `COALESCE(team,'') NOT IN ('', '<name>')`. **Verified:** the example's GUID
  `ec74d716-…-1819` resolves to team name **"ACS Docs"** via `raw_json`.
- If an ID can't be resolved → reject with "could not resolve <field> id <x>
  locally; use the display name".

### 8.6 Function registry
- Each JQL function is either **resolved** or **rejected** (never silently dropped,
  except provably-redundant cases):
  - `standardIssueTypes()` / `subTaskIssueTypes()` — need per-type subtask metadata
    (not synced per issue row). v1: reject, *unless* another `issuetype =`/`IN`
    clause makes it redundant (then it's a no-op). Later: sync issue-type metadata.
  - `now()` / `startOfDay/Week/Month()` / `-Nd|w|M|y` — resolve to SQL date math.
  - `currentUser()` — map to the configured jai user (if available) else reject.
  - `membersOf("group")`, `votedIssues()`, `filter = "saved"` — reject (not synced).
  - `linkedIssues("KEY")` — resolvable later via `issue_links` (defer to v1.1).

### 8.7 History operators (NOT in v1)
`WAS`, `WAS IN`, `CHANGED`, `DURING`, `BEFORE`, `AFTER` need the changelog /
`fact_field_interval` (event-time) — jai *has* this data, so it's a strong future
increment, but it roughly doubles the translator's complexity (temporal joins,
as-of semantics). v1 **rejects** them with "historical operators not supported
locally yet".

### 8.8 Typing rules
- Read columns as VARCHAR (`sqlite_all_varchar`), then:
  - date predicates → `TRY_CAST(col AS TIMESTAMP)`
  - numeric predicates → `TRY_CAST(col AS DOUBLE)`
  - string predicates → direct, with proper single-quote escaping of literals.
- All literals escaped (double single-quotes); values are data, never identifiers —
  same guarantee as `BuildIssues` today.

### 8.9 Worked example (verified end-to-end)

Input JQL:
```
project = ROX and issuetype in standardIssueTypes() and Team != ec74d716-af36-4b3c-950f-f79213d08f71-1819
and issuetype = Bug and (priority in (Critical, Blocker) or PX_Impact_Score >= 10000)
and created < -90d and resolved is EMPTY and status in (New, Backlog, Refinement, "To Do")
```

Compiled SQL (runs read-only over `jai.db`):
```sql
-- session: SET enable_external_access=false; SET GLOBAL sqlite_all_varchar=true;
WITH t AS (  -- Team id → name resolution
  SELECT DISTINCT team AS name FROM jai.issues
  WHERE raw_json LIKE '%ec74d716-af36-4b3c-950f-f79213d08f71-1819%' AND team IS NOT NULL AND team <> ''
)
SELECT key, type, priority, status, team, px_impact_score, created, summary
FROM jai.issues i
WHERE i.project = 'ROX'
  AND i.type = 'Bug'                                            -- issuetype = Bug (standardIssueTypes() redundant → dropped)
  AND COALESCE(i.team,'') NOT IN ('', (SELECT name FROM t))     -- Team != <id>  (JQL != excludes EMPTY)
  AND ( i.priority IN ('Critical','Blocker') OR TRY_CAST(i.px_impact_score AS DOUBLE) >= 10000 )
  AND TRY_CAST(i.created AS TIMESTAMP) < (CURRENT_TIMESTAMP - INTERVAL 90 DAY)
  AND (i.resolved IS NULL OR i.resolved = '')                   -- resolved is EMPTY
  AND i.status IN ('New','Backlog','Refinement','To Do');
```
**Result (verified):** 8 issues (all via the `Critical/Blocker` branch; 0 via
`px_impact_score`). Every "hard" category — a function, a team-by-GUID, a custom
numeric field, a relative date, `IS EMPTY`, nested `OR` — resolved locally.

---

## 9. API / endpoints

### 9.1 `POST /explore/sql`  (read-only SQL)
Body: `{ "sql": "SELECT …" }` → `{ "columns": [{name,type}], "rows": [...],
"truncated": bool, "sql": "<echoed>" }`. 400 on non-SELECT / disallowed verb.

### 9.2 `POST /explore/jql`  (translate then run)
Body: `{ "jql": "project = ROX AND …" }` → same response shape, plus
`{ "compiled_sql": "…" }`. 400 on parse error / unknown field / unsupported
function, with a precise message (offset + token).

### 9.3 CSRF / method
Free-form input makes CSRF matter (a web page can POST to localhost). Use `POST` +
require a same-origin token minted into the page (`/meta` or index) and checked on
`/explore/*`. Keep localhost-bind. (Document that the whitelisted `/q`/`/issues`
already resist injection; the new endpoints need the token because they run
free-form SQL/JQL.)

### 9.4 Save-as-panel (optional v1.x)
Extend `specs.Panel` with `kind: query` + `{ source: "sql"|"jql", query: "...",
groupBy, measure, agg, segment, chart }`. The SPA's "Save" writes
`./.jai/dashboards/<id>.yaml`; the existing loader picks it up with no restart.
Rendering a `query` panel runs `/explore/*` then the client aggregation.

---

## 10. Security model (summary)
1. **Read-only engine:** `jai.db` ATTACHed `READ_ONLY`; `SET
   enable_external_access=false` + `lock_configuration=true` after setup (blocks
   file/http/attach escapes — verified).
2. **SELECT-only guard:** single statement, `SELECT`/`WITH` only, disallowed-verb
   list; defense-in-depth over the lockdown.
3. **JQL compiles to parameter-safe SQL:** literals escaped, values never become
   identifiers; unknown fields/functions rejected, not guessed.
4. **CSRF token** on `/explore/*` (free-form input).
5. **Row cap** (e.g. 5,000) + statement timeout (reuse `queryTimeout`).
6. Client-side shaping (group/slice/filter) touches only returned JSON.

---

## 11. Data model reference (verified)
- `issues` has denormalized columns incl. `key, project, type, status,
  status_category, priority, assignee, reporter, created, updated, resolved,
  labels, components, fix_version, parent_key, epic_key, story_points, team,
  px_impact_score, …` plus `raw_json` (full payload, carries entity IDs).
- `labels` / `components` are JSON string arrays: `["4.10.0","4.9.0"]`,
  `["Vuln Management"]`.
- `team` stores display names ("ACS UI", "ACS Docs"); entity **IDs** live in
  `raw_json`.
- Event-time facts for history ops (future): `fact_field_interval`
  (status/priority/assignee/Rank), `fact_issue_membership` (component/label/sprint),
  `fact_issue_flow`, `changelog`.
- DuckDB sqlite scanner types columns (e.g. `created`→TIMESTAMP); empty strings
  break scans → use `sqlite_all_varchar` + `TRY_CAST`.

---

## 12. Implementation plan (phased)

**Phase 1 — SQL mode (smallest, highest leverage)**
- Engine lockdown (`enable_external_access=false`, `lock_configuration=true`,
  `sqlite_all_varchar=true`) in `engine.go setup()`; verify `/q` still green.
- SELECT-only validator + `POST /explore/sql` + CSRF token.
- Client: Query tab, textarea, run, `{columns,rows}` introspection, X/Y/Series +
  chart-type pickers, client aggregation + filters, issue table w/ Jira links.
- Tests: validator rejects non-SELECT/verbs; lockdown blocks file read; a sample
  SELECT returns columns+rows; client aggregation unit (if extracted).

**Phase 2 — JQL translator (snapshot subset)**
- `internal/jql` lexer + parser + AST (operators, IN, functions, dates, bool, parens,
  ORDER BY).
- Field resolver (name→column map from jai field mappings + alias table).
- Entity id→name resolver (raw_json lookup, cached).
- Function registry (resolve dates/now; reject unsupported with message; drop
  provably-redundant `standardIssueTypes()`).
- WHERE compiler → `SELECT <issue cols> FROM issues WHERE …`; `POST /explore/jql`
  returning `compiled_sql` + results.
- SPA: JQL sub-tab; show compiled SQL (read-only disclosure) for transparency.
- Tests: the §8.9 example → expected SQL + non-zero rows; operator table cases;
  `!=`/EMPTY semantics; unknown field/function → 400; id→name resolution.

**Phase 3 — polish / optional**
- Save-as-panel (`kind: query` spec) + loader rendering.
- "SQL (SQLite dialect)" variant for exact `jai query` parity (if requested).
- CSV export of the issue list.

**Phase 4 — later increment (separate)**
- History operators (`WAS`/`CHANGED`/`DURING`) via `changelog` /
  `fact_field_interval` temporal joins.
- `linkedIssues()` via `issue_links`; issue-type metadata for `standardIssueTypes()`.

---

## 13. Testing strategy
- **Translator unit tests** (pure Go, no DB): JQL → expected SQL string for the
  operator table, the §8.9 example, EMPTY/`!=` semantics, precedence, rejects.
- **Engine security tests:** lockdown blocks `read_csv`/`COPY`/`ATTACH`; SELECT-only
  guard rejects each disallowed verb; row cap + timeout honored.
- **Integration (duckdb present):** compiled SQL for the example returns the
  expected issue set against a fixture; `/explore/jql` round-trip.
- **Browser smoke:** paste SQL and JQL, confirm columns populate pickers, chart
  renders, filters work, issue table links resolve.

---

## 14. Open questions / decisions
- SQL dialect: DuckDB only in v1, or also a SQLite ("`jai query` parity") mode?
- Constrain `FROM` to an `issues` view, or just require a key column? (Recommend the
  latter + read-only lockdown.)
- Field map source of truth: reuse the exact mapping behind `jai fields` — confirm
  it covers custom fields by display name and `cf[id]` form.
- Entity id→name: scan `raw_json` on demand vs. build a persisted lookup table at
  sync time (faster, but schema change).
- How faithfully to mirror JQL EMPTY/`!=`/null quirks vs. a simpler, documented
  local semantics.

---

## 15. Effort estimate
- Phase 1 (SQL mode + client): ~0.5–1 day.
- Phase 2 (JQL snapshot-subset translator + endpoint + UI): ~1–1.5 days.
- Phase 3 (save-as-panel, export): ~0.5 day.
- Phase 4 (history ops): separate, ~1–2 days.

**Recommended first cut:** Phase 1 + Phase 2 (snapshot subset; reject history ops &
unsupported functions with clear messages). That already turns pasted Jira JQL
filters — like the verified §8.9 example — into local, chartable dashboards.

---

## 16. CLI integration — `jai query` accepts SQL **or** JQL (auto-detect)

Extend the existing `jai query` command so the same translator powers the CLI, not
just the dashboard. This unifies both surfaces on one code path and lets users run
their existing Jira JQL filters **offline** from the terminal.

### 16.1 Behavior
| Invocation | Today | Proposed |
|---|---|---|
| `jai query "SELECT … FROM issues WHERE …"` | local SQL (SQLite) | unchanged |
| `jai query --jql "project = ROX AND …"` | **live** Jira API | unchanged (kept for back-compat / real-time) |
| `jai query "project = ROX AND …"` (bare, non-SQL) | errors (invalid SQL) | **NEW**: auto-detected as JQL → translated to local SQL → runs on `jai.db` |
| `jai query --sql "…"` / `jai query --local-jql "…"` | — | optional explicit overrides for the detector |

Back-compat is preserved: valid prior usage was always SQL (or `--jql` live), so
auto-detecting *bare non-SQL* input as local JQL is purely additive.

### 16.2 Detection
- Leading token (case-insensitive) is `SELECT` or `WITH` → **SQL**.
- Otherwise → **JQL** (`field OP value` grammar; JQL never contains `SELECT`/`FROM`).
- Edge cases (`EXPLAIN`, `PRAGMA`): rare for this tool; the explicit `--sql` flag
  disambiguates. `--jql` always means **live**; add `--local-jql` if an explicit
  local-translate flag is wanted.

### 16.3 Shared translator, two dialect emitters (key architecture point)
`jai query` runs on **SQLite** today (no `duckdb` dependency); the dashboard uses
**DuckDB**. To keep the CLI dependency-free *and* keep both surfaces consistent,
build the translator as a standalone, dialect-agnostic package consumed by both:

```
internal/jql/           lexer + parser + AST + field/id/function resolution  (dialect-agnostic)
  emit_sqlite.go        AST → SQLite WHERE  (used by `jai query`)
  emit_duckdb.go        AST → DuckDB WHERE  (used by the dashboard /explore + /q path)
```

Only the leaf emitters differ, in a handful of spellings:

| Concern | SQLite emit | DuckDB emit |
|---|---|---|
| JSON array contains (`labels`/`components`) | `EXISTS (SELECT 1 FROM json_each(labels) WHERE value = 'x')` | `list_contains(CAST(labels AS JSON)::VARCHAR[], 'x')` |
| Relative date `-90d` | `created < datetime('now','-90 day')` | `TRY_CAST(created AS TIMESTAMP) < now() - INTERVAL 90 DAY` |
| Numeric cast | `CAST(x AS REAL)` (SQLite is lax) | `TRY_CAST(x AS DOUBLE)` |
| Empty/`IS EMPTY` | `(col IS NULL OR col = '')` | same |

Issue-list filtering needs **no percentiles**, so SQLite is fully sufficient for the
CLI path; DuckDB stays for the dashboard's analytical aggregates.

### 16.4 Output & drill
Reuse `jai query`'s existing `--format` (table/json/csv/tsv/markdown) and `--fields`.
The translated JQL emits `SELECT <issue cols> FROM issues WHERE …`, so results flow
through the current formatter unchanged. (Nice combo: `jai query "<jql>" --json` →
pipe to anything.)

### 16.5 Local vs live semantics (document loudly)
- Bare JQL / `--local-jql` = **local, as of last sync**, synced projects/fields only;
  supports the v1 snapshot subset (§8.2) and rejects history ops/unsupported
  functions with a clear message.
- `--jql` = **live Jira**, full JQL, real-time — may return different results than the
  local translation. Keep both; make the difference explicit in `--help` and output.

### 16.6 Plan delta
- Fold the translator into Phase 2 as the shared **`internal/jql`** package with the
  SQLite emitter; wire `jai query` detection + `--sql`/`--local-jql` flags there.
- The dashboard's DuckDB emitter (Phase 2) is the second consumer of the same AST.
- Add CLI tests: detection (SQL vs JQL), `--jql` still live, bare JQL → local SQL →
  rows, `--format` passthrough, unsupported-construct error messages.
- Incremental effort on top of Phase 2: ~0.25–0.5 day (mostly the SQLite emitter +
  CLI flag wiring + tests; parser/resolver already built for the dashboard).
