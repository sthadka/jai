## Goal: jai — Query Jira with SQL

### Type
epic

### Priority
0

### Description
Build jai: a Go CLI tool that syncs Jira Cloud data to a local SQLite database and exposes it via SQL queries, a hybrid CLI, and a full-screen TUI. Targets AI agents (compact JSON output, schema introspection) and humans (interactive TUI, saved views) equally.

Core loop: jai sync → jai query → jai tui.

### Acceptance Criteria
- jai sync downloads Jira Cloud issues to local SQLite DB
- jai query runs arbitrary SQL against the DB (JOINs, aggregations, FTS5)
- jai tui provides full-screen interactive views
- --json flag returns compact structured output for agents
- Distributable via Homebrew as a single binary

---

## Phase 1: Project scaffolding

### Type
task

### Priority
1

### Description
Initialize Go module, directory structure per spec, Makefile with build/test/lint targets, build tag -tags fts5 for mattn/go-sqlite3. Add dependencies: cobra, mattn/go-sqlite3.

Expected structure: cmd/jai/main.go, internal/{cli,config,db,jira,sync,query,tui,output}/, Makefile, go.mod.

## Phase 1: Config loading

### Type
task

### Priority
1

### Description
Implement internal/config/ package: YAML parsing with ${VAR} env var substitution, Config struct with jira/sync/db/fields/views sections, default values, config file path resolution (~/.config/jai/config.yaml), validation of required fields (url, email, token, projects).

## Phase 1: Database layer

### Type
task

### Priority
1

### Description
Implement internal/db/ package: SQLite connection with WAL mode + pragmas (journal_mode=WAL, busy_timeout=30000, foreign_keys=ON, synchronous=NORMAL, cache_size=-64000), schema creation for issues/comments/sync_metadata/field_map/schema_version tables, migration framework (sequential versioned migrations), issue upsert (INSERT OR REPLACE), comment upsert, sync metadata read/write.

Build tag: -tags fts5.

## Phase 1: Jira client

### Type
task

### Priority
1

### Description
Implement internal/jira/ package: HTTP client with Basic auth (base64 email:token), rate limiter (golang.org/x/time/rate, 10 req/s), retry with exponential backoff on 429/5xx (max 3 retries), paginated GET /rest/api/3/search iterator (Go 1.23 iter.Seq2), GET /rest/api/3/field for field discovery, GET /rest/api/3/myself for connection test, Jira API response types, ADF→plaintext converter.

## Phase 1: Sync engine

### Type
task

### Priority
1

### Description
Implement internal/sync/ package: field discovery (fetch field metadata → populate field_map), denormalization (raw JSON → column values per field type), dynamic column creation (ALTER TABLE for new custom fields + backfill from raw_json), incremental sync (JQL updated>=last_sync_time, paginate, batch upsert), full sync (delete + re-fetch in transaction), comment extraction, sync metadata updates.

## Phase 1: Query engine

### Type
task

### Priority
1

### Description
Implement internal/query/ package: SQL execution against DB, template variable resolution ({{me}}, {{team}}, {{today}}, {{week_ago}}), Results struct (columns/rows/count), human table output (columnar format), JSON output envelope ({"ok":true,"columns":[...],"rows":[...],"count":N}).

## Phase 1: CLI commands (sync, query, get)

### Type
task

### Priority
1

### Description
Implement internal/cli/ Phase 1 commands using cobra:
- jai sync: run incremental sync, print progress (N new, M updated)
- jai sync --full: full resync
- jai query <sql>: execute SQL, print table
- jai get <key>: fetch single issue from DB, print all fields

## Phase 1: Tests

### Type
task

### Priority
2

### Description
Write tests for Phase 1:
- DB schema creation and migration tests
- Jira client tests with httptest mock server
- Denormalization tests with fixture JSON (testdata/)
- Sync engine integration test (mock Jira → real SQLite in-memory DB)
- Query execution tests

---

## Phase 2: --json and --fields flags

### Type
task

### Priority
1

### Description
Implement internal/output/ package: compact JSON serializer (no indentation), envelope {"ok":true,"data":{...}} for single items and {"ok":true,"columns":[...],"rows":[...],"count":N} for queries, error envelope {"ok":false,"error":{"type":"...","message":"..."}}. Apply --json to all commands: get, query, search, fields, status.

Implement --fields flag: parse comma-separated field list, filter output columns, validate against field_map with Levenshtein typo suggestions.

## Phase 2: jai schema command

### Type
task

### Priority
2

### Description
Implement internal/cli/schema.go: schema registry where each command registers its parameter schema, output includes command name/params/flags/output_fields (derived from DB field_map). "jai schema" with no args lists all commands. Always outputs JSON (agent-facing command).

## Phase 2: jai fields command

### Type
task

### Priority
2

### Description
Implement internal/cli/fields.go: query field_map table, human output as formatted table (name, jira_id, type, FTS flag), JSON output as array of field objects, --filter flag for pattern matching.

## Phase 2: jai status command

### Type
task

### Priority
2

### Description
Implement internal/cli/status.go: sync metadata per project (issues count, last sync time), pending changes count, DB file size. Human and JSON output modes.

