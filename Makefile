# Makefile for Bifrost

# Variables
HOST ?= localhost
PORT ?= 8080
APP_DIR ?=
PROMETHEUS_LABELS ?=
LOG_STYLE ?= json
LOG_LEVEL ?= info
TEST_REPORTS_DIR ?= test-reports
GOTESTSUM_FORMAT ?= standard-verbose
FLOW ?=
VERSION ?= dev-build
LOCAL ?=
DEBUG ?=

# Colors for output
RED=\033[0;31m
GREEN=\033[0;32m
YELLOW=\033[1;33m
BLUE=\033[0;34m
CYAN=\033[0;36m
NC=\033[0m # No Color
ECHO := printf '%b\n'

# nvm requires bash-compatible shell semantics; /bin/sh is dash on some Linux distros.
SHELL := /bin/bash

# Ensures the Node version pinned in .nvmrc is active before any npm/node call.
# nvm is a shell function, so each recipe that needs it must inline this snippet
# via `$(USE_NODE); <your command>`.
USE_NODE = NVM_SH="$${NVM_DIR:-$$HOME/.nvm}/nvm.sh"; \
	[ -s "$$NVM_SH" ] || NVM_SH="$$(brew --prefix nvm 2>/dev/null)/nvm.sh"; \
	if [ -s "$$NVM_SH" ]; then . "$$NVM_SH" >/dev/null && nvm install >/dev/null 2>&1 && nvm use >/dev/null 2>&1; fi

.PHONY: all help dev dev-pulse build-ui build build-cli run run-cli install-air install-pulse clean test test-cli install-ui setup-workspace work-init work-clean docs docker-image docker-run cleanup-enterprise mod-tidy test-integrations-py test-integrations-ts install-playwright run-e2e run-e2e-ui run-e2e-headed

all: help

# Include deployment recipes
include recipes/fly.mk
include recipes/ecs.mk
include recipes/local-k8s.mk

# Default target
help: ## Show this help message
	@$(ECHO) "$(BLUE)Bifrost Development - Available Commands:$(NC)"
	@$(ECHO) ""
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(GREEN)%-15s$(NC) %s\n", $$1, $$2}'
	@$(ECHO) ""
	@$(ECHO) "$(YELLOW)Environment Variables:$(NC)"
	@$(ECHO) "  HOST              Server host (default: localhost)"
	@$(ECHO) "  PORT              Server port (default: 8080)"
	@$(ECHO) "  PROMETHEUS_LABELS Labels for Prometheus metrics"
	@$(ECHO) "  LOG_STYLE         Logger output format: json|pretty (default: json)"
	@$(ECHO) "  LOG_LEVEL         Logger level: debug|info|warn|error (default: info)"
	@$(ECHO) "  APP_DIR           App data directory inside container (default: /app/data)"
	@$(ECHO) "  LOCAL             Use local go.work for builds (e.g., make build LOCAL=1)"
	@$(ECHO) "  DEBUG             Enable delve debugger on port 2345 (e.g., make dev DEBUG=1, make test-core DEBUG=1, make test-governance DEBUG=1)"
	@$(ECHO) ""
	@$(ECHO) "$(YELLOW)Test Configuration:$(NC)"
	@$(ECHO) "  TEST_REPORTS_DIR  Directory for HTML test reports (default: test-reports)"
	@$(ECHO) "  GOTESTSUM_FORMAT  Test output format: testname|dots|pkgname|standard-verbose (default: standard-verbose)"
	@$(ECHO) "  TESTCASE          Exact test name to run (e.g., TestVirtualKeyTokenRateLimit)"
	@$(ECHO) "  PATTERN           Substring pattern to filter tests (alternative to TESTCASE)"
	@$(ECHO) "  FLOW              E2E test flow to run: providers|virtual-keys (default: all)"

cleanup-enterprise: ## Clean up enterprise directories if present
	@$(ECHO) "$(GREEN)Cleaning up enterprise...$(NC)"
	@if [ -d "ui/app/enterprise" ]; then rm -rf ui/app/enterprise; fi
	@$(ECHO) "$(GREEN)Enterprise cleaned up$(NC)"

install-ui: cleanup-enterprise
	@$(USE_NODE); \
	 which node > /dev/null || ($(ECHO) "$(RED)Error: Node.js is not installed. Please install Node.js first.$(NC)" && exit 1); \
	 which npm > /dev/null || ($(ECHO) "$(RED)Error: npm is not installed. Please install npm first.$(NC)" && exit 1); \
	 $(ECHO) "$(GREEN)Node.js $$(node -v) and npm $$(npm -v) are installed$(NC)"; \
	 if [ ! -d "ui/node_modules" ] || [ "ui/package.json" -nt "ui/node_modules/.package-lock.json" ] || [ "ui/package-lock.json" -nt "ui/node_modules/.package-lock.json" ]; then \
	   $(ECHO) "$(YELLOW)Dependencies changed, running npm ci...$(NC)"; \
	   cd ui && npm ci; \
	 else \
	   $(ECHO) "$(GREEN)UI dependencies up to date, skipping install$(NC)"; \
	 fi
	@$(ECHO) "$(GREEN)UI deps are in sync$(NC)"

install-air: ## Install air for hot reloading (if not already installed)
	@which air > /dev/null || ($(ECHO) "$(YELLOW)Installing air for hot reloading...$(NC)" && go install github.com/air-verse/air@latest)
	@$(ECHO) "$(GREEN)Air is ready$(NC)"

install-pulse: ## Install pulse for hot reloading (if not already installed)
	@which pulse > /dev/null || ($(ECHO) "$(YELLOW)Installing pulse for hot reloading...$(NC)" && go install github.com/Pratham-Mishra04/pulse@latest)
	@$(ECHO) "$(GREEN)Pulse is ready$(NC)"

install-delve: ## Install delve for debugging (if not already installed)
	@which dlv > /dev/null || ($(ECHO) "$(YELLOW)Installing delve for debugging...$(NC)" && go install github.com/go-delve/delve/cmd/dlv@latest)
	@$(ECHO) "$(GREEN)Delve is ready$(NC)"

install-gotestsum: ## Install gotestsum for test reporting (if not already installed)
	@which gotestsum > /dev/null || ($(ECHO) "$(YELLOW)Installing gotestsum for test reporting...$(NC)" && go install gotest.tools/gotestsum@latest)
	@$(ECHO) "$(GREEN)gotestsum is ready$(NC)"

install-junit-viewer: ## Install junit-viewer for HTML report generation (if not already installed)
	@if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
		if which junit-viewer > /dev/null 2>&1; then \
			$(ECHO) "$(GREEN)junit-viewer is already installed$(NC)"; \
		else \
			$(ECHO) "$(YELLOW)Installing junit-viewer for HTML reports...$(NC)"; \
			$(USE_NODE); \
			if npm install -g junit-viewer 2>&1; then \
				$(ECHO) "$(GREEN)junit-viewer installed successfully$(NC)"; \
			else \
				$(ECHO) "$(RED)Failed to install junit-viewer. HTML reports will be skipped.$(NC)"; \
				$(ECHO) "$(YELLOW)You can install it manually: npm install -g junit-viewer$(NC)"; \
				exit 0; \
			fi; \
		fi; \
	else \
		$(ECHO) "$(YELLOW)CI environment detected, skipping junit-viewer installation$(NC)"; \
	fi

dev: install-ui install-air setup-workspace $(if $(DEBUG),install-delve) ## Start complete development environment (UI + API with proxy)
	@$(ECHO) "$(GREEN)Starting Bifrost complete development environment...$(NC)"
	@$(ECHO) "$(YELLOW)This will start:$(NC)"
	@$(ECHO) "  1. UI development server (localhost:3000)"
	@$(ECHO) "  2. API server with UI proxy (localhost:$(PORT))"
	@$(ECHO) "$(CYAN)Access everything at: http://localhost:$(PORT)$(NC)"
	@if [ -n "$(DEBUG)" ]; then \
		$(ECHO) "$(CYAN)  3. Debugger (delve) listening on port 2345$(NC)"; \
	fi
	@if [ ! -d "transports/bifrost-http/ui" ]; then \
		$(ECHO) "$(YELLOW)Creating transports/bifrost-http/ui directory...$(NC)"; \
		mkdir -p transports/bifrost-http/ui; \
		touch transports/bifrost-http/ui/.tmp; \
	fi
	@$(ECHO) ""
	@$(ECHO) "$(YELLOW)Starting UI development server...$(NC)"
	@$(USE_NODE); if [ -n "$(DISABLE_PROFILER)" ]; then \
		$(ECHO) "$(CYAN)DevProfiler disabled for testing$(NC)"; \
		cd ui && BIFROST_DISABLE_PROFILER=1 npm run dev & \
	else \
		cd ui && npm run dev & \
	fi
	@sleep 3
	@$(ECHO) "$(YELLOW)Starting API server with UI proxy...$(NC)"
	@$(MAKE) setup-workspace >/dev/null
	@if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	if [ -n "$(DEBUG)" ]; then \
		$(ECHO) "$(CYAN)Starting with air + delve debugger on port 2345...$(NC)"; \
		$(ECHO) "$(YELLOW)Attach your debugger to localhost:2345$(NC)"; \
		cd transports/bifrost-http && BIFROST_UI_DEV=true air -c .air.debug.toml -- \
			-host "$(HOST)" \
			-port "$(PORT)" \
			-log-style "$(LOG_STYLE)" \
			-log-level "$(LOG_LEVEL)" \
			$(if $(PROMETHEUS_LABELS),-prometheus-labels "$(PROMETHEUS_LABELS)") \
			$(if $(APP_DIR),-app-dir "$(APP_DIR)"); \
	else \
		cd transports/bifrost-http && BIFROST_UI_DEV=true air -c .air.toml -- \
			-host "$(HOST)" \
			-port "$(PORT)" \
			-log-style "$(LOG_STYLE)" \
			-log-level "$(LOG_LEVEL)" \
			$(if $(PROMETHEUS_LABELS),-prometheus-labels "$(PROMETHEUS_LABELS)") \
			$(if $(APP_DIR),-app-dir "$(APP_DIR)"); \
	fi

dev-pulse: install-ui install-pulse setup-workspace $(if $(DEBUG),install-delve) ## Start complete development environment using pulse for hot reloading
	@$(ECHO) "$(GREEN)Starting Bifrost complete development environment (pulse)...$(NC)"
	@$(ECHO) "$(YELLOW)This will start:$(NC)"
	@$(ECHO) "  1. UI development server (localhost:3000)"
	@$(ECHO) "  2. API server with UI proxy (localhost:$(PORT))"
	@$(ECHO) "$(CYAN)Access everything at: http://localhost:$(PORT)$(NC)"
	@if [ -n "$(DEBUG)" ]; then \
		$(ECHO) "$(CYAN)  3. Debugger (delve) listening on port 2345$(NC)"; \
	fi
	@if [ ! -d "transports/bifrost-http/ui" ]; then \
		$(ECHO) "$(YELLOW)Creating transports/bifrost-http/ui directory...$(NC)"; \
		mkdir -p transports/bifrost-http/ui; \
		touch transports/bifrost-http/ui/.tmp; \
	fi
	@$(ECHO) ""
	@$(ECHO) "$(YELLOW)Starting UI development server...$(NC)"
	@$(USE_NODE); if [ -n "$(DISABLE_PROFILER)" ]; then \
		$(ECHO) "$(CYAN)DevProfiler disabled for testing$(NC)"; \
		cd ui && BIFROST_DISABLE_PROFILER=1 npm run dev & \
	else \
		cd ui && npm run dev & \
	fi
	@sleep 3
	@$(ECHO) "$(YELLOW)Starting API server with UI proxy...$(NC)"
	@$(MAKE) setup-workspace >/dev/null
	@if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	if [ -n "$(DEBUG)" ]; then \
		$(ECHO) "$(CYAN)Starting with pulse + delve debugger on port 2345...$(NC)"; \
		$(ECHO) "$(YELLOW)Attach your debugger to localhost:2345$(NC)"; \
		BIFROST_UI_DEV=true pulse -- \
			-host "$(HOST)" \
			-port "$(PORT)" \
			-log-style "$(LOG_STYLE)" \
			-log-level "$(LOG_LEVEL)" \
			$(if $(PROMETHEUS_LABELS),-prometheus-labels "$(PROMETHEUS_LABELS)") \
			$(if $(APP_DIR),-app-dir "$(APP_DIR)"); \
	else \
		BIFROST_UI_DEV=true pulse -- \
			-host "$(HOST)" \
			-port "$(PORT)" \
			-log-style "$(LOG_STYLE)" \
			-log-level "$(LOG_LEVEL)" \
			$(if $(PROMETHEUS_LABELS),-prometheus-labels "$(PROMETHEUS_LABELS)") \
			$(if $(APP_DIR),-app-dir "$(APP_DIR)"); \
	fi

build-ui: install-ui ## Build ui
	@$(ECHO) "$(GREEN)Building ui...$(NC)"
	@rm -rf ui/.next
	@$(USE_NODE); cd ui && npm run build && npm run copy-build

build: build-ui ## Build bifrost-http binary
	@if [ -n "$(LOCAL)" ]; then \
		$(ECHO) "$(GREEN)╔═══════════════════════════════════════════════╗$(NC)"; \
		$(ECHO) "$(GREEN)║  Building bifrost-http with local go.work...  ║$(NC)"; \
		$(ECHO) "$(GREEN)╚═══════════════════════════════════════════════╝$(NC)"; \
	else \
		$(ECHO) "$(GREEN)╔═══════════════════════════════════════╗$(NC)"; \
		$(ECHO) "$(GREEN)║  Building bifrost-http...             ║$(NC)"; \
		$(ECHO) "$(GREEN)╚═══════════════════════════════════════╝$(NC)"; \
	fi
	@if [ -n "$(DYNAMIC)" ]; then \
		$(ECHO) "$(YELLOW)Note: This will create a dynamically linked build.$(NC)"; \
	else \
		$(ECHO) "$(YELLOW)Note: This will create a statically linked build.$(NC)"; \
	fi
	@mkdir -p ./tmp
	@TARGET_OS="$(GOOS)"; \
	TARGET_ARCH="$(GOARCH)"; \
	ACTUAL_OS=$$(uname -s | tr '[:upper:]' '[:lower:]' | sed 's/darwin/darwin/;s/linux/linux/;s/mingw.*/windows/'); \
	ACTUAL_ARCH=$$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/;s/arm64/arm64/'); \
	if [ -z "$$TARGET_OS" ]; then \
		TARGET_OS=$$ACTUAL_OS; \
	fi; \
	if [ -z "$$TARGET_ARCH" ]; then \
		TARGET_ARCH=$$ACTUAL_ARCH; \
	fi; \
	HOST_OS=$$ACTUAL_OS; \
	HOST_ARCH=$$ACTUAL_ARCH; \
	$(ECHO) "$(CYAN)Host: $$HOST_OS/$$HOST_ARCH | Target: $$TARGET_OS/$$TARGET_ARCH$(NC)"; \
	if [ "$$TARGET_OS" = "linux" ] && [ "$$HOST_OS" = "linux" ]; then \
		if [ -n "$(DYNAMIC)" ]; then \
			$(ECHO) "$(CYAN)Building for $$TARGET_OS/$$TARGET_ARCH with dynamic linking...$(NC)"; \
			cd transports/bifrost-http && CGO_ENABLED=1 GOOS=$$TARGET_OS GOARCH=$$TARGET_ARCH $(if $(LOCAL),,GOWORK=off) go build \
				-ldflags="-w -s -X main.Version=v$(VERSION)" \
				-a -trimpath \
				-o ../../tmp/bifrost-http \
				.; \
		else \
			$(ECHO) "$(CYAN)Building for $$TARGET_OS/$$TARGET_ARCH with static linking...$(NC)"; \
			cd transports/bifrost-http && CGO_ENABLED=1 GOOS=$$TARGET_OS GOARCH=$$TARGET_ARCH $(if $(LOCAL),,GOWORK=off) go build \
				-ldflags="-w -s -extldflags "-static" -X main.Version=v$(VERSION)" \
				-a -trimpath \
				-tags "sqlite_static" \
				-o ../../tmp/bifrost-http \
				.; \
		fi; \
		$(ECHO) "$(GREEN)Built: tmp/bifrost-http (version: v$(VERSION))$(NC)"; \
	elif [ "$$TARGET_OS" = "$$HOST_OS" ] && [ "$$TARGET_ARCH" = "$$HOST_ARCH" ]; then \
		$(ECHO) "$(CYAN)Building for $$TARGET_OS/$$TARGET_ARCH (native build with CGO)...$(NC)"; \
		cd transports/bifrost-http && CGO_ENABLED=1 GOOS=$$TARGET_OS GOARCH=$$TARGET_ARCH $(if $(LOCAL),,GOWORK=off) go build \
			-ldflags="-w -s -X main.Version=v$(VERSION)" \
			-a -trimpath \
			-tags "sqlite_static" \
			-o ../../tmp/bifrost-http \
			.; \
		$(ECHO) "$(GREEN)Built: tmp/bifrost-http (version: v$(VERSION))$(NC)"; \
	else \
		$(ECHO) "$(YELLOW)Cross-compilation detected: $$HOST_OS/$$HOST_ARCH -> $$TARGET_OS/$$TARGET_ARCH$(NC)"; \
		$(ECHO) "$(CYAN)Using Docker for cross-compilation...$(NC)"; \
		$(MAKE) _build-with-docker TARGET_OS=$$TARGET_OS TARGET_ARCH=$$TARGET_ARCH $(if $(DYNAMIC),DYNAMIC=$(DYNAMIC)); \
	fi

build-cli: ## Build bifrost CLI binary
	@$(ECHO) "$(GREEN)Building bifrost CLI...$(NC)"
	@mkdir -p ./tmp
	@cd cli && $(if $(LOCAL),,GOWORK=off) go build -ldflags "-X main.version=v0.1.1-dev" -o ../tmp/bifrost .
	@$(ECHO) "$(GREEN)Built: tmp/bifrost$(NC)"

_build-with-docker: # Internal target for Docker-based cross-compilation
	@$(ECHO) "$(CYAN)Using Docker for cross-compilation...$(NC)"; \
	if [ "$(TARGET_OS)" = "linux" ]; then \
		if [ -n "$(DYNAMIC)" ]; then \
			$(ECHO) "$(CYAN)Building for $(TARGET_OS)/$(TARGET_ARCH) in Docker container with dynamic linking...$(NC)"; \
			docker run --rm \
				--platform linux/$(TARGET_ARCH) \
				-v "$(shell pwd):/workspace" \
				-w /workspace/transports/bifrost-http \
				-e CGO_ENABLED=1 \
				-e GOOS=$(TARGET_OS) \
				-e GOARCH=$(TARGET_ARCH) \
				 $(if $(LOCAL),,-e GOWORK=off) \
				golang:1.26.1-alpine3.23 \
				sh -c "apk add --no-cache gcc musl-dev && \
				go build \
					-ldflags='-w -s -X main.Version=v$(VERSION)' \
					-a -trimpath \
					-o ../../tmp/bifrost-http \
					."; \
		else \
			$(ECHO) "$(CYAN)Building for $(TARGET_OS)/$(TARGET_ARCH) in Docker container...$(NC)"; \
			docker run --rm \
				--platform linux/$(TARGET_ARCH) \
				-v "$(shell pwd):/workspace" \
				-w /workspace/transports/bifrost-http \
				-e CGO_ENABLED=1 \
				-e GOOS=$(TARGET_OS) \
				-e GOARCH=$(TARGET_ARCH) \
				 $(if $(LOCAL),,-e GOWORK=off) \
				golang:1.26.1-alpine3.23 \
				sh -c "apk add --no-cache gcc musl-dev && \
				go build \
					-ldflags='-w -s -extldflags "-static" -X main.Version=v$(VERSION)' \
					-a -trimpath \
					-tags sqlite_static \
					-o ../../tmp/bifrost-http \
					."; \
		fi; \
		$(ECHO) "$(GREEN)Built: tmp/bifrost-http ($(TARGET_OS)/$(TARGET_ARCH), version: v$(VERSION))$(NC)"; \
	else \
		$(ECHO) "$(RED)Error: Docker cross-compilation only supports Linux targets$(NC)"; \
		$(ECHO) "$(YELLOW)For $(TARGET_OS), please build on a native $(TARGET_OS) machine$(NC)"; \
		exit 1; \
	fi

docker-image: build-ui ## Build Docker image (LOCAL=1 to use Dockerfile.local)
	@$(ECHO) "$(GREEN)Building Docker image...$(NC)"
	$(eval GIT_SHA=$(shell git rev-parse --short HEAD))
	$(eval DOCKERFILE=$(if $(LOCAL),transports/Dockerfile.local,transports/Dockerfile))
	@docker build -f $(DOCKERFILE) -t bifrost -t bifrost:$(GIT_SHA) -t bifrost:latest .
	@$(ECHO) "$(GREEN)Docker image built: bifrost, bifrost:$(GIT_SHA), bifrost:latest (using $(DOCKERFILE))$(NC)"

docker-run: ## Run Docker container (Usage: make docker-run [CONFIG=path/to/config.json or path/to/dir/])
	@$(ECHO) "$(GREEN)Running Docker container...$(NC)"
	@CONFIG_PATH="$(abspath $(CONFIG))"; \
	if [ -n "$(CONFIG)" ]; then \
		if [ -d "$$CONFIG_PATH" ]; then \
			CONFIG_PATH="$$CONFIG_PATH/config.json"; \
		fi; \
		CONFIG_MOUNT="-v $$CONFIG_PATH:/app/data/config.json"; \
	else \
		CONFIG_MOUNT=""; \
	fi; \
	docker run -e APP_PORT=$(PORT) -e APP_HOST=0.0.0.0 -p $(PORT):$(PORT) -e LOG_LEVEL=$(LOG_LEVEL) -e LOG_STYLE=$(LOG_STYLE) -v $(shell pwd):/app/data $$CONFIG_MOUNT bifrost

docs: ## Prepare local docs
	@$(ECHO) "$(GREEN)Preparing local docs...$(NC)"
	@cd docs && npx --yes mintlify@latest dev

run: build ## Build and run bifrost-http (no hot reload)
	@$(ECHO) "$(GREEN)Running bifrost-http...$(NC)"
	@./tmp/bifrost-http \
		-host "$(HOST)" \
		-port "$(PORT)" \
		-log-style "$(LOG_STYLE)" \
		-log-level "$(LOG_LEVEL)" \
		$(if $(PROMETHEUS_LABELS),-prometheus-labels "$(PROMETHEUS_LABELS)")
		$(if $(APP_DIR),-app-dir "$(APP_DIR)")

run-cli: build-cli ## Run bifrost CLI (Usage: make run-cli [ARGS="--config ~/.bifrost/config.json"])
	@$(ECHO) "$(GREEN)Running bifrost CLI...$(NC)"
	@./tmp/bifrost $(ARGS)

clean: ## Clean build artifacts and temporary files
	@$(ECHO) "$(YELLOW)Cleaning build artifacts...$(NC)"
	@rm -rf tmp/
	@rm -f transports/bifrost-http/build-errors.log
	@rm -rf transports/bifrost-http/tmp/
	@rm -rf $(TEST_REPORTS_DIR)/
	@$(ECHO) "$(GREEN)Clean complete$(NC)"

clean-test-reports: ## Clean test reports only
	@$(ECHO) "$(YELLOW)Cleaning test reports...$(NC)"
	@rm -rf $(TEST_REPORTS_DIR)/
	@$(ECHO) "$(GREEN)Test reports cleaned$(NC)"

generate-html-reports: ## Convert existing XML reports to HTML
	@if ! which junit-viewer > /dev/null 2>&1; then \
		$(ECHO) "$(RED)Error: junit-viewer not installed$(NC)"; \
		$(ECHO) "$(YELLOW)Install with: make install-junit-viewer$(NC)"; \
		exit 1; \
	fi
	@$(ECHO) "$(GREEN)Converting XML reports to HTML...$(NC)"
	@if [ ! -d "$(TEST_REPORTS_DIR)" ] || [ -z "$$(ls -A $(TEST_REPORTS_DIR)/*.xml 2>/dev/null)" ]; then \
		$(ECHO) "$(YELLOW)No XML reports found in $(TEST_REPORTS_DIR)$(NC)"; \
		$(ECHO) "$(YELLOW)Run tests first: make test-all$(NC)"; \
		exit 0; \
	fi
	@for xml in $(TEST_REPORTS_DIR)/*.xml; do \
		html=$${xml%.xml}.html; \
		$(ECHO) "  Converting $$(basename $$xml) → $$(basename $$html)"; \
		junit-viewer --results=$$xml --save=$$html 2>/dev/null || true; \
	done
	@$(ECHO) ""
	@$(ECHO) "$(GREEN)✓ HTML reports generated$(NC)"
	@$(ECHO) "$(CYAN)View reports:$(NC)"
	@ls -1 $(TEST_REPORTS_DIR)/*.html 2>/dev/null | sed 's|$(TEST_REPORTS_DIR)/|  open $(TEST_REPORTS_DIR)/|' || true

test: install-gotestsum ## Run tests for bifrost-http
	@$(ECHO) "$(GREEN)Running bifrost-http tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@cd transports/bifrost-http && GOWORK=off gotestsum \
		--format=$(GOTESTSUM_FORMAT) \
		--junitfile=../../$(TEST_REPORTS_DIR)/bifrost-http.xml \
		-- -v ./...
	@if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
		if which junit-viewer > /dev/null 2>&1; then \
			$(ECHO) "$(YELLOW)Generating HTML report...$(NC)"; \
			if junit-viewer --results=$(TEST_REPORTS_DIR)/bifrost-http.xml --save=$(TEST_REPORTS_DIR)/bifrost-http.html 2>/dev/null; then \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)HTML report: $(TEST_REPORTS_DIR)/bifrost-http.html$(NC)"; \
				$(ECHO) "$(CYAN)Open with: open $(TEST_REPORTS_DIR)/bifrost-http.html$(NC)"; \
			else \
				$(ECHO) "$(YELLOW)HTML generation failed. JUnit XML report available.$(NC)"; \
				$(ECHO) "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/bifrost-http.xml$(NC)"; \
			fi; \
		else \
			$(ECHO) ""; \
			$(ECHO) "$(YELLOW)junit-viewer not installed. Install with: make install-junit-viewer$(NC)"; \
			$(ECHO) "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/bifrost-http.xml$(NC)"; \
		fi; \
	else \
		$(ECHO) ""; \
		$(ECHO) "$(CYAN)JUnit XML report: $(TEST_REPORTS_DIR)/bifrost-http.xml$(NC)"; \
	fi

test-core: install-gotestsum $(if $(DEBUG),install-delve) ## Run core tests (Usage: make test-core PROVIDER=openai TESTCASE=TestName or PATTERN=substring, DEBUG=1 for debugger)
	@$(ECHO) "$(GREEN)Running core tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@if [ -n "$(PATTERN)" ] && [ -n "$(TESTCASE)" ]; then \
		$(ECHO) "$(RED)Error: PATTERN and TESTCASE are mutually exclusive$(NC)"; \
		$(ECHO) "$(YELLOW)Use PATTERN for substring matching or TESTCASE for exact match$(NC)"; \
		exit 1; \
	fi
	@TEST_FAILED=0; \
	REPORT_FILE=""; \
	if [ -n "$(PROVIDER)" ]; then \
		$(ECHO) "$(CYAN)Running tests for provider: $(PROVIDER)$(NC)"; \
		if [ ! -f "core/providers/$(PROVIDER)/$(PROVIDER)_test.go" ]; then \
			$(ECHO) "$(RED)Error: Provider test file '$(PROVIDER)_test.go' not found in core/providers/$(PROVIDER)/$(NC)"; \
			$(ECHO) "$(YELLOW)Available providers:$(NC)"; \
			find core/providers -name "*_test.go" -type f 2>/dev/null | sed 's|core/providers/\([^/]*\)/.*|\1|' | sort -u | sed 's/^/  - /'; \
			exit 1; \
		fi; \
	fi; \
	if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	if [ -n "$(DEBUG)" ]; then \
		$(ECHO) "$(CYAN)Debug mode enabled - delve debugger will listen on port 2345$(NC)"; \
		$(ECHO) "$(YELLOW)Attach your debugger to localhost:2345$(NC)"; \
	fi; \
	if [ -n "$(PROVIDER)" ]; then \
		PROVIDER_TEST_NAME=$$($(ECHO) "$(PROVIDER)" | awk '{print toupper(substr($$0,1,1)) tolower(substr($$0,2))}' | sed 's/openai/OpenAI/i; s/sgl/SGL/i'); \
		if [ -n "$(TESTCASE)" ]; then \
			CLEAN_TESTCASE="$(TESTCASE)"; \
			CLEAN_TESTCASE=$${CLEAN_TESTCASE#Test$${PROVIDER_TEST_NAME}/}; \
			CLEAN_TESTCASE=$${CLEAN_TESTCASE#$${PROVIDER_TEST_NAME}Tests/}; \
			CLEAN_TESTCASE=$$($(ECHO) "$$CLEAN_TESTCASE" | sed 's|^Test[A-Z][A-Za-z]*/[A-Z][A-Za-z]*Tests/||'); \
			$(ECHO) "$(CYAN)Running Test$${PROVIDER_TEST_NAME}/$${PROVIDER_TEST_NAME}Tests/$$CLEAN_TESTCASE...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/core-$(PROVIDER)-$$(echo $$CLEAN_TESTCASE | sed 's|/|_|g').xml"; \
			if [ -n "$(DEBUG)" ]; then \
				cd core/providers/$(PROVIDER) && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 -- -test.v -test.run "^Test$${PROVIDER_TEST_NAME}$$/.*Tests/$$CLEAN_TESTCASE$$" || TEST_FAILED=1; \
			else \
				cd core/providers/$(PROVIDER) && GOWORK=off gotestsum \
					--format=$(GOTESTSUM_FORMAT) \
					--junitfile=../../../$$REPORT_FILE \
					-- -v -timeout 20m -run "^Test$${PROVIDER_TEST_NAME}$$/.*Tests/$$CLEAN_TESTCASE$$" || TEST_FAILED=1; \
			fi; \
			cd ../../..; \
			$(MAKE) cleanup-junit-xml REPORT_FILE=$$REPORT_FILE; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					$(ECHO) "$(YELLOW)Generating HTML report...$(NC)"; \
					junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
					$(ECHO) ""; \
					$(ECHO) "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
					$(ECHO) "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
				else \
					$(ECHO) ""; \
					$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
				fi; \
			else \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		elif [ -n "$(PATTERN)" ]; then \
			$(ECHO) "$(CYAN)Running tests matching '$(PATTERN)' for $${PROVIDER_TEST_NAME}...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/core-$(PROVIDER)-$(PATTERN).xml"; \
			if [ -n "$(DEBUG)" ]; then \
				cd core/providers/$(PROVIDER) && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 -- -test.v -test.run ".*$(PATTERN).*" || TEST_FAILED=1; \
			else \
				cd core/providers/$(PROVIDER) && GOWORK=off gotestsum \
					--format=$(GOTESTSUM_FORMAT) \
					--junitfile=../../../$$REPORT_FILE \
					-- -v -timeout 20m -run ".*$(PATTERN).*" || TEST_FAILED=1; \
			fi; \
			cd ../../..; \
			$(MAKE) cleanup-junit-xml REPORT_FILE=$$REPORT_FILE; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					$(ECHO) "$(YELLOW)Generating HTML report...$(NC)"; \
					junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
					$(ECHO) ""; \
					$(ECHO) "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
					$(ECHO) "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
				else \
					$(ECHO) ""; \
					$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
				fi; \
			else \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			$(ECHO) "$(CYAN)Running Test$${PROVIDER_TEST_NAME}...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/core-$(PROVIDER).xml"; \
			if [ -n "$(DEBUG)" ]; then \
				cd core/providers/$(PROVIDER) && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 -- -test.v -test.run "^Test$${PROVIDER_TEST_NAME}$$" || TEST_FAILED=1; \
			else \
				cd core/providers/$(PROVIDER) && GOWORK=off gotestsum \
					--format=$(GOTESTSUM_FORMAT) \
					--junitfile=../../../$$REPORT_FILE \
					-- -v -timeout 20m -run "^Test$${PROVIDER_TEST_NAME}$$" || TEST_FAILED=1; \
			fi; \
			cd ../../..; \
			$(MAKE) cleanup-junit-xml REPORT_FILE=$$REPORT_FILE; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					$(ECHO) "$(YELLOW)Generating HTML report...$(NC)"; \
					junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
					$(ECHO) ""; \
					$(ECHO) "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
					$(ECHO) "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
				else \
					$(ECHO) ""; \
					$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
				fi; \
			else \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		fi; \
	else \
		if [ -n "$(TESTCASE)" ]; then \
			$(ECHO) "$(RED)Error: TESTCASE requires PROVIDER to be specified$(NC)"; \
			$(ECHO) "$(YELLOW)Usage: make test-core PROVIDER=openai TESTCASE=SpeechSynthesisStreamAdvanced/MultipleVoices_Streaming/StreamingVoice_echo$(NC)"; \
			exit 1; \
		fi; \
		if [ -n "$(PATTERN)" ]; then \
			$(ECHO) "$(CYAN)Running tests matching '$(PATTERN)' across all providers...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/core-all-$(PATTERN).xml"; \
			if [ -n "$(DEBUG)" ]; then \
				cd core && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 ./providers/... -- -test.v -test.run ".*$(PATTERN).*" || TEST_FAILED=1; \
			else \
				cd core && GOWORK=off gotestsum \
					--format=$(GOTESTSUM_FORMAT) \
					--junitfile=../$$REPORT_FILE \
					-- -v -timeout 20m -run ".*$(PATTERN).*" ./providers/... || TEST_FAILED=1; \
			fi; \
		else \
			REPORT_FILE="$(TEST_REPORTS_DIR)/core-all.xml"; \
			if [ -n "$(DEBUG)" ]; then \
				cd core && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 ./providers/... -- -test.v || TEST_FAILED=1; \
			else \
				cd core && GOWORK=off gotestsum \
					--format=$(GOTESTSUM_FORMAT) \
					--junitfile=../$$REPORT_FILE \
					-- -v ./providers/... || TEST_FAILED=1; \
			fi; \
		fi; \
		cd ..; \
		$(MAKE) cleanup-junit-xml REPORT_FILE=$$REPORT_FILE; \
		if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
			if which junit-viewer > /dev/null 2>&1; then \
				$(ECHO) "$(YELLOW)Generating HTML report...$(NC)"; \
				junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
				$(ECHO) "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
			else \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			$(ECHO) ""; \
			$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
		fi; \
	fi; \
	if [ -f "$$REPORT_FILE" ]; then \
		ALL_FAILED=$$(grep -B 1 '<failure' "$$REPORT_FILE" 2>/dev/null | \
			grep '<testcase' | \
			sed 's/.*name="\([^"]*\)".*/\1/' | \
			sort -u); \
		MAX_DEPTH=$$($(ECHO) "$$ALL_FAILED" | awk -F'/' '{print NF}' | sort -n | tail -1); \
		FAILED_TESTS=$$($(ECHO) "$$ALL_FAILED" | awk -F'/' -v max="$$MAX_DEPTH" 'NF == max'); \
		FAILURES=$$($(ECHO) "$$FAILED_TESTS" | grep -v '^$$' | wc -l | tr -d ' '); \
		if [ "$$FAILURES" -gt 0 ]; then \
			$(ECHO) ""; \
			$(ECHO) "$(RED)═══════════════════════════════════════════════════════════$(NC)"; \
			$(ECHO) "$(RED)                    FAILED TEST CASES                      $(NC)"; \
			$(ECHO) "$(RED)═══════════════════════════════════════════════════════════$(NC)"; \
			$(ECHO) ""; \
			printf "$(YELLOW)%-60s %-20s$(NC)\n" "Test Name" "Status"; \
			printf "$(YELLOW)%-60s %-20s$(NC)\n" "─────────────────────────────────────────────────────────────" "────────────────────"; \
			$(ECHO) "$$FAILED_TESTS" | while read -r testname; do \
				if [ -n "$$testname" ]; then \
					printf "$(RED)%-60s %-20s$(NC)\n" "$$testname" "FAILED"; \
				fi; \
			done; \
			$(ECHO) ""; \
			$(ECHO) "$(RED)Total Failures: $$FAILURES$(NC)"; \
			$(ECHO) ""; \
		else \
			$(ECHO) ""; \
			$(ECHO) "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"; \
			$(ECHO) "$(GREEN)                 ALL TESTS PASSED ✓                       $(NC)"; \
			$(ECHO) "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"; \
			$(ECHO) ""; \
		fi; \
	fi; \
	if [ $$TEST_FAILED -eq 1 ]; then \
		exit 1; \
	fi

cleanup-junit-xml: ## Internal: Clean up JUnit XML to remove parent test cases with child failures
	@if [ -z "$(REPORT_FILE)" ]; then \
		$(ECHO) "$(RED)Error: REPORT_FILE not specified$(NC)"; \
		exit 1; \
	fi
	@if [ ! -f "$(REPORT_FILE)" ]; then \
		exit 0; \
	fi
	@ALL_FAILED=$$(grep -B 1 '<failure' "$(REPORT_FILE)" 2>/dev/null | \
		grep '<testcase' | \
		sed 's/.*name="\([^"]*\)".*/\1/' | \
		sort -u); \
	if [ -n "$$ALL_FAILED" ]; then \
		MAX_DEPTH=$$($(ECHO) "$$ALL_FAILED" | awk -F'/' '{print NF}' | sort -n | tail -1); \
		PARENT_TESTS=$$($(ECHO) "$$ALL_FAILED" | awk -F'/' -v max="$$MAX_DEPTH" 'NF < max'); \
		if [ -n "$$PARENT_TESTS" ]; then \
			cp "$(REPORT_FILE)" "$(REPORT_FILE).tmp"; \
			$(ECHO) "$$PARENT_TESTS" | while IFS= read -r parent; do \
				if [ -n "$$parent" ]; then \
					ESCAPED=$$($(ECHO) "$$parent" | sed 's/[\/&]/\\&/g'); \
					perl -i -pe 'BEGIN{undef $$/;} s/<testcase[^>]*name="'"$$ESCAPED"'"[^>]*>.*?<failure.*?<\/testcase>//gs' "$(REPORT_FILE).tmp" 2>/dev/null || true; \
				fi; \
			done; \
			if [ -f "$(REPORT_FILE).tmp" ]; then \
				mv "$(REPORT_FILE).tmp" "$(REPORT_FILE)"; \
			fi; \
		fi; \
	fi

test-plugins: install-gotestsum ## Run plugin tests
	@$(ECHO) "$(GREEN)Running plugin tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	cd plugins && find . -name "*.go" -path "*/tests/*" -o -name "*_test.go" | head -1 > /dev/null && \
		for dir in $$(find . -name "*_test.go" -exec dirname {} \; | sort -u); do \
			plugin_name=$$(echo $$dir | sed 's|^\./||' | sed 's|/|-|g'); \
			$(ECHO) "Testing $$dir..."; \
			cd $$dir && gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../$(TEST_REPORTS_DIR)/plugin-$$plugin_name.xml \
				-- -v ./... && cd - > /dev/null; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					$(ECHO) "$(YELLOW)Generating HTML report for $$plugin_name...$(NC)"; \
					junit-viewer --results=../$(TEST_REPORTS_DIR)/plugin-$$plugin_name.xml --save=../$(TEST_REPORTS_DIR)/plugin-$$plugin_name.html 2>/dev/null || true; \
				fi; \
			fi; \
		done || $(ECHO) "No plugin tests found"
	@$(ECHO) ""
	@if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
		$(ECHO) "$(CYAN)HTML reports saved to $(TEST_REPORTS_DIR)/plugin-*.html$(NC)"; \
	else \
		$(ECHO) "$(CYAN)JUnit XML reports saved to $(TEST_REPORTS_DIR)/plugin-*.xml$(NC)"; \
	fi

test-framework: install-gotestsum ## Run framework tests
	@$(ECHO) "$(GREEN)Running framework tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	cd framework && find . -name "*.go" -path "*/tests/*" -o -name "*_test.go" | head -1 > /dev/null && \
		for dir in $$(find . -name "*_test.go" -exec dirname {} \; | sort -u); do \
			pkg_name=$$(echo $$dir | sed 's|^\./||' | sed 's|/|-|g'); \
			$(ECHO) "Testing $$dir..."; \
			cd $$dir && gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../$(TEST_REPORTS_DIR)/framework-$$pkg_name.xml \
				-- -v ./... && cd - > /dev/null; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					$(ECHO) "$(YELLOW)Generating HTML report for $$pkg_name...$(NC)"; \
					junit-viewer --results=../$(TEST_REPORTS_DIR)/framework-$$pkg_name.xml --save=../$(TEST_REPORTS_DIR)/framework-$$pkg_name.html 2>/dev/null || true; \
				fi; \
			fi; \
		done || $(ECHO) "No framework tests found"
	@$(ECHO) ""
	@if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
		$(ECHO) "$(CYAN)HTML reports saved to $(TEST_REPORTS_DIR)/framework-*.html$(NC)"; \
	else \
		$(ECHO) "$(CYAN)JUnit XML reports saved to $(TEST_REPORTS_DIR)/framework-*.xml$(NC)"; \
	fi

test-http-transport: install-gotestsum ## Run HTTP transport tests
	@$(ECHO) "$(GREEN)Running HTTP transport tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	cd transports/bifrost-http && find . -name "*.go" -path "*/tests/*" -o -name "*_test.go" | head -1 > /dev/null && \
		for dir in $$(find . -name "*_test.go" -exec dirname {} \; | sort -u); do \
			pkg_name=$$(echo $$dir | sed 's|^\./||' | sed 's|/|-|g'); \
			$(ECHO) "Testing $$dir..."; \
			cd $$dir && gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../../$(TEST_REPORTS_DIR)/http-transport-$$pkg_name.xml \
				-- -v ./... && cd - > /dev/null; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					$(ECHO) "$(YELLOW)Generating HTML report for $$pkg_name...$(NC)"; \
					junit-viewer --results=../../$(TEST_REPORTS_DIR)/http-transport-$$pkg_name.xml --save=../../$(TEST_REPORTS_DIR)/http-transport-$$pkg_name.html 2>/dev/null || true; \
				fi; \
			fi; \
		done || $(ECHO) "No HTTP transport tests found"
	@$(ECHO) ""
	@if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
		$(ECHO) "$(CYAN)HTML reports saved to $(TEST_REPORTS_DIR)/http-transport-*.html$(NC)"; \
	else \
		$(ECHO) "$(CYAN)JUnit XML reports saved to $(TEST_REPORTS_DIR)/http-transport-*.xml$(NC)"; \
	fi

test-governance: install-gotestsum $(if $(DEBUG),install-delve) ## Run governance tests (Usage: make test-governance TESTCASE=TestName or PATTERN=substring, DEBUG=1 for debugger)
	@$(ECHO) "$(GREEN)Running governance tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@if [ -n "$(PATTERN)" ] && [ -n "$(TESTCASE)" ]; then \
		$(ECHO) "$(RED)Error: PATTERN and TESTCASE are mutually exclusive$(NC)"; \
		$(ECHO) "$(YELLOW)Use PATTERN for substring matching or TESTCASE for exact match$(NC)"; \
		exit 1; \
	fi
	@if [ ! -d "tests/governance" ]; then \
		$(ECHO) "$(RED)Error: Governance tests directory not found$(NC)"; \
		exit 1; \
	fi
	@TEST_FAILED=0; \
	REPORT_FILE=""; \
	if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	if [ -n "$(DEBUG)" ]; then \
		$(ECHO) "$(CYAN)Debug mode enabled - delve debugger will listen on port 2345$(NC)"; \
		$(ECHO) "$(YELLOW)Attach your debugger to localhost:2345$(NC)"; \
	fi; \
	if [ -n "$(TESTCASE)" ]; then \
		$(ECHO) "$(CYAN)Running test case: $(TESTCASE)$(NC)"; \
		REPORT_FILE="$(TEST_REPORTS_DIR)/governance-$$(echo $(TESTCASE) | sed 's|/|_|g').xml"; \
		if [ -n "$(DEBUG)" ]; then \
			cd tests/governance && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 -- -test.v -test.run "^$(TESTCASE)$$" || TEST_FAILED=1; \
		else \
			cd tests/governance && GOWORK=off gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../$$REPORT_FILE \
				-- -v -run "^$(TESTCASE)$$" || TEST_FAILED=1; \
		fi; \
		cd ../..; \
		$(MAKE) cleanup-junit-xml REPORT_FILE=$$REPORT_FILE; \
		if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
			if which junit-viewer > /dev/null 2>&1; then \
				$(ECHO) "$(YELLOW)Generating HTML report...$(NC)"; \
				junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
				$(ECHO) "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
			else \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			$(ECHO) ""; \
			$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
		fi; \
	elif [ -n "$(PATTERN)" ]; then \
		$(ECHO) "$(CYAN)Running tests matching '$(PATTERN)'...$(NC)"; \
		REPORT_FILE="$(TEST_REPORTS_DIR)/governance-$(PATTERN).xml"; \
		if [ -n "$(DEBUG)" ]; then \
			cd tests/governance && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 -- -test.v -test.run ".*$(PATTERN).*" || TEST_FAILED=1; \
		else \
			cd tests/governance && GOWORK=off gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../$$REPORT_FILE \
				-- -v -run ".*$(PATTERN).*" || TEST_FAILED=1; \
		fi; \
		cd ../..; \
		$(MAKE) cleanup-junit-xml REPORT_FILE=$$REPORT_FILE; \
		if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
			if which junit-viewer > /dev/null 2>&1; then \
				$(ECHO) "$(YELLOW)Generating HTML report...$(NC)"; \
				junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
				$(ECHO) "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
			else \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			$(ECHO) ""; \
			$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
		fi; \
	else \
		$(ECHO) "$(CYAN)Running all governance tests...$(NC)"; \
		REPORT_FILE="$(TEST_REPORTS_DIR)/governance-all.xml"; \
		if [ -n "$(DEBUG)" ]; then \
			cd tests/governance && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 -- -test.v || TEST_FAILED=1; \
		else \
			cd tests/governance && GOWORK=off gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../$$REPORT_FILE \
				-- -v || TEST_FAILED=1; \
		fi; \
		cd ../..; \
		$(MAKE) cleanup-junit-xml REPORT_FILE=$$REPORT_FILE; \
		if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
			if which junit-viewer > /dev/null 2>&1; then \
				$(ECHO) "$(YELLOW)Generating HTML report...$(NC)"; \
				junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
				$(ECHO) "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
			else \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			$(ECHO) ""; \
			$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
		fi; \
	fi; \
	if [ -f "$$REPORT_FILE" ]; then \
		ALL_FAILED=$$(grep -B 1 '<failure' "$$REPORT_FILE" 2>/dev/null | \
			grep '<testcase' | \
			sed 's/.*name="\([^"]*\)".*/\1/' | \
			sort -u); \
		MAX_DEPTH=$$($(ECHO) "$$ALL_FAILED" | awk -F'/' '{print NF}' | sort -n | tail -1); \
		FAILED_TESTS=$$($(ECHO) "$$ALL_FAILED" | awk -F'/' -v max="$$MAX_DEPTH" 'NF == max'); \
		FAILURES=$$($(ECHO) "$$FAILED_TESTS" | grep -v '^$$' | wc -l | tr -d ' '); \
		if [ "$$FAILURES" -gt 0 ]; then \
			$(ECHO) ""; \
			$(ECHO) "$(RED)═══════════════════════════════════════════════════════════$(NC)"; \
			$(ECHO) "$(RED)                    FAILED TEST CASES                      $(NC)"; \
			$(ECHO) "$(RED)═══════════════════════════════════════════════════════════$(NC)"; \
			$(ECHO) ""; \
			printf "$(YELLOW)%-60s %-20s$(NC)\n" "Test Name" "Status"; \
			printf "$(YELLOW)%-60s %-20s$(NC)\n" "─────────────────────────────────────────────────────────────" "────────────────────"; \
			$(ECHO) "$$FAILED_TESTS" | while read -r testname; do \
				if [ -n "$$testname" ]; then \
					printf "$(RED)%-60s %-20s$(NC)\n" "$$testname" "FAILED"; \
				fi; \
			done; \
			$(ECHO) ""; \
			$(ECHO) "$(RED)Total Failures: $$FAILURES$(NC)"; \
			$(ECHO) ""; \
		else \
			$(ECHO) ""; \
			$(ECHO) "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"; \
			$(ECHO) "$(GREEN)                 ALL TESTS PASSED ✓                       $(NC)"; \
			$(ECHO) "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"; \
			$(ECHO) ""; \
		fi; \
	fi; \
	if [ $$TEST_FAILED -eq 1 ]; then \
		exit 1; \
	fi

setup-mcp-tests: ## Build all MCP test servers in examples/mcps/ (Go and TypeScript)
	@$(ECHO) "$(GREEN)Building MCP test servers...$(NC)"
	@$(USE_NODE); \
	FAILED=0; \
	for mcp_dir in examples/mcps/*/; do \
		if [ -d "$$mcp_dir" ]; then \
			mcp_name=$$(basename $$mcp_dir); \
			if [ -f "$$mcp_dir/go.mod" ]; then \
				$(ECHO) "$(CYAN)Building $$mcp_name (Go)...$(NC)"; \
				mkdir -p "$$mcp_dir/bin"; \
				if cd "$$mcp_dir" && GOWORK=off go build -o bin/$$mcp_name . && cd - > /dev/null; then \
					$(ECHO) "$(GREEN)  ✓ $$mcp_name$(NC)"; \
				else \
					$(ECHO) "$(RED)  ✗ $$mcp_name failed$(NC)"; \
					FAILED=1; \
					cd - > /dev/null 2>&1 || true; \
				fi; \
			elif [ -f "$$mcp_dir/package.json" ]; then \
				$(ECHO) "$(CYAN)Building $$mcp_name (TypeScript)...$(NC)"; \
				if cd "$$mcp_dir" && npm install --silent && npm run build && cd - > /dev/null; then \
					$(ECHO) "$(GREEN)  ✓ $$mcp_name$(NC)"; \
				else \
					$(ECHO) "$(RED)  ✗ $$mcp_name failed$(NC)"; \
					FAILED=1; \
					cd - > /dev/null 2>&1 || true; \
				fi; \
			fi; \
		fi; \
	done; \
	if [ $$FAILED -eq 1 ]; then \
		$(ECHO) "$(RED)Some MCP test servers failed to build$(NC)"; \
		exit 1; \
	fi
	@$(ECHO) ""
	@$(ECHO) "$(GREEN)✓ All MCP test servers built$(NC)"

test-mcp: install-gotestsum setup-mcp-tests ## Run MCP tests (Usage: make test-mcp [TYPE=connection] [TESTCASE=TestName] [PATTERN=substring])
	@$(ECHO) "$(GREEN)Running MCP tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@if [ -n "$(PATTERN)" ] && [ -n "$(TESTCASE)" ]; then \
		$(ECHO) "$(RED)Error: PATTERN and TESTCASE are mutually exclusive$(NC)"; \
		$(ECHO) "$(YELLOW)Use PATTERN for substring matching or TESTCASE for exact match$(NC)"; \
		exit 1; \
	fi
	@if [ ! -d "core/internal/mcptests" ]; then \
		$(ECHO) "$(RED)Error: MCP tests directory not found$(NC)"; \
		exit 1; \
	fi
	@TEST_FAILED=0; \
	REPORT_FILE=""; \
	if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	if [ -n "$(TYPE)" ]; then \
		TYPE_CLEAN=$$(echo $(TYPE) | sed 's/_test\.go$$//'); \
		TEST_FILE="core/internal/mcptests/$${TYPE_CLEAN}_test.go"; \
		if [ ! -f "$$TEST_FILE" ]; then \
			$(ECHO) "$(RED)Error: Test file '$$TEST_FILE' not found$(NC)"; \
			$(ECHO) "$(YELLOW)Available test types:$(NC)"; \
			ls -1 core/internal/mcptests/*_test.go 2>/dev/null | sed 's|core/internal/mcptests/||' | sed 's|_test\.go$$||' | sed 's/^/  - /'; \
			exit 1; \
		fi; \
		TEST_PATTERN=$$(grep -h "^func Test" $$TEST_FILE 2>/dev/null | sed 's/func \(Test[^(]*\).*/\1/' | paste -sd '|' - || $(ECHO) "^Test"); \
		if [ -n "$(TESTCASE)" ]; then \
			$(ECHO) "$(CYAN)Running $(TYPE) test: $(TESTCASE)...$(NC)"; \
			SAFE_TESTCASE=$$($(ECHO) "$(TESTCASE)" | sed 's|/|_|g'); \
			REPORT_FILE="$(TEST_REPORTS_DIR)/mcp-$(TYPE)-$$SAFE_TESTCASE.xml"; \
			cd core/internal/mcptests && GOWORK=off gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../../$$REPORT_FILE \
				-- -v -race -run "^$(TESTCASE)$$" . || TEST_FAILED=1; \
		elif [ -n "$(PATTERN)" ]; then \
			$(ECHO) "$(CYAN)Running $(TYPE) tests matching '$(PATTERN)'...$(NC)"; \
			SAFE_PATTERN=$$($(ECHO) "$(PATTERN)" | sed 's|/|_|g'); \
			REPORT_FILE="$(TEST_REPORTS_DIR)/mcp-$(TYPE)-$$SAFE_PATTERN.xml"; \
			cd core/internal/mcptests && GOWORK=off gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../../$$REPORT_FILE \
				-- -v -race -run ".*$(PATTERN).*" . || TEST_FAILED=1; \
		else \
			$(ECHO) "$(CYAN)Running all $(TYPE) tests (pattern: $$TEST_PATTERN)...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/mcp-$(TYPE).xml"; \
			cd core/internal/mcptests && GOWORK=off gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../../$$REPORT_FILE \
				-- -v -race -run "$$TEST_PATTERN" . || TEST_FAILED=1; \
		fi; \
		cd ../../..; \
		if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
			if which junit-viewer > /dev/null 2>&1; then \
				$(ECHO) "$(YELLOW)Generating HTML report...$(NC)"; \
				junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
				$(ECHO) "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
			else \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			$(ECHO) ""; \
			$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
		fi; \
	else \
		if [ -n "$(TESTCASE)" ]; then \
			$(ECHO) "$(CYAN)Running test case: $(TESTCASE) across all MCP tests...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/mcp-all-$(TESTCASE).xml"; \
			cd core/internal/mcptests && GOWORK=off gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../../$$REPORT_FILE \
				-- -v -race -run "^$(TESTCASE)$$" || TEST_FAILED=1; \
		elif [ -n "$(PATTERN)" ]; then \
			$(ECHO) "$(CYAN)Running tests matching '$(PATTERN)' across all MCP tests...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/mcp-all-$(PATTERN).xml"; \
			cd core/internal/mcptests && GOWORK=off gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../../$$REPORT_FILE \
				-- -v -race -run ".*$(PATTERN).*" || TEST_FAILED=1; \
		else \
			$(ECHO) "$(CYAN)Running all MCP tests...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/mcp-all.xml"; \
			cd core/internal/mcptests && GOWORK=off gotestsum \
				--format=$(GOTESTSUM_FORMAT) \
				--junitfile=../../../$$REPORT_FILE \
				-- -v -race || TEST_FAILED=1; \
		fi; \
		cd ../../..; \
		if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
			if which junit-viewer > /dev/null 2>&1; then \
				$(ECHO) "$(YELLOW)Generating HTML report...$(NC)"; \
				junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
				$(ECHO) "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
			else \
				$(ECHO) ""; \
				$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			$(ECHO) ""; \
			$(ECHO) "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
		fi; \
	fi; \
	if [ $$TEST_FAILED -eq 1 ]; then \
		exit 1; \
	fi

test-all: test-core test-framework test-plugins test-http-transport test test-cli ## Run all tests
	@$(ECHO) ""
	@$(ECHO) "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"
	@$(ECHO) "$(GREEN)              All Tests Complete - Summary                 $(NC)"
	@$(ECHO) "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"
	@$(ECHO) ""
	@if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
		$(ECHO) "$(YELLOW)Generating combined HTML report...$(NC)"; \
		junit-viewer --results=$(TEST_REPORTS_DIR) --save=$(TEST_REPORTS_DIR)/index.html 2>/dev/null || true; \
		$(ECHO) ""; \
		$(ECHO) "$(CYAN)HTML reports available in $(TEST_REPORTS_DIR)/:$(NC)"; \
		ls -1 $(TEST_REPORTS_DIR)/*.html 2>/dev/null | sed 's/^/  ✓ /' || $(ECHO) "  No reports found"; \
		$(ECHO) ""; \
		$(ECHO) "$(YELLOW)📊 View all test results:$(NC)"; \
		$(ECHO) "$(CYAN)  open $(TEST_REPORTS_DIR)/index.html$(NC)"; \
		$(ECHO) ""; \
		$(ECHO) "$(YELLOW)Or view individual reports:$(NC)"; \
		ls -1 $(TEST_REPORTS_DIR)/*.html 2>/dev/null | grep -v index.html | sed 's|$(TEST_REPORTS_DIR)/|  open $(TEST_REPORTS_DIR)/|' || true; \
		$(ECHO) ""; \
	else \
		$(ECHO) "$(CYAN)JUnit XML reports available in $(TEST_REPORTS_DIR)/:$(NC)"; \
		ls -1 $(TEST_REPORTS_DIR)/*.xml 2>/dev/null | sed 's/^/  ✓ /' || $(ECHO) "  No reports found"; \
		$(ECHO) ""; \
	fi

test-chatbot: ## Run interactive chatbot integration test (Usage: RUN_CHATBOT_TEST=1 make test-chatbot)
	@$(ECHO) "$(GREEN)Running interactive chatbot integration test...$(NC)"
	@if [ -z "$(RUN_CHATBOT_TEST)" ]; then \
		$(ECHO) "$(YELLOW)⚠️  This is an interactive test. Set RUN_CHATBOT_TEST=1 to run it.$(NC)"; \
		$(ECHO) "$(CYAN)Usage: RUN_CHATBOT_TEST=1 make test-chatbot$(NC)"; \
		$(ECHO) ""; \
		$(ECHO) "$(YELLOW)Required environment variables:$(NC)"; \
		$(ECHO) "  - OPENAI_API_KEY (required)"; \
		$(ECHO) "  - ANTHROPIC_API_KEY (optional)"; \
		$(ECHO) "  - Additional provider keys as needed"; \
		exit 0; \
	fi
	@if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi
	@cd core && RUN_CHATBOT_TEST=1 go test -v -run TestChatbot

test-integrations-py: ## Run Python integration tests (Usage: make test-integrations-py [INTEGRATION=openai] [TESTCASE=test_name] [PATTERN=substring] [VERBOSE=1])
	@$(ECHO) "$(GREEN)Running Python integration tests...$(NC)"
	@if [ ! -d "tests/integrations/python" ]; then \
		$(ECHO) "$(RED)Error: tests/integrations/python directory not found$(NC)"; \
		exit 1; \
	fi; \
	if [ -n "$(PATTERN)" ] && [ -n "$(TESTCASE)" ]; then \
		$(ECHO) "$(RED)Error: PATTERN and TESTCASE are mutually exclusive$(NC)"; \
		$(ECHO) "$(YELLOW)Use PATTERN for substring matching or TESTCASE for exact match$(NC)"; \
		exit 1; \
	fi; \
	if [ -n "$(TESTCASE)" ] && [ -z "$(INTEGRATION)" ]; then \
		$(ECHO) "$(RED)Error: TESTCASE requires INTEGRATION to be specified$(NC)"; \
		$(ECHO) "$(YELLOW)Usage: make test-integrations-py INTEGRATION=anthropic TESTCASE=test_05_end2end_tool_calling$(NC)"; \
		exit 1; \
	fi; \
	if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	BIFROST_STARTED=0; \
	BIFROST_PID=""; \
	TAIL_PID=""; \
	TEST_PORT=$${PORT:-8080}; \
	TEST_HOST=$${HOST:-localhost}; \
	$(ECHO) "$(CYAN)Checking if Bifrost is running on $$TEST_HOST:$$TEST_PORT...$(NC)"; \
	if curl -s -o /dev/null -w "%{http_code}" http://$$TEST_HOST:$$TEST_PORT/health 2>/dev/null | grep -q "200\|404"; then \
		$(ECHO) "$(GREEN)✓ Bifrost is already running$(NC)"; \
	else \
		$(ECHO) "$(YELLOW)Bifrost not running, starting it...$(NC)"; \
		./tmp/bifrost-http -host "$$TEST_HOST" -port "$$TEST_PORT" -log-style "$(LOG_STYLE)" -log-level "$(LOG_LEVEL)" -app-dir tests/integrations/python > /tmp/bifrost-test.log 2>&1 & \
		BIFROST_PID=$$!; \
		BIFROST_STARTED=1; \
		$(ECHO) "$(YELLOW)Waiting for Bifrost to be ready...$(NC)"; \
		$(ECHO) "$(CYAN)Bifrost logs: /tmp/bifrost-test.log$(NC)"; \
		(tail -f /tmp/bifrost-test.log 2>/dev/null | grep -E "error|panic|Error|ERRO|fatal|Fatal|FATAL" --line-buffered &) & \
		TAIL_PID=$$!; \
		for i in 1 2 3 4 5 6 7 8 9 10; do \
			if curl -s -o /dev/null http://$$TEST_HOST:$$TEST_PORT/health 2>/dev/null; then \
				$(ECHO) "$(GREEN)✓ Bifrost is ready (PID: $$BIFROST_PID)$(NC)"; \
				break; \
			fi; \
			if [ $$i -eq 10 ]; then \
				$(ECHO) "$(RED)Failed to start Bifrost$(NC)"; \
				$(ECHO) "$(YELLOW)Bifrost logs:$(NC)"; \
				cat /tmp/bifrost-test.log 2>/dev/null || $(ECHO) "No log file found"; \
				[ -n "$$BIFROST_PID" ] && kill $$BIFROST_PID 2>/dev/null; \
				[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null; \
				exit 1; \
			fi; \
			sleep 1; \
		done; \
	fi; \
	TEST_FAILED=0; \
	if ! which uv > /dev/null 2>&1; then \
		$(ECHO) "$(YELLOW)uv not found, checking for pytest...$(NC)"; \
		if ! which pytest > /dev/null 2>&1; then \
			$(ECHO) "$(RED)Error: Neither uv nor pytest found$(NC)"; \
			$(ECHO) "$(YELLOW)Install uv: curl -LsSf https://astral.sh/uv/install.sh | sh$(NC)"; \
			$(ECHO) "$(YELLOW)Or install pytest: pip install pytest$(NC)"; \
			[ $$BIFROST_STARTED -eq 1 ] && [ -n "$$BIFROST_PID" ] && kill $$BIFROST_PID 2>/dev/null; \
			[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null; \
			exit 1; \
		fi; \
		$(ECHO) "$(CYAN)Using pytest directly$(NC)"; \
		if [ -n "$(INTEGRATION)" ]; then \
			if [ -n "$(TESTCASE)" ]; then \
				$(ECHO) "$(CYAN)Running $(INTEGRATION) integration test: $(TESTCASE)...$(NC)"; \
				cd tests/integrations/python && pytest tests/test_$(INTEGRATION).py::$(TESTCASE) $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			elif [ -n "$(PATTERN)" ]; then \
				$(ECHO) "$(CYAN)Running $(INTEGRATION) integration tests matching '$(PATTERN)'...$(NC)"; \
				cd tests/integrations/python && pytest tests/test_$(INTEGRATION).py -k "$(PATTERN)" $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			else \
				$(ECHO) "$(CYAN)Running $(INTEGRATION) integration tests...$(NC)"; \
				cd tests/integrations/python && pytest tests/test_$(INTEGRATION).py $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			fi; \
		else \
			if [ -n "$(PATTERN)" ]; then \
				$(ECHO) "$(CYAN)Running all integration tests matching '$(PATTERN)'...$(NC)"; \
				cd tests/integrations/python && pytest -k "$(PATTERN)" $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			else \
				$(ECHO) "$(CYAN)Running all integration tests...$(NC)"; \
				cd tests/integrations/python && pytest $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			fi; \
		fi; \
	else \
		$(ECHO) "$(CYAN)Using uv (fast mode)$(NC)"; \
		cd tests/integrations/python && \
		if [ -n "$(INTEGRATION)" ]; then \
			if [ -n "$(TESTCASE)" ]; then \
				$(ECHO) "$(CYAN)Running $(INTEGRATION) integration test: $(TESTCASE)...$(NC)"; \
				uv run pytest tests/test_$(INTEGRATION).py::$(TESTCASE) $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			elif [ -n "$(PATTERN)" ]; then \
				$(ECHO) "$(CYAN)Running $(INTEGRATION) integration tests matching '$(PATTERN)'...$(NC)"; \
				uv run pytest tests/test_$(INTEGRATION).py -k "$(PATTERN)" $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			else \
				$(ECHO) "$(CYAN)Running $(INTEGRATION) integration tests...$(NC)"; \
				uv run pytest tests/test_$(INTEGRATION).py $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			fi; \
		else \
			if [ -n "$(PATTERN)" ]; then \
				$(ECHO) "$(CYAN)Running all integration tests matching '$(PATTERN)'...$(NC)"; \
				uv run pytest -k "$(PATTERN)" $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			else \
				$(ECHO) "$(CYAN)Running all integration tests...$(NC)"; \
				uv run pytest $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			fi; \
		fi; \
	fi; \
	if [ $$BIFROST_STARTED -eq 1 ] && [ -n "$$BIFROST_PID" ]; then \
		$(ECHO) "$(YELLOW)Stopping Bifrost (PID: $$BIFROST_PID)...$(NC)"; \
		kill $$BIFROST_PID 2>/dev/null || true; \
		[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null || true; \
		wait $$BIFROST_PID 2>/dev/null || true; \
		$(ECHO) "$(GREEN)✓ Bifrost stopped$(NC)"; \
		if [ $$TEST_FAILED -eq 1 ]; then \
			$(ECHO) ""; \
			$(ECHO) "$(YELLOW)Last 50 lines of Bifrost logs:$(NC)"; \
			tail -50 /tmp/bifrost-test.log 2>/dev/null || $(ECHO) "No log file found"; \
		fi; \
	fi; \
	$(ECHO) ""; \
	if [ $$TEST_FAILED -eq 1 ]; then \
		$(ECHO) "$(RED)✗ Integration tests failed$(NC)"; \
		$(ECHO) "$(CYAN)Full Bifrost logs: /tmp/bifrost-test.log$(NC)"; \
		exit 1; \
	else \
		$(ECHO) "$(GREEN)✓ Integration tests complete$(NC)"; \
	fi

test-integrations-ts: ## Run TypeScript integration tests (Usage: make test-integrations-ts [INTEGRATION=openai] [TESTCASE=test_name] [PATTERN=substring] [VERBOSE=1])
	@$(ECHO) "$(GREEN)Running TypeScript integration tests...$(NC)"
	@if [ ! -d "tests/integrations/typescript" ]; then \
		$(ECHO) "$(RED)Error: tests/integrations/typescript directory not found$(NC)"; \
		exit 1; \
	fi; \
	if [ -n "$(PATTERN)" ] && [ -n "$(TESTCASE)" ]; then \
		$(ECHO) "$(RED)Error: PATTERN and TESTCASE are mutually exclusive$(NC)"; \
		$(ECHO) "$(YELLOW)Use PATTERN for substring matching or TESTCASE for exact match$(NC)"; \
		exit 1; \
	fi; \
	if [ -n "$(TESTCASE)" ] && [ -z "$(INTEGRATION)" ]; then \
		$(ECHO) "$(RED)Error: TESTCASE requires INTEGRATION to be specified$(NC)"; \
		$(ECHO) "$(YELLOW)Usage: make test-integrations-ts INTEGRATION=openai TESTCASE=test_simple_chat$(NC)"; \
		exit 1; \
	fi; \
	if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	BIFROST_STARTED=0; \
	BIFROST_PID=""; \
	TAIL_PID=""; \
	TEST_PORT=$${PORT:-8080}; \
	TEST_HOST=$${HOST:-localhost}; \
	$(ECHO) "$(CYAN)Checking if Bifrost is running on $$TEST_HOST:$$TEST_PORT...$(NC)"; \
	if curl -s -o /dev/null -w "%{http_code}" http://$$TEST_HOST:$$TEST_PORT/health 2>/dev/null | grep -q "200\|404"; then \
		$(ECHO) "$(GREEN)✓ Bifrost is already running$(NC)"; \
	else \
		$(ECHO) "$(YELLOW)Bifrost not running, starting it...$(NC)"; \
		./tmp/bifrost-http -host "$$TEST_HOST" -port "$$TEST_PORT" -log-style "$(LOG_STYLE)" -log-level "$(LOG_LEVEL)" -app-dir tests/integrations/typescript > /tmp/bifrost-test.log 2>&1 & \
		BIFROST_PID=$$!; \
		BIFROST_STARTED=1; \
		$(ECHO) "$(YELLOW)Waiting for Bifrost to be ready...$(NC)"; \
		$(ECHO) "$(CYAN)Bifrost logs: /tmp/bifrost-test.log$(NC)"; \
		(tail -f /tmp/bifrost-test.log 2>/dev/null | grep -E "error|panic|Error|ERRO|fatal|Fatal|FATAL" --line-buffered &) & \
		TAIL_PID=$$!; \
		for i in 1 2 3 4 5 6 7 8 9 10; do \
			if curl -s -o /dev/null http://$$TEST_HOST:$$TEST_PORT/health 2>/dev/null; then \
				$(ECHO) "$(GREEN)✓ Bifrost is ready (PID: $$BIFROST_PID)$(NC)"; \
				break; \
			fi; \
			if [ $$i -eq 10 ]; then \
				$(ECHO) "$(RED)Failed to start Bifrost$(NC)"; \
				$(ECHO) "$(YELLOW)Bifrost logs:$(NC)"; \
				cat /tmp/bifrost-test.log 2>/dev/null || $(ECHO) "No log file found"; \
				[ -n "$$BIFROST_PID" ] && kill $$BIFROST_PID 2>/dev/null; \
				[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null; \
				exit 1; \
			fi; \
			sleep 1; \
		done; \
	fi; \
	TEST_FAILED=0; \
	$(USE_NODE); \
	if ! which npm > /dev/null 2>&1; then \
		$(ECHO) "$(RED)Error: npm not found$(NC)"; \
		$(ECHO) "$(YELLOW)Install Node.js: https://nodejs.org/$(NC)"; \
		[ $$BIFROST_STARTED -eq 1 ] && [ -n "$$BIFROST_PID" ] && kill $$BIFROST_PID 2>/dev/null; \
		[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null; \
		exit 1; \
	fi; \
	$(ECHO) "$(CYAN)Using npm$(NC)"; \
	cd tests/integrations/typescript && \
	if [ ! -d "node_modules" ]; then \
		$(ECHO) "$(YELLOW)Installing dependencies...$(NC)"; \
		npm install; \
	fi; \
	if [ -n "$(INTEGRATION)" ]; then \
		if [ -n "$(TESTCASE)" ]; then \
			$(ECHO) "$(CYAN)Running $(INTEGRATION) integration test: $(TESTCASE)...$(NC)"; \
			npm test -- tests/test-$(INTEGRATION).test.ts -t "$(TESTCASE)" $(if $(VERBOSE),--reporter=verbose,) || TEST_FAILED=1; \
		elif [ -n "$(PATTERN)" ]; then \
			$(ECHO) "$(CYAN)Running $(INTEGRATION) integration tests matching '$(PATTERN)'...$(NC)"; \
			npm test -- tests/test-$(INTEGRATION).test.ts -t "$(PATTERN)" $(if $(VERBOSE),--reporter=verbose,) || TEST_FAILED=1; \
		else \
			$(ECHO) "$(CYAN)Running $(INTEGRATION) integration tests...$(NC)"; \
			npm test -- tests/test-$(INTEGRATION).test.ts $(if $(VERBOSE),--reporter=verbose,) || TEST_FAILED=1; \
		fi; \
	else \
		if [ -n "$(PATTERN)" ]; then \
			$(ECHO) "$(CYAN)Running all integration tests matching '$(PATTERN)'...$(NC)"; \
			npm test -- -t "$(PATTERN)" $(if $(VERBOSE),--reporter=verbose,) || TEST_FAILED=1; \
		else \
			$(ECHO) "$(CYAN)Running all integration tests...$(NC)"; \
			npm test $(if $(VERBOSE),-- --reporter=verbose,) || TEST_FAILED=1; \
		fi; \
	fi; \
	if [ $$BIFROST_STARTED -eq 1 ] && [ -n "$$BIFROST_PID" ]; then \
		$(ECHO) "$(YELLOW)Stopping Bifrost (PID: $$BIFROST_PID)...$(NC)"; \
		kill $$BIFROST_PID 2>/dev/null || true; \
		[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null || true; \
		wait $$BIFROST_PID 2>/dev/null || true; \
		$(ECHO) "$(GREEN)✓ Bifrost stopped$(NC)"; \
		if [ $$TEST_FAILED -eq 1 ]; then \
			$(ECHO) ""; \
			$(ECHO) "$(YELLOW)Last 50 lines of Bifrost logs:$(NC)"; \
			tail -50 /tmp/bifrost-test.log 2>/dev/null || $(ECHO) "No log file found"; \
		fi; \
	fi; \
	$(ECHO) ""; \
	if [ $$TEST_FAILED -eq 1 ]; then \
		$(ECHO) "$(RED)✗ TypeScript integration tests failed$(NC)"; \
		$(ECHO) "$(CYAN)Full Bifrost logs: /tmp/bifrost-test.log$(NC)"; \
		exit 1; \
	else \
		$(ECHO) "$(GREEN)✓ TypeScript integration tests complete$(NC)"; \
	fi

install-playwright: ## Install Playwright test dependencies
	@$(ECHO) "$(GREEN)Installing Playwright dependencies...$(NC)"
	@which node > /dev/null || ($(ECHO) "$(RED)Error: Node.js is not installed. Please install Node.js first.$(NC)" && exit 1)
	@which npm > /dev/null || ($(ECHO) "$(RED)Error: npm is not installed. Please install npm first.$(NC)" && exit 1)
	@$(USE_NODE); cd tests/e2e && npm ci
	@cd tests/e2e && if npx playwright install --list 2>/dev/null | grep -q "chromium"; then \
		$(ECHO) "$(CYAN)Chromium is already installed, skipping download$(NC)"; \
	else \
		$(ECHO) "$(CYAN)Installing Chromium...$(NC)"; \
		npx playwright install --with-deps chromium; \
	fi
	@$(ECHO) "$(GREEN)Playwright is ready$(NC)"

build-test-plugin: ## Build test plugin for E2E tests (copies to tmp/bifrost-test-plugin.so)
	@$(ECHO) "$(GREEN)Building test plugin for E2E tests...$(NC)"
	@cd examples/plugins/hello-world && make dev
	@mkdir -p tmp
	@cp examples/plugins/hello-world/build/hello-world.so tmp/bifrost-test-plugin.so
	@$(ECHO) "$(GREEN)✓ Test plugin ready at tmp/bifrost-test-plugin.so$(NC)"

run-e2e: install-playwright ## Run E2E tests (Usage: make run-e2e [FLOW=providers|virtual-keys|config])
	@$(ECHO) "$(GREEN)Running Playwright E2E tests...$(NC)"
	@if [ -n "$(FLOW)" ]; then \
		$(ECHO) "$(CYAN)Running $(FLOW) tests...$(NC)"; \
		if [ "$(FLOW)" = "config" ]; then \
			cd tests/e2e && npx playwright test --project=chromium-config; \
		else \
			cd tests/e2e && npx playwright test features/$(FLOW); \
		fi; \
	else \
		$(ECHO) "$(CYAN)Running all E2E tests...$(NC)"; \
		cd tests/e2e && npx playwright test; \
	fi
	@$(ECHO) ""
	@$(ECHO) "$(GREEN)E2E tests complete$(NC)"
	@$(ECHO) "$(CYAN)View HTML report: cd tests/e2e && npx playwright show-report$(NC)"

run-e2e-ui: install-playwright ## Run E2E tests in interactive UI mode
	@if [ -f .env ]; then \
		$(ECHO) "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	$(ECHO) "$(GREEN)Opening Playwright UI...$(NC)"; \
	cd tests/e2e && npx playwright test --ui

run-e2e-headed: install-playwright ## Run E2E tests in headed browser mode
	@$(ECHO) "$(GREEN)Running E2E tests in headed mode...$(NC)"
	@if [ -n "$(FLOW)" ]; then \
		$(ECHO) "$(CYAN)Running $(FLOW) tests (headed)...$(NC)"; \
		if [ "$(FLOW)" = "config" ]; then \
			cd tests/e2e && npx playwright test --project=chromium-config --headed; \
		else \
			cd tests/e2e && npx playwright test features/$(FLOW) --headed; \
		fi; \
	else \
		$(ECHO) "$(CYAN)Running all E2E tests (headed)...$(NC)"; \
		cd tests/e2e && npx playwright test --headed; \
	fi

# Quick start with example config
quick-start: ## Quick start with example config and maxim plugin
	@$(ECHO) "$(GREEN)Quick starting Bifrost with example configuration...$(NC)"
	@$(MAKE) dev

# Linting and formatting
lint: ## Run linter for Go code
	@$(ECHO) "$(GREEN)Running golangci-lint...$(NC)"
	@golangci-lint run ./...

fmt: ## Format Go code
	@$(ECHO) "$(GREEN)Formatting Go code...$(NC)"
	@gofmt -s -w .
	@goimports -w .

# Workspace helpers
setup-workspace: ## Set up Go workspace with all local modules for development
	@$(ECHO) "$(GREEN)Setting up Go workspace for local development...$(NC)"
	@$(ECHO) "$(YELLOW)Cleaning existing workspace...$(NC)"
	@rm -f go.work go.work.sum || true
	@$(ECHO) "$(YELLOW)Initializing new workspace...$(NC)"
	@go work init ./cli ./core ./framework ./transports
	@$(ECHO) "$(YELLOW)Adding plugin modules...$(NC)"
	@for plugin_dir in ./plugins/*/; do \
		if [ -d "$$plugin_dir" ] && [ -f "$$plugin_dir/go.mod" ]; then \
			$(ECHO) "  Adding plugin: $$(basename $$plugin_dir)"; \
			go work use "$$plugin_dir"; \
		fi; \
	done
	@$(ECHO) "$(YELLOW)Syncing workspace...$(NC)"
	@go work sync
	@$(ECHO) "$(GREEN)✓ Go workspace ready with all local modules$(NC)"
	@$(ECHO) ""
	@$(ECHO) "$(CYAN)Local modules in workspace:$(NC)"
	@go list -m all | grep "github.com/maximhq/bifrost" | grep -v " v" | sed 's/^/  ✓ /'
	@$(ECHO) ""
	@$(ECHO) "$(CYAN)Remote modules (no local version):$(NC)"
	@go list -m all | grep "github.com/maximhq/bifrost" | grep " v" | sed 's/^/  → /'
	@$(ECHO) ""
	@$(ECHO) "$(YELLOW)Note: go.work files are not committed to version control$(NC)"

work-init: ## Create local go.work to use local modules for development (legacy)
	@$(ECHO) "$(YELLOW)⚠️  work-init is deprecated, use 'make setup-workspace' instead$(NC)"
	@$(MAKE) setup-workspace

work-clean: ## Remove local go.work
	@rm -f go.work go.work.sum || true
	@$(ECHO) "$(GREEN)Removed local go.work files$(NC)"

# Module parameter for mod-tidy (all/core/plugins/framework/transport)
MODULE ?= all

mod-tidy: ## Run go mod tidy on modules (Usage: make mod-tidy [MODULE=all|cli|core|plugins|framework|transport])
	@$(ECHO) "$(GREEN)Running go mod tidy...$(NC)"
	@if [ "$(MODULE)" = "all" ] || [ "$(MODULE)" = "cli" ]; then \
		$(ECHO) "$(CYAN)Tidying cli...$(NC)"; \
		cd cli && $(if $(LOCAL),,GOWORK=off) go mod tidy && $(ECHO) "$(GREEN)  ✓ cli$(NC)"; \
	fi
	@if [ "$(MODULE)" = "all" ] || [ "$(MODULE)" = "core" ]; then \
		$(ECHO) "$(CYAN)Tidying core...$(NC)"; \
		cd core && go mod tidy && $(ECHO) "$(GREEN)  ✓ core$(NC)"; \
	fi
	@if [ "$(MODULE)" = "all" ] || [ "$(MODULE)" = "framework" ]; then \
		$(ECHO) "$(CYAN)Tidying framework...$(NC)"; \
		cd framework && go mod tidy && $(ECHO) "$(GREEN)  ✓ framework$(NC)"; \
	fi
	@if [ "$(MODULE)" = "all" ] || [ "$(MODULE)" = "transport" ]; then \
		$(ECHO) "$(CYAN)Tidying transports...$(NC)"; \
		cd transports && go mod tidy && $(ECHO) "$(GREEN)  ✓ transports$(NC)"; \
	fi
	@if [ "$(MODULE)" = "all" ] || [ "$(MODULE)" = "plugins" ]; then \
		$(ECHO) "$(CYAN)Tidying plugins...$(NC)"; \
		for plugin_dir in ./plugins/*/; do \
			if [ -d "$$plugin_dir" ] && [ -f "$$plugin_dir/go.mod" ]; then \
				plugin_name=$$(basename $$plugin_dir); \
				cd $$plugin_dir && go mod tidy && cd ../.. && $(ECHO) "$(GREEN)  ✓ plugins/$$plugin_name$(NC)"; \
			fi; \
		done; \
	fi
	@$(ECHO) ""
	@$(ECHO) "$(GREEN)✓ go mod tidy complete$(NC)"

test-cli: install-gotestsum ## Run CLI tests
	@$(ECHO) "$(GREEN)Running CLI tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@cd cli && GOWORK=off gotestsum \
		--format=$(GOTESTSUM_FORMAT) \
		--junitfile=../$(TEST_REPORTS_DIR)/cli.xml \
		-- ./...
