# ==============================================================================
#  jai — Query Jira with SQL
# ==============================================================================

.PHONY: build test lint vet check clean install run help

BINARY      := jai
BUILD_FLAGS := -tags fts5
LDFLAGS     := -ldflags "-s -w"

# ── Colors ────────────────────────────────────────────────────────────────────
RESET  := \033[0m
BOLD   := \033[1m
DIM    := \033[2m
GREEN  := \033[32m
YELLOW := \033[33m
CYAN   := \033[36m

.DEFAULT_GOAL := help

# ── Help ──────────────────────────────────────────────────────────────────────

help: ## Show this help
	@printf "\n  $(BOLD)jai$(RESET) — Query Jira with SQL\n\n"
	@printf "  $(CYAN)Usage:$(RESET) make $(DIM)<target>$(RESET)\n\n"
	@printf "  $(CYAN)Targets:$(RESET)\n"
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ \
	  { printf "    $(GREEN)%-10s$(RESET) %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf "\n"

# ── Build ─────────────────────────────────────────────────────────────────────

build: ## Compile the jai binary
	@printf "  $(CYAN)→$(RESET) Building $(BOLD)$(BINARY)$(RESET)...\n"
	@go build $(BUILD_FLAGS) $(LDFLAGS) -o $(BINARY) ./cmd/jai && \
	  printf "  $(GREEN)✓$(RESET) $(BOLD)./$(BINARY)$(RESET) ready\n"

install: ## Install jai to $$GOPATH/bin
	@printf "  $(CYAN)→$(RESET) Installing $(BOLD)$(BINARY)$(RESET)...\n"
	@go install $(BUILD_FLAGS) $(LDFLAGS) ./cmd/jai && \
	  printf "  $(GREEN)✓$(RESET) Installed to $$(go env GOPATH)/bin/$(BINARY)\n"

clean: ## Remove the compiled binary
	@printf "  $(YELLOW)→$(RESET) Removing $(BOLD)./$(BINARY)$(RESET)\n"
	@rm -f $(BINARY)
	@printf "  $(GREEN)✓$(RESET) Clean\n"

run: build ## Build then run jai (use ARGS="..." to pass flags)
	@./$(BINARY) $(ARGS)

# ── Quality ───────────────────────────────────────────────────────────────────

test: ## Run all tests
	@printf "  $(CYAN)→$(RESET) go test ./...\n"
	@go test $(BUILD_FLAGS) ./... && printf "  $(GREEN)✓$(RESET) All tests passed\n"

vet: ## Run go vet
	@printf "  $(CYAN)→$(RESET) go vet ./...\n"
	@go vet $(BUILD_FLAGS) ./... && printf "  $(GREEN)✓$(RESET) go vet passed\n"

lint: ## Run golangci-lint (must be installed separately)
	@printf "  $(CYAN)→$(RESET) golangci-lint run ./...\n"
	@golangci-lint run ./...

check: vet test ## Run vet + tests (no external tools required)

# ── Onboarding ────────────────────────────────────────────────────────────────

setup: install ## Install then run the jai init wizard
	@$(BINARY) init
