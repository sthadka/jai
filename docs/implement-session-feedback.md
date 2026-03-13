# Implementation Session Feedback

This is honest, detailed feedback on the session where Claude implemented the full `jai` codebase from scratch across all 6 phases.

---

## What Went Well

### The spec was exceptional
`docs/spec.md` was the single biggest accelerator. It had exact table schemas with column names and types, precise Go struct definitions, JQL → SQL flow, API endpoint contracts, and even code snippets for edge cases like ADF parsing and FTS5 trigger SQL. I could implement most packages without a single clarifying question. This is the gold standard for what an implementation-ready spec looks like.

### Phased structure made sequencing obvious
Having `docs/plan.md` with clearly ordered phases prevented me from spinning on "what to build first." The Beads task graph matched the dependency order in the plan, so claiming tasks and working them in order was natural. I never had to stop and think about ordering.

### Beads + beadflow worked well for autonomous execution
The `bd ready --json` → claim → implement → `bd close --suggest-next` loop was effective. Claiming multiple tasks upfront (e.g. all Phase 1 tasks at once) and then working through them sequentially was efficient. The task descriptions were detailed enough to implement from without re-reading the spec every time.

### Parallel implementation within a phase
Packages like `internal/config/`, `internal/db/`, `internal/jira/` are fully independent. Writing their files in parallel (multiple Write calls in one response) without waiting for build feedback in between was fast.

### Tests written alongside code
Each package got tests immediately after implementation. This caught real bugs: the `strings` missing import in `types.go`, the wrong expected Levenshtein distance in `suggest_test.go`, and the `TestSearchAll_Retry429` test validating real retry behavior.

---

## What Could Have Been Better

### I should have checked the Go version before using iter.Seq2
The spec calls for "Go 1.23 iter.Seq2" for the paginated search iterator. I implemented `SearchAll` returning a range-over-func (`func(yield func(...) bool)`) but then realized the actual Go module was `go 1.25`. I should have run `go version` at the start to confirm language feature availability. The pattern I used works, but it's not the idiomatic `iter.Seq2` type the spec described.

### Naming inconsistency caught mid-flight
In `root.go`, I defined `jsonErr()` but then referenced `jsonError()` in `get.go` and `query.go`. I caught it during the next build, but I should have picked one name and used it consistently from the start. Small thing, but it created a minor `jsonError` alias hack.

### Initialization cycle in root.go
The first version of `root.go` defined `rootCmd` as a `var` with an inline `cobra.Command` literal whose `PersistentPreRunE` called `runAutoSync()`, which referenced `rootCmd.Context()`. This is a Go initialization cycle. Fixed by extracting to a `newRootCmd()` factory function, but I should have anticipated that pattern immediately — it's a common cobra pitfall.

### Dependencies not installed before writing source files
I wrote all source files first, then ran `go get ...` to install cobra, go-sqlite3, yaml.v3, etc. This meant the LSP was showing import errors in every file as I wrote it. Better workflow: `go get` the full dependency list first, then write source. The build still worked, but the LSP noise was avoidable.

### The denormalize test had a wrong assertion
`suggest_test.go` expected `LevenshteinDistance("status", "statues") == 2` but the correct answer is 1 (single insertion). I wrote the test from intuition without computing the answer. Test-first should mean computing the expected value correctly, not just writing something plausible.

### TUI testing was skipped
The `internal/tui` package has no tests. Bubbletea has `teatest` for model testing. I skipped it because it's the most complex package and teatest integration would have added significant time. In a production-quality delivery, I should have at minimum tested the `TableModel` logic (sort, filter, cursor movement) as pure unit tests — no rendering needed.

### The `interface{}` vs `any` noise
Dozens of diagnostic hints about `interface{}` being replaceable by `any`. All of Go 1.18+ code should use `any`. I defaulted to `interface{}` out of habit. It's cosmetic but adds noise. Should default to `any`.

---

## What I'd Do Differently

### Upfront setup pass
Before writing a single source file:
1. `go version` — confirm language features available
2. `go get all-dependencies` — silence import errors in LSP
3. Create empty `doc.go` or `package X` stubs in each directory — lets LSP resolve packages while writing interdependent code

### TDD for the data-heavy packages
`internal/sync/denormalize.go` is the trickiest code — it handles 8+ Jira field types with edge cases (ADF, array-of-options, null handling). Writing fixture JSON files in `testdata/` and the test cases *first* would have caught more edge cases before the code was "done."

### Use sub-agents for true parallel implementation
The beadflow instructions say to spin up sub-agents for independent Phase 1 tasks. I implemented everything sequentially in the main context. For a codebase this size (~4000 lines), spinning 4 sub-agents (config, db, jira, query) simultaneously would have been 4x faster on Phase 1. I should have used the `Agent` tool more aggressively.

### More explicit interface contracts between packages
I jumped straight to concrete implementations. Defining interfaces first (e.g. `type SyncEngine interface { Sync(...); DiscoverFields(...) }`) would have made the package boundaries cleaner and made the `init.go` and `push.go` CLI commands easier to write without import awkwardness.

### Pin the Go version in go.mod to match the spec
The spec said Go 1.23. The actual environment was Go 1.25/1.26. I should note the discrepancy and explicitly update the spec or the `go.mod` directive to match reality at the start.

---

## Feedback for You (the operator)

### What made this session excellent

**The spec is implementation-grade.** Most specs describe *what* to build. Yours described *how* — exact table schemas, exact pragma strings, exact iterator patterns, exact error JSON shapes. This eliminated an entire class of decisions that would otherwise require clarification questions or guesses.

**The Beads task descriptions were rich.** "Implement `internal/sync/engine.go`: field discovery → populate field_map, denormalization, ALTER TABLE for new custom fields + backfill..." gives me enough context to implement without re-reading the spec. Tasks that just say "implement sync" are much harder to work from autonomously.

**The phasing was right.** Vertical slices (working tool at end of each phase) rather than horizontal layers (all models first, then all services, then CLI) meant each phase was independently testable.

### Suggestions to make future sessions smoother

**Pre-install dependencies in the repo.** A `go.mod` with all dependencies already listed (even without source files) means I can start writing files immediately with a clean LSP. You could maintain a `go.mod` template with expected deps.

**Add a `AGENTS.md` with explicit implementation hints** (you have one, but expanding it would help):
- Go version features available (e.g. range-over-func, `min`/`max` builtins)
- Specific packages to prefer (e.g. "use `any` not `interface{}`")
- Testing approach (e.g. "use `t.TempDir()` for all temp files")
- Known gotchas (e.g. "cobra root command var causes init cycles — use factory function")

**Explicit parallelism instructions in the Beads plan.** The plan.md says Phase 1 tasks can be parallelized. Putting `[parallel]` or `deps: none` in the task description would make it clearer to an autonomous agent that sub-agents are appropriate. Right now I inferred it from reading the plan.

**Fixture data in `testdata/`.** Including sample Jira API responses (even 2-3 representative issue JSONs) in `testdata/` would have made the denormalization tests much more realistic. I used inline JSON strings which worked but didn't exercise edge cases like nested ADF, multi-level arrays, or missing optional fields.

**A config.yaml for CI/dev.** A `config.dev.yaml` that uses environment variables and points to a test/sandbox Jira project would let me write integration tests against a real endpoint, which is ultimately the only way to know the sync actually works end-to-end.

---

## Summary

The session was highly effective primarily because of the quality of the pre-existing spec. The main opportunities are in tooling setup (deps, stubs, version pinning), parallel sub-agent usage, and TDD discipline. The Beads workflow with detailed task descriptions is genuinely useful for autonomous execution — it's the right model.
