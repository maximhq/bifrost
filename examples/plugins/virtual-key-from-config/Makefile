.PHONY: build clean test

PLUGIN_NAME = virtual-key-from-config
OUTPUT_DIR = build

build:
	@mkdir -p $(OUTPUT_DIR)
	@go build -buildmode=plugin -o $(OUTPUT_DIR)/$(PLUGIN_NAME).so .

test:
	@go test ./...

clean:
	@rm -rf $(OUTPUT_DIR)
