.PHONY: all build clean help check-tinygo

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

# TinyGo build flags
TINYGO_TARGET = wasi
TINYGO_SCHEDULER = none

help: ## Show this help message
	@echo '$(COLOR_BOLD)Hello World WASM Plugin$(COLOR_RESET)'
	@echo ''
	@echo '$(COLOR_BOLD)Usage:$(COLOR_RESET) make [target]'
	@echo ''
	@echo '$(COLOR_BOLD)Prerequisites:$(COLOR_RESET)'
	@echo '  - TinyGo (https://tinygo.org/getting-started/install/)'
	@echo '    macOS: brew install tinygo'
	@echo '    Linux: See TinyGo installation docs'
	@echo ''
	@echo '$(COLOR_BOLD)Available targets:$(COLOR_RESET)'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  $(COLOR_INFO)%-15s$(COLOR_RESET) %s\n", $$1, $$2}' $(MAKEFILE_LIST)

check-tinygo: ## Check if TinyGo is installed
	@which tinygo > /dev/null 2>&1 || (echo "$(COLOR_ERROR)Error: TinyGo is not installed$(COLOR_RESET)"; \
		echo "$(COLOR_INFO)Install TinyGo:$(COLOR_RESET)"; \
		echo "  macOS: brew install tinygo"; \
		echo "  Linux: See https://tinygo.org/getting-started/install/"; \
		exit 1)
	@echo "$(COLOR_SUCCESS)✓ TinyGo found: $$(tinygo version)$(COLOR_RESET)"

build: check-tinygo ## Build the WASM plugin
	@mkdir -p $(OUTPUT_DIR)
	@echo "$(COLOR_INFO)Building WASM plugin...$(COLOR_RESET)"
	GOWORK=off tinygo build -o $(OUTPUT) -target=$(TINYGO_TARGET) -scheduler=$(TINYGO_SCHEDULER) .
	@echo "$(COLOR_SUCCESS)✓ Plugin built successfully: $(OUTPUT)$(COLOR_RESET)"
	@ls -lh $(OUTPUT) | awk '{print "  Size: " $$5}'

build-optimized: check-tinygo ## Build the WASM plugin with size optimizations
	@mkdir -p $(OUTPUT_DIR)
	@echo "$(COLOR_INFO)Building optimized WASM plugin...$(COLOR_RESET)"
	GOWORK=off tinygo build -o $(OUTPUT) -target=$(TINYGO_TARGET) -scheduler=$(TINYGO_SCHEDULER) -no-debug -gc=leaking .
	@echo "$(COLOR_SUCCESS)✓ Optimized plugin built: $(OUTPUT)$(COLOR_RESET)"
	@ls -lh $(OUTPUT) | awk '{print "  Size: " $$5}'

clean: ## Remove build artifacts
	@echo "$(COLOR_INFO)Cleaning build artifacts...$(COLOR_RESET)"
	@rm -rf $(OUTPUT_DIR)
	@echo "$(COLOR_SUCCESS)✓ Clean complete$(COLOR_RESET)"

info: ## Show build information
	@echo "$(COLOR_BOLD)Build Configuration$(COLOR_RESET)"
	@echo "  Plugin Name:  $(PLUGIN_NAME)"
	@echo "  Output:       $(OUTPUT)"
	@echo "  Target:       $(TINYGO_TARGET)"
	@echo "  Scheduler:    $(TINYGO_SCHEDULER)"
	@echo ""
	@if [ -f "$(OUTPUT)" ]; then \
		echo "$(COLOR_SUCCESS)Plugin exists:$(COLOR_RESET)"; \
		ls -lh $(OUTPUT) | awk '{print "  " $$9 " (" $$5 ")"}'; \
	else \
		echo "$(COLOR_WARNING)Plugin not built yet$(COLOR_RESET)"; \
	fi

.DEFAULT_GOAL := help