## Phase 2: Auto-sync

### Type
task

### Priority
2

### Description
Implement auto-sync in internal/cli/root.go: before command execution check last_sync_time vs configured interval, if stale run incremental sync (with brief progress message), --no-sync flag to skip. Skip auto-sync for: sync, init, config, schema, fields commands.

## Phase 2: Tests

### Type
task

### Priority
3

### Description
Tests for Phase 2: JSON output formatting, field filtering, --fields flag validation with Levenshtein suggestions, schema output, auto-sync trigger logic.

---

## Phase 3: TUI foundation

### Type
task

### Priority
1

### Description
Implement internal/tui/app.go: root bubbletea model with layout (tab bar, table area, status bar), view loading from config (ViewConfig struct: title/query/columns/group_by/color_rules/status_summary/sort_by/sort_desc), SQL query execution per view, basic table rendering with lipgloss styling.

## Phase 3: Tab bar

### Type
task

### Priority
2

### Description
Implement internal/tui/tabs.go: render view tabs at top, Tab/Shift-Tab navigation, number keys 1-9 for direct jump, active tab highlight with lipgloss.

## Phase 3: Table component

### Type
task

### Priority
1

### Description
Implement internal/tui/table.go: column headers with alignment, scrollable rows (j/k/arrows/Ctrl-d/u/PgUp/PgDn/gg/G/Home/End), column width auto-sizing based on content, row highlighting for current selection, mouse scroll support. Sort by column with s key (toggle asc/desc, show ▲/▼ indicator in header).

## Phase 3: Filter component

### Type
task

### Priority
2

### Description
Implement internal/tui/filter.go: / key opens filter input overlay, in-memory substring match across all visible columns, filter indicator + match count display, Esc to clear filter.

## Phase 3: Grouping

### Type
task

### Priority
2

### Description
Implement internal/tui/grouping.go: group_by config option renders collapsible groups, g key opens column selector to group by, expand/collapse with Enter on group header, group header shows count (e.g. "In Progress (12)").

## Phase 3: Issue detail pane

### Type
task

### Priority
2

### Description
Implement internal/tui/detail.go: Enter on row opens split pane (right or bottom), shows all issue fields + comments, o key opens issue in browser (open/xdg-open), Esc closes pane.

## Phase 3: Status summary bar

### Type
task

### Priority
3

### Description
Implement internal/tui/statusbar.go: when status_summary: true in view config, bottom bar shows counts by status (e.g. "12 To Do | 8 In Progress | 3 Done").

## Phase 3: Hierarchy view

### Type
task

### Priority
3

### Description
Implement internal/tui/hierarchy.go: detect parent_key/epic_key relationships, render as indented tree, expand/collapse children with Enter.

## Phase 3: Background sync

### Type
task

### Priority
1

### Description
Implement internal/tui/sync.go: goroutine runs incremental sync on configured interval, sends SyncMsg via bubbletea program.Send() on completion, TUI refreshes current view query, sync indicator in top-right (spinner during active sync).

## Phase 3: jai view command

### Type
task

### Priority
2

### Description
Implement internal/cli/view.go: execute view's SQL query from config, render as CLI table or JSON, list available views when no name given. Template variable overrides via --version/--team flags.

## Phase 3: Template variables in views

### Type
task

### Priority
2

### Description
Implement internal/query/template.go: resolve {{me}}, {{team}}, {{version}}, {{project}}, {{today}}, {{week_ago}} from config + CLI flag overrides. Used by both jai view (CLI) and TUI.

## Phase 3: Tests

### Type
task

### Priority
3

### Description
Tests for Phase 3: view config parsing, table sorting/filtering logic (unit tests on data not rendering), TUI smoke tests with teatest.

---

## Phase 4: pending_changes table

### Type
task

### Priority
1

### Description
Implement internal/db/pending.go: insert pending change, list pending changes (for status display), mark as synced (update synced_at), update retry_count and last_error.

Operations payload format: set_field {"field":"status","value":"In Progress"}, add_comment {"body":"..."}, transition {"transition_id":"31","transition_name":"Start Progress"}.

## Phase 4: jai set command

### Type
task

### Priority
1

### Description
Implement internal/cli/set.go: validate field exists in field_map, resolve readable name → jira_id, insert into pending_changes, optimistic local update (also update issues table immediately), print confirmation with pending status.

## Phase 4: jai comment command

### Type
task

### Priority
1

### Description
Implement internal/cli/comment.go: insert into pending_changes (operation: add_comment), insert into local comments table immediately, print confirmation.

## Phase 4: Write sync processor

### Type
task

### Priority
1

### Description
Implement internal/sync/writer.go: process pending_changes WHERE synced_at IS NULL ORDER BY created_at, build Jira API requests per operation (set_field → PUT /issue/{key}, add_comment → POST /issue/{key}/comment, transition → POST /issue/{key}/transitions), on success mark synced_at=NOW(), on failure increment retry_count/log error, skip after retry_count > 5.

## Phase 4: jai push command

### Type
task

### Priority
1

