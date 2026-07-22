# jai User Guide

This guide covers day-to-day usage of jai. For installation and initial setup, see the [README](../README.md).

---

## Getting started

```sh
export JAI_TOKEN=your-jira-api-token
jai init                # interactive setup wizard
jai sync                # sync issues from Jira
jai query "SELECT key, summary, status FROM issues LIMIT 10"
```

### Multiple environments

Use `--config` to point to a different config file (e.g., staging vs production):

```sh
jai --config ~/.config/jai/staging.yaml sync
jai --config ~/.config/jai/staging.yaml query "SELECT key, status FROM issues LIMIT 5"
```

The init wizard respects `--config` too — it reads/writes the specified file and auto-derives the database path from the config filename (e.g., `staging.yaml` uses `staging.db`):

```sh
jai init --config ~/.config/jai/staging.yaml
```

---

## Reading data

### Single issue

```sh
jai get ROX-123
jai get ROX-123 --json --fields key,summary,status,labels
```

### SQL queries

```sh
jai query "SELECT key, summary, status FROM issues WHERE assignee_email = '{{me}}'"
```

The `{{me}}` template variable is replaced with the `me:` value from your config.

### Full-text search

```sh
jai search "authentication token expired"
```

### Named views

```sh
jai view my-work
```

Views are defined in your config YAML. See the README for examples.

### Field discovery

```sh
jai fields              # list all fields with Jira IDs and types
jai schema get          # command schema for agents
```

---

## Write operations

All write commands (except `transition` and `link`) queue changes locally. Run `jai push` to sync them to Jira.

### Set a field

```sh
jai set ROX-123 priority High
jai set ROX-123 summary "Updated title"
```

### Array fields (labels, components, fixVersions)

Add or remove individual values without replacing the entire array:

```sh
jai set ROX-123 labels --add backend
jai set ROX-123 labels --add security --add urgent
jai set ROX-123 labels --remove backend
```

Replace all values at once with comma-separated syntax:

```sh
jai set ROX-123 labels "bug,security,backend"
```

Using `--add`/`--remove` on a non-array field produces an error:

```sh
jai set ROX-123 priority --add High
# Error: priority is not an array field
```

### Bulk set

Set the same field on multiple issues at once:

```sh
# Comma-separated keys
jai set ROX-1,ROX-2,ROX-3 priority Major

# SQL query — any query returning a 'key' column
jai set --query "SELECT key FROM issues WHERE type = 'Bug' AND status = 'To Do'" priority Major
```

### Transitions

Transition an issue to a new workflow status. Transitions push to Jira immediately (no `jai push` needed).

```sh
# List available transitions
jai transition ROX-123 --list

# Execute a transition (case-insensitive)
jai transition ROX-123 "In Progress"
jai transition ROX-123 "done"
```

If the transition name doesn't match, jai lists the available options:

```sh
jai transition ROX-123 "NotAStatus"
# Error: unknown transition "NotAStatus" for ROX-123
# Available transitions:
#   - To Do (id: 41)
#   - In Progress (id: 51)
#   - Done (id: 91)
```

### Issue links

Create links between issues. Links push to Jira immediately.

```sh
# Default link type
jai link ROX-1 ROX-2

# Specify link type (case-insensitive)
jai link ROX-1 ROX-2 --type "Blocks"

# List available link types
jai link --list-types
```

If the specified type doesn't exist on your Jira instance, jai lists the available types.

### Comments

```sh
jai comment ROX-123 "Fixed in PR #4892"
```

### Create issues

```sh
jai create ROX --type Bug --summary "Login fails on SSO" --priority High --labels backend,auth
```

### Push

```sh
jai push
```

---

## Sync

```sh
jai sync                # incremental sync
jai sync --full         # full resync with deletion detection
jai sync --changelogs   # sync status transition history
```

Use `--no-sync` on any command to skip the auto-sync that runs before queries:

```sh
jai query "SELECT count(*) FROM issues" --no-sync
```

### Sync status

```sh
jai status
```

---

## Agent mode

Every command supports `--json` for structured output and `--fields` to select columns:

```sh
jai get ROX-123 --json --fields key,summary,status
# {"ok":true,"data":{"key":"ROX-123","summary":"...","status":"In Progress"}}

jai set ROX-1,ROX-2 priority Major --json
# {"ok":true,"data":{"count":2,"keys":["ROX-1","ROX-2"]}}

jai transition ROX-123 --list --json
# {"ok":true,"data":{"issue_key":"ROX-123","transitions":[...]}}

jai link --list-types --json
# {"ok":true,"data":{"link_types":[...]}}
```

Errors are structured:
```json
{"ok":false,"error":{"type":"QueryError","message":"no such column: statuss"}}
```

---

## Global flags

| Flag | Description |
|------|-------------|
| `--json` | Structured JSON output |
| `--fields` | Comma-separated field names to include |
| `--no-sync` | Skip auto-sync before the command |
| `--config` | Path to config file (default: `~/.config/jai/config.yaml`) |
| `--db` | Path to database file |
