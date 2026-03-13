# Implementation Plan: jai

## Phasing Strategy

The plan is structured around **vertical slices** — each phase delivers a working, usable tool that builds on the previous phase. No phase produces dead code waiting for a future phase to make it useful.

```
Phase 1: Foundation     → jai sync + jai query + jai get (core loop works)
Phase 2: Agent Mode     → --json, --fields, jai schema, jai fields (agents can use it)
Phase 3: TUI            → jai tui with views, tables, keyboard nav (humans can use it)
Phase 4: Write Path     → jai set, jai comment, jai push, pending_changes (bidirectional)
Phase 5: Polish         → jai init wizard, default views, FTS5 search, color rules, docs
Phase 6: Release        → Homebrew, README, HN launch prep
```

---

## Phase 1: Foundation

**Goal**: `jai sync` downloads issues, `jai query` runs SQL, `jai get` fetches a single issue. The core data loop works end-to-end.

### Tasks

- [ ] **Project scaffolding**
  - Go module init, directory structure per spec
  - Makefile with `build`, `test`, `lint` targets
  - Build tag `-tags fts5` for mattn/go-sqlite3
  - Dependencies: cobra, mattn/go-sqlite3

- [ ] **Config loading** (`internal/config/`)
  - YAML parsing with env var substitution (`${VAR}` syntax)
  - Config struct with jira, sync, db, fields sections
  - Default values, config file path resolution (`~/.config/jai/config.yaml`)
  - Validation: required fields (url, email, token, projects)

- [ ] **Database layer** (`internal/db/`)
  - Connection management with WAL mode and pragmas
  - Schema creation: `issues`, `comments`, `sync_metadata`, `field_map`, `schema_version`
  - Migration framework (version tracking, sequential application)
  - Issue upsert (INSERT OR REPLACE with raw_json + denormalized columns)
  - Comment upsert
  - Sync metadata read/write

- [ ] **Jira client** (`internal/jira/`)
  - HTTP client with Basic auth (email:token)
  - Rate limiter (`golang.org/x/time/rate`, 10 req/s default)
  - Retry with exponential backoff on 429/5xx
  - `GET /rest/api/3/search` with pagination (iterator pattern)
  - `GET /rest/api/3/field` for field discovery
  - `GET /rest/api/3/myself` for connection test
  - Jira API response types
  - ADF → plaintext converter

- [ ] **Sync engine** (`internal/sync/`)
  - Field discovery: fetch field metadata, populate `field_map`
  - Denormalization: raw JSON → column values using field_map
  - Dynamic column creation: ALTER TABLE for new custom fields
  - Incremental sync: JQL `updated >=`, paginated fetch, batch upsert
  - Full sync: delete + re-fetch within transaction
  - Comment extraction and storage
  - Sync metadata updates

- [ ] **Query engine** (`internal/query/`)
  - SQL execution against DB
  - Template variable resolution (`{{me}}`, `{{team}}`, etc.)
  - Result struct (columns, rows, count)
  - Human table output (simple columnar format, no lipgloss yet)

- [ ] **CLI commands** (`internal/cli/`)
  - `jai sync` — run incremental sync, print progress
  - `jai sync --full` — full resync
  - `jai query <sql>` — execute SQL, print table
  - `jai get <key>` — fetch single issue from DB, print details

- [ ] **Tests**
  - DB schema creation and migration tests
  - Jira client tests with httptest mock server
  - Denormalization tests with fixture JSON
  - Sync engine integration test (mock Jira → real SQLite)
  - Query execution tests

### Exit Criteria
- `jai sync` downloads issues from a real Jira Cloud instance to local SQLite
- `jai query "SELECT key, summary, status FROM issues LIMIT 10"` returns results
- `jai get ROX-123` shows issue details
- Tests pass

---

## Phase 2: Agent Mode

**Goal**: Agents can discover jai's capabilities and get compact, structured output. Auto-sync works.

### Tasks

- [ ] **`--json` flag** (`internal/output/`)
  - Compact JSON serializer (no indentation)
  - Envelope: `{"ok":true,"data":{...}}` for single items, `{"ok":true,"columns":[...],"rows":[...],"count":N}` for queries
  - Error envelope: `{"ok":false,"error":{"type":"...","message":"..."}}`
  - Apply to all commands: get, query, search, fields, status

