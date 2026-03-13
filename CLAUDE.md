# jai

## Development Workflow

- Use **conventional commits** message format (e.g., `feat:`, `fix:`, `chore:`, `refactor:`, `test:`, `docs:`)
- Create a commit after every meaningful change
- Proceed with implementation **autonomously** unless you hit a roadblock or have a question
- Implement as many tasks **in parallel** as feasible
- Track all work in **Beads** (`bd`) — see `.beads/` directory

## Code Navigation: Prefer LSP

When working in any codebase with LSP support (Go, Python, Rust, TypeScript, etc.), always try LSP first before falling back to Grep or Read.

### Reading / Understanding Code

1. **To find a definition** — use `goToDefinition`, not `grep "func MyFunc"` or `grep "class MyClass"`
2. **To find all usages** — use `findReferences`, not `grep "MyFunc"`
3. **To trace call chains** — use `incomingCalls` / `outgoingCalls`, not grep-based manual tracing
4. **To check a type or signature** — use `hover`, not reading the entire file
5. **To list symbols in a file** — use `documentSymbol`, not skimming with Read
6. **To search symbols across the workspace** — use `workspaceSymbol`, not Glob + Grep
7. **To find interface implementations** — use `goToImplementation`, not grepping for method names

### When Grep / Glob / Read Are Still Appropriate

- Initial search when you have no file/line/column coordinates yet
- Searching for non-symbol strings: error messages, config values, comments, TODOs
- Searching across files by content pattern (e.g. "all files importing X")
- Reading documentation, config files, or non-code files
- When LSP is unavailable or returns no results for the given file type

### Workflow

1. Start with Grep/Glob only to locate the first relevant file and symbol
2. Once you have a file + line + column, switch to LSP for all further navigation
3. Use LSP to build the full picture (definitions, references, call hierarchy) before proposing changes

---

## Autonomous Execution

- Work **autonomously** through all Beads tasks until everything is complete or you hit a genuine blocker
- Use `bd ready --json` to find the next actionable task; claim it with `bd update <id> --status in_progress`
- Close each task with `bd close <id> --reason "..." --suggest-next --json` and immediately continue with the suggested next task
- Only stop to ask the user if you hit an external dependency (missing credentials, ambiguous requirement, unresolvable conflict)
- **Parallelize aggressively**: spin up multiple sub-agents (via the Agent tool) for independent tasks in the same phase — e.g. all Phase 1 tasks (config, DB, Jira client, sync engine, query engine) can be implemented simultaneously
- Each sub-agent should: claim its Beads task, implement it, write tests, run them, commit, then close the task
- Coordinate via Beads: check `bd list --status=in_progress --json` before starting a task to avoid duplicate work

## Project Context

**jai** is a Go CLI tool that syncs Jira Cloud data to a local SQLite database and exposes it via SQL queries, a hybrid CLI, and a full-screen TUI.

**Tagline**: "Query Jira with SQL"

**Core loop**: `jai sync` → `jai query` → `jai tui`

**Target users**: AI agents (compact JSON output, schema introspection, 10-50x token savings) and humans (full-screen TUI as Jira Plan replacement)

**Tech stack**:
- Language: Go
- CLI: cobra
- TUI: bubbletea + lipgloss + bubbles
- DB: mattn/go-sqlite3 with CGO + FTS5 (`-tags fts5`)
- Config: YAML with `${ENV_VAR}` substitution
- Auth: Jira Cloud API Token + email (Basic auth)
- Distribution: Homebrew single binary, CGO cross-compiled via `zig cc`

**Key architectural decisions**:
- DB-first: SQLite is the single source of truth; no command hits Jira API directly for reads
- All fields downloaded as raw JSON and denormalized into queryable columns — no resync needed for new fields
- Write operations queue locally in `pending_changes` table, synced to Jira via `jai push` or background goroutine
- Custom field names auto-discovered from Jira's field metadata API and stored in `field_map` table
- FTS5 virtual table (`issues_fts`) with porter unicode61 tokenizer for full-text search
- WAL mode + pragmas for concurrent read/write performance

**Project structure** (per `docs/spec.md`):
```
cmd/jai/main.go
internal/
  cli/        — cobra commands (get, query, search, view, sync, push, set, comment, fields, schema, status, init, tui)
  config/     — YAML loading, env var substitution, view definitions, defaults
  db/         — connection, schema, migrations, upserts, pending_changes, field_map
  jira/       — HTTP client, pagination, ADF→plaintext, write operations
  sync/       — incremental/full sync, denormalization, deletion detection, write queue processor
  query/      — SQL execution, template variable resolution, result formatting
  tui/        — bubbletea app, table, tabs, filter, grouping, hierarchy, detail pane, colors, sync
  output/     — compact JSON (agent mode), lipgloss tables (human mode), schema introspection
```

**Implementation phases** (tracked in Beads — `bd ready --json` for next tasks):
- Phase 1: Foundation — sync, query, get (core data loop)
- Phase 2: Agent Mode — --json, --fields, jai schema, jai fields, auto-sync
- Phase 3: TUI — full-screen views, sorting, filtering, grouping, background sync
- Phase 4: Write Path — jai set, jai comment, jai push, pending_changes
- Phase 5: Polish — jai init wizard, FTS5 search, color rules, default views, error UX
- Phase 6: Release — README, Homebrew, CI/CD, vhs demo

**Output format** (agent mode `--json`):
- Single item: `{"ok":true,"data":{...}}`
- Query results: `{"ok":true,"columns":[...],"rows":[...],"count":N}`
- Error: `{"ok":false,"error":{"type":"...","message":"..."}}`

Reference docs (read before implementing any feature):
- `docs/idea.md` — Polished idea, high level overview
- `docs/spec.md` — Go data models, full API surface, TUI design, sync engine
- `docs/plan.md` — phased task breakdown (source of truth for what to build)
- `docs/research.md` — prior art, language/approach decisions
