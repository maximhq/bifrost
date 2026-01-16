# Makefile for Bifrost

# Variables
HOST ?= localhost
PORT ?= 8080
APP_DIR ?= 
PROMETHEUS_LABELS ?=
LOG_STYLE ?= json
LOG_LEVEL ?= info
TEST_REPORTS_DIR ?= test-reports
GOTESTSUM_FORMAT ?= testname
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

# Include deployment recipes
include recipes/fly.mk
include recipes/ecs.mk
include recipes/local-k8s.mk

.PHONY: all help dev build-ui build run install-air clean test install-ui setup-workspace work-init work-clean docs build-docker-image cleanup-enterprise mod-tidy test-integrations-py test-integrations-ts

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
	@echo "  LOCAL             Use local go.work for builds (e.g., make build LOCAL=1)"
	@echo "  DEBUG             Enable delve debugger on port 2345 (e.g., make dev DEBUG=1, make test-core DEBUG=1, make test-governance DEBUG=1)"
	@echo ""
	@echo "$(YELLOW)Test Configuration:$(NC)"
	@echo "  TEST_REPORTS_DIR  Directory for HTML test reports (default: test-reports)"
	@echo "  GOTESTSUM_FORMAT  Test output format: testname|dots|pkgname|standard-verbose (default: testname)"
	@echo "  TESTCASE          Exact test name to run (e.g., TestVirtualKeyTokenRateLimit)"
	@echo "  PATTERN           Substring pattern to filter tests (alternative to TESTCASE)"

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

install-delve: ## Install delve for debugging (if not already installed)
	@which dlv > /dev/null || (echo "$(YELLOW)Installing delve for debugging...$(NC)" && go install github.com/go-delve/delve/cmd/dlv@latest)
	@echo "$(GREEN)Delve is ready$(NC)"

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

dev: install-ui install-air setup-workspace $(if $(DEBUG),install-delve) ## Start complete development environment (UI + API with proxy)
	@echo "$(GREEN)Starting Bifrost complete development environment...$(NC)"
	@echo "$(YELLOW)This will start:$(NC)"
	@echo "  1. UI development server (localhost:3000)"
	@echo "  2. API server with UI proxy (localhost:$(PORT))"
	@echo "$(CYAN)Access everything at: http://localhost:$(PORT)$(NC)"
	@if [ -n "$(DEBUG)" ]; then \
		echo "$(CYAN)  3. Debugger (delve) listening on port 2345$(NC)"; \
	fi
	@echo ""
	@echo "$(YELLOW)Starting UI development server...$(NC)"
	@cd ui && npm run dev &
	@sleep 3
	@echo "$(YELLOW)Starting API server with UI proxy...$(NC)"
	@$(MAKE) setup-workspace >/dev/null
	@if [ -f .env ]; then \
		echo "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	if [ -n "$(DEBUG)" ]; then \
		echo "$(CYAN)Starting with air + delve debugger on port 2345...$(NC)"; \
		echo "$(YELLOW)Attach your debugger to localhost:2345$(NC)"; \
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

build-ui: install-ui ## Build ui
	@echo "$(GREEN)Building ui...$(NC)"
	@rm -rf ui/.next
	@cd ui && npm run build && npm run copy-build

build: build-ui ## Build bifrost-http binary
	@if [ -n "$(LOCAL)" ]; then \
		echo "$(GREEN)╔═══════════════════════════════════════════════╗$(NC)"; \
		echo "$(GREEN)║  Building bifrost-http with local go.work...  ║$(NC)"; \
		echo "$(GREEN)╚═══════════════════════════════════════════════╝$(NC)"; \
	else \
		echo "$(GREEN)╔═══════════════════════════════════════╗$(NC)"; \
		echo "$(GREEN)║  Building bifrost-http...             ║$(NC)"; \
		echo "$(GREEN)╚═══════════════════════════════════════╝$(NC)"; \
	fi
	@if [ -n "$(DYNAMIC)" ]; then \
		echo "$(YELLOW)Note: This will create a dynamically linked build.$(NC)"; \
	else \
		echo "$(YELLOW)Note: This will create a statically linked build.$(NC)"; \
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
	echo "$(CYAN)Host: $$HOST_OS/$$HOST_ARCH | Target: $$TARGET_OS/$$TARGET_ARCH$(NC)"; \
	if [ "$$TARGET_OS" = "linux" ] && [ "$$HOST_OS" = "linux" ]; then \
		if [ -n "$(DYNAMIC)" ]; then \
			echo "$(CYAN)Building for $$TARGET_OS/$$TARGET_ARCH with dynamic linking...$(NC)"; \
			cd transports/bifrost-http && CGO_ENABLED=1 GOOS=$$TARGET_OS GOARCH=$$TARGET_ARCH $(if $(LOCAL),,GOWORK=off) go build \
				-ldflags="-w -s -X main.Version=v$(VERSION)" \
				-a -trimpath \
				-o ../../tmp/bifrost-http \
				.; \
		else \
			echo "$(CYAN)Building for $$TARGET_OS/$$TARGET_ARCH with static linking...$(NC)"; \
			cd transports/bifrost-http && CGO_ENABLED=1 GOOS=$$TARGET_OS GOARCH=$$TARGET_ARCH $(if $(LOCAL),,GOWORK=off) go build \
				-ldflags="-w -s -extldflags "-static" -X main.Version=v$(VERSION)" \
				-a -trimpath \
				-tags "sqlite_static" \
				-o ../../tmp/bifrost-http \
				.; \
		fi; \
		echo "$(GREEN)Built: tmp/bifrost-http (version: v$(VERSION))$(NC)"; \
	elif [ "$$TARGET_OS" = "$$HOST_OS" ] && [ "$$TARGET_ARCH" = "$$HOST_ARCH" ]; then \
		echo "$(CYAN)Building for $$TARGET_OS/$$TARGET_ARCH (native build with CGO)...$(NC)"; \
		cd transports/bifrost-http && CGO_ENABLED=1 GOOS=$$TARGET_OS GOARCH=$$TARGET_ARCH $(if $(LOCAL),,GOWORK=off) go build \
			-ldflags="-w -s -X main.Version=v$(VERSION)" \
			-a -trimpath \
			-tags "sqlite_static" \
			-o ../../tmp/bifrost-http \
			.; \
		echo "$(GREEN)Built: tmp/bifrost-http (version: v$(VERSION))$(NC)"; \
	else \
		echo "$(YELLOW)Cross-compilation detected: $$HOST_OS/$$HOST_ARCH -> $$TARGET_OS/$$TARGET_ARCH$(NC)"; \
		echo "$(CYAN)Using Docker for cross-compilation...$(NC)"; \
		$(MAKE) _build-with-docker TARGET_OS=$$TARGET_OS TARGET_ARCH=$$TARGET_ARCH $(if $(DYNAMIC),DYNAMIC=$(DYNAMIC)); \
	fi