- [ ] **`--fields` flag**
  - Parse comma-separated field list
  - Filter output columns (for `get`: filter JSON keys; for `query`: wrap SQL or filter result columns)
  - Validate field names against field_map, suggest corrections for typos (Levenshtein)

- [ ] **`jai schema <command>`** (`internal/cli/schema.go`, `internal/output/schema.go`)
  - Schema registry: each command registers its parameter schema
  - Output: command name, params, flags, output_fields (from DB field_map)
  - `jai schema` (no args): list all commands with brief descriptions

- [ ] **`jai fields`** (`internal/cli/fields.go`)
  - Query field_map table
  - Human output: formatted table (name, jira_id, type, FTS flag)
  - Agent output: JSON array of field objects
  - `--filter` flag for pattern matching

- [ ] **`jai status`** (`internal/cli/status.go`)
  - Sync metadata per project
  - Pending changes count
  - DB file size
  - Human and JSON output

- [ ] **Auto-sync** (`internal/cli/root.go`)
  - Before command execution: check last_sync_time vs interval
  - If stale: run incremental sync (with brief progress message)
  - `--no-sync` flag to skip
  - Don't auto-sync for `sync`, `init`, `config`, `schema`, `fields` commands

- [ ] **Tests**
  - JSON output formatting tests
  - Field filtering tests
  - Schema output tests
  - Auto-sync trigger logic tests

### Exit Criteria
- `jai get ROX-123 --json --fields key,summary,status` returns compact JSON with only requested fields
- `jai schema get` returns valid parameter schema
- `jai fields` shows all available fields with mappings
- Auto-sync runs when data is stale

---

## Phase 3: TUI

**Goal**: Full-screen interactive TUI with views, tables, sorting, filtering, grouping, and hierarchy.

### Tasks

- [ ] **TUI foundation** (`internal/tui/`)
  - Root bubbletea model with layout (tab bar, table area, status bar)
  - View loading from config
  - SQL query execution per view
  - Basic table rendering with lipgloss styling

- [ ] **Tab bar** (`internal/tui/tabs.go`)
  - Render view tabs at top
  - Tab/Shift-Tab navigation
  - Number keys 1-9 for direct jump
  - Active tab highlight

- [ ] **Table component** (`internal/tui/table.go`)
  - Column headers with alignment
  - Scrollable rows (j/k, arrows, Ctrl-d/u, PgUp/PgDn, gg/G, Home/End)
  - Column width auto-sizing based on content
  - Row highlighting (current selection)
  - Mouse scroll support

- [ ] **Sorting** (`internal/tui/table.go`)
  - `s` key → column selector → sort by selected column
  - Toggle asc/desc
  - Sort indicator in column header (▲/▼)

- [ ] **Filtering** (`internal/tui/filter.go`)
  - `/` key → filter input overlay
  - Filter rows in-memory (substring match across all visible columns)
  - Show filter indicator + match count
  - Esc to clear filter

- [ ] **Grouping** (`internal/tui/grouping.go`)
  - `group_by` config option renders collapsible groups
  - `g` key → column selector → group by selected column
  - Expand/collapse with Enter on group header
  - Group header shows count (e.g., "In Progress (12)")

- [ ] **Issue detail pane** (`internal/tui/detail.go`)
  - Enter on a row opens split view (right pane or bottom pane)
  - Shows all issue fields
  - Shows comments
  - `o` to open in browser (`open` / `xdg-open`)
  - Esc to close detail pane

- [ ] **Status summary bar** (`internal/tui/statusbar.go`)
  - When `status_summary: true` in view config
  - Bottom bar: "12 To Do | 8 In Progress | 3 Done"
  - Counts by `status_category` or `status` field

- [ ] **Hierarchy view** (`internal/tui/hierarchy.go`)
  - Detect parent_key/epic_key relationships
  - Render as indented tree
  - Expand/collapse children

- [ ] **Background sync** (`internal/tui/sync.go`)
  - Goroutine runs incremental sync on interval
  - Sends bubbletea message on completion
  - TUI refreshes current view
  - Sync indicator in top-right (spinning icon during sync)

- [ ] **`jai view <name>`** (`internal/cli/view.go`)
  - Execute view's SQL query
  - Render as CLI table or JSON
  - List available views when no name given

