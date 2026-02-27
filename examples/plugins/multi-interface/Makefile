.PHONY: build clean

build:
	@echo "Building Multi-Interface plugin..."
	@mkdir -p build
	@go build -buildmode=plugin -o build/multi-interface.so main.go
	@echo "Plugin built successfully: build/multi-interface.so"

clean:
	@echo "Cleaning build directory..."
	@rm -rf build
	@echo "Clean complete"
