# ==============================================================================
#  jai — Sync Jira Cloud to local SQLite and query it with SQL, CLI, and TUI
# ==============================================================================

MODULE      := github.com/sthadka/jai
BINARY      := jai

GO          := go
# The SQLite schema uses FTS5 virtual tables, so every go invocation needs the
# fts5 build tag and CGO. Without them the DB layer fails with "no such module:
# fts5" at runtime and tests refuse to compile.
BUILD_TAGS  := fts5
GOFLAGS     := -tags $(BUILD_TAGS)
LDFLAGS     := -ldflags "-s -w"
BUILDFLAGS  := -trimpath

# Go files we own (used by fmt/fmt-check).
GOFILES      = $(shell git ls-files '*.go')

# ── MCP server (override on the command line) ─────────────────────────────────
# The MCP server backs AI-agent integration. Callers that speak HTTP (e.g. a
# sidecar) expect it reachable at http://localhost:$(PORT)/mcp — start it with
# `make mcp-http`. Configure with:
#   make mcp-http PORT=9000               # bind a different port
#   make mcp-http TOOLSETS=read,schema    # restrict enabled toolsets
#   make mcp-http READONLY=1              # block all write operations
PORT        ?= 8947
TOOLSETS    ?=
READONLY    ?=
SERVE_FLAGS  = $(if $(TOOLSETS),--toolsets $(TOOLSETS),) $(if $(READONLY),--read-only,)