- [ ] **Template variables in views**
  - Resolve `{{me}}`, `{{team}}`, `{{version}}`, `{{today}}`, `{{week_ago}}`
  - CLI flag overrides: `--team`, `--version`

- [ ] **Tests**
  - View config parsing tests
  - Table sorting/filtering logic tests (unit tests on data, not rendering)
  - TUI smoke tests with teatest

### Exit Criteria
- `jai tui` launches full-screen TUI with configured views as tabs
- Views display data from local DB
- Sorting, filtering, grouping work
- Issue detail pane works
- Background sync runs and TUI refreshes

---

## Phase 4: Write Path

**Goal**: Users can modify Jira issues through jai. Changes queue locally and sync to Jira.

### Tasks

- [ ] **pending_changes table** (`internal/db/pending.go`)
  - Insert pending change
  - List pending changes (for status display)
  - Mark as synced
  - Update retry count and error

- [ ] **`jai set <key> <field> <value>`** (`internal/cli/set.go`)
  - Validate field exists in field_map
  - Resolve readable name → jira_id
  - Insert into pending_changes
  - Optimistic local update: also update issues table immediately
  - Print confirmation with pending status

- [ ] **`jai comment <key> <text>`** (`internal/cli/comment.go`)
  - Insert into pending_changes (operation: add_comment)
  - Insert into local comments table
  - Print confirmation

- [ ] **Write sync processor** (`internal/sync/writer.go`)
  - Process pending_changes queue
  - Build Jira API requests per operation type
  - `set_field` → PUT `/rest/api/3/issue/{key}` with fields payload
  - `add_comment` → POST `/rest/api/3/issue/{key}/comment`
  - `transition` → POST `/rest/api/3/issue/{key}/transitions`
  - Handle success (mark synced) and failure (increment retry, log error)

- [ ] **`jai push`** (`internal/cli/push.go`)
  - Trigger write sync processor
  - Print results (succeeded/failed with details)

- [ ] **Jira write client** (`internal/jira/write.go`)
  - Issue field update
  - Comment creation
  - Transition execution
  - Get available transitions for an issue

- [ ] **TUI quick actions** (`internal/tui/editor.go`)
  - `e` key → field selector → value input → queue pending change
  - `c` key → text input → queue comment
  - Show pending indicator (⟳) on modified rows

- [ ] **Auto-push**
  - When auto_sync is on and pending changes exist, push before sync
  - TUI: push as part of background sync cycle

- [ ] **Tests**
  - Pending changes CRUD tests
  - Write processor tests with mock Jira API
  - Optimistic local update tests
  - Push command tests

### Exit Criteria
- `jai set ROX-123 status "In Progress"` queues change and updates local DB
- `jai push` sends queued changes to Jira
- TUI shows pending indicator on modified issues
- Changes confirmed on next sync

---

## Phase 5: Polish

**Goal**: Smooth first-run experience, rich search, visual polish, documentation.

### Tasks

- [ ] **`jai init` wizard** (`internal/cli/init.go`)
  - Bubbletea-based interactive wizard (runs in terminal)
  - Steps: welcome → connection → project selection → sync → field discovery → done
  - Background sync during wizard (progress bar)
  - Config file generation
  - Default view creation based on selected projects

- [ ] **FTS5 search** (`internal/cli/search.go`)
  - `jai search <text>` command
  - FTS5 MATCH query with ranking
  - Configurable FTS fields from `search.fts_fields` config
  - Comments concatenated into `comments_text` column for FTS

- [ ] **Color rules** (`internal/tui/colors.go`)
  - Evaluate color_rules from view config
  - Conditions: `older_than`, `equals`, `not_equals`, `contains`, `in`
  - Apply lipgloss color to matching rows/cells

- [ ] **Deletion detection** (`internal/sync/deletions.go`)
  - Run during `jai sync --full`
  - Key reconciliation: fetch all keys from Jira, compare with local
  - Mark deleted issues

- [ ] **Changelog sync** (`internal/sync/engine.go`, `internal/db/changelog.go`)
  - When `sync.history: true`: fetch expand=changelog during sync
  - Store in changelog table
  - Example views using changelog data

- [ ] **Default views** (`internal/config/defaults.go`)
  - Ship with starter views: my-work, team-board, recent-updates, stale-issues
  - Generated during `jai init` based on user's project/team