_build-with-docker: # Internal target for Docker-based cross-compilation
	@echo "$(CYAN)Using Docker for cross-compilation...$(NC)"; \
	if [ "$(TARGET_OS)" = "linux" ]; then \
		if [ -n "$(DYNAMIC)" ]; then \
			echo "$(CYAN)Building for $(TARGET_OS)/$(TARGET_ARCH) in Docker container with dynamic linking...$(NC)"; \
			docker run --rm \
				--platform linux/$(TARGET_ARCH) \
				-v "$(shell pwd):/workspace" \
				-w /workspace/transports/bifrost-http \
				-e CGO_ENABLED=1 \
				-e GOOS=$(TARGET_OS) \
				-e GOARCH=$(TARGET_ARCH) \
				 $(if $(LOCAL),,-e GOWORK=off) \
				golang:1.25.5-alpine3.22 \
				sh -c "apk add --no-cache gcc musl-dev && \
				go build \
					-ldflags='-w -s -X main.Version=v$(VERSION)' \
					-a -trimpath \
					-o ../../tmp/bifrost-http \
					."; \
		else \
			echo "$(CYAN)Building for $(TARGET_OS)/$(TARGET_ARCH) in Docker container...$(NC)"; \
			docker run --rm \
				--platform linux/$(TARGET_ARCH) \
				-v "$(shell pwd):/workspace" \
				-w /workspace/transports/bifrost-http \
				-e CGO_ENABLED=1 \
				-e GOOS=$(TARGET_OS) \
				-e GOARCH=$(TARGET_ARCH) \
				 $(if $(LOCAL),,-e GOWORK=off) \
				golang:1.25.5-alpine3.22 \
				sh -c "apk add --no-cache gcc musl-dev && \
				go build \
					-ldflags='-w -s -extldflags "-static" -X main.Version=v$(VERSION)' \
					-a -trimpath \
					-tags sqlite_static \
					-o ../../tmp/bifrost-http \
					."; \
		fi; \
		echo "$(GREEN)Built: tmp/bifrost-http ($(TARGET_OS)/$(TARGET_ARCH), version: v$(VERSION))$(NC)"; \
	else \
		echo "$(RED)Error: Docker cross-compilation only supports Linux targets$(NC)"; \
		echo "$(YELLOW)For $(TARGET_OS), please build on a native $(TARGET_OS) machine$(NC)"; \
		exit 1; \
	fi

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

