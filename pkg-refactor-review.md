# Review: `refactor-pkg-engine` vs `jai-pkg-refactor-prompt.md`

**Verdict: accurate on the core goals, with 4 deviations worth a decision and 1 repo-hygiene issue.**

Scope reviewed: 13 commits on `refactor-pkg-engine` (`origin/main..HEAD`), spec at `~/personal/idea/lyra/jai-pkg-refactor-prompt.md`.

---

## Objective-by-objective assessment

### 1. Move engine packages `internal/` → `pkg/` — ✅ (with deviations)
- `internal/{jql,jira,db,sync,metrics,mcp,query,output,fieldwrap,config}` → `pkg/{jql,jira,store,sync,metrics,mcp,query,output,fieldwrap,config}`, plus new `pkg/clock`, `pkg/core`. `db` renamed to `store` as specified. `denormalize.go` landed in `pkg/sync` (it was never a standalone pure-transforms package; acceptable).
- Nothing under `pkg/` imports anything under `internal/` (verified by grep; commit `6bc4510` claims "pkg is now internal-free" and it holds).
- No stale `github.com/sthadka/jai/pkg` (non-`/v5`) import paths anywhere.
- **Deviations:**
  - **Dashboard moved INTO `pkg/`** (`pkg/metrics/dashboard/live`, with embedded ECharts assets and an HTTP server). The spec explicitly says "Keep cobra commands, the TUI, and the dashboard under `internal/`/`cmd/`". Arguably it's embeddable-value (a GUI could reuse the live server), but it is not what the prompt asked for.
  - **`output` moved to `pkg/output`** (CSV/JSON/table/export formatting). Objective 6 says output formatting stays "strictly in the CLI/TUI layer". Nothing inside `pkg/` imports it, so the move buys nothing for embeddability and contradicts the letter of the spec.

### 2. Remove `var g globals` — ✅
- Gone. `internal/cli/root.go` now builds a `CLI` struct holding a `core.Engine`, bound to the cobra command context (`bindRoot`). No engine package reaches for process globals. Remaining `var` declarations in `internal/cli/*` are per-command cobra/flag definitions (CLI layer, fine).
- `pkg/core.Engine` is the small assembled struct the spec asked for: `Config`, `Store`, `Client`, `Query`, `Sync`, `Metrics` + `New(cfg, store, client)` + `Close()`.

### 3. Interfaces at the seams — ✅ (one gap)
- `store.Store` (`pkg/store/store.go`, `var _ Store = (*DB)(nil)`), `jira.JiraClient` (`pkg/jira/iface.go`), `clock.Clock` (`pkg/clock`, `System`/`Fixed`), `jira.RateLimiter` — all interfaces, all consumed by engine packages (`sync.New(db.Store, jira.JiraClient, cfg)`).
- **Gap: `RateLimiter` is not injectable.** The interface exists and `Client.limiter` is typed as it, but there is no public constructor/setter to supply a custom limiter — `New`/`NewWithClient` always build a fresh `*rate.Limiter` from `ratePerSec`. The doc comment on `RateLimiter` even claims embedders may supply one "via NewWithClient's options", but `NewWithClient` has no such option. Spec: "consumers can substitute/mock: … `RateLimiter`". Not currently substitutable.
- Design note: `Store` is a 60+ method interface including promoted raw `*sql.DB` methods (`Query`/`Exec`/`Begin`). Mocking it is heavy. Spec didn't constrain size, but as a "stable, importable API" this is a wide commitment.

### 4. Pluggable Jira auth — ✅
- `BasicAuthTransport` (an `http.RoundTripper`) is the default; `NewWithClient(baseURL, *http.Client, rate)` is the injection seam for e.g. an OAuth token source. No OAuth implemented, as specified. Covered by `pkg/jira/auth_test.go` (`TestNew_SendsBasicAuth`, `TestNewWithClient_UsesCustomTransport`, httptest-based). Auth behavior (401/403 → `*AuthError`, fail-loud `VerifyAuth`, CLI exit on auth failure before auto-sync) preserved in `internal/cli/root.go`.

### 5. Config split — ✅ (minor purity note)
- `pkg/config` is the pure policy struct (incl. `MetricsConfig` + `CompilePolicy`); YAML file reading + `${ENV}` expansion + path resolution live in `internal/cli/configloader`. Engine packages accept `*config.Config`.
- Minor: `pkg/config` still contains `DefaultConfigPath`/`DefaultDBPath` (`os.UserHomeDir`), a small env-dependent convenience in the "pure" package. Harmless, but not strictly I/O-free.

### 6. No cobra/printing in engine packages — ⚠️ partial
- **No cobra in `pkg/`** — verified.
- **Engine packages still print.** `pkg/sync` has ~20 direct `fmt.Fprintf(os.Stderr, "warning: …")` call sites (`engine.go`, `reconcile.go`, `sprint.go`, `devinfo.go`, `background.go`, `writer.go`); `pkg/mcp/server.go` binds `os.Stdin/os.Stdout` (that one is inherent to an MCP stdio server). The spec says engine packages "must not print; they return data/errors" with progress via channels/callbacks. The `Progress` channel was kept (good), but the warning prints were kept too — presumably to honor "no behavior change". This is a real spec deviation; a logger/callback seam would satisfy both (CLI installs a stderr logger, embedder installs their own).

