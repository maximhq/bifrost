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

# Include deployment recipes
include recipes/fly.mk
include recipes/ecs.mk

.PHONY: all help dev build-ui build run install-air clean test install-ui setup-workspace work-init work-clean docs build-docker-image cleanup-enterprise

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
	@echo "  LOG_STYLE         Logger output format: json|pretty (default: json)"
	@echo "  LOG_LEVEL         Logger level: debug|info|warn|error (default: info)"
	@echo "  APP_DIR           App data directory inside container (default: /app/data)"
	@echo ""
	@echo "$(YELLOW)Test Configuration:$(NC)"
	@echo "  TEST_REPORTS_DIR  Directory for HTML test reports (default: test-reports)"
	@echo "  GOTESTSUM_FORMAT  Test output format: testname|dots|pkgname|standard-verbose (default: testname)"

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

install-gotestsum: ## Install gotestsum for test reporting (if not already installed)
	@which gotestsum > /dev/null || (echo "$(YELLOW)Installing gotestsum for test reporting...$(NC)" && go install gotest.tools/gotestsum@latest)
	@echo "$(GREEN)gotestsum is ready$(NC)"

install-junit-viewer: ## Install junit-viewer for HTML report generation (if not already installed)
	@if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
		if which junit-viewer > /dev/null 2>&1; then \
			echo "$(GREEN)junit-viewer is already installed$(NC)"; \
		else \
			echo "$(YELLOW)Installing junit-viewer for HTML reports...$(NC)"; \
			if npm install -g junit-viewer 2>&1; then \
				echo "$(GREEN)junit-viewer installed successfully$(NC)"; \
			else \
				echo "$(RED)Failed to install junit-viewer. HTML reports will be skipped.$(NC)"; \
				echo "$(YELLOW)You can install it manually: npm install -g junit-viewer$(NC)"; \
				exit 0; \
			fi; \
		fi \
	else \
		echo "$(YELLOW)CI environment detected, skipping junit-viewer installation$(NC)"; \
	fi

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

build-docker-image: build-ui ## Build Docker image
	@echo "$(GREEN)Building Docker image...$(NC)"
	$(eval GIT_SHA=$(shell git rev-parse --short HEAD))
	@docker build -f transports/Dockerfile -t bifrost -t bifrost:$(GIT_SHA) -t bifrost:latest .
	@echo "$(GREEN)Docker image built: bifrost, bifrost:$(GIT_SHA), bifrost:latest$(NC)"

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
	@rm -rf $(TEST_REPORTS_DIR)/
	@echo "$(GREEN)Clean complete$(NC)"

clean-test-reports: ## Clean test reports only
	@echo "$(YELLOW)Cleaning test reports...$(NC)"
	@rm -rf $(TEST_REPORTS_DIR)/
	@echo "$(GREEN)Test reports cleaned$(NC)"