test-core: install-gotestsum $(if $(DEBUG),install-delve) ## Run core tests (Usage: make test-core PROVIDER=openai TESTCASE=TestName or PATTERN=substring, DEBUG=1 for debugger)
	@echo "$(GREEN)Running core tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@if [ -n "$(PATTERN)" ] && [ -n "$(TESTCASE)" ]; then \
		echo "$(RED)Error: PATTERN and TESTCASE are mutually exclusive$(NC)"; \
		echo "$(YELLOW)Use PATTERN for substring matching or TESTCASE for exact match$(NC)"; \
		exit 1; \
	fi
	@TEST_FAILED=0; \
	REPORT_FILE=""; \
	if [ -n "$(PROVIDER)" ]; then \
		echo "$(CYAN)Running tests for provider: $(PROVIDER)$(NC)"; \
		if [ ! -f "core/providers/$(PROVIDER)/$(PROVIDER)_test.go" ]; then \
			echo "$(RED)Error: Provider test file '$(PROVIDER)_test.go' not found in core/providers/$(PROVIDER)/$(NC)"; \
			echo "$(YELLOW)Available providers:$(NC)"; \
			find core/providers -name "*_test.go" -type f 2>/dev/null | sed 's|core/providers/\([^/]*\)/.*|\1|' | sort -u | sed 's/^/  - /'; \
			exit 1; \
		fi; \
	fi; \
	if [ -f .env ]; then \
		echo "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	if [ -n "$(DEBUG)" ]; then \
		echo "$(CYAN)Debug mode enabled - delve debugger will listen on port 2345$(NC)"; \
		echo "$(YELLOW)Attach your debugger to localhost:2345$(NC)"; \
	fi; \
	if [ -n "$(PROVIDER)" ]; then \
		PROVIDER_TEST_NAME=$$(echo "$(PROVIDER)" | awk '{print toupper(substr($$0,1,1)) tolower(substr($$0,2))}' | sed 's/openai/OpenAI/i; s/sgl/SGL/i'); \
		if [ -n "$(TESTCASE)" ]; then \
			CLEAN_TESTCASE="$(TESTCASE)"; \
			CLEAN_TESTCASE=$${CLEAN_TESTCASE#Test$${PROVIDER_TEST_NAME}/}; \
			CLEAN_TESTCASE=$${CLEAN_TESTCASE#$${PROVIDER_TEST_NAME}Tests/}; \
			CLEAN_TESTCASE=$$(echo "$$CLEAN_TESTCASE" | sed 's|^Test[A-Z][A-Za-z]*/[A-Z][A-Za-z]*Tests/||'); \
			echo "$(CYAN)Running Test$${PROVIDER_TEST_NAME}/$${PROVIDER_TEST_NAME}Tests/$$CLEAN_TESTCASE...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/core-$(PROVIDER)-$$(echo $$CLEAN_TESTCASE | sed 's|/|_|g').xml"; \
			if [ -n "$(DEBUG)" ]; then \
				cd core/providers/$(PROVIDER) && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 -- -test.v -test.run "^Test$${PROVIDER_TEST_NAME}$$/.*Tests/$$CLEAN_TESTCASE$$" || TEST_FAILED=1; \
			else \
				cd core/providers/$(PROVIDER) && GOWORK=off gotestsum \
					--format=$(GOTESTSUM_FORMAT) \
					--junitfile=../../../$$REPORT_FILE \
					-- -v -run "^Test$${PROVIDER_TEST_NAME}$$/.*Tests/$$CLEAN_TESTCASE$$" || TEST_FAILED=1; \
			fi; \
			cd ../../..; \
			$(MAKE) cleanup-junit-xml REPORT_FILE=$$REPORT_FILE; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					echo "$(YELLOW)Generating HTML report...$(NC)"; \
					junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
					echo ""; \
					echo "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
					echo "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
				else \
					echo ""; \
					echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
				fi; \
			else \
				echo ""; \
				echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		elif [ -n "$(PATTERN)" ]; then \
			echo "$(CYAN)Running tests matching '$(PATTERN)' for $${PROVIDER_TEST_NAME}...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/core-$(PROVIDER)-$(PATTERN).xml"; \
			if [ -n "$(DEBUG)" ]; then \
				cd core/providers/$(PROVIDER) && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 -- -test.v -test.run ".*$(PATTERN).*" || TEST_FAILED=1; \
			else \
				cd core/providers/$(PROVIDER) && GOWORK=off gotestsum \
					--format=$(GOTESTSUM_FORMAT) \
					--junitfile=../../../$$REPORT_FILE \
					-- -v -run ".*$(PATTERN).*" || TEST_FAILED=1; \
			fi; \
			cd ../../..; \
			$(MAKE) cleanup-junit-xml REPORT_FILE=$$REPORT_FILE; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					echo "$(YELLOW)Generating HTML report...$(NC)"; \
					junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
					echo ""; \
					echo "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
					echo "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
				else \
					echo ""; \
					echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
				fi; \
			else \
				echo ""; \
				echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			echo "$(CYAN)Running Test$${PROVIDER_TEST_NAME}...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/core-$(PROVIDER).xml"; \
			if [ -n "$(DEBUG)" ]; then \
				cd core/providers/$(PROVIDER) && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 -- -test.v -test.run "^Test$${PROVIDER_TEST_NAME}$$" || TEST_FAILED=1; \
			else \
				cd core/providers/$(PROVIDER) && GOWORK=off gotestsum \
					--format=$(GOTESTSUM_FORMAT) \
					--junitfile=../../../$$REPORT_FILE \
					-- -v -run "^Test$${PROVIDER_TEST_NAME}$$" || TEST_FAILED=1; \
			fi; \
			cd ../../..; \
			$(MAKE) cleanup-junit-xml REPORT_FILE=$$REPORT_FILE; \
			if [ -z "$$CI" ] && [ -z "$$GITHUB_ACTIONS" ] && [ -z "$$GITLAB_CI" ] && [ -z "$$CIRCLECI" ] && [ -z "$$JENKINS_HOME" ]; then \
				if which junit-viewer > /dev/null 2>&1; then \
					echo "$(YELLOW)Generating HTML report...$(NC)"; \
					junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
					echo ""; \
					echo "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
					echo "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
				else \
					echo ""; \
					echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
				fi; \
			else \
				echo ""; \
				echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		fi \
	else \
		if [ -n "$(TESTCASE)" ]; then \
			echo "$(RED)Error: TESTCASE requires PROVIDER to be specified$(NC)"; \
			echo "$(YELLOW)Usage: make test-core PROVIDER=openai TESTCASE=SpeechSynthesisStreamAdvanced/MultipleVoices_Streaming/StreamingVoice_echo$(NC)"; \
			exit 1; \
		fi; \
		if [ -n "$(PATTERN)" ]; then \
			echo "$(CYAN)Running tests matching '$(PATTERN)' across all providers...$(NC)"; \
			REPORT_FILE="$(TEST_REPORTS_DIR)/core-all-$(PATTERN).xml"; \
			if [ -n "$(DEBUG)" ]; then \
				cd core && GOWORK=off dlv test --headless --listen=:2345 --api-version=2 ./providers/... -- -test.v -test.run ".*$(PATTERN).*" || TEST_FAILED=1; \
			else \
				cd core && GOWORK=off gotestsum \
					--format=$(GOTESTSUM_FORMAT) \
					--junitfile=../$$REPORT_FILE \
					-- -v -run ".*$(PATTERN).*" ./providers/... || TEST_FAILED=1; \
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
				echo "$(YELLOW)Generating HTML report...$(NC)"; \
				junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
				echo ""; \
				echo "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
				echo "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
			else \
				echo ""; \
				echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			echo ""; \
			echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
		fi; \
	fi; \
	if [ -f "$$REPORT_FILE" ]; then \
		ALL_FAILED=$$(grep -B 1 '<failure' "$$REPORT_FILE" 2>/dev/null | \
			grep '<testcase' | \
			sed 's/.*name="\([^"]*\)".*/\1/' | \
			sort -u); \
		MAX_DEPTH=$$(echo "$$ALL_FAILED" | awk -F'/' '{print NF}' | sort -n | tail -1); \
		FAILED_TESTS=$$(echo "$$ALL_FAILED" | awk -F'/' -v max="$$MAX_DEPTH" 'NF == max'); \
		FAILURES=$$(echo "$$FAILED_TESTS" | grep -v '^$$' | wc -l | tr -d ' '); \
		if [ "$$FAILURES" -gt 0 ]; then \
			echo ""; \
			echo "$(RED)═══════════════════════════════════════════════════════════$(NC)"; \
			echo "$(RED)                    FAILED TEST CASES                      $(NC)"; \
			echo "$(RED)═══════════════════════════════════════════════════════════$(NC)"; \
			echo ""; \
			printf "$(YELLOW)%-60s %-20s$(NC)\n" "Test Name" "Status"; \
			printf "$(YELLOW)%-60s %-20s$(NC)\n" "─────────────────────────────────────────────────────────────" "────────────────────"; \
			echo "$$FAILED_TESTS" | while read -r testname; do \
				if [ -n "$$testname" ]; then \
					printf "$(RED)%-60s %-20s$(NC)\n" "$$testname" "FAILED"; \
				fi; \
			done; \
			echo ""; \
			echo "$(RED)Total Failures: $$FAILURES$(NC)"; \
			echo ""; \
		else \
			echo ""; \
			echo "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"; \
			echo "$(GREEN)                 ALL TESTS PASSED ✓                       $(NC)"; \
			echo "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"; \
			echo ""; \
		fi; \
	fi; \
	if [ $$TEST_FAILED -eq 1 ]; then \
		exit 1; \
	fi

