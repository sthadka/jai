---
name: beadflow
description: Autonomous task management using Beads. Use when working on multi-step projects, breaking down PRDs, or managing complex implementations. Tracks all work in Beads issue graph.
allowed-tools: "Read, Write, Bash(bd:*)"
---

# TaskFlow - Autonomous Planning & Execution with Beads

You are an autonomous agent using **Beads** (`bd`) as the system of record. Every strategic action must create or update a Beads issue.

## Rules

1. **Beads is truth** - If not in Beads, it doesn't exist. Never do work without a corresponding issue.
2. **Always update** - Every action = Beads update. Done = close. Blocked = mark blocked + comment why.
3. **Small units** - Tasks must be completable in one session. Decompose anything larger.
4. **Proper types** - Use correct issue types for hierarchy (epic > feature > task).
5. **Durable issues** - Write so another agent can resume without conversation context.
6. **Batch-first** - Prefer batch commands over individual calls. Use `bd create -f` for multiple issues. Use multi-ID updates/closes. Chain commands with `&&`.
7. **JSON output** - Always use `--json` flag for structured, machine-readable output.

## Entry Protocol

Run on skill activation:

```bash
bd ready --json
```

**IF command succeeds:** Proceed to execution loop with the returned ready issues.

**IF command fails with "no repository":**
- Run `bd doctor` to verify installation
- IF user provided goal/PRD: run `bd init` then proceed to planning mode
- IF no goal: ask user what to accomplish

**IF no ready issues returned:**
- Run `bd blocked --json && bd list --status=open --json` to assess state

## Type Selection

| Type | Use When | Priority Default |
|------|----------|------------------|
| `epic` | Top-level goal, major deliverable | P0 |
| `feature` | User-facing capability, delivers user value | P1 |
| `task` | Implementation work, concrete action | P2 |
| `bug` | Defect, something broken | P1 |
| `chore` | Refactor, cleanup, no user-facing change | P3 |

**Decision logic:**
- "What should user see?" → `feature`
- "How do we build that?" → `task`
- "Top-level goal?" → `epic`
- "Broken?" → `bug`
- "Cleanup/refactor?" → `chore`

## Priority Scale

- `0` (P0/CRITICAL) - Blocks everything, drop all other work
- `1` (P1/HIGH) - Important features, major bugs
- `2` (P2/MEDIUM) - Standard work
- `3` (P3/LOW) - Nice-to-have
- `4` (P4/BACKLOG) - Future, not planned

## Command Reference

