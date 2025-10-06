# Makefile for Bifrost

# Variables
HOST ?= localhost
PORT ?= 8080
APP_DIR ?= 
PROMETHEUS_LABELS ?=
LOG_STYLE ?= json
LOG_LEVEL ?= info

# Colors for output
RED=\033[0;31m
GREEN=\033[0;32m
YELLOW=\033[1;33m
BLUE=\033[0;34m
CYAN=\033[0;36m
NC=\033[0m # No Color

.PHONY: all help dev build-ui build run install-air clean test install-ui setup-workspace work-init work-clean docs docker-build cleanup-enterprise

all: help

# Default target
help: ## Show this help message
	@echo "$(BLUE)Bifrost Development - Available Commands:$(NC)"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(GREEN)%-15s$(NC) %s\n", $$1, $$2}'
	@echo ""
	@echo "$(YELLOW)Environment Variables:$(NC)"
	@echo "  HOST              Server host (default: localhost)"
	@echo "  PORT              Server port (default: 8080)"
	@echo "  PROMETHEUS_LABELS Labels for Prometheus metrics"
	@echo "  LOG_STYLE Logger output format: json|pretty (default: json)"
	@echo "  LOG_LEVEL Logger level: debug|info|warn|error (default: info)"
	@echo "  APP_DIR           App data directory inside container (default: /app/data)"

cleanup-enterprise: ## Clean up enterprise directories if present
	@echo "$(GREEN)Cleaning up enterprise...$(NC)"
	@if [ -d "ui/app/enterprise" ]; then rm -rf ui/app/enterprise; fi
	@echo "$(GREEN)Enterprise cleaned up$(NC)"

install-ui: cleanup-enterprise
	@which node > /dev/null || (echo "$(RED)Error: Node.js is not installed. Please install Node.js first.$(NC)" && exit 1)
	@which npm > /dev/null || (echo "$(RED)Error: npm is not installed. Please install npm first.$(NC)" && exit 1)
	@echo "$(GREEN)Node.js and npm are installed$(NC)"
	@cd ui && npm install
	@which next > /dev/null || (echo "$(YELLOW)Installing nextjs...$(NC)" && npm install -g next)
	@echo "$(GREEN)UI deps are in sync$(NC)"

install-air: ## Install air for hot reloading (if not already installed)
	@which air > /dev/null || (echo "$(YELLOW)Installing air for hot reloading...$(NC)" && go install github.com/air-verse/air@latest)
	@echo "$(GREEN)Air is ready$(NC)"

dev: install-ui install-air setup-workspace ## Start complete development environment (UI + API with proxy)
	@echo "$(GREEN)Starting Bifrost complete development environment...$(NC)"
	@echo "$(YELLOW)This will start:$(NC)"
	@echo "  1. UI development server (localhost:3000)"
	@echo "  2. API server with UI proxy (localhost:$(PORT))"
	@echo "$(CYAN)Access everything at: http://localhost:$(PORT)$(NC)"
	@echo ""
	@echo "$(YELLOW)Starting UI development server...$(NC)"
	@cd ui && npm run dev &
	@sleep 3
	@echo "$(YELLOW)Starting API server with UI proxy...$(NC)"
	@$(MAKE) setup-workspace >/dev/null
	@cd transports/bifrost-http && BIFROST_UI_DEV=true air -c .air.toml -- \
		-host "$(HOST)" \
		-port "$(PORT)" \
		-log-style "$(LOG_STYLE)" \
		-log-level "$(LOG_LEVEL)" \
		$(if $(PROMETHEUS_LABELS),-prometheus-labels "$(PROMETHEUS_LABELS)") \
		$(if $(APP_DIR),-app-dir "$(APP_DIR)")

build-ui: install-ui ## Build ui
	@echo "$(GREEN)Building ui...$(NC)"
	@rm -rf ui/.next
	@cd ui && npm run build && npm run copy-build

build: build-ui ## Build bifrost-http binary
	@echo "$(GREEN)Building bifrost-http...$(NC)"
	@cd transports/bifrost-http && GOWORK=off go build -o ../../tmp/bifrost-http .
	@echo "$(GREEN)Built: tmp/bifrost-http$(NC)"

docker-build: build-ui ## Build Docker image
	@echo "$(GREEN)Building Docker image...$(NC)"
	@docker build -f transports/Dockerfile -t bifrost .
	@echo "$(GREEN)Docker image built: bifrost$(NC)"

docker-run: ## Run Docker container
	@echo "$(GREEN)Running Docker container...$(NC)"
	@docker run -e APP_PORT=$(PORT) -e APP_HOST=0.0.0.0 -p $(PORT):$(PORT) -e LOG_LEVEL=$(LOG_LEVEL) -e LOG_STYLE=$(LOG_STYLE) -v $(shell pwd):/app/data  bifrost

docs: ## Prepare local docs
	@echo "$(GREEN)Preparing local docs...$(NC)"
	@cd docs && npx --yes mintlify@latest dev

run: build ## Build and run bifrost-http (no hot reload)
	@echo "$(GREEN)Running bifrost-http...$(NC)"
	@./tmp/bifrost-http \
		-host "$(HOST)" \
		-port "$(PORT)" \
		-log-style "$(LOG_STYLE)" \
		-log-level "$(LOG_LEVEL)" \
		$(if $(PROMETHEUS_LABELS),-prometheus-labels "$(PROMETHEUS_LABELS)")
		$(if $(APP_DIR),-app-dir "$(APP_DIR)")

clean: ## Clean build artifacts and temporary files
	@echo "$(YELLOW)Cleaning build artifacts...$(NC)"
	@rm -rf tmp/
	@rm -f transports/bifrost-http/build-errors.log
	@rm -rf transports/bifrost-http/tmp/
	@echo "$(GREEN)Clean complete$(NC)"

test: ## Run tests for bifrost-http
	@echo "$(GREEN)Running bifrost-http tests...$(NC)"
	@cd transports/bifrost-http && GOWORK=off go test -v ./...

test-core: ## Run core tests
	@echo "$(GREEN)Running core tests...$(NC)"
	@cd core && go test -v ./...

test-plugins: ## Run plugin tests
	@echo "$(GREEN)Running plugin tests...$(NC)"
	@cd plugins && find . -name "*.go" -path "*/tests/*" -o -name "*_test.go" | head -1 > /dev/null && \
		for dir in $$(find . -name "*_test.go" -exec dirname {} \; | sort -u); do \
			echo "Testing $$dir..."; \
			cd $$dir && go test -v ./... && cd - > /dev/null; \
		done || echo "No plugin tests found"

test-all: test-core test-plugins test ## Run all tests

# Quick start with example config
quick-start: ## Quick start with example config and maxim plugin
	@echo "$(GREEN)Quick starting Bifrost with example configuration...$(NC)"
	@$(MAKE) dev

# Linting and formatting
lint: ## Run linter for Go code
	@echo "$(GREEN)Running golangci-lint...$(NC)"
	@golangci-lint run ./...

fmt: ## Format Go code
	@echo "$(GREEN)Formatting Go code...$(NC)"
	@gofmt -s -w .
	@goimports -w .

# Workspace helpers
setup-workspace: ## Set up Go workspace with all local modules for development
	@echo "$(GREEN)Setting up Go workspace for local development...$(NC)"
	@echo "$(YELLOW)Cleaning existing workspace...$(NC)"
	@rm -f go.work go.work.sum || true
	@echo "$(YELLOW)Initializing new workspace...$(NC)"
	@go work init ./core ./framework ./transports
	@echo "$(YELLOW)Adding plugin modules...$(NC)"
	@for plugin_dir in ./plugins/*/; do \
		if [ -d "$$plugin_dir" ] && [ -f "$$plugin_dir/go.mod" ]; then \
			echo "  Adding plugin: $$(basename $$plugin_dir)"; \
			go work use "$$plugin_dir"; \
		fi; \
	done
	@echo "$(YELLOW)Syncing workspace...$(NC)"
	@go work sync
	@echo "$(GREEN)✓ Go workspace ready with all local modules$(NC)"
	@echo ""
	@echo "$(CYAN)Local modules in workspace:$(NC)"
	@go list -m all | grep "github.com/maximhq/bifrost" | grep -v " v" | sed 's/^/  ✓ /'
	@echo ""
	@echo "$(CYAN)Remote modules (no local version):$(NC)"
	@go list -m all | grep "github.com/maximhq/bifrost" | grep " v" | sed 's/^/  → /'
	@echo ""
	@echo "$(YELLOW)Note: go.work files are not committed to version control$(NC)"

work-init: ## Create local go.work to use local modules for development (legacy)
	@echo "$(YELLOW)⚠️  work-init is deprecated, use 'make setup-workspace' instead$(NC)"
	@$(MAKE) setup-workspace

work-clean: ## Remove local go.work
	@rm -f go.work go.work.sum || true
	@echo "$(GREEN)Removed local go.work files$(NC)"
