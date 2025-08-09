# Makefile for Bifrost

# Variables
PORT ?= 8080
PLUGINS ?= maxim
PROMETHEUS_LABELS ?=
LOGGING_STYLE ?= json
LOGGING_LEVEL ?= info

# Colors for output
RED=\033[0;31m
GREEN=\033[0;32m
YELLOW=\033[1;33m
BLUE=\033[0;34m
CYAN=\033[0;36m
NC=\033[0m # No Color

.PHONY: help dev dev-ui build run install-air clean test install-ui work-init work-clean

# Default target
help: ## Show this help message
	@echo "$(BLUE)Bifrost Development - Available Commands:$(NC)"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(GREEN)%-15s$(NC) %s\n", $$1, $$2}'
	@echo ""
	@echo "$(YELLOW)Environment Variables:$(NC)"
	@echo "  PORT              Server port (default: 8080)"
	@echo "  PLUGINS           Comma-separated plugins to load (default: maxim)"
	@echo "  PROMETHEUS_LABELS Labels for Prometheus metrics"
	@echo "  LOGGING_STYLE Logger output format: json|pretty (default: json)"
	@echo "  LOGGING_LEVEL Logger level: debug|info|warn|error (default: info)"

install-ui:
	@which node > /dev/null || (echo "$(RED)Error: Node.js is not installed. Please install Node.js first.$(NC)" && exit 1)
	@which npm > /dev/null || (echo "$(RED)Error: npm is not installed. Please install npm first.$(NC)" && exit 1)
	@echo "$(GREEN)Node.js and npm are installed$(NC)"
	@cd ui && npm install
	@which next > /dev/null || (echo "$(YELLOW)Installing nextjs...$(NC)" && npm install -g next)
	@echo "$(GREEN)UI deps are in sync$(NC)"

install-air: ## Install air for hot reloading (if not already installed)
	@which air > /dev/null || (echo "$(YELLOW)Installing air for hot reloading...$(NC)" && go install github.com/air-verse/air@latest)
	@echo "$(GREEN)Air is ready$(NC)"

dev: install-ui install-air ## Start complete development environment (UI + API with proxy)
	@echo "$(GREEN)Starting Bifrost complete development environment...$(NC)"
	@echo "$(YELLOW)This will start:$(NC)"
	@echo "  1. UI development server (localhost:3000)"
	@echo "  2. API server with UI proxy (localhost:$(PORT)/ui)"
	@echo "$(CYAN)Access everything at: http://localhost:$(PORT)/ui$(NC)"
	@echo ""
	@echo "$(YELLOW)Starting UI development server...$(NC)"
	@cd ui && npm run dev &
	@sleep 3
	@echo "$(YELLOW)Starting API server with UI proxy...$(NC)"
	@$(MAKE) work-init >/dev/null
	@cd transports/bifrost-http && BIFROST_UI_DEV=true air -c .air.toml -- \
		-port "$(PORT)" \
		-plugins "$(PLUGINS)" \
		-log-style "$(LOGGING_STYLE)" \
		-log-level "$(LOGGING_LEVEL)" \
		$(if $(PROMETHEUS_LABELS),-prometheus-labels "$(PROMETHEUS_LABELS)")

build-ui: install-ui ## Build ui
	@echo "$(GREEN)Building ui...$(NC)"
	@rm -rf ui/.next
	@cd ui && npm run build && npm run copy-build

build: build-ui ## Build bifrost-http binary
	@echo "$(GREEN)Building bifrost-http...$(NC)"
	@cd transports/bifrost-http && GOWORK=off go build -o ../../tmp/bifrost-http .
	@echo "$(GREEN)Built: tmp/bifrost-http$(NC)"

run: build ## Build and run bifrost-http (no hot reload)
	@echo "$(GREEN)Running bifrost-http...$(NC)"
	@./tmp/bifrost-http \
		-port "$(PORT)" \
		-plugins "$(PLUGINS)" \
		-log-style "$(LOGGING_STYLE)" \
		-log-level "$(LOGGING_LEVEL)" \
		$(if $(PROMETHEUS_LABELS),-prometheus-labels "$(PROMETHEUS_LABELS)")

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
	@$(MAKE) dev CONFIG_FILE=transports/config.example.json PLUGINS=maxim

docker-build:
	@echo "$(GREEN)Building Docker image...$(NC)"
	@docker build -f transports/Dockerfile -t bifrost .
	@echo "$(GREEN)Docker image built: bifrost$(NC)"

docker-run: ## Run Docker container
	@echo "$(GREEN)Running Docker container...$(NC)"
	@docker run -p $(PORT):8080 -v $(shell pwd):/app/data bifrost

# Linting and formatting
lint: ## Run linter for Go code
	@echo "$(GREEN)Running golangci-lint...$(NC)"
	@golangci-lint run ./...

fmt: ## Format Go code
	@echo "$(GREEN)Formatting Go code...$(NC)"
	@gofmt -s -w .
	@goimports -w .

# Git hooks and development setup
setup-git-hooks: ## Set up Git hooks for development
	@echo "$(GREEN)Setting up Git hooks...$(NC)"
	@echo "#!/bin/sh\nmake fmt\nmake lint" > .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "$(GREEN)Git hooks installed$(NC)"

# Workspace helpers
work-init: ## Create local go.work to use local modules for development
	@echo "$(GREEN)Initializing Go workspace...$(NC)"
	@test -f go.work || go work init ./core ./transports
	@go work use ./core ./transports
	@echo "$(YELLOW)Go workspace ready (not committed).$(NC)"

work-clean: ## Remove local go.work
	@rm -f go.work go.work.sum || true
	@echo "$(GREEN)Removed local go.work files$(NC)"