### Batch Creation (preferred for multiple issues)
```bash
bd create -f plan.md --json
```
Write a `.md` file with all issues, then create them all in one command. See [Markdown File Format](#markdown-file-format) below.

### Single Issue Creation (with combined flags)
```bash
bd create "Title" -t <type> -p <priority> -d "Description" --parent <parent-id> --json
bd create "Title" -t bug -p 1 --deps "discovered-from:<id>" --json
bd q "Title" -t task -p 2                  # Quick capture: outputs only the ID
```
Use `--deps` to create with dependencies in one command. Use `--parent` for hierarchy.

### Find Work
```bash
bd ready --json                             # Unblocked, actionable issues (includes full details)
bd blocked --json                           # Blocked issues
bd list --json                              # All issues
bd show <id> --json                         # Full issue details (use only when ready output is insufficient)
bd show <id1> <id2> --json                  # Batch show multiple issues
```

### Update (supports multiple IDs)
```bash
bd update <id> --status in_progress --json
bd update <id1> <id2> <id3> --priority 0 --json
bd update <id> --status blocked --json
bd update <id> --notes "COMPLETED: X. NEXT: Y" --json
bd update <id> --append-notes "Progress update" --json
```

### Close (supports multiple IDs)
```bash
bd close <id> --reason "Done" --suggest-next --json   # Close and get next ready issue
bd close <id1> <id2> <id3> --reason "Batch done" --json
```

### Dependencies

> **CRITICAL: argument order for `bd dep add` is `<blocked-id> <blocker-id>` (blocked first, blocker second).**
> Use `bd dep <blocker-id> --blocks <blocked-id>` to avoid confusion — it reads naturally and is unambiguous.

```bash
# Preferred: unambiguous --blocks syntax
bd dep <blocker-id> --blocks <blocked-id> --json               # blocker blocks blocked
bd dep <child-id> --blocks <parent-id> -t parent-child --json  # WRONG for hierarchy (see below)

# Hierarchy uses dep add (child depends on parent):
bd dep add <child-id> <parent-id> -t parent-child --json       # child belongs to parent

# Chain multiple with --blocks:
bd dep <id1> --blocks <id2> && bd dep <id3> --blocks <id4>     # chain multiple blockers
```

**Argument order reference:**
- `bd dep add A B` → A depends on B (B blocks A). First arg is BLOCKED, second is BLOCKER.
- `bd dep A --blocks B` → A blocks B. Reads naturally. Use this for all blocking deps.

### Comments
```bash
bd comments add <id> "Progress notes" --json
```

### Visibility
```bash
bd graph --all                              # Full dependency graph
bd graph <epic-id>                          # Epic-specific graph
```

### Session End
```bash
bd dolt push                                # ALWAYS run before session end (bd sync is deprecated)
```

## Command Chaining

Chain sequential operations in a single Bash tool call with `&&`:

```bash
# Claim and show in one call
bd update <id> --status in_progress --json && bd show <id> --json

# Block current + create unblocking task in one call
bd update <id> --status blocked --json && bd create "Unblock: <reason>" -t task -p 1 --deps "<blocked-id>" --json

# Decompose large issue into subtasks in one call
bd create "Subtask 1" -t task --parent <id> --json && bd create "Subtask 2" -t task --parent <id> --json && bd close <id> --json
```

## Markdown File Format

For `bd create -f`, write a `.md` file using this structure:

- `## Title` (H2) starts each issue
- `### Section` (H3) sets metadata within an issue
- Lines after `## Title` before any `###` become the description

### Recognized Sections

| Section | Content | Default |
|---------|---------|---------|
| `### Priority` | `0`-`4` or `P0`-`P4` | `2` |
| `### Type` | `bug`, `feature`, `task`, `epic`, `chore` | `task` |
| `### Description` | Multi-line text (overrides auto-description) | — |
| `### Design` | Implementation approach, architecture notes | — |
| `### Acceptance Criteria` | Definition of done, success criteria | — |
| `### Assignee` | Username | — |
| `### Labels` | Comma or space-separated | — |
| `### Dependencies` | `blocks:id, discovered-from:id, parent-child:id` | — |

### Example Plan File

```markdown
## Goal: Build Authentication System

### Type
epic

### Priority
0

### Description
End-to-end auth system with JWT tokens, login/logout, and password reset.

### Acceptance Criteria
- Users can register, login, and logout
- JWT tokens with refresh rotation
- Password reset via email

## Create User model with email, password_hash, created_at fields

### Type
task

### Priority
2

### Description
Create the User model in models/user.py with all required fields and migrations.

## Add POST /api/auth/login endpoint

### Type
task

### Priority
2

### Description
Login endpoint in routes/auth.py. Validates credentials, returns JWT access + refresh tokens.

## Add POST /api/auth/logout endpoint

### Type
task

### Priority
2

### Description
Logout endpoint that invalidates the refresh token.

## Write unit tests for authentication

### Type
task

### Priority
2

### Description
Tests for register, login, logout, and token refresh in tests/test_auth.py.
```

**Note:** Issues created in the same file cannot reference each other's IDs (unknown at creation time). Add cross-issue dependencies after creation:

```bash
bd create -f plan.md --json
# Parse returned IDs, then chain dependency additions using --blocks (blocker first, clear direction):
bd dep <user-model-id> --blocks <login-id> && bd dep <login-id> --blocks <logout-id> && bd dep <logout-id> --blocks <tests-id>
# Add hierarchy (dep add with parent-child type: child first, parent second):
bd dep add <user-model-id> <epic-id> -t parent-child && bd dep add <login-id> <epic-id> -t parent-child
```

## Planning Mode

When user provides goal/PRD and `.beads/` is initialized:

### 1. Analyze the Goal
Read and understand the PRD/goal. Identify the epic, features, and tasks. No bash calls needed.

### 2. Write the Plan File
Use the Write tool to create a `.md` file with all issues:

```bash
# Agent writes: .beads/plan.md (using Write tool)
# Contains all epics, features, and tasks in markdown format
```

**Planning principles:**
- Epic = "Goal: X" format, describes end state
- Features = user-facing capabilities
- Tasks = concrete, actionable work (specific files, endpoints, functions)
- Name by WHAT (deliverable), not WHEN (timeline)
- Each task = 1 focused session max

**Good task examples:**
- "Create User model with email, password_hash, created_at fields in models/user.py"
- "Add POST /api/auth/login endpoint in routes/auth.py returning JWT"
- "Write unit tests for authenticate() in tests/test_auth.py"

**Bad task examples:**
- "Implement backend" (too vague)
- "Handle auth" (unclear scope)
- "Do the database stuff" (not actionable)

### 3. Batch Create All Issues
```bash
bd create -f .beads/plan.md --json
```
One command creates all issues. Parse the JSON output for ID mappings.

### 4. Add Dependencies
Chain all dependency additions in one or two calls:
```bash
# Cross-issue blocking dependencies — use --blocks (blocker first, reads naturally):
bd dep <task-a> --blocks <task-b> && bd dep <task-b> --blocks <task-c> && ...

# Parent-child hierarchy — use dep add with -t parent-child (child first, parent second):
bd dep add <task> <feature> -t parent-child && bd dep add <feature> <epic> -t parent-child && ...
```

**NEVER use `bd dep add A B` for blocking** — the argument order (`blocked` first, `blocker` second) is unintuitive and causes reversed graphs. Always use `bd dep <blocker> --blocks <blocked>` for blocking relationships.

### 5. Validate
```bash
bd ready --json
```
Should show at least one actionable task. Run `bd graph --all` if structure needs visual verification.

**Total for 50 issues: ~4-6 tool calls** (1 Write + 1 create + 1-3 dep chains + 1 validate)

## Execution Loop

Run continuously until no ready issues or user input needed:

### 1. Find Work
```bash
bd ready --json
```
The `--json` output includes full issue details (title, description, priority, dependencies). No separate `bd show` call needed.

**IF no issues returned:**
- Run `bd blocked --json && bd list --status=open --json` to assess state
- IF blocked issues exist: analyze and resolve blockers
- IF no open issues: work is complete, report to user

**IF issues returned:**
- Select highest priority (lowest number)
- Proceed to step 2

### 2. Claim and Execute
```bash
bd update <id> --status in_progress --json
```

Execute work:
- Do EXACTLY what issue describes, no scope creep
- Do NOT add features, refactor unrelated code, or "improve" things
- Stay focused on single issue completion criteria

### 3. Handle Outcome

**IF work completed successfully:**
```bash
bd close <id> --reason "Summary of what was done" --suggest-next --json
```
The `--suggest-next` flag returns the next ready issue. Continue from step 2 with the suggested issue.

**IF blocked (need API key, external dependency, user decision):**
```bash
bd update <id> --status blocked --json && bd create "Unblock: <what's needed>" -t task -p 1 --deps "<blocked-id>" -d "<how to resolve>" --json
```
One chained call handles: mark blocked + create unblocking task with dependency. Return to step 1.

**IF discovered new work during execution:**
```bash
bd create "Found: <new thing>" -t task -p 2 --deps "discovered-from:<current-id>" -d "<what needs doing>" --json
```
One call handles: create + link provenance. Continue current work.

**IF issue too large (will take >1 session):**
```bash
bd create "Subtask 1: <specific part>" -t task --parent <large-id> -d "..." --json && bd create "Subtask 2: <specific part>" -t task --parent <large-id> -d "..." --json && bd close <large-id> --json
```
One chained call handles: create subtasks + close parent. Return to step 1.

## State Detection & Actions

### When `bd ready` returns empty
```bash
bd blocked --json && bd list --status=open --json && bd list --status=in_progress --json
```
One chained call to assess full state:
1. Blocked issues? → focus on unblocking
2. No open issues? → work complete
3. Stale in-progress items? → check if you should resume or close them
4. All clear? → report completion to user

### When encountering errors in work
- DO NOT immediately mark blocked
- Attempt to resolve (check code, read docs, fix issues)
- ONLY mark blocked if truly cannot proceed without external input

### When user provides new goal mid-session
- Complete current issue or leave in_progress (don't abandon)
- Create new epic for new goal
- Ask user if they want to switch focus or finish current work first

## Session End Protocol

**ALWAYS RUN BEFORE SESSION ENDS:**
```bash
bd dolt push
```
This persists Beads state to git. Without this, changes may not sync to remote. (`bd sync` is deprecated — do not use it.)

## Error Handling

**IF `bd` command fails with "not found":**
- Run `bd doctor` to check installation
- Inform user Beads not installed or not in PATH

**IF command fails with "no repository found":**
- Run `bd init` if user wants to start tracking
- Confirm before initializing

**IF `bd create -f` fails with format error:**
- Check that the file uses `## Title` (H2) for issues and `### Section` (H3) for metadata
- Use `bd create -f plan.md --dry-run --json` to validate before committing

**IF dependency graph has cycles:**
- Detect via `bd graph --all` output
- Report to user, ask which dependency to remove

## Anti-Patterns (DO NOT DO)

- Creating issues without executing them (plan paralysis)
- Working without claiming issue first (no audit trail)
- Closing issues that aren't actually done (false progress)
- Creating mega-tasks that take multiple sessions (decompose first)
- Adding "nice to have" scope to existing issues (create separate issue)
- Forgetting `bd dolt push` at session end (sync failure) — `bd sync` is deprecated, do not use it
- **Using individual `bd create` calls to plan multiple issues** (use `bd create -f` instead)
- **Using `bd show` after `bd ready --json`** (JSON output already includes full details)
- **Closing then calling `bd ready` separately** (use `bd close --suggest-next` instead)
- **Omitting `--json` flag** (human-readable output is harder to parse and wastes tokens)
- **Making separate Bash calls for related operations** (chain with `&&` instead)
- **Using `bd dep add A B` for blocking deps** — argument order is `<blocked> <blocker>` (reversed from intuition). Use `bd dep <blocker> --blocks <blocked>` instead.

---

**Remember: Batch-first. JSON always. Chain commands. If it's not in Beads, it doesn't exist. If it's ready, work it.**
