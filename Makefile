# Makefile for Bifrost

# Variables
CONFIG_FILE ?= transports/config.example.json
PORT ?= 8080
POOL_SIZE ?= 300
PLUGINS ?= maxim
PROMETHEUS_LABELS ?= 

# Colors for output
RED=\033[0;31m
GREEN=\033[0;32m
YELLOW=\033[1;33m
BLUE=\033[0;34m
NC=\033[0m # No Color

.PHONY: help dev build run install-air clean test ui-dev ui-build ui-install

# Default target
help: ## Show this help message
	@echo "$(BLUE)Bifrost Development - Available Commands:$(NC)"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(GREEN)%-15s$(NC) %s\n", $$1, $$2}'
	@echo ""
	@echo "$(YELLOW)Environment Variables:$(NC)"
	@echo "  CONFIG_FILE       Path to config file (default: transports/config.example.json)"
	@echo "  PORT              Server port (default: 8080)"
	@echo "  POOL_SIZE         Connection pool size (default: 300)"
	@echo "  PLUGINS           Comma-separated plugins to load (default: maxim)"
	@echo "  PROMETHEUS_LABELS Labels for Prometheus metrics"

dev: install-air ## Start bifrost-http with hot reload using Air
	@echo "$(GREEN)Starting Bifrost HTTP with hot reload...$(NC)"
	@echo "$(YELLOW)Watching: transports/bifrost-http/, core/, plugins/$(NC)"
	@echo "$(YELLOW)Config: $(CONFIG_FILE)$(NC)"
	@echo "$(YELLOW)Port: $(PORT)$(NC)"
	@echo "$(YELLOW)Plugins: $(PLUGINS)$(NC)"
	@echo ""
	@cd transports/bifrost-http && air -c .air.toml -- \
		-config "../../$(CONFIG_FILE)" \
		-port "$(PORT)" \
		-pool-size $(POOL_SIZE) \
		-plugins "$(PLUGINS)" \
		$(if $(PROMETHEUS_LABELS),-prometheus-labels "$(PROMETHEUS_LABELS)")

build: ## Build bifrost-http binary
	@echo "$(GREEN)Building bifrost-http...$(NC)"
	@cd transports/bifrost-http && go build -o ../../tmp/bifrost-http .
	@echo "$(GREEN)Built: tmp/bifrost-http$(NC)"

run: build ## Build and run bifrost-http (no hot reload)
	@echo "$(GREEN)Running bifrost-http...$(NC)"
	@./tmp/bifrost-http \
		-config "$(CONFIG_FILE)" \
		-port "$(PORT)" \
		-pool-size $(POOL_SIZE) \
		-plugins "$(PLUGINS)" \
		$(if $(PROMETHEUS_LABELS),-prometheus-labels "$(PROMETHEUS_LABELS)")

install-air: ## Install Air for hot reloading
	@echo "$(YELLOW)Checking if Air is installed...$(NC)"
	@if ! command -v air > /dev/null 2>&1; then \
		echo "$(YELLOW)Installing Air...$(NC)"; \
		go install github.com/air-verse/air@latest; \
		echo "$(GREEN)Air installed successfully!$(NC)"; \
	else \
		echo "$(GREEN)Air is already installed$(NC)"; \
	fi

clean: ## Clean build artifacts and temporary files
	@echo "$(YELLOW)Cleaning build artifacts...$(NC)"
	@rm -rf tmp/
	@rm -f transports/bifrost-http/build-errors.log
	@rm -f transports/bifrost-http/tmp/
	@echo "$(GREEN)Clean complete$(NC)"

test: ## Run tests for bifrost-http
	@echo "$(GREEN)Running bifrost-http tests...$(NC)"
	@cd transports/bifrost-http && go test -v ./...

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

# UI Development targets
ui-dev: ## Start UI development server
	@echo "$(GREEN)Starting Bifrost UI development server...$(NC)"
	@cd transports/bifrost-ui && npm run dev

ui-build: ## Build UI for production (static export)
	@echo "$(GREEN)Building Bifrost UI for production...$(NC)"
	@cd transports/bifrost-ui && npm run build
	@echo "$(GREEN)UI built successfully! Files in transports/bifrost-ui/out/$(NC)"

ui-install: ## Install UI dependencies
	@echo "$(GREEN)Installing UI dependencies...$(NC)"
	@cd transports/bifrost-ui && npm install
	@echo "$(GREEN)UI dependencies installed$(NC)"

# Development workflow targets
dev-full: ui-install install-air ## Set up full development environment
	@echo "$(GREEN)Development environment setup complete!$(NC)"
	@echo "$(YELLOW)Use 'make dev' to start the API server with hot reload$(NC)"
	@echo "$(YELLOW)Use 'make ui-dev' to start the UI development server$(NC)"

# Quick start with example config
quick-start: ## Quick start with example config and maxim plugin
	@echo "$(GREEN)Quick starting Bifrost with example configuration...$(NC)"
	@$(MAKE) dev CONFIG_FILE=transports/config.example.json PLUGINS=maxim

# Production build
prod-build: build ui-build ## Build both API and UI for production
	@echo "$(GREEN)Production build complete!$(NC)"
	@echo "$(YELLOW)API binary: tmp/bifrost-http$(NC)"
	@echo "$(YELLOW)UI static files: transports/bifrost-ui/out/$(NC)"

# Docker targets
docker-build: ## Build Docker image
	@echo "$(GREEN)Building Docker image...$(NC)"
	@cd transports && docker build -t bifrost .
	@echo "$(GREEN)Docker image built: bifrost$(NC)"

docker-run: ## Run Docker container
	@echo "$(GREEN)Running Docker container...$(NC)"
	@docker run -p $(PORT):$(PORT) \
		-v $(PWD)/$(CONFIG_FILE):/app/config/config.json \
		--env-file <(env | grep -E '^(OPENAI|ANTHROPIC|AZURE|AWS|COHERE|VERTEX)_') \
		bifrost

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