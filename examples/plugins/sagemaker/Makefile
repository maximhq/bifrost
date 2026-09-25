.PHONY: build clean test verify-load workspace

PLUGIN_NAME := sagemaker
OUTPUT_DIR := build
OUTPUT := $(OUTPUT_DIR)/$(PLUGIN_NAME).so
HOSTCHECK := $(OUTPUT_DIR)/hostcheck
BIFROST_ROOT := $(abspath ../../..)
WORKSPACE_FILE := $(BIFROST_ROOT)/go.work

workspace:
	@if [ ! -f "$(WORKSPACE_FILE)" ]; then \
		cd "$(BIFROST_ROOT)" && \
		go work init ./cli ./core ./framework ./transports && \
		for plugin_dir in ./plugins/*/; do \
			if [ -f "$${plugin_dir}/go.mod" ]; then go work use "$${plugin_dir}"; fi; \
		done; \
	fi
	@cd "$(BIFROST_ROOT)" && go work use ./examples/plugins/sagemaker

build: workspace
	@mkdir -p $(OUTPUT_DIR)
	GOWORK=$(WORKSPACE_FILE) CGO_ENABLED=1 go build -trimpath -buildmode=plugin -o $(OUTPUT) .

verify-load: workspace
	@mkdir -p $(OUTPUT_DIR)
	GOWORK=$(WORKSPACE_FILE) CGO_ENABLED=1 go build -trimpath -o $(HOSTCHECK) ./hostcheck
	GOWORK=$(WORKSPACE_FILE) CGO_ENABLED=1 go build -trimpath -buildmode=plugin -o $(OUTPUT) .
	$(HOSTCHECK) $(OUTPUT)

test:
	GOWORK=off go test -race ./...

clean:
	rm -rf $(OUTPUT_DIR)
