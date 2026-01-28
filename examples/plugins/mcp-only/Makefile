.PHONY: build clean

build:
	@echo "Building MCP-Only plugin..."
	@mkdir -p build
	@go build -buildmode=plugin -o build/mcp-only.so main.go
	@echo "Plugin built successfully: build/mcp-only.so"

clean:
	@echo "Cleaning build directory..."
	@rm -rf build
	@echo "Clean complete"