generate-html-reports: ## Convert existing XML reports to HTML
	@if ! which junit-viewer > /dev/null 2>&1; then \
		echo "$(RED)Error: junit-viewer not installed$(NC)"; \
		echo "$(YELLOW)Install with: make install-junit-viewer$(NC)"; \
		exit 1; \
	fi
	@echo "$(GREEN)Converting XML reports to HTML...$(NC)"
	@if [ ! -d "$(TEST_REPORTS_DIR)" ] || [ -z "$$(ls -A $(TEST_REPORTS_DIR)/*.xml 2>/dev/null)" ]; then \
		echo "$(YELLOW)No XML reports found in $(TEST_REPORTS_DIR)$(NC)"; \
		echo "$(YELLOW)Run tests first: make test-all$(NC)"; \
		exit 0; \
	fi
	@for xml in $(TEST_REPORTS_DIR)/*.xml; do \
		html=$${xml%.xml}.html; \
		echo "  Converting $$(basename $$xml) → $$(basename $$html)"; \
		junit-viewer --results=$$xml --save=$$html 2>/dev/null || true; \
	done
	@echo ""
	@echo "$(GREEN)✓ HTML reports generated$(NC)"
	@echo "$(CYAN)View reports:$(NC)"
	@ls -1 $(TEST_REPORTS_DIR)/*.html 2>/dev/null | sed 's|$(TEST_REPORTS_DIR)/|  open $(TEST_REPORTS_DIR)/|' || true

test: install-gotestsum ## Run tests for bifrost-http
	@echo "$(GREEN)Running bifrost-http tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@cd transports/bifrost-http && GOWORK=off gotestsum \
		--format=$(GOTESTSUM_FORMAT) \
		--junitfile=../../$(TEST_REPORTS_DIR)/bifrost-http.xml \
		-- -v ./...
	@if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
		if which junit-viewer > /dev/null 2>&1; then \
			echo "$(YELLOW)Generating HTML report...$(NC)"; \
			if junit-viewer --results=$(TEST_REPORTS_DIR)/bifrost-http.xml --save=$(TEST_REPORTS_DIR)/bifrost-http.html 2>/dev/null; then \
				echo ""; \
				echo "$(CYAN)HTML report: $(TEST_REPORTS_DIR)/bifrost-http.html$(NC)"; \
				echo "$(CYAN)Open with: open $(TEST_REPORTS_DIR)/bifrost-http.html$(NC)"; \
			else \
				echo "$(YELLOW)HTML generation failed. JUnit XML report available.$(NC)"; \
				echo "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/bifrost-http.xml$(NC)"; \
			fi; \
		else \
			echo ""; \
			echo "$(YELLOW)junit-viewer not installed. Install with: make install-junit-viewer$(NC)"; \
			echo "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/bifrost-http.xml$(NC)"; \
		fi \
	else \
		echo ""; \
		echo "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/bifrost-http.xml$(NC)"; \
	fi

test-core: install-gotestsum ## Run core tests (Usage: make test-core PROVIDER=anthropic TESTCASE=SimpleChat)
	@echo "$(GREEN)Running core tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@if [ -n "$(PROVIDER)" ]; then \
		echo "$(CYAN)Running tests for provider: $(PROVIDER)$(NC)"; \
		if [ ! -f "tests/core-providers/$(PROVIDER)_test.go" ]; then \
			echo "$(RED)Error: Provider test file '$(PROVIDER)_test.go' not found$(NC)"; \
			echo "$(YELLOW)Available providers:$(NC)"; \
			ls tests/core-providers/*_test.go 2>/dev/null | grep -v cross_provider | xargs -n 1 basename | sed 's/_test\.go//' | sed 's/^/  - /'; \
			exit 1; \
		fi; \
	fi; \
	if [ -f .env ]; then \
		echo "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	if [ -n "$(PROVIDER)" ]; then \
		PROVIDER_TEST_NAME=$$(echo "$(PROVIDER)" | awk '{print toupper(substr($$0,1,1)) tolower(substr($$0,2))}' | sed 's/openai/OpenAI/i; s/sgl/SGL/i'); \
		if [ -n "$(TESTCASE)" ]; then \
			echo "$(CYAN)Running Test$${PROVIDER_TEST_NAME}/$${PROVIDER_TEST_NAME}Tests/$(TESTCASE)...$(NC)"; \
			cd tests/core-providers && GOWORK=off gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../$(TEST_REPORTS_DIR)/core-$(PROVIDER)-$(TESTCASE).xml \
				-- -v -run "^Test$${PROVIDER_TEST_NAME}$$/.*Tests/$(TESTCASE)$$"; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					echo "$(YELLOW)Generating HTML report...$(NC)"; \
					junit-viewer --results=../../$(TEST_REPORTS_DIR)/core-$(PROVIDER)-$(TESTCASE).xml --save=../../$(TEST_REPORTS_DIR)/core-$(PROVIDER)-$(TESTCASE).html 2>/dev/null || true; \
					echo ""; \
					echo "$(CYAN)HTML report: $(TEST_REPORTS_DIR)/core-$(PROVIDER)-$(TESTCASE).html$(NC)"; \
					echo "$(CYAN)Open with: open $(TEST_REPORTS_DIR)/core-$(PROVIDER)-$(TESTCASE).html$(NC)"; \
				else \
					echo ""; \
					echo "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/core-$(PROVIDER)-$(TESTCASE).xml$(NC)"; \
				fi; \
			else \
				echo ""; \
				echo "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/core-$(PROVIDER)-$(TESTCASE).xml$(NC)"; \
			fi; \
		else \
			echo "$(CYAN)Running Test$${PROVIDER_TEST_NAME}...$(NC)"; \
			cd tests/core-providers && GOWORK=off gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../$(TEST_REPORTS_DIR)/core-$(PROVIDER).xml \
				-- -v -run "^Test$${PROVIDER_TEST_NAME}$$"; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					echo "$(YELLOW)Generating HTML report...$(NC)"; \
					junit-viewer --results=../../$(TEST_REPORTS_DIR)/core-$(PROVIDER).xml --save=../../$(TEST_REPORTS_DIR)/core-$(PROVIDER).html 2>/dev/null || true; \
					echo ""; \
					echo "$(CYAN)HTML report: $(TEST_REPORTS_DIR)/core-$(PROVIDER).html$(NC)"; \
					echo "$(CYAN)Open with: open $(TEST_REPORTS_DIR)/core-$(PROVIDER).html$(NC)"; \
				else \
					echo ""; \
					echo "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/core-$(PROVIDER).xml$(NC)"; \
				fi; \
			else \
				echo ""; \
				echo "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/core-$(PROVIDER).xml$(NC)"; \
			fi; \
		fi \
	else \
		if [ -n "$(TESTCASE)" ]; then \
			echo "$(RED)Error: TESTCASE requires PROVIDER to be specified$(NC)"; \
			echo "$(YELLOW)Usage: make test-core PROVIDER=anthropic TESTCASE=SimpleChat$(NC)"; \
			exit 1; \
		fi; \
		cd tests/core-providers && GOWORK=off gotestsum \
			--format=$(GOTESTSUM_FORMAT) \
			--junitfile=../../$(TEST_REPORTS_DIR)/core-all.xml \
			-- -v ./...; \
		if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
			if which junit-viewer > /dev/null 2>&1; then \
				echo "$(YELLOW)Generating HTML report...$(NC)"; \
				junit-viewer --results=../../$(TEST_REPORTS_DIR)/core-all.xml --save=../../$(TEST_REPORTS_DIR)/core-all.html 2>/dev/null || true; \
				echo ""; \
				echo "$(CYAN)HTML report: $(TEST_REPORTS_DIR)/core-all.html$(NC)"; \
				echo "$(CYAN)Open with: open $(TEST_REPORTS_DIR)/core-all.html$(NC)"; \
			else \
				echo ""; \
				echo "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/core-all.xml$(NC)"; \
			fi; \
		else \
			echo ""; \
			echo "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/core-all.xml$(NC)"; \
		fi; \
	fi

test-plugins: install-gotestsum ## Run plugin tests
	@echo "$(GREEN)Running plugin tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@cd plugins && find . -name "*.go" -path "*/tests/*" -o -name "*_test.go" | head -1 > /dev/null && \
		for dir in $$(find . -name "*_test.go" -exec dirname {} \; | sort -u); do \
			plugin_name=$$(echo $$dir | sed 's|^\./||' | sed 's|/|-|g'); \
			echo "Testing $$dir..."; \
			cd $$dir && gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../$(TEST_REPORTS_DIR)/plugin-$$plugin_name.xml \
				-- -v ./... && cd - > /dev/null; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					echo "$(YELLOW)Generating HTML report for $$plugin_name...$(NC)"; \
					junit-viewer --results=../$(TEST_REPORTS_DIR)/plugin-$$plugin_name.xml --save=../$(TEST_REPORTS_DIR)/plugin-$$plugin_name.html 2>/dev/null || true; \
				fi; \
			fi; \
		done || echo "No plugin tests found"
	@echo ""
	@if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
		echo "$(CYAN)HTML reports saved to $(TEST_REPORTS_DIR)/plugin-*.html$(NC)"; \
	else \
		echo "$(CYAN)JUnit XML reports saved to $(TEST_REPORTS_DIR)/plugin-*.xml$(NC)"; \
	fi

test-all: test-core test-plugins test ## Run all tests
	@echo ""
	@echo "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"
	@echo "$(GREEN)              All Tests Complete - Summary                 $(NC)"
	@echo "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"
	@echo ""
	@if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
		echo "$(YELLOW)Generating combined HTML report...$(NC)"; \
		junit-viewer --results=$(TEST_REPORTS_DIR) --save=$(TEST_REPORTS_DIR)/index.html 2>/dev/null || true; \
		echo ""; \
		echo "$(CYAN)HTML reports available in $(TEST_REPORTS_DIR)/:$(NC)"; \
		ls -1 $(TEST_REPORTS_DIR)/*.html 2>/dev/null | sed 's/^/  ✓ /' || echo "  No reports found"; \
		echo ""; \
		echo "$(YELLOW)📊 View all test results:$(NC)"; \
		echo "$(CYAN)  open $(TEST_REPORTS_DIR)/index.html$(NC)"; \
		echo ""; \
		echo "$(YELLOW)Or view individual reports:$(NC)"; \
		ls -1 $(TEST_REPORTS_DIR)/*.html 2>/dev/null | grep -v index.html | sed 's|$(TEST_REPORTS_DIR)/|  open $(TEST_REPORTS_DIR)/|' || true; \
		echo ""; \
	else \
		echo "$(CYAN)JUnit XML reports available in $(TEST_REPORTS_DIR)/:$(NC)"; \
		ls -1 $(TEST_REPORTS_DIR)/*.xml 2>/dev/null | sed 's/^/  ✓ /' || echo "  No reports found"; \
		echo ""; \
	fi

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
