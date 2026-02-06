.PHONY: build clean

build:
	@echo "Building HTTP-Transport-Only plugin..."
	@mkdir -p build
	@go build -buildmode=plugin -o build/http-transport-only.so main.go
	@echo "Plugin built successfully: build/http-transport-only.so"

clean:
	@echo "Cleaning build directory..."
	@rm -rf build
	@echo "Clean complete"
