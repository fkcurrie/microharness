.PHONY: build install clean init daemon run

BUILD_DIR=bin
BINARY_NAME=microharness
INSTALL_PATH=$(HOME)/.local/bin

build:
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/microharness
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

install: build
	@mkdir -p $(INSTALL_PATH)
	cp $(BUILD_DIR)/$(BINARY_NAME) $(INSTALL_PATH)/$(BINARY_NAME)
	@echo "Installed $(BINARY_NAME) to $(INSTALL_PATH)/$(BINARY_NAME)"

init: build
	./$(BUILD_DIR)/$(BINARY_NAME) init

daemon: build
	./$(BUILD_DIR)/$(BINARY_NAME) daemon

run: build
	./$(BUILD_DIR)/$(BINARY_NAME)

clean:
	rm -rf $(BUILD_DIR)

bench: build
	@echo "Running Model Latency Regression Suite..."
	@go run ./cmd/bench

setup-hooks:
	@mkdir -p .git/hooks
	@echo '#!/usr/bin/env bash\nset -e\n\necho "🔍 Running pre-commit model latency regression benchmark..."\nmake bench' > .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "✅ Pre-commit latency regression hook installed successfully!"


