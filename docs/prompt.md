# idea.md
## Agent-First Jira CLI

### Summary

Build a **minimal, agent-first command line interface for Jira** designed primarily for automation and AI agents and secondarily for interactive human use.

Most existing Jira CLI tools try to mirror the entire Jira API and become large, complex, and difficult to use in automated workflows.

This project instead focuses on:

- predictable commands
- structured inputs
- machine-readable outputs
- minimal surface area
- discoverable schemas for agents
- token efficient responses

The result should be a **small, composable CLI tool that works well inside agent workflows, scripts, and automation systems.**
And additionally support clean, effective commands for human use.

---

# Motivation

Modern development workflows increasingly involve **automation and AI agents** that interact with developer tools.

Traditional CLIs are designed for humans:

- complex flags
- verbose help text
- loosely structured output
- inconsistent formatting

These characteristics make them inefficient for agents to use.

An **agent-first CLI** should instead provide:

- structured inputs
- deterministic outputs
- introspection capabilities
- minimal ambiguity

Jira is widely used but **difficult to integrate cleanly into agent workflows**, making it a strong candidate for an agent-optimized CLI.

We'll still support interactive, useful commands for humans.

---

# Design Principles

## 1. Agent-First Interface

Commands should prioritize machine interaction over human ergonomics.

Design characteristics:

- JSON input
- JSON output
- stable schemas
- minimal formatting

Example:

```

jira-agent command --params '{...}' --output json

```

But additionally support human ergonomics as a secondary goal.

---

## 2. Predictable Output

Every command must support structured output.

Default:

```

--output json

````

Example output:

```json
{
  "success": true,
  "data": {}
}
````

Errors should also be structured:

```json
{
  "error": {
    "type": "IssueNotFound",
    "message": "Issue ABC-123 not found"
  }
}
```

---

## 3. Schema Introspection

The CLI must allow agents to discover how commands work.

Example:

```
jira-agent schema <command>
```

Example output:

```json
{
  "command": "get-issue",
  "parameters": {
    "issue": "string"
  }
}
```

This enables agents to dynamically learn how to call the CLI.

---

## 4. Small Command Surface

Instead of exposing the full Jira API, the CLI should implement a **small set of high-value primitives**.

Examples include:

* retrieving issues
* searching issues
* modifying issue fields
* retrieving metadata
* reading issue history

The goal is to support **automation workflows**, not replicate Jira's entire feature set.

We can iteratively add more primitives as we need.

---

## 5. Composable Commands

Commands should behave like **clean building blocks** that agents can chain together.

Example workflow:

```
get issue
→ analyze fields
→ update metadata
→ query related issues
```

Each command should perform **one clear operation**.

---

# Architecture

## Language

Preferred:

* Go

Reasons:

* easy distribution as a single binary
* strong ecosystem for CLI tools
* good HTTP libraries
* widely used for infrastructure tools

Alternative options may include Rust if stronger type guarantees are desired.

---

## Project Structure

Suggested layout:

```
cmd/
  root.go

internal/

  cli/
    commands.go
    schemas.go

  jira/
    client.go
    issues.go
    search.go

  output/
    formatter.go

  errors/
    errors.go
```

Also built TUI for humans using https://github.com/charmbracelet/bubbletea

---

# Configuration

The CLI should read configuration from environment variables.

Example:

```
JIRA_BASE_URL
JIRA_EMAIL
JIRA_API_TOKEN
```

Authentication should use Jira's REST API token mechanism.

These can also come from .env files.

---

# Core Commands

The CLI should implement a **small initial command set**.

Examples:

### Get Issue

Fetch a Jira issue and return structured metadata.

```
jira-agent get-issue --params '{"issue":"ABC-123"}'
```

I am open to exploring this in more detail to make it more token efficient, and also discuss the data structures

---

### Search Issues

Execute a JQL query.

```
jira-agent search --params '{"jql":"project = ABC"}'
```

---

### Update Issue

Modify fields on a Jira issue.

```
jira-agent update-issue --params '{...}'
```

---

### Get Issue History

Retrieve historical changes to an issue.

```
jira-agent issue-history --params '{"issue":"ABC-123"}'
```

---

### List Available Fields

Allow agents to discover available issue fields.

```
jira-agent list-fields
```

---

# Extensibility

The design should make it easy to add:

* new commands
* new Jira endpoints
* additional schemas

Command schemas should be defined centrally so they can be exposed through the CLI.

---

# Error Handling

Errors should be:

* structured
* deterministic
* machine readable

Example:

```json
{
  "error": {
    "type": "AuthenticationError",
    "message": "Invalid API token"
  }
}
```

---

# Offline mode

SQL is way richer than JQL.

* We can download all the issues from a Jira project or a JQL locally on a SQLite DB with FTS5 enabled on certain text fields.
* This will make it extremely powerful since we'll use SQL to query Jira data instead of JQL, which has many limitations.
* We can support both full fetch or incremental fetch.
* If offline mode is enabled in config, all calls will only look up the local DB, reducing the latency, network calls
* Certain powerful commands can be supported only in the local mode since creating that in JQL wouldn't be possible, e.g. any commands that need SQL JOINs
* We could download all fields and store them as JSON blob, or we can configure all fields and have the code dynamically create columns and update them as we resync
* We have a full implementation of this pattern in ~/work/code/jira-search but written in Python

---

# Testing

Tests should include:

* CLI command tests
* Jira API mock tests
* schema validation tests

Use mocked HTTP servers to simulate Jira responses.

---

# Example Agent Workflow

Example automated workflow:

1. Agent queries issues assigned to a user
2. Agent analyzes metadata
3. Agent updates issue fields
4. Agent retrieves updated state

Example command chain:

```
jira-agent search
jira-agent get-issue
jira-agent update-issue
```

---

# Deliverables

The implementation should produce:

* CLI binary
* README
* command documentation
* schemas for each command
* automated tests