cleanup-junit-xml: ## Internal: Clean up JUnit XML to remove parent test cases with child failures
	@if [ -z "$(REPORT_FILE)" ]; then \
		echo "$(RED)Error: REPORT_FILE not specified$(NC)"; \
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
		MAX_DEPTH=$$(echo "$$ALL_FAILED" | awk -F'/' '{print NF}' | sort -n | tail -1); \
		PARENT_TESTS=$$(echo "$$ALL_FAILED" | awk -F'/' -v max="$$MAX_DEPTH" 'NF < max'); \
		if [ -n "$$PARENT_TESTS" ]; then \
			cp "$(REPORT_FILE)" "$(REPORT_FILE).tmp"; \
			echo "$$PARENT_TESTS" | while IFS= read -r parent; do \
				if [ -n "$$parent" ]; then \
					ESCAPED=$$(echo "$$parent" | sed 's/[\/&]/\\&/g'); \
					perl -i -pe 'BEGIN{undef $$/;} s/<testcase[^>]*name="'"$$ESCAPED"'"[^>]*>.*?<failure.*?<\/testcase>//gs' "$(REPORT_FILE).tmp" 2>/dev/null || true; \
				fi; \
			done; \
			if [ -f "$(REPORT_FILE).tmp" ]; then \
				mv "$(REPORT_FILE).tmp" "$(REPORT_FILE)"; \
			fi; \
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

test-governance: install-gotestsum $(if $(DEBUG),install-delve) ## Run governance tests (Usage: make test-governance TESTCASE=TestName or PATTERN=substring, DEBUG=1 for debugger)
	@echo "$(GREEN)Running governance tests...$(NC)"
	@mkdir -p $(TEST_REPORTS_DIR)
	@if [ -n "$(PATTERN)" ] && [ -n "$(TESTCASE)" ]; then \
		echo "$(RED)Error: PATTERN and TESTCASE are mutually exclusive$(NC)"; \
		echo "$(YELLOW)Use PATTERN for substring matching or TESTCASE for exact match$(NC)"; \
		exit 1; \
	fi
	@if [ ! -d "tests/governance" ]; then \
		echo "$(RED)Error: Governance tests directory not found$(NC)"; \
		exit 1; \
	fi
	@TEST_FAILED=0; \
	REPORT_FILE=""; \
	if [ -f .env ]; then \
		echo "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	if [ -n "$(DEBUG)" ]; then \
		echo "$(CYAN)Debug mode enabled - delve debugger will listen on port 2345$(NC)"; \
		echo "$(YELLOW)Attach your debugger to localhost:2345$(NC)"; \
	fi; \
	if [ -n "$(TESTCASE)" ]; then \
		echo "$(CYAN)Running test case: $(TESTCASE)$(NC)"; \
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
				echo "$(YELLOW)Generating HTML report...$(NC)"; \
				junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
				echo ""; \
				echo "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
				echo "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
			else \
				echo ""; \
				echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			echo ""; \
			echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
		fi; \
	elif [ -n "$(PATTERN)" ]; then \
		echo "$(CYAN)Running tests matching '$(PATTERN)'...$(NC)"; \
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
				echo "$(YELLOW)Generating HTML report...$(NC)"; \
				junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
				echo ""; \
				echo "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
				echo "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
			else \
				echo ""; \
				echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			echo ""; \
			echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
		fi; \
	else \
		echo "$(CYAN)Running all governance tests...$(NC)"; \
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
				echo "$(YELLOW)Generating HTML report...$(NC)"; \
				junit-viewer --results=$$REPORT_FILE --save=$${REPORT_FILE%.xml}.html 2>/dev/null || true; \
				echo ""; \
				echo "$(CYAN)HTML report: $${REPORT_FILE%.xml}.html$(NC)"; \
				echo "$(CYAN)Open with: open $${REPORT_FILE%.xml}.html$(NC)"; \
			else \
				echo ""; \
				echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
			fi; \
		else \
			echo ""; \
			echo "$(CYAN)JUnit XML report: $$REPORT_FILE$(NC)"; \
		fi; \
	fi; \
	if [ -f "$$REPORT_FILE" ]; then \
		ALL_FAILED=$$(grep -B 1 '<failure' "$$REPORT_FILE" 2>/dev/null | \
			grep '<testcase' | \
			sed 's/.*name="\([^"]*\)".*/\1/' | \
			sort -u); \
		MAX_DEPTH=$$(echo "$$ALL_FAILED" | awk -F'/' '{print NF}' | sort -n | tail -1); \
		FAILED_TESTS=$$(echo "$$ALL_FAILED" | awk -F'/' -v max="$$MAX_DEPTH" 'NF == max'); \
		FAILURES=$$(echo "$$FAILED_TESTS" | grep -v '^$$' | wc -l | tr -d ' '); \
		if [ "$$FAILURES" -gt 0 ]; then \
			echo ""; \
			echo "$(RED)═══════════════════════════════════════════════════════════$(NC)"; \
			echo "$(RED)                    FAILED TEST CASES                      $(NC)"; \
			echo "$(RED)═══════════════════════════════════════════════════════════$(NC)"; \
			echo ""; \
			printf "$(YELLOW)%-60s %-20s$(NC)\n" "Test Name" "Status"; \
			printf "$(YELLOW)%-60s %-20s$(NC)\n" "─────────────────────────────────────────────────────────────" "────────────────────"; \
			echo "$$FAILED_TESTS" | while read -r testname; do \
				if [ -n "$$testname" ]; then \
					printf "$(RED)%-60s %-20s$(NC)\n" "$$testname" "FAILED"; \
				fi; \
			done; \
			echo ""; \
			echo "$(RED)Total Failures: $$FAILURES$(NC)"; \
			echo ""; \
		else \
			echo ""; \
			echo "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"; \
			echo "$(GREEN)                 ALL TESTS PASSED ✓                       $(NC)"; \
			echo "$(GREEN)═══════════════════════════════════════════════════════════$(NC)"; \
			echo ""; \
		fi; \
	fi; \
	if [ $$TEST_FAILED -eq 1 ]; then \
		exit 1; \
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

test-chatbot: ## Run interactive chatbot integration test (Usage: RUN_CHATBOT_TEST=1 make test-chatbot)
	@echo "$(GREEN)Running interactive chatbot integration test...$(NC)"
	@if [ -z "$(RUN_CHATBOT_TEST)" ]; then \
		echo "$(YELLOW)⚠️  This is an interactive test. Set RUN_CHATBOT_TEST=1 to run it.$(NC)"; \
		echo "$(CYAN)Usage: RUN_CHATBOT_TEST=1 make test-chatbot$(NC)"; \
		echo ""; \
		echo "$(YELLOW)Required environment variables:$(NC)"; \
		echo "  - OPENAI_API_KEY (required)"; \
		echo "  - ANTHROPIC_API_KEY (optional)"; \
		echo "  - Additional provider keys as needed"; \
		exit 0; \
	fi
	@if [ -f .env ]; then \
		echo "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi
	@cd core && RUN_CHATBOT_TEST=1 go test -v -run TestChatbot