### Description
Implement internal/cli/push.go: trigger write sync processor, print results table (succeeded/failed with details). Format: "✓ ROX-123: status → In Progress", "✗ ROX-456: assignee → jane (error: user not found)".

## Phase 4: Jira write client

### Type
task

### Priority
1

### Description
Implement internal/jira/write.go: issue field update (PUT /rest/api/3/issue/{key}), comment creation (POST /rest/api/3/issue/{key}/comment), transition execution (POST /rest/api/3/issue/{key}/transitions), get available transitions (GET /rest/api/3/issue/{key}/transitions).

## Phase 4: TUI quick actions

### Type
task

### Priority
2

### Description
Implement internal/tui/editor.go: e key opens field selector then value input → queue pending change, c key opens text input → queue comment, show pending indicator (⟳) on modified rows until confirmed by sync.

## Phase 4: Tests

### Type
task

### Priority
3

### Description
Tests for Phase 4: pending_changes CRUD, write processor with mock Jira API (httptest), optimistic local update verification, push command output formatting.

---

## Phase 5: jai init wizard

### Type
task

### Priority
2

### Description
Implement internal/cli/init.go: bubbletea interactive wizard steps: welcome → Jira URL/email/token input → connection test → project selection (multi-select) → background sync with progress bar → field discovery display → config file generation (~/.config/jai/config.yaml) → done screen with next steps. Goal: zero to working TUI in under 5 minutes.

## Phase 5: FTS5 search

### Type
task

### Priority
1

### Description
Implement internal/cli/search.go: jai search <text> command using FTS5 MATCH with ranking, configurable FTS fields from search.fts_fields config, comments concatenated into comments_text column for FTS. Uses issues_fts virtual table with porter unicode61 tokenizer.

Also create the FTS5 virtual table and triggers in internal/db/schema.go.

## Phase 5: Color rules

### Type
task

### Priority
2

### Description
Implement internal/tui/colors.go: evaluate color_rules from view config per row, conditions: older_than (parse duration, compare time.Now()), equals, not_equals, contains, in. Apply lipgloss color to matching rows/cells.

## Phase 5: Deletion detection

### Type
task

### Priority
2

### Description
Implement internal/sync/deletions.go: during jai sync --full, fetch total issue count from Jira (GET /search?maxResults=0), if local > remote run full key reconciliation (fetch all keys from Jira paginated, compare with local, mark missing as is_deleted=1/deleted_at=NOW()).

## Phase 5: Changelog sync

### Type
task

### Priority
3

### Description
Implement changelog sync in internal/sync/engine.go and internal/db/changelog.go: when sync.history: true, fetch expand=changelog during sync, store in changelog table. Add example views using changelog data (e.g. "what changed this week", "time in status").

## Phase 5: Default views

### Type
task

### Priority
2

### Description
Implement internal/config/defaults.go: ship starter views generated during jai init based on user's project/team: my-work (assignee={{me}}, status!=Done), team-board (group by status), recent-updates (ORDER BY updated DESC), stale-issues (updated < 28 days), stale-bugs (type=Bug, color rules: red >56d, yellow >28d).

## Phase 5: Human output polish

### Type
task

### Priority
2

### Description
Implement internal/output/table.go: lipgloss-styled tables for all CLI output, consistent column formatting, colored status values (green=Done, yellow=In Progress, red=Blocked), configurable column widths. Apply to get, query, search, fields, status, view commands.

## Phase 5: Error UX

### Type
task

### Priority
2

### Description
Implement Levenshtein-based typo suggestions for unknown field names in QueryError. Helpful error messages with "did you mean X?" for column names. Config validation with actionable error messages for missing required fields. JQL-to-SQL reference in jai help query.

## Phase 5: Tests

### Type
task

### Priority
3

### Description
Tests for Phase 5: init wizard flow, FTS5 search tokenization and ranking, color rule evaluation, deletion detection, default view generation.

---

## Phase 6: README

### Type
task

### Priority
2

### Description
Write README.md: hook "Query Jira with SQL", quick demo (vhs recording), installation (brew install jai), quick start (init→sync→query→tui), compelling SQL examples from idea.md (JOINs/CTEs/window functions), agent usage section (--json/--fields/schema), view configuration, JQL→SQL migration guide, architecture overview, contributing guide.

## Phase 6: Homebrew formula

### Type
task

### Priority
1

### Description
Write .goreleaser.yaml config, create Homebrew tap repository, configure multi-platform builds (darwin-arm64, darwin-amd64, linux-amd64, linux-arm64) with CGO cross-compilation via zig cc.

## Phase 6: CI/CD

### Type
task

### Priority
1

### Description
Set up GitHub Actions: build+test on push, golangci-lint, release workflow (tag → goreleaser → Homebrew tap update). All platforms must pass.

## Phase 6: Terminal recording

### Type
task

### Priority
2

### Description
Create vhs terminal recording showing init→sync→query→tui workflow. Include as demo gif in README.

## Phase 6: License and CHANGELOG

### Type
task

### Priority
3

### Description
Add LICENSE file (MIT), create CHANGELOG.md with initial v0.1.0 release notes.