### 7. Optional change-event seam — ✅
- `pkg/sync/events.go`: `ChangeEvent{Source, New, Updated, Total}`, `ChangeHook`, `Engine.OnChange(hook)`; emitted once per source after commit (`engine.go:561`, after `UpdateSyncMeta`). Nil hook = no-op; CLI never sets one; sync semantics untouched. Tested in `pkg/sync/clock_events_test.go`.

### 8. Public API hygiene — ⚠️ mostly good
- Exported-symbol doc coverage (non-test): store 62/62, sync 37/37, jira 69/70, query 11/11, config 25/25, output 13/13, mcp 14/14, clock 6/6, core 3/3, fieldwrap 5/5, jql 41/44, metrics 120/168. The metrics gaps are unexported-type methods (e.g. `cycleTimeProvider.Compute`) implementing exported interfaces — acceptable; jql gaps are `Error()` methods.
- **Missing package-level doc comments** (`// Package …`) for `pkg/jira`, `pkg/store`, `pkg/sync`, `pkg/config`, `pkg/query`, `pkg/output`, `pkg/mcp`. Spec: "godoc exists on all exported types/functions in `pkg/`" — package docs are the most-read godoc surface; these seven packages render without one.

---

## Acceptance criteria

| Criterion | Status |
|---|---|
| Separate module imports `pkg/...`, does store+sync+query+metric+JQL with exported APIs only | ✅ `examples/embed` (own go.mod) — opens store, runs sync via mocked `JiraClient`, queries, computes `throughput`, translates JQL; no cobra/TUI/internal imports |
| `var g globals` gone, explicit assembly | ✅ |
| `Store`, `JiraClient`, `Clock`, `RateLimiter` interfaces; auth injectable | ⚠️ all interfaces exist, but `RateLimiter` has no injection point |
| Engine config pure struct, separate from loader | ✅ |
| All existing tests pass; new seam tests (httptest jira, temp-DB store, fixed-clock sync/metrics) | ✅ `make check` green; `pkg/jira/auth_test.go`, `pkg/store/*_test.go`, `pkg/sync/clock_events_test.go`, `pkg/metrics/clock_test.go` present |
| godoc on all exported pkg symbols | ⚠️ symbols ~97% covered; 7 packages lack package docs |

## Constraints

- **No behavior change** — ✅ strongest evidence: `pkg/store/{issues,fields,changelog,pending,migrations,schema,comments}.go` are byte-identical to `origin/main:internal/db/*` modulo the `package` clause. Watermark non-advance, lookback, reconcile, `INSERT … ON CONFLICT` (with `INSERT OR REPLACE` only on the same non-referenced tables as before), `AuthError`, changelog-by-history-id code all carried over verbatim. Jira client internals changed (transport-based auth, `RateLimiter` interface field) but tests pass and wire behavior is equivalent.
- **CI green / fts5 / CGO** — ✅ `make check` (gofmt+vet+test) passes; Makefile still sets `GOFLAGS := -tags fts5`; gofmt clean.
- **Incremental reviewable commits** — ✅ 13 commits, roughly one package per commit in the spec's suggested order (`jql` → `jira`+auth seam → `output` → `db`→`store` → `query` → `metrics` → `sync`+clock/events → `mcp`/`fieldwrap` → CLI/`var g` removal → `/v5` module path). Delivered as one branch rather than one-PR-per-package, but the commit history preserves the reviewability.
- **Semver bump** — ✅ module path is now `github.com/sthadka/jai/v5` (main was unsuffixed `github.com/sthadka/jai` while carrying `v4`/`v5` tags — the suffix fixes a pre-existing Go-module violation). Note: `examples/embed/go.mod` requires `v5.2.0` but the newest tag is `v5.1.0`; it works only via the `replace ../..` directive. Tag `v5.2.0` (or lower the require) before publishing.

---

## Issues found (ranked)

1. **Stale staged snapshot (repo hygiene, fix before merge).** All 38 modified files are staged *and* have an unstaged diff that exactly reverts the staged one — `git diff HEAD` is empty (working tree == HEAD). The index holds an intermediate state; committing now would commit the intermediate version. Run `git reset` to unstage; the commits already on the branch are the real content.
2. **`RateLimiter` seam not actually injectable** (see §3). Add e.g. `NewWithLimiter(baseURL, httpClient, limiter RateLimiter)` or a `WithLimiter` option, or fix the doc comment that claims it exists.
3. **Engine packages still print to stderr** (see §6) — the one clear "must" from objective 6 that isn't met. Suggest a small `Logger`/callback seam defaulting to stderr in the CLI.
4. **Dashboard and `output` moved into `pkg/`** contrary to the spec's placement instructions (see §1). Either move them back under `internal/` or record the deviation as intentional.
5. **Missing package doc comments** on 7 of 13 `pkg/` packages (see §8).
6. **Minor:** `examples/embed` needs `GOFLAGS=-tags=fts5` to run (its test fails without it) — worth a README note in the example; `pkg/config`'s `Default*Path` helpers touch `os.UserHomeDir`.

## Verification performed

- `make check` — pass (gofmt, vet, all tests).
- `go test ./...` with and without `-tags fts5`; `examples/embed` module tested separately (passes with the tag).
- Grep audits: no `internal/` imports from `pkg/`, no cobra/bubbletea in `pkg/` engine packages, no stale non-`v5` import paths, no `var g globals`.
- Byte-diff of moved `pkg/store` files against `origin/main:internal/db` — identical except package clause.
- Exported-symbol doc-comment coverage scan across all `pkg/` packages.