test-integrations-py: ## Run Python integration tests (Usage: make test-integrations-py [INTEGRATION=openai] [TESTCASE=test_name] [PATTERN=substring] [VERBOSE=1])
	@echo "$(GREEN)Running Python integration tests...$(NC)"
	@if [ ! -d "tests/integrations/python" ]; then \
		echo "$(RED)Error: tests/integrations/python directory not found$(NC)"; \
		exit 1; \
	fi; \
	if [ -n "$(PATTERN)" ] && [ -n "$(TESTCASE)" ]; then \
		echo "$(RED)Error: PATTERN and TESTCASE are mutually exclusive$(NC)"; \
		echo "$(YELLOW)Use PATTERN for substring matching or TESTCASE for exact match$(NC)"; \
		exit 1; \
	fi; \
	if [ -n "$(TESTCASE)" ] && [ -z "$(INTEGRATION)" ]; then \
		echo "$(RED)Error: TESTCASE requires INTEGRATION to be specified$(NC)"; \
		echo "$(YELLOW)Usage: make test-integrations-py INTEGRATION=anthropic TESTCASE=test_05_end2end_tool_calling$(NC)"; \
		exit 1; \
	fi; \
	if [ -f .env ]; then \
		echo "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	BIFROST_STARTED=0; \
	BIFROST_PID=""; \
	TAIL_PID=""; \
	TEST_PORT=$${PORT:-8080}; \
	TEST_HOST=$${HOST:-localhost}; \
	echo "$(CYAN)Checking if Bifrost is running on $$TEST_HOST:$$TEST_PORT...$(NC)"; \
	if curl -s -o /dev/null -w "%{http_code}" http://$$TEST_HOST:$$TEST_PORT/health 2>/dev/null | grep -q "200\|404"; then \
		echo "$(GREEN)✓ Bifrost is already running$(NC)"; \
	else \
		echo "$(YELLOW)Bifrost not running, starting it...$(NC)"; \
		./tmp/bifrost-http -host "$$TEST_HOST" -port "$$TEST_PORT" -log-style "$(LOG_STYLE)" -log-level "$(LOG_LEVEL)" -app-dir tests/integrations/python > /tmp/bifrost-test.log 2>&1 & \
		BIFROST_PID=$$!; \
		BIFROST_STARTED=1; \
		echo "$(YELLOW)Waiting for Bifrost to be ready...$(NC)"; \
		echo "$(CYAN)Bifrost logs: /tmp/bifrost-test.log$(NC)"; \
		(tail -f /tmp/bifrost-test.log 2>/dev/null | grep -E "error|panic|Error|ERRO|fatal|Fatal|FATAL" --line-buffered &) & \
		TAIL_PID=$$!; \
		for i in 1 2 3 4 5 6 7 8 9 10; do \
			if curl -s -o /dev/null http://$$TEST_HOST:$$TEST_PORT/health 2>/dev/null; then \
				echo "$(GREEN)✓ Bifrost is ready (PID: $$BIFROST_PID)$(NC)"; \
				break; \
			fi; \
			if [ $$i -eq 10 ]; then \
				echo "$(RED)Failed to start Bifrost$(NC)"; \
				echo "$(YELLOW)Bifrost logs:$(NC)"; \
				cat /tmp/bifrost-test.log 2>/dev/null || echo "No log file found"; \
				[ -n "$$BIFROST_PID" ] && kill $$BIFROST_PID 2>/dev/null; \
				[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null; \
				exit 1; \
			fi; \
			sleep 1; \
		done; \
	fi; \
	TEST_FAILED=0; \
	if ! which uv > /dev/null 2>&1; then \
		echo "$(YELLOW)uv not found, checking for pytest...$(NC)"; \
		if ! which pytest > /dev/null 2>&1; then \
			echo "$(RED)Error: Neither uv nor pytest found$(NC)"; \
			echo "$(YELLOW)Install uv: curl -LsSf https://astral.sh/uv/install.sh | sh$(NC)"; \
			echo "$(YELLOW)Or install pytest: pip install pytest$(NC)"; \
			[ $$BIFROST_STARTED -eq 1 ] && [ -n "$$BIFROST_PID" ] && kill $$BIFROST_PID 2>/dev/null; \
			[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null; \
			exit 1; \
		fi; \
		echo "$(CYAN)Using pytest directly$(NC)"; \
		if [ -n "$(INTEGRATION)" ]; then \
			if [ -n "$(TESTCASE)" ]; then \
				echo "$(CYAN)Running $(INTEGRATION) integration test: $(TESTCASE)...$(NC)"; \
				cd tests/integrations/python && pytest tests/test_$(INTEGRATION).py::$(TESTCASE) $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			elif [ -n "$(PATTERN)" ]; then \
				echo "$(CYAN)Running $(INTEGRATION) integration tests matching '$(PATTERN)'...$(NC)"; \
				cd tests/integrations/python && pytest tests/test_$(INTEGRATION).py -k "$(PATTERN)" $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			else \
				echo "$(CYAN)Running $(INTEGRATION) integration tests...$(NC)"; \
				cd tests/integrations/python && pytest tests/test_$(INTEGRATION).py $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			fi; \
		else \
			if [ -n "$(PATTERN)" ]; then \
				echo "$(CYAN)Running all integration tests matching '$(PATTERN)'...$(NC)"; \
				cd tests/integrations/python && pytest -k "$(PATTERN)" $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			else \
				echo "$(CYAN)Running all integration tests...$(NC)"; \
				cd tests/integrations/python && pytest $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			fi; \
		fi; \
	else \
		echo "$(CYAN)Using uv (fast mode)$(NC)"; \
		cd tests/integrations/python && \
		if [ -n "$(INTEGRATION)" ]; then \
			if [ -n "$(TESTCASE)" ]; then \
				echo "$(CYAN)Running $(INTEGRATION) integration test: $(TESTCASE)...$(NC)"; \
				uv run pytest tests/test_$(INTEGRATION).py::$(TESTCASE) $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			elif [ -n "$(PATTERN)" ]; then \
				echo "$(CYAN)Running $(INTEGRATION) integration tests matching '$(PATTERN)'...$(NC)"; \
				uv run pytest tests/test_$(INTEGRATION).py -k "$(PATTERN)" $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			else \
				echo "$(CYAN)Running $(INTEGRATION) integration tests...$(NC)"; \
				uv run pytest tests/test_$(INTEGRATION).py $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			fi; \
		else \
			if [ -n "$(PATTERN)" ]; then \
				echo "$(CYAN)Running all integration tests matching '$(PATTERN)'...$(NC)"; \
				uv run pytest -k "$(PATTERN)" $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			else \
				echo "$(CYAN)Running all integration tests...$(NC)"; \
				uv run pytest $(if $(VERBOSE),-v,-q) || TEST_FAILED=1; \
			fi; \
		fi; \
	fi; \
	if [ $$BIFROST_STARTED -eq 1 ] && [ -n "$$BIFROST_PID" ]; then \
		echo "$(YELLOW)Stopping Bifrost (PID: $$BIFROST_PID)...$(NC)"; \
		kill $$BIFROST_PID 2>/dev/null || true; \
		[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null || true; \
		wait $$BIFROST_PID 2>/dev/null || true; \
		echo "$(GREEN)✓ Bifrost stopped$(NC)"; \
		if [ $$TEST_FAILED -eq 1 ]; then \
			echo ""; \
			echo "$(YELLOW)Last 50 lines of Bifrost logs:$(NC)"; \
			tail -50 /tmp/bifrost-test.log 2>/dev/null || echo "No log file found"; \
		fi; \
	fi; \
	echo ""; \
	if [ $$TEST_FAILED -eq 1 ]; then \
		echo "$(RED)✗ Integration tests failed$(NC)"; \
		echo "$(CYAN)Full Bifrost logs: /tmp/bifrost-test.log$(NC)"; \
		exit 1; \
	else \
		echo "$(GREEN)✓ Integration tests complete$(NC)"; \
	fi

test-integrations-ts: ## Run TypeScript integration tests (Usage: make test-integrations-ts [INTEGRATION=openai] [TESTCASE=test_name] [PATTERN=substring] [VERBOSE=1])
	@echo "$(GREEN)Running TypeScript integration tests...$(NC)"
	@if [ ! -d "tests/integrations/typescript" ]; then \
		echo "$(RED)Error: tests/integrations/typescript directory not found$(NC)"; \
		exit 1; \
	fi; \
	if [ -n "$(PATTERN)" ] && [ -n "$(TESTCASE)" ]; then \
		echo "$(RED)Error: PATTERN and TESTCASE are mutually exclusive$(NC)"; \
		echo "$(YELLOW)Use PATTERN for substring matching or TESTCASE for exact match$(NC)"; \
		exit 1; \
	fi; \
	if [ -n "$(TESTCASE)" ] && [ -z "$(INTEGRATION)" ]; then \
		echo "$(RED)Error: TESTCASE requires INTEGRATION to be specified$(NC)"; \
		echo "$(YELLOW)Usage: make test-integrations-ts INTEGRATION=openai TESTCASE=test_simple_chat$(NC)"; \
		exit 1; \
	fi; \
	if [ -f .env ]; then \
		echo "$(YELLOW)Loading environment variables from .env...$(NC)"; \
		set -a; . ./.env; set +a; \
	fi; \
	BIFROST_STARTED=0; \
	BIFROST_PID=""; \
	TAIL_PID=""; \
	TEST_PORT=$${PORT:-8080}; \
	TEST_HOST=$${HOST:-localhost}; \
	echo "$(CYAN)Checking if Bifrost is running on $$TEST_HOST:$$TEST_PORT...$(NC)"; \
	if curl -s -o /dev/null -w "%{http_code}" http://$$TEST_HOST:$$TEST_PORT/health 2>/dev/null | grep -q "200\|404"; then \
		echo "$(GREEN)✓ Bifrost is already running$(NC)"; \
	else \
		echo "$(YELLOW)Bifrost not running, starting it...$(NC)"; \
		./tmp/bifrost-http -host "$$TEST_HOST" -port "$$TEST_PORT" -log-style "$(LOG_STYLE)" -log-level "$(LOG_LEVEL)" -app-dir tests/integrations/typescript > /tmp/bifrost-test.log 2>&1 & \
		BIFROST_PID=$$!; \
		BIFROST_STARTED=1; \
		echo "$(YELLOW)Waiting for Bifrost to be ready...$(NC)"; \
		echo "$(CYAN)Bifrost logs: /tmp/bifrost-test.log$(NC)"; \
		(tail -f /tmp/bifrost-test.log 2>/dev/null | grep -E "error|panic|Error|ERRO|fatal|Fatal|FATAL" --line-buffered &) & \
		TAIL_PID=$$!; \
		for i in 1 2 3 4 5 6 7 8 9 10; do \
			if curl -s -o /dev/null http://$$TEST_HOST:$$TEST_PORT/health 2>/dev/null; then \
				echo "$(GREEN)✓ Bifrost is ready (PID: $$BIFROST_PID)$(NC)"; \
				break; \
			fi; \
			if [ $$i -eq 10 ]; then \
				echo "$(RED)Failed to start Bifrost$(NC)"; \
				echo "$(YELLOW)Bifrost logs:$(NC)"; \
				cat /tmp/bifrost-test.log 2>/dev/null || echo "No log file found"; \
				[ -n "$$BIFROST_PID" ] && kill $$BIFROST_PID 2>/dev/null; \
				[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null; \
				exit 1; \
			fi; \
			sleep 1; \
		done; \
	fi; \
	TEST_FAILED=0; \
	if ! which npm > /dev/null 2>&1; then \
		echo "$(RED)Error: npm not found$(NC)"; \
		echo "$(YELLOW)Install Node.js: https://nodejs.org/$(NC)"; \
		[ $$BIFROST_STARTED -eq 1 ] && [ -n "$$BIFROST_PID" ] && kill $$BIFROST_PID 2>/dev/null; \
		[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null; \
		exit 1; \
	fi; \
	echo "$(CYAN)Using npm$(NC)"; \
	cd tests/integrations/typescript && \
	if [ ! -d "node_modules" ]; then \
		echo "$(YELLOW)Installing dependencies...$(NC)"; \
		npm install; \
	fi; \
	if [ -n "$(INTEGRATION)" ]; then \
		if [ -n "$(TESTCASE)" ]; then \
			echo "$(CYAN)Running $(INTEGRATION) integration test: $(TESTCASE)...$(NC)"; \
			npm test -- tests/test-$(INTEGRATION).test.ts -t "$(TESTCASE)" $(if $(VERBOSE),--reporter=verbose,) || TEST_FAILED=1; \
		elif [ -n "$(PATTERN)" ]; then \
			echo "$(CYAN)Running $(INTEGRATION) integration tests matching '$(PATTERN)'...$(NC)"; \
			npm test -- tests/test-$(INTEGRATION).test.ts -t "$(PATTERN)" $(if $(VERBOSE),--reporter=verbose,) || TEST_FAILED=1; \
		else \
			echo "$(CYAN)Running $(INTEGRATION) integration tests...$(NC)"; \
			npm test -- tests/test-$(INTEGRATION).test.ts $(if $(VERBOSE),--reporter=verbose,) || TEST_FAILED=1; \
		fi; \
	else \
		if [ -n "$(PATTERN)" ]; then \
			echo "$(CYAN)Running all integration tests matching '$(PATTERN)'...$(NC)"; \
			npm test -- -t "$(PATTERN)" $(if $(VERBOSE),--reporter=verbose,) || TEST_FAILED=1; \
		else \
			echo "$(CYAN)Running all integration tests...$(NC)"; \
			npm test $(if $(VERBOSE),-- --reporter=verbose,) || TEST_FAILED=1; \
		fi; \
	fi; \
	if [ $$BIFROST_STARTED -eq 1 ] && [ -n "$$BIFROST_PID" ]; then \
		echo "$(YELLOW)Stopping Bifrost (PID: $$BIFROST_PID)...$(NC)"; \
		kill $$BIFROST_PID 2>/dev/null || true; \
		[ -n "$$TAIL_PID" ] && kill $$TAIL_PID 2>/dev/null || true; \
		wait $$BIFROST_PID 2>/dev/null || true; \
		echo "$(GREEN)✓ Bifrost stopped$(NC)"; \
		if [ $$TEST_FAILED -eq 1 ]; then \
			echo ""; \
			echo "$(YELLOW)Last 50 lines of Bifrost logs:$(NC)"; \
			tail -50 /tmp/bifrost-test.log 2>/dev/null || echo "No log file found"; \
		fi; \
	fi; \
	echo ""; \
	if [ $$TEST_FAILED -eq 1 ]; then \
		echo "$(RED)✗ TypeScript integration tests failed$(NC)"; \
		echo "$(CYAN)Full Bifrost logs: /tmp/bifrost-test.log$(NC)"; \
		exit 1; \
	else \
		echo "$(GREEN)✓ TypeScript integration tests complete$(NC)"; \
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

# Module parameter for mod-tidy (all/core/plugins/framework/transport)
MODULE ?= all

mod-tidy: ## Run go mod tidy on modules (Usage: make mod-tidy [MODULE=all|core|plugins|framework|transport])
	@echo "$(GREEN)Running go mod tidy...$(NC)"
	@if [ "$(MODULE)" = "all" ] || [ "$(MODULE)" = "core" ]; then \
		echo "$(CYAN)Tidying core...$(NC)"; \
		cd core && go mod tidy && echo "$(GREEN)  ✓ core$(NC)"; \
	fi
	@if [ "$(MODULE)" = "all" ] || [ "$(MODULE)" = "framework" ]; then \
		echo "$(CYAN)Tidying framework...$(NC)"; \
		cd framework && go mod tidy && echo "$(GREEN)  ✓ framework$(NC)"; \
	fi
	@if [ "$(MODULE)" = "all" ] || [ "$(MODULE)" = "transport" ]; then \
		echo "$(CYAN)Tidying transports...$(NC)"; \
		cd transports && go mod tidy && echo "$(GREEN)  ✓ transports$(NC)"; \
	fi
	@if [ "$(MODULE)" = "all" ] || [ "$(MODULE)" = "plugins" ]; then \
		echo "$(CYAN)Tidying plugins...$(NC)"; \
		for plugin_dir in ./plugins/*/; do \
			if [ -d "$$plugin_dir" ] && [ -f "$$plugin_dir/go.mod" ]; then \
				plugin_name=$$(basename $$plugin_dir); \
				cd $$plugin_dir && go mod tidy && cd ../.. && echo "$(GREEN)  ✓ plugins/$$plugin_name$(NC)"; \
			fi; \
		done; \
	fi
	@echo ""
	@echo "$(GREEN)✓ go mod tidy complete$(NC)"