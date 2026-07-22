# jira-cli Assessment

> Assessment of [ankitpokhrel/jira-cli](https://github.com/ankitpokhrel/jira-cli) — a feature-rich,
> interactive Jira CLI written in Go.

## Overview

jira-cli is a comprehensive terminal interface for Jira that covers the vast majority of Jira's
web UI functionality. It takes an **API-first** approach: every command hits the Jira REST API
directly, with no local data layer. It supports Jira Cloud, Server, and Data Center installations.

**Tech stack**: Go, Cobra, Viper (config), tview (TUI), glamour (markdown rendering), survey
(interactive prompts). CGO disabled — ships as a single static binary via GoReleaser + Homebrew.

**Codebase**: ~22,600 lines of Go across a well-structured `internal/` + `pkg/` layout.

---

## Authentication

jira-cli supports three auth methods — more than most Jira CLIs:

| Method | Details |
|--------|---------|
| **Basic Auth** | API Token + email (Cloud) or username + password (Server) |
| **Bearer / PAT** | Personal Access Tokens (Server 8.14+, Data Center) |
| **mTLS** | Mutual TLS with client certificates — rare in CLI tooling |

Credentials can be stored in:
- `JIRA_API_TOKEN` environment variable (highest priority)
- `~/.netrc` file (standard GNU format)
- OS keychain (macOS Keychain, GNOME Keyring, Windows Credential Manager) via `go-keyring`

## Configuration

YAML config at `~/.jira/.config.yml`. Viper-based with env var substitution.

Key configurable items:
- Installation type (cloud vs local/server)
- Default project and board
- Epic field mappings (auto-discovered during `jira init`)
- Issue type handles (critical for non-English Jira instances)
- Custom field mappings (auto-discovered)
- Timezone, default comment count, TLS settings
- Multi-config support via `--config` flag or `JIRA_CONFIG_FILE` env var

The `jira init` wizard auto-discovers custom fields, epic fields, and issue types from the Jira
API metadata — users don't need to manually configure field IDs.

---

## Command Surface

### Issue Management (`jira issue`)

| Command | Description | Notable Details |
|---------|-------------|-----------------|
| `list` / `ls` / `search` | Search and list issues | 20+ filter flags, date ranges, negation with `~` prefix, pagination, interactive TUI by default |
| `create` | Create issue | All standard + custom fields, markdown body, templates from file/stdin, `--web` opens in browser after |
| `edit` | Edit issue | Delta operations for arrays (add/remove with `-` prefix), custom fields |
| `view` | View issue detail | Glamour-rendered markdown, ADF conversion, pager integration |
| `assign` | Assign/unassign | Fuzzy user search, `x` for unassign, `$(jira me)` for self |
| `move` / `transition` | Transition status | Interactive transition picker, comment + resolution + assignee during transition |
| `clone` | Clone issue | Copy all fields, find/replace in summary/description |
| `delete` | Delete issue | `--cascade` for subtasks |
| `comment add` | Add comment | Markdown, templates, stdin pipe, `--internal` for Service Desk |
| `worklog add` | Log work | Jira time format (`2h 30m`), started time, new estimate |
| `link` | Link issues | Interactive link type selection |
| `link remote` | Add web link | URL + title |
| `unlink` | Remove link | Auto-determines link ID |
| `watch` | Add watcher | Fuzzy user search |

### Epic Management (`jira epic`)

| Command | Description |
|---------|-------------|
| `list` | List epics or issues within an epic — explorer view (sidebar + content pane) |
| `create` | Create epic with `--name` flag, auto-sets type |
| `add` | Add up to 50 issues to epic in batch |
| `remove` | Remove up to 50 issues from epic in batch |

### Sprint Management (`jira sprint`)

| Command | Description |
|---------|-------------|
| `list` | List sprints or issues in sprint — `--current`, `--prev`, `--next`, `--state` filters |
| `add` | Add up to 50 issues to sprint in batch |
| `close` | Close sprint, moves incomplete to backlog |

### Other Commands

| Command | Description |
|---------|-------------|
| `project list` | List accessible projects |
| `board list` | List boards (with type filter) |
| `release list` | List project versions/releases |
| `open` | Open issue/project in browser |
| `me` | Print current user (useful in `$(jira me)` substitution) |
| `serverinfo` | Jira server version/build info |
| `init` | Interactive setup wizard with field auto-discovery |
| `completion` | Shell completions (bash, zsh, fish, powershell) |
| `man` | Generate UNIX man pages |

---

## Search and Querying

jira-cli has a sophisticated JQL builder with a fluent Go API:

- **CLI-level filter composition**: All flags (`-t`, `-s`, `-y`, `-a`, `-l`, etc.) are AND-ed into JQL
- **Negation syntax**: Prefix `~` negates any filter (`-s ~Done` = status != Done, `-a ~x` = assigned)
- **Empty/not-empty**: `-a x` = unassigned, `-a ~x` = assigned
- **Date shortcuts**: `--created week`, `--updated -7d`, `--created-after 2024-01-01`
- **Relative time**: `-1h`, `-30m`, `-7d` (maps to Jira date functions)
- **Text search**: Positional arg maps to JQL `text ~ "..."` 
- **Issue history**: `--history` uses `issueHistory()` JQL function
- **Watching**: `-w` for watched issues
- **Raw JQL**: `--jql` for anything the builder can't express
- **Pagination**: `--paginate 10:50` (skip:limit)
- **Sorting**: `--order-by`, `--reverse`

There is **no SQL layer** — all querying goes through Jira's JQL API.

---

## Output Formats

| Format | Flag | Use Case |
|--------|------|----------|
| Interactive TUI | (default) | Human browsing with keyboard nav |
| Plain table | `--plain` | Tab-delimited, parseable |
| CSV | `--csv` | Spreadsheet import |
| Raw JSON | `--raw` | Direct API response for scripting |
| Markdown | (view command) | Glamour-rendered issue detail |

Column customization via `--columns key,summary,status`. Headers can be suppressed with
`--no-headers`. Delimiter is configurable with `--delimiter`.

There is **no structured JSON envelope** (no `{"ok":true,"data":...}` wrapper) — `--raw` returns
the Jira API response verbatim.

---

## TUI Features

### Table View
- Keyboard navigation: arrows, j/k/h/l, g/G, Ctrl-f/b, PgUp/PgDn
- `v`: View selected issue detail (rendered markdown)
- `m`: Transition selected issue (modal with available transitions)
- `Enter`: Open in browser
- `c`: Copy issue URL to clipboard
- `Ctrl+k`: Copy issue key to clipboard
- `Ctrl+r` / `F5`: Refresh list
- `?`: Help overlay

### Explorer View (Epics / Sprints)
- Split-pane: sidebar (hierarchy) + content (issues)
- `w` / `Tab`: Toggle focus between panes
- Same filtering and navigation as table view

### Interactive Prompts (via survey library)
- Text input with validation
- Single/multi select
- Confirm dialogs
- Editor launch for long-form text (respects `JIRA_EDITOR` > `VISUAL` > `EDITOR`)
- Fuzzy search for users, boards, projects

### Clipboard
- Requires `xclip`/`xsel` on Linux; native on macOS/Windows
- Copy URL or issue key from TUI

**Notable absence**: No tab-based multi-view system. No background sync. No configurable views.
The TUI shows one query result at a time.

---

## Markdown / ADF Handling

One of jira-cli's strongest differentiators:

- **Input**: GitHub-flavored markdown converted to Jira Wiki Markup (v2 API) or ADF (v3 API)
  automatically based on installation type
- **Output**: ADF and Jira Wiki Markup both converted back to terminal-rendered markdown via glamour
- **Bidirectional**: Full round-trip for tables, code blocks, lists, mentions, emojis, panels,
  blockquotes, inline cards
- **Custom parsers**: AST-based Jira Wiki parser, ADF translator — not simple regex replacements

---

## Write Operations

All writes hit the Jira API immediately (no local queue):

- **Create**: Full field support including custom fields, templates, markdown conversion
- **Edit**: Delta semantics for arrays (add `-label` to remove, add `label` to add)
- **Transition**: With optional comment, resolution, and assignee change in one call
- **Clone**: Deep copy with field overrides and find/replace
- **Delete**: With `--cascade` for subtasks
- **Comments**: Markdown, templates, stdin, internal (Service Desk)
- **Worklogs**: Jira time format, start time, estimate adjustment
- **Links**: Issue-to-issue and remote web links
- **Watchers**: Fuzzy user search and add
- **Epics**: Batch add/remove up to 50 issues
- **Sprints**: Batch add up to 50 issues, close sprint

---

## Integration Points

| Integration | Details |
|-------------|---------|
| **Browser** | `jira open`, `--web` on create, `Enter` in TUI. Respects `JIRA_BROWSER`/`BROWSER` env vars |
| **Editor** | Opens for long descriptions/comments. `JIRA_EDITOR` > `VISUAL` > `EDITOR` |
| **Pager** | `less` by default, configurable via `PAGER` |
| **Clipboard** | Copy URLs/keys. Platform-native |
| **Shell completion** | Bash, Zsh, Fish, PowerShell |
| **Man pages** | Self-generating via `jira man --generate` |
| **Scripting** | `--plain`, `--raw`, `--csv`, `--no-headers`, `--no-input` for CI/CD |
| **Docker** | `ghcr.io/ankitpokhrel/jira-cli` image |
| **OS Keychain** | macOS Keychain, GNOME Keyring, Windows Credential Manager |
| **`.netrc`** | Standard credential file support |

---

## API Version Handling

jira-cli has a proxy layer (`api/` package) that abstracts Jira API version differences:

| API | Usage |
|-----|-------|
| v3 (`/rest/api/3`) | Jira Cloud — ADF for rich text |
| v2 (`/rest/api/2`) | Jira Server/DC — Wiki markup for rich text |
| v1 (`/rest/agile/1.0`) | Agile endpoints (boards, sprints, epics) — both Cloud and Server |

Version detection uses `/serverInfo` endpoint. The proxy pattern means commands don't need to know
which API version they're talking to.

---

## Architecture Highlights

### Patterns
- **Command pattern** (Cobra): Each command is its own package under `internal/cmd/`
- **Proxy pattern**: `api/` abstracts v2/v3 differences (`ProxyCreate()`, `ProxyGetIssue()`)
- **Builder pattern**: Fluent JQL construction (`jql.NewJQL("PROJ").FilterBy(...).In(...)`)
- **Strategy pattern**: Output rendering swappable between TUI, plain, CSV, JSON
- **Adapter pattern**: Markdown/ADF/Wiki bidirectional conversion

### Code Organization
```
internal/cmd/         — Cobra command implementations (one package per command)
internal/cmdutil/     — Shared utilities (spinners, formatters)
internal/config/      — Config generation and management
internal/query/       — JQL query construction
internal/view/        — Output rendering (table, plain, CSV)
pkg/jira/             — Jira HTTP client with filter support
pkg/jql/              — JQL builder (public, reusable)
pkg/adf/              — ADF parser/translator
pkg/md/               — Markdown conversion (includes Jira Wiki parser)
pkg/tui/              — TUI components and primitives
pkg/netrc/            — .netrc reader
api/                  — API version proxy layer
```

### Design Decisions
- **No local data layer**: Every read hits Jira API
- **No ORM**: Direct HTTP + JSON marshaling
- **CGO disabled**: Static binary, easy cross-compilation
- **tview for TUI**: Rich widgets but heavier than bubbletea
- **Viper for config**: Env vars, multiple sources, hot reload

---

## Salient Points

### Strengths
1. **Comprehensive coverage**: Covers ~90% of common Jira workflows
2. **Jira Server/DC support**: Full parity with Cloud — rare among Jira CLIs
3. **ADF bidirectional conversion**: Custom AST-based parsers for round-trip fidelity
4. **mTLS support**: Enterprise-grade auth option
5. **Smart JQL builder**: `~` negation, date shortcuts, empty operators — genuinely easier than raw JQL
6. **Delta edit semantics**: Add/remove for array fields without replacing
7. **Explorer view**: Split-pane for epics/sprints is better than flat tables
8. **Batch operations**: 50 issues at once for epic/sprint management
9. **Template system**: File/stdin templates for standardized issue creation
10. **Cross-platform**: Linux, macOS, FreeBSD, NetBSD, Windows

### Gaps (relative to jai's approach)
1. **No local data layer**: Every query hits Jira API — no offline reads, no SQL, no aggregations
2. **No structured JSON for agents**: `--raw` returns raw API responses, not agent-optimized envelopes
3. **No schema introspection**: No `jai schema` equivalent for AI agents to discover capabilities
4. **No full-text search on local data**: Depends on Jira's API-based text search
5. **No multi-view TUI**: Shows one query at a time, no tabs or saved views
6. **No background sync**: Data is always fresh but always requires network
7. **No config-driven views**: No equivalent to jai's YAML view definitions
8. **No bulk update by query**: Can batch epic/sprint adds, but can't "update all issues matching X"
9. **Single project context**: Default project baked into config; multi-project requires config switching
10. **No token-efficient output**: Raw API responses are verbose for AI consumption
