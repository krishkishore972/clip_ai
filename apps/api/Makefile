# apps/api/Makefile
#
# Human + CI entrypoint for the Go API.
# Recipes below MUST be indented with tabs.

SHELL := /bin/bash

BIN_DIR := bin
CMD_DIR := ./cmd
GO      ?= go
PORT    ?=
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

BUILD_FLAGS := -trimpath
LDFLAGS     := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := help

# Load .env when present (simple KEY=value files only).
ifneq (,$(wildcard ./.env))
include ./.env
export
endif

## ---- help -----------------------------------------------------------

.PHONY: help
help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

## ---- build ----------------------------------------------------------

.PHONY: build
build: ## Build every command under ./cmd into bin/
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/ $(CMD_DIR)/...

## ---- run ------------------------------------------------------------

.PHONY: env
env: ## Create .env from .env.example when missing
	@test -s .env || cp .env.example .env
	@echo "using ./.env"

.PHONY: run
run: ## Run the API server (make run PORT=9090 overrides API_ADDR)
	@test -s .env || { echo "missing or empty .env; run 'make env' first" >&2; exit 1; }
	$(if $(PORT),API_ADDR=:$(PORT)) $(GO) run $(CMD_DIR)/server

## ---- quality --------------------------------------------------------

# Migrations are explicit, never part of server startup. DATABASE_URL is
# inherited from .env/environment, not expanded into command-line arguments.
# Run one migration process at a time. Rollbacks are tested only in a test schema.
.PHONY: migrate-up migrate-status test-migrations
migrate-up: ## Apply pending Goose migrations
	$(GO) run ./cmd/migrate up

migrate-status: ## Show Goose migration versions
	$(GO) run ./cmd/migrate status

test-migrations: ## Test Up/Down in a disposable schema (TEST_DATABASE_URL: direct test DB connection)
	@test -n "$$TEST_DATABASE_URL" || { echo "TEST_DATABASE_URL is required (use a test database)" >&2; exit 1; }
	$(GO) test -v ./migrations -run TestMediaAssetsMigration -count=1

.PHONY: test
test: ## Run unit tests
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run tests with the race detector
	$(GO) test -race ./...

.PHONY: cover
cover: ## Tests + HTML coverage report
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

.PHONY: lint
lint: ## go vet + gofmt check
	$(GO) vet ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: fmt
fmt: ## Format all Go code
	$(GO) fmt ./...

.PHONY: tidy
tidy: ## Sync go.mod / go.sum
	$(GO) mod tidy

.PHONY: check
check: tidy lint test ## What CI runs

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out coverage.html
