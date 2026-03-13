# Research: jai — Agent-First Jira CLI

## Problem Space

Jira is ubiquitous in software development but painful to integrate into automated workflows. The problems fall into two categories:

### For AI Agents
- Jira's REST API is verbose — a single issue response can be 50KB+ of JSON with deeply nested fields, custom field IDs like `customfield_12345`, and redundant metadata
- Existing Jira CLIs (e.g., `go-jira`, `jira-cli`) are human-first: complex flags, unstructured output, no schema introspection
- Agents waste tokens parsing large responses when they only need 3-4 fields
- Multi-step workflows (find issues → filter → analyze → update) require multiple API round trips with no way to compose operations
- JQL is limited: no JOINs, no aggregation, no full-text search with ranking

### For Humans
- Jira's web UI is slow and cluttered for common operations
- Jira Plan is useful but locked behind the web UI — no terminal equivalent
- No good way to create custom, saved views of issues from the terminal
- Context-switching between terminal and browser breaks flow

### Who Has This Problem
- **AI agents and automation systems** that need to read/write Jira programmatically
- **Engineers** who live in the terminal and want fast Jira access
- **Engineering managers** who want custom dashboards and views without Jira's UI overhead
- **DevOps/SRE teams** building automation around issue tracking

### Why It Matters
- Agent-tooling is an emerging category with no established winner for Jira
- The "offline-first with SQL" angle is a genuine differentiator — no existing tool does this well
- Open source Jira tools consistently hit HN frontpage (go-jira, jira-cli had significant traction)

## Prior Art

### Existing Jira CLI Tools

| Tool | Language | Approach | Limitations |
|------|----------|----------|-------------|
| [go-jira](https://github.com/go-jira/jira) | Go | Full API wrapper, template-based output | Archived, complex, human-first |
| [jira-cli](https://github.com/ankitpokhrel/jira-cli) | Go | Interactive TUI + CLI | Large surface area, no agent mode, no offline |
| [jira-terminal](https://github.com/nicktrav/jira-terminal) | Go | Terminal viewer | Read-only, no offline |
| [Official Atlassian CLI](https://developer.atlassian.com/) | Various | REST API wrappers | Verbose, not agent-optimized |

### Key Gaps in Prior Art
- **No agent-first design**: None provide schema introspection, selectable fields, or compact output
- **No offline/SQL mode**: All are online-only, subject to API latency and JQL limitations
- **No TUI plan view**: No terminal equivalent of Jira Plan with grouping, filtering, hierarchy
- **No token optimization**: All return full issue payloads regardless of what's needed

### Reference Implementation: jira-search (Python)
The existing Python project at `~/work/code/jira-search` provides a proven foundation:

**Architecture**: Flask web app + Click CLI + SQLite/FTS5
**What works well**:
- Denormalized SQLite schema with FTS5 for fast full-text search
- Incremental sync via Jira's `updated` field
- Custom ranking algorithm for natural language search
- Environment variable substitution in YAML config
- Session-based bulk operations for efficient sync
- Three search modes: natural language, JQL-to-SQL, regex

**What to improve in jai**:
- Custom field mapping is hardcoded (should be config-driven, powered by DB)
- No exponential backoff on rate limits (just fails)
- No deletion detection during sync
- No concurrency control beyond WAL mode
- Schema evolution has no migration strategy

**Production stats**: ~1GB DB, sub-second search, deployed on Kubernetes with 15-min cron sync

## Technical Landscape

### Go Ecosystem

**CLI Framework**: [cobra](https://github.com/spf13/cobra) — industry standard, used by kubectl, gh, docker
**TUI Framework**: [bubbletea](https://github.com/charmbracelet/bubbletea) — Elm architecture, composable, excellent ecosystem (lipgloss, bubbles, glamour)
**SQLite**: [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) with CGO — mature FTS5 support via build tag, cross-compile with `zig cc`
**HTTP Client**: stdlib `net/http` + structured client wrapper
**Config**: [viper](https://github.com/spf13/viper) or simpler YAML + envsubst
**Output**: stdlib `encoding/json` with custom formatters
**Testing**: stdlib + [httptest](https://pkg.go.dev/net/http/httptest) for API mocks

### Jira REST API
- **Target**: Jira Cloud REST API v3 only (Data Center is deprecated)
- **Auth**: API Token + email (simple, no OAuth complexity)
- **Pagination**: `startAt` + `maxResults`
- **Sync strategy**: Download all fields as raw JSON during sync — prevents resync when needing new fields. Extract and denormalize at storage time.
- **Expand**: `changelog`, `renderedFields`, `transitions` (fetched during sync)

## Resolved Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| SQLite binding | CGO (`mattn/go-sqlite3`) | Battle-tested FTS5, cross-compile with `zig cc` |
| Jira target | Cloud only, API v3 | Data Center deprecated, reduces scope |
| Auth | API Token + email | Simple, sufficient for Cloud |
| Sync granularity | All fields, select at query time | Disk is cheap, avoids resync when config changes |
| TUI architecture | Full-screen (like lazygit) | Jira has lots of content, needs the screen real estate |
| View definitions | YAML config → SQL queries | User-editable, simple implementation |
| Write operations | Queue table in DB → background sync to Jira | Works offline, persists updates, tracks staleness |
| Plugin model | Monolithic for now | Keep simple, revisit later |
| Distribution | Homebrew | Standard for CLI tools |
| Sync daemon | None in v1 | Manual sync or auto-incremental during CLI commands (disableable with flag) |
| Primary query language | SQL (not JQL) | SQL is strictly more powerful; this is the headline feature |

## Key Insights

1. **SQL is the killer feature**: The ability to run arbitrary SQL against Jira data (JOINs, aggregations, window functions, CTEs) is genuinely novel. No existing tool offers this. This is the headline — skip JQL entirely, SQL is the primary query interface.

2. **Field selection is critical for agents**: A typical Jira issue JSON response is 10-50KB. Agents usually need <1KB. Local field selection via SQL `SELECT` = massive token savings. This is a key selling point for the README.

3. **The TUI is the human differentiator**: An interactive Jira Plan replacement in the terminal, powered by instant SQLite queries. This is what gets on HN.

4. **Hybrid CLI**: `jai get ROX-123` for humans, `jai get ROX-123 --json --fields key,summary,status` for agents. Same command, different output modes.

5. **Schema introspection from DB**: Agents can discover available fields, parameters, and output shapes. Easy to derive from DB table schema.

6. **Sync must be invisible**: Auto incremental sync in TUI mode. During CLI commands, run incremental sync opportunistically (disableable with `--no-sync` flag).

7. **Custom field mapping powered by DB**: Config maps `customfield_12345` → `team`. This mapping is stored in DB and flows through CLI flags, SQL column names, JSON output keys, TUI display.