# ── Colors ────────────────────────────────────────────────────────────────────
RESET  := \033[0m
BOLD   := \033[1m
DIM    := \033[2m
RED    := \033[31m
GREEN  := \033[32m
YELLOW := \033[33m
CYAN   := \033[36m

.DEFAULT_GOAL := help

.PHONY: help \
        build install run clean \
        mcp mcp-stdio mcp-http \
        fmt vet test test-race lint check ci \
        setup hooks doctor

# ── Help ────────────────────────────────────────────────────────────────────

help: ## Show this help
	@printf "\n  $(BOLD)jai$(RESET) — Query Jira with SQL\n\n"
	@printf "  $(CYAN)Usage:$(RESET) make $(DIM)<target>$(RESET)   $(DIM)(run 'make setup' first on a fresh clone)$(RESET)\n"
	@awk 'BEGIN {FS = ":.*##"} \
	  /^##@/ { printf "\n  $(BOLD)%s$(RESET)\n", substr($$0, 5); next } \
	  /^[a-zA-Z0-9_.-]+:.*##/ { printf "    $(GREEN)%-16s$(RESET) %s\n", $$1, $$2 }' \
	  $(MAKEFILE_LIST)
	@printf "\n  $(DIM)Examples:$(RESET) make run ARGS=\"get ROX-1234\"  ·  make mcp-http PORT=9000  ·  make check\n\n"

##@ Build

build: ## Compile the jai binary (CGO + fts5)
	@printf "  $(CYAN)→$(RESET) Building $(BOLD)$(BINARY)$(RESET)...\n"
	@$(GO) build $(GOFLAGS) $(BUILDFLAGS) $(LDFLAGS) -o $(BINARY) ./cmd/jai && \
	  printf "  $(GREEN)✓$(RESET) $(BOLD)./$(BINARY)$(RESET) ready\n"

install: ## Install jai to $$GOPATH/bin
	@printf "  $(CYAN)→$(RESET) Installing $(BOLD)$(BINARY)$(RESET)...\n"
	@$(GO) install $(GOFLAGS) $(BUILDFLAGS) $(LDFLAGS) ./cmd/jai && \
	  printf "  $(GREEN)✓$(RESET) installed to $$($(GO) env GOPATH)/bin/$(BINARY)\n"

clean: ## Remove the compiled binary
	@printf "  $(YELLOW)→$(RESET) Removing $(BOLD)./$(BINARY)$(RESET)\n"
	@rm -f $(BINARY)
	@printf "  $(GREEN)✓$(RESET) clean\n"

##@ Run

run: build ## Build then run jai (use ARGS="..." to pass a subcommand + flags)
	@./$(BINARY) $(ARGS)

mcp: mcp-stdio ## Alias for mcp-stdio (default MCP transport)

mcp-stdio: build ## Run the MCP server over stdio (for local agent config)
	@printf "  $(CYAN)→$(RESET) jai serve (stdio)$(SERVE_FLAGS)\n" >&2
	@./$(BINARY) serve --transport stdio $(SERVE_FLAGS)

mcp-http: build ## Run the MCP server over HTTP (default :8947, override PORT=…)
	@printf "  $(CYAN)→$(RESET) MCP server on $(BOLD)http://localhost:$(PORT)/mcp$(RESET)$(SERVE_FLAGS)\n"
	@printf "  $(DIM)Ctrl-C to stop · override with PORT=… TOOLSETS=… READONLY=1$(RESET)\n"
	@./$(BINARY) serve --transport http --port $(PORT) $(SERVE_FLAGS)

##@ Quality

fmt: ## Format Go code (gofmt -w)
	@gofmt -w $(GOFILES)
	@printf "  $(GREEN)✓$(RESET) formatted\n"

vet: ## Run go vet
	@printf "  $(CYAN)→$(RESET) go vet ./...\n"
	@$(GO) vet $(GOFLAGS) ./... && printf "  $(GREEN)✓$(RESET) go vet passed\n"

test: ## Run all tests
	@printf "  $(CYAN)→$(RESET) go test ./...\n"
	@$(GO) test $(GOFLAGS) ./... && printf "  $(GREEN)✓$(RESET) all tests passed\n"

test-race: ## Run all tests with the race detector
	@printf "  $(CYAN)→$(RESET) go test -race ./...\n"
	@$(GO) test $(GOFLAGS) -race ./... && printf "  $(GREEN)✓$(RESET) race tests passed\n"

lint: ## Run golangci-lint (must be installed separately)
	@printf "  $(CYAN)→$(RESET) golangci-lint run ./...\n"
	@golangci-lint run ./...

check: ## Pre-commit gate: gofmt + vet + tests
	@out=$$(gofmt -l $(GOFILES)); \
	  if [ -n "$$out" ]; then printf "  $(RED)✗$(RESET) needs gofmt (run make fmt):\n%s\n" "$$out"; exit 1; \
	  else printf "  $(GREEN)✓$(RESET) gofmt clean\n"; fi
	@$(MAKE) --no-print-directory vet test

ci: check lint ## Mirror CI locally: gofmt + vet + tests + lint

##@ Onboarding

setup: install hooks ## Install jai, enable git hooks, then run the init wizard
	@$(BINARY) init

hooks: ## Enable the tracked git hooks (pre-push runs make check)
	@git config core.hooksPath .beads/hooks
	@printf "  $(GREEN)✓$(RESET) git hooks enabled $(DIM)(.beads/hooks — pre-push runs make check; CI_HOOK_TARGET=ci adds lint)$(RESET)\n"

doctor: ## Check your toolchain + environment (go, CGO, fts5, golangci-lint)
	@printf "\n  $(BOLD)Environment check$(RESET)\n\n"
	@printf "  %-24s" "go (need 1.25.x)"; \
	  if command -v $(GO) >/dev/null 2>&1; then printf "$(GREEN)✓$(RESET) %s\n" "$$($(GO) version | awk '{print $$3}')"; \
	  else printf "$(RED)✗ missing$(RESET)\n"; fi
	@printf "  %-24s" "CGO (need enabled)"; \
	  if [ "$$($(GO) env CGO_ENABLED)" = "1" ]; then printf "$(GREEN)✓$(RESET)\n"; \
	  else printf "$(RED)✗ CGO_ENABLED=0 (set CGO_ENABLED=1)$(RESET)\n"; fi
	@printf "  %-24s" "C compiler"; \
	  if command -v cc >/dev/null 2>&1 || command -v gcc >/dev/null 2>&1 || command -v clang >/dev/null 2>&1; then printf "$(GREEN)✓$(RESET)\n"; \
	  else printf "$(RED)✗ missing (needed for sqlite3)$(RESET)\n"; fi
	@printf "  %-24s" "fts5 build"; \
	  if $(GO) build $(GOFLAGS) -o /dev/null ./cmd/jai >/dev/null 2>&1; then printf "$(GREEN)✓$(RESET)\n"; \
	  else printf "$(RED)✗ build with -tags fts5 failed$(RESET)\n"; fi
	@printf "  %-24s" "gofmt"; \
	  if command -v gofmt >/dev/null 2>&1; then printf "$(GREEN)✓$(RESET)\n"; else printf "$(RED)✗$(RESET)\n"; fi
	@printf "  %-24s" "golangci-lint"; \
	  if command -v golangci-lint >/dev/null 2>&1; then printf "$(GREEN)✓$(RESET) %s\n" "$$(golangci-lint version 2>/dev/null | head -1)"; \
	  else printf "$(YELLOW)! optional (make lint)$(RESET)\n"; fi
	@printf "\n"
