.PHONY: build clean

build:
	@echo "Building Voice Live Realtime plugin..."
	@mkdir -p build
	@go build -buildmode=plugin -o build/voicelive-realtime.so main.go
	@echo "Plugin built successfully: build/voicelive-realtime.so"

clean:
	@echo "Cleaning build directory..."
	@rm -rf build
	@echo "Clean complete"
