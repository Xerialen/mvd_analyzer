# MVD Analyzer Makefile — WASM build

WASM_MAIN := ./cmd/wasm
DIST_DIR := dist
STATIC_DIR := internal/web/static
LDFLAGS := -ldflags "-s -w"

.PHONY: build serve clean test fmt help

# Default target
build:
	@echo "Building WASM module..."
	@mkdir -p $(DIST_DIR)
	GOOS=js GOARCH=wasm go build $(LDFLAGS) -o $(DIST_DIR)/analyzer.wasm $(WASM_MAIN)
	@echo "Copying wasm_exec.js..."
	@cp "$$(go env GOROOT)/misc/wasm/wasm_exec.js" $(DIST_DIR)/
	@echo "Copying static files..."
	@cp $(STATIC_DIR)/index.html $(DIST_DIR)/
	@cp $(STATIC_DIR)/styles.css $(DIST_DIR)/
	@cp $(STATIC_DIR)/app.js $(DIST_DIR)/
	@cp $(STATIC_DIR)/worker.js $(DIST_DIR)/
	@echo "Build complete!"
	@ls -lh $(DIST_DIR)/

# Serve locally for testing
serve: build
	@echo "Serving on http://localhost:8080"
	@cd $(DIST_DIR) && python3 -m http.server 8080

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -rf $(DIST_DIR)

# Format code
fmt:
	go fmt ./...

# Show help
help:
	@echo "MVD Analyzer — WASM Build"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "  build   Build WASM + copy static files to dist/"
	@echo "  serve   Build and serve on http://localhost:8080"
	@echo "  test    Run Go tests"
	@echo "  clean   Remove dist/"
	@echo "  fmt     Format Go code"
	@echo "  help    Show this help"
