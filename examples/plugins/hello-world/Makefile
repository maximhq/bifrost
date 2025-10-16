.PHONY: build clean install help

# Plugin name
PLUGIN_NAME = hello-world
OUTPUT_DIR = build

# Platform detection
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Linux)
	PLUGIN_EXT = .so
	PLATFORM = linux
endif
ifeq ($(UNAME_S),Darwin)
	PLUGIN_EXT = .so
	PLATFORM = darwin
endif
ifeq ($(UNAME_S),Windows_NT)
	PLUGIN_EXT = .dll
	PLATFORM = windows
endif

# Architecture detection
UNAME_M := $(shell uname -m)
ifeq ($(UNAME_M),x86_64)
	ARCH = amd64
endif
ifeq ($(UNAME_M),arm64)
	ARCH = arm64
endif
ifeq ($(UNAME_M),aarch64)
	ARCH = arm64
endif

# Output file
OUTPUT = $(OUTPUT_DIR)/$(PLUGIN_NAME)$(PLUGIN_EXT)

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the plugin for current platform
	@echo "Building plugin for $(PLATFORM)/$(ARCH)..."
	@mkdir -p $(OUTPUT_DIR)
	go build -buildmode=plugin -o $(OUTPUT) main.go
	@echo "Plugin built successfully: $(OUTPUT)"

build-linux-amd64: ## Build the plugin for Linux AMD64
	@echo "Building plugin for linux/amd64..."
	@mkdir -p $(OUTPUT_DIR)
	GOOS=linux GOARCH=amd64 go build -buildmode=plugin -o $(OUTPUT_DIR)/$(PLUGIN_NAME)-linux-amd64.so main.go
	@echo "Plugin built successfully: $(OUTPUT_DIR)/$(PLUGIN_NAME)-linux-amd64.so"

build-linux-arm64: ## Build the plugin for Linux ARM64
	@echo "Building plugin for linux/arm64..."
	@mkdir -p $(OUTPUT_DIR)
	GOOS=linux GOARCH=arm64 go build -buildmode=plugin -o $(OUTPUT_DIR)/$(PLUGIN_NAME)-linux-arm64.so main.go
	@echo "Plugin built successfully: $(OUTPUT_DIR)/$(PLUGIN_NAME)-linux-arm64.so"

build-darwin-amd64: ## Build the plugin for macOS AMD64
	@echo "Building plugin for darwin/amd64..."
	@mkdir -p $(OUTPUT_DIR)
	GOOS=darwin GOARCH=amd64 go build -buildmode=plugin -o $(OUTPUT_DIR)/$(PLUGIN_NAME)-darwin-amd64.so main.go
	@echo "Plugin built successfully: $(OUTPUT_DIR)/$(PLUGIN_NAME)-darwin-amd64.so"

build-darwin-arm64: ## Build the plugin for macOS ARM64
	@echo "Building plugin for darwin/arm64..."
	@mkdir -p $(OUTPUT_DIR)
	GOOS=darwin GOARCH=arm64 go build -buildmode=plugin -o $(OUTPUT_DIR)/$(PLUGIN_NAME)-darwin-arm64.so main.go
	@echo "Plugin built successfully: $(OUTPUT_DIR)/$(PLUGIN_NAME)-darwin-arm64.so"

build-all: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 ## Build for all supported platforms

clean: ## Remove build artifacts
	@echo "Cleaning build artifacts..."
	@rm -rf $(OUTPUT_DIR)
	@echo "Clean complete"

install: build ## Build and install the plugin to Bifrost plugins directory
	@echo "Installing plugin..."
	@mkdir -p ~/.bifrost/plugins
	@cp $(OUTPUT) ~/.bifrost/plugins/
	@echo "Plugin installed to ~/.bifrost/plugins/$(PLUGIN_NAME)$(PLUGIN_EXT)"

test: ## Run tests
	go test -v ./...

deps: ## Download dependencies
	go mod download
	go mod tidy

.DEFAULT_GOAL := help
