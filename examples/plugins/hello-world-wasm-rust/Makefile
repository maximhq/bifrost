.PHONY: all build build-optimized clean help check-rust

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
TARGET = wasm32-unknown-unknown

help: ## Show this help message
	@echo '$(COLOR_BOLD)Hello World WASM Plugin (Rust)$(COLOR_RESET)'
	@echo ''
	@echo '$(COLOR_BOLD)Usage:$(COLOR_RESET) make [target]'
	@echo ''
	@echo '$(COLOR_BOLD)Prerequisites:$(COLOR_RESET)'
	@echo '  - Rust with wasm32-unknown-unknown target'
	@echo '    rustup target add wasm32-unknown-unknown'
	@echo ''
	@echo '$(COLOR_BOLD)Available targets:$(COLOR_RESET)'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  $(COLOR_INFO)%-15s$(COLOR_RESET) %s\n", $$1, $$2}' $(MAKEFILE_LIST)

check-rust: ## Check if Rust and WASM target are installed
	@which cargo > /dev/null 2>&1 || (echo "$(COLOR_ERROR)Error: Rust/Cargo is not installed$(COLOR_RESET)"; \
		echo "$(COLOR_INFO)Install Rust: https://rustup.rs/$(COLOR_RESET)"; \
		exit 1)
	@rustup target list --installed | grep -q $(TARGET) || (echo "$(COLOR_ERROR)Error: WASM target not installed$(COLOR_RESET)"; \
		echo "$(COLOR_INFO)Install with: rustup target add $(TARGET)$(COLOR_RESET)"; \
		exit 1)
	@echo "$(COLOR_SUCCESS)✓ Rust found: $$(rustc --version)$(COLOR_RESET)"
	@echo "$(COLOR_SUCCESS)✓ WASM target: $(TARGET)$(COLOR_RESET)"

build: check-rust ## Build the WASM plugin
	@mkdir -p $(OUTPUT_DIR)
	@echo "$(COLOR_INFO)Building WASM plugin...$(COLOR_RESET)"
	cargo build --release --target $(TARGET)
	@cp target/$(TARGET)/release/hello_world_wasm_rust.wasm $(OUTPUT)
	@echo "$(COLOR_SUCCESS)✓ Plugin built successfully: $(OUTPUT)$(COLOR_RESET)"
	@ls -lh $(OUTPUT) | awk '{print "  Size: " $$5}'

build-optimized: check-rust ## Build with wasm-opt optimization (requires wasm-opt)
	@mkdir -p $(OUTPUT_DIR)
	@echo "$(COLOR_INFO)Building optimized WASM plugin...$(COLOR_RESET)"
	cargo build --release --target $(TARGET)
	@cp target/$(TARGET)/release/hello_world_wasm_rust.wasm $(OUTPUT)
	@if which wasm-opt > /dev/null 2>&1; then \
		echo "$(COLOR_INFO)Running wasm-opt...$(COLOR_RESET)"; \
		wasm-opt -Os -o $(OUTPUT) $(OUTPUT); \
	else \
		echo "$(COLOR_WARNING)wasm-opt not found, skipping optimization$(COLOR_RESET)"; \
	fi
	@echo "$(COLOR_SUCCESS)✓ Plugin built: $(OUTPUT)$(COLOR_RESET)"
	@ls -lh $(OUTPUT) | awk '{print "  Size: " $$5}'

clean: ## Remove build artifacts
	@echo "$(COLOR_INFO)Cleaning build artifacts...$(COLOR_RESET)"
	@cargo clean
	@rm -rf $(OUTPUT_DIR)
	@echo "$(COLOR_SUCCESS)✓ Clean complete$(COLOR_RESET)"

info: ## Show build information
	@echo "$(COLOR_BOLD)Build Configuration$(COLOR_RESET)"
	@echo "  Plugin Name:  $(PLUGIN_NAME)"
	@echo "  Output:       $(OUTPUT)"
	@echo "  Target:       $(TARGET)"
	@echo ""
	@if [ -f "$(OUTPUT)" ]; then \
		echo "$(COLOR_SUCCESS)Plugin exists:$(COLOR_RESET)"; \
		ls -lh $(OUTPUT) | awk '{print "  " $$9 " (" $$5 ")"}'; \
	else \
		echo "$(COLOR_WARNING)Plugin not built yet$(COLOR_RESET)"; \
	fi

.DEFAULT_GOAL := help
