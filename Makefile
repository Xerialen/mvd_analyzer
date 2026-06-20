# Top-level Makefile — coordinates the workspace.
#
# Layout:
#   mvd-reader/     — event schema + MVD source (ingestion layer)
#   mvd-analytics/  — analysis pipeline + result schema + view query API
#   mvd-api/        — HTTP REST server on top of mvd-analytics/view
#   mvd-mcp/        — stdio MCP shim that forwards to a running mvd-api
#   mvd-web/        — browser UX + WASM glue
#
# `make build` produces dist/ for Netlify deploy (the web app). Build
# targets for mvd-api / mvd-mcp produce distributable binaries.

WASM_MAIN  := ./mvd-web/cmd/wasm
API_MAIN   := ./mvd-api
MCP_MAIN   := ./mvd-mcp
DIST_DIR   := dist
STATIC_DIR := mvd-web/static
LOC_DATA   := mvd-analytics/loc/data
MAPENTS_DATA := mvd-analytics/mapents/data
BSP_DIR    := bsps
GIT_HASH   := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_TAG    := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "dev")
BUILD_DATE := $(shell date -u +%Y-%m-%d)
LDFLAGS    := -ldflags "-s -w -X main.GitHash=$(GIT_HASH) -X main.GitTag=$(GIT_TAG) -X main.BuildDate=$(BUILD_DATE)"

.PHONY: build build-api build-mcp build-bin build-all-platforms \
        build-api-linux build-api-darwin build-api-windows \
        build-mcp-linux build-mcp-darwin build-mcp-windows \
        bsps serve clean test fmt help

# Build the deployable web bundle into dist/.
build:
	@rm -rf $(DIST_DIR)
	@echo "Building WASM module..."
	@mkdir -p $(DIST_DIR)
	GOOS=js GOARCH=wasm go build $(LDFLAGS) -o $(DIST_DIR)/analyzer.wasm $(WASM_MAIN)
	@echo "Copying wasm_exec.js..."
	@cp "$$(go env GOROOT)/misc/wasm/wasm_exec.js" $(DIST_DIR)/ 2>/dev/null || cp $(STATIC_DIR)/wasm_exec.js $(DIST_DIR)/
	@echo "Copying static files..."
	@cp $(STATIC_DIR)/index.html $(DIST_DIR)/
	@cp $(STATIC_DIR)/styles.css $(DIST_DIR)/
	@cp $(STATIC_DIR)/app.js $(DIST_DIR)/
	@cp $(STATIC_DIR)/map-gl.js $(DIST_DIR)/
	@cp $(STATIC_DIR)/worker.js $(DIST_DIR)/
	@cp -r $(STATIC_DIR)/lib $(DIST_DIR)/
	@cp -r $(STATIC_DIR)/maps $(DIST_DIR)/
	@if [ -d $(STATIC_DIR)/maps3d ]; then cp -r $(STATIC_DIR)/maps3d $(DIST_DIR)/; fi
	@echo "Copying loc corpus from $(LOC_DATA)..."
	@mkdir -p $(DIST_DIR)/locs && cp $(LOC_DATA)/*.loc $(DIST_DIR)/locs/
	@echo "Copying map-entity corpus from $(MAPENTS_DATA)..."
	@mkdir -p $(DIST_DIR)/mapents && cp $(MAPENTS_DATA)/*.json $(DIST_DIR)/mapents/
	@if [ -d $(BSP_DIR) ] && ls $(BSP_DIR)/*.bsp >/dev/null 2>&1; then \
		echo "Copying BSPs from $(BSP_DIR)/ for WASM visibility filter..."; \
		mkdir -p $(DIST_DIR)/bsps && cp $(BSP_DIR)/*.bsp $(DIST_DIR)/bsps/; \
	else \
		echo "Skipping BSP copy ($(BSP_DIR)/ empty — run 'make bsps' to populate; locvis falls back to V1)."; \
	fi
	@echo "Build complete!"
	@ls -lh $(DIST_DIR)/

# Build the host-platform binaries.
build-api:
	@mkdir -p $(DIST_DIR)
	go build $(LDFLAGS) -o $(DIST_DIR)/mvd-api $(API_MAIN)

build-mcp:
	@mkdir -p $(DIST_DIR)
	go build $(LDFLAGS) -o $(DIST_DIR)/mvd-mcp $(MCP_MAIN)

build-bin: build-api build-mcp

# Cross-compile binaries for distribution.
build-api-linux:
	@mkdir -p $(DIST_DIR)
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/mvd-api-linux-amd64    $(API_MAIN)

build-api-darwin:
	@mkdir -p $(DIST_DIR)
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/mvd-api-darwin-amd64   $(API_MAIN)
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/mvd-api-darwin-arm64   $(API_MAIN)

build-api-windows:
	@mkdir -p $(DIST_DIR)
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/mvd-api-windows-amd64.exe $(API_MAIN)

build-mcp-linux:
	@mkdir -p $(DIST_DIR)
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/mvd-mcp-linux-amd64    $(MCP_MAIN)

build-mcp-darwin:
	@mkdir -p $(DIST_DIR)
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/mvd-mcp-darwin-amd64   $(MCP_MAIN)
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/mvd-mcp-darwin-arm64   $(MCP_MAIN)

build-mcp-windows:
	@mkdir -p $(DIST_DIR)
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/mvd-mcp-windows-amd64.exe $(MCP_MAIN)

build-all-platforms: build-api-linux build-api-darwin build-api-windows \
                     build-mcp-linux build-mcp-darwin build-mcp-windows
	@ls -lh $(DIST_DIR)/mvd-api-* $(DIST_DIR)/mvd-mcp-*

# Download the curated set of QW competitive BSPs into $(BSP_DIR). These
# files are NOT committed to git (see .gitignore). The locvis visibility
# filter loads them at runtime when attributing player positions to loc
# names; maps without a BSP fall back to the V1 Euclidean nearest-
# neighbour. See scripts/fetch-bsps.sh for the URL/sha256 list.
bsps:
	@./scripts/fetch-bsps.sh $(BSP_DIR)

# Serve the built web bundle on localhost.
serve: build
	@echo "Serving on http://localhost:8080"
	@cd $(DIST_DIR) && python3 -m http.server 8080

# Run tests across every workspace module.
test:
	go test ./mvd-reader/... ./mvd-analytics/... ./mvd-api/... ./mvd-mcp/... ./mvd-web/...

# Remove dist/.
clean:
	rm -rf $(DIST_DIR)

# Format every module.
fmt:
	go fmt ./mvd-reader/... ./mvd-analytics/... ./mvd-api/... ./mvd-mcp/... ./mvd-web/...

# Help.
help:
	@echo "MVD Analyzer — five-module workspace"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "  build               Build WASM + copy static assets + loc corpus into dist/"
	@echo "  build-api           Build mvd-api binary for the host platform"
	@echo "  build-mcp           Build mvd-mcp binary for the host platform"
	@echo "  build-bin           build-api + build-mcp"
	@echo "  build-all-platforms Cross-compile mvd-api and mvd-mcp for linux/darwin/windows"
	@echo "  bsps                Download competitive QW BSPs into $(BSP_DIR)/ for locvis visibility filter"
	@echo "  serve               make build, then python3 -m http.server 8080 in dist/"
	@echo "  test                Run tests across every module"
	@echo "  clean               Remove dist/"
	@echo "  fmt                 Format code across every module"
	@echo "  help                Show this help"