- [ ] **Human output polish** (`internal/output/table.go`)
  - lipgloss-styled tables for CLI output
  - Consistent formatting across all commands
  - Colored status values (green for Done, yellow for In Progress, etc.)

- [ ] **JQL-to-SQL reference**
  - Documentation: common JQL patterns → SQL equivalents
  - Include in README and `jai help query`

- [ ] **Error UX**
  - Typo suggestions for field names (Levenshtein distance)
  - Helpful error messages with "did you mean?" and next steps
  - Config validation with actionable error messages

- [ ] **Tests**
  - Init wizard flow tests
  - FTS5 search tests (tokenization, ranking)
  - Color rule evaluation tests
  - Deletion detection tests
  - Default view generation tests

### Exit Criteria
- `jai init` guides user from zero to working setup
- `jai search "memory leak"` returns ranked results
- TUI has color rules working
- Error messages are helpful with suggestions

---

## Phase 6: Release

**Goal**: Open source release ready for public use.

### Tasks

- [ ] **README.md**
  - Hook: "Query Jira with SQL"
  - Quick demo (gif or terminal recording via vhs)
  - Installation (`brew install jai`)
  - Quick start (init → sync → query → tui)
  - SQL examples (the compelling ones from idea.md)
  - Agent usage section (--json, --fields, schema)
  - View configuration
  - JQL → SQL migration guide
  - Architecture overview (one paragraph + diagram)
  - Contributing guide

- [ ] **Homebrew formula**
  - goreleaser config (`.goreleaser.yaml`)
  - Homebrew tap repository
  - Multi-platform builds (darwin-arm64, darwin-amd64, linux-amd64, linux-arm64)
  - CGO cross-compilation with zig cc

- [ ] **CI/CD** (GitHub Actions)
  - Build + test on push
  - Lint (golangci-lint)
  - Release workflow (tag → goreleaser → Homebrew tap update)

- [ ] **Terminal recording**
  - Use [vhs](https://github.com/charmbracelet/vhs) to record demo
  - Show: init → sync → query → tui workflow
  - Include in README as gif

- [ ] **License**
  - Apache 2.0 or MIT (pick one)

- [ ] **CHANGELOG.md**
  - Initial release notes

### Exit Criteria
- `brew install jai` works
- README is compelling with demo gif
- CI passes on all platforms
- Ready for HN/Reddit launch

---

## Dependencies Between Phases

```
Phase 1 (Foundation) ← everything depends on this
    ↓
Phase 2 (Agent Mode) ← depends on Phase 1
    ↓
Phase 3 (TUI) ← depends on Phase 1, Phase 2 (for view command)
    ↓
Phase 4 (Write Path) ← depends on Phase 1, can start in parallel with Phase 3
    ↓
Phase 5 (Polish) ← depends on Phase 1-4
    ↓
Phase 6 (Release) ← depends on Phase 1-5
```

Phase 3 (TUI) and Phase 4 (Write Path) can be developed in parallel after Phase 2.

---

## Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| **CGO cross-compilation** — mattn/go-sqlite3 requires CGO, complicating multi-platform builds | Can't distribute on all platforms | Use zig cc for cross-compilation; test early in Phase 1; fallback: use modernc.org/sqlite |
| **FTS5 trigger complexity** — keeping FTS index in sync with dynamic columns | Stale search results, data corruption | Test thoroughly; use SQLite transactions; keep FTS trigger logic simple and regenerate triggers when schema changes |
| **Jira API rate limits** — large initial syncs may hit rate limits | Slow or failed first sync | Implement proper backoff from Phase 1; show clear progress; allow resumable sync |
| **Dynamic schema** — ALTER TABLE for new custom fields during sync | Schema drift, migration complexity | Keep raw_json as fallback; track schema changes in field_map; test with many custom fields |
| **ADF parsing** — Atlassian Document Format is complex with many node types | Missing content in plaintext conversion | Start with basic node types (paragraph, text, heading); iterate based on real data; raw ADF preserved in raw_json |
| **TUI complexity** — bubbletea is powerful but full-screen apps are complex | Slow TUI development, bugs | Build incrementally: basic table first, then add features one by one; use teatest for regression |
| **Large datasets** — projects with 50K+ issues may strain SQLite | Slow queries, large DB files | Set explicit recommendations on dataset size; use proper indexes; test with large fixtures |
