.PHONY: all build build-debug clean help install check-node

# Colors
COLOR_RESET   = \033[0m
COLOR_INFO    = \033[36m
COLOR_SUCCESS = \033[32m
COLOR_WARNING = \033[33m
COLOR_ERROR   = \033[31m
COLOR_BOLD    = \033[1m

# Plugin configuration
PLUGIN_NAME = hello-world
OUTPUT_DIR = build
OUTPUT = $(OUTPUT_DIR)/$(PLUGIN_NAME).wasm

help: ## Show this help message
	@echo '$(COLOR_BOLD)Hello World WASM Plugin (TypeScript/AssemblyScript)$(COLOR_RESET)'
	@echo ''
	@echo '$(COLOR_BOLD)Usage:$(COLOR_RESET) make [target]'
	@echo ''
	@echo '$(COLOR_BOLD)Prerequisites:$(COLOR_RESET)'
	@echo '  - Node.js (https://nodejs.org/)'
	@echo '  - npm install (to install AssemblyScript)'
	@echo ''
	@echo '$(COLOR_BOLD)Available targets:$(COLOR_RESET)'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  $(COLOR_INFO)%-15s$(COLOR_RESET) %s\n", $$1, $$2}' $(MAKEFILE_LIST)

check-node: ## Check if Node.js is installed
	@which node > /dev/null 2>&1 || (echo "$(COLOR_ERROR)Error: Node.js is not installed$(COLOR_RESET)"; \
		echo "$(COLOR_INFO)Install Node.js: https://nodejs.org/$(COLOR_RESET)"; \
		exit 1)
	@echo "$(COLOR_SUCCESS)✓ Node.js found: $$(node --version)$(COLOR_RESET)"

install: check-node ## Install dependencies
	@echo "$(COLOR_INFO)Installing dependencies...$(COLOR_RESET)"
	npm install
	@echo "$(COLOR_SUCCESS)✓ Dependencies installed$(COLOR_RESET)"

build: install ## Build the WASM plugin
	@mkdir -p $(OUTPUT_DIR)
	@echo "$(COLOR_INFO)Building WASM plugin...$(COLOR_RESET)"
	npm run build
	@echo "$(COLOR_SUCCESS)✓ Plugin built successfully: $(OUTPUT)$(COLOR_RESET)"
	@ls -lh $(OUTPUT) | awk '{print "  Size: " $$5}'

build-debug: install ## Build with debug info
	@mkdir -p $(OUTPUT_DIR)
	@echo "$(COLOR_INFO)Building WASM plugin (debug)...$(COLOR_RESET)"
	npm run build:debug
	@echo "$(COLOR_SUCCESS)✓ Debug plugin built: $(OUTPUT)$(COLOR_RESET)"
	@ls -lh $(OUTPUT) | awk '{print "  Size: " $$5}'

clean: ## Remove build artifacts
	@echo "$(COLOR_INFO)Cleaning build artifacts...$(COLOR_RESET)"
	@rm -rf $(OUTPUT_DIR) node_modules
	@echo "$(COLOR_SUCCESS)✓ Clean complete$(COLOR_RESET)"

info: ## Show build information
	@echo "$(COLOR_BOLD)Build Configuration$(COLOR_RESET)"
	@echo "  Plugin Name:  $(PLUGIN_NAME)"
	@echo "  Output:       $(OUTPUT)"
	@echo ""
	@if [ -f "$(OUTPUT)" ]; then \
		echo "$(COLOR_SUCCESS)Plugin exists:$(COLOR_RESET)"; \
		ls -lh $(OUTPUT) | awk '{print "  " $$9 " (" $$5 ")"}'; \
	else \
		echo "$(COLOR_WARNING)Plugin not built yet$(COLOR_RESET)"; \
	fi

.DEFAULT_GOAL := help
