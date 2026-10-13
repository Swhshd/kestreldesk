# This file has been modified for the KestrelDesk project.
APP := kestreldesk
MAIN := ./cmd/server
WEB_DIR := web
DIST_DIR := dist

GO ?= go
PNPM ?= pnpm
GOOS ?= $(shell $(GO) env GOOS)
GOARCH ?= $(shell $(GO) env GOARCH)
DEV_PORT ?= 8083
DEV_API_BASE_URL ?= http://127.0.0.1:$(DEV_PORT)
DEV_SERVER_URL ?= $(DEV_API_BASE_URL)/api/health
LANCEDB_VERSION ?= v0.1.2
LANCEDB_DOWNLOAD_SCRIPT ?= https://raw.githubusercontent.com/lancedb/lancedb-go/main/scripts/download-artifacts.sh
LANCEDB ?= 0

UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)

ifeq ($(GOOS),windows)
	APP_EXT := .exe
else
	APP_EXT :=
endif

ifeq ($(LANCEDB),1)
	BUILD_NAME_SUFFIX := -lancedb
	BUILD_TAGS := -tags lancedb
	BUILD_CGO_ENABLED := 1
else
	BUILD_NAME_SUFFIX :=
	BUILD_TAGS :=
	BUILD_CGO_ENABLED :=
endif

BUILD_OUTPUT := $(DIST_DIR)/$(APP)$(BUILD_NAME_SUFFIX)$(APP_EXT)

ifeq ($(UNAME_M),x86_64)
	LANCEDB_ARCH := amd64
else ifeq ($(UNAME_M),amd64)
	LANCEDB_ARCH := amd64
else ifeq ($(UNAME_M),arm64)
	LANCEDB_ARCH := arm64
else ifeq ($(UNAME_M),aarch64)
	LANCEDB_ARCH := arm64
else
	LANCEDB_ARCH := unsupported
endif

ifeq ($(UNAME_S),Darwin)
	LANCEDB_PLATFORM := darwin
	LANCEDB_SYSTEM_LDFLAGS := -framework Security -framework CoreFoundation
else ifeq ($(UNAME_S),Linux)
	LANCEDB_PLATFORM := linux
	LANCEDB_SYSTEM_LDFLAGS := -lm -ldl -lpthread
else ifneq (,$(findstring MINGW,$(UNAME_S)))
	LANCEDB_PLATFORM := windows
	LANCEDB_ARCH := amd64
	LANCEDB_SYSTEM_LDFLAGS :=
else ifneq (,$(findstring MSYS,$(UNAME_S)))
	LANCEDB_PLATFORM := windows
	LANCEDB_ARCH := amd64
	LANCEDB_SYSTEM_LDFLAGS :=
else ifneq (,$(findstring CYGWIN,$(UNAME_S)))
	LANCEDB_PLATFORM := windows
	LANCEDB_ARCH := amd64
	LANCEDB_SYSTEM_LDFLAGS :=
else
	LANCEDB_PLATFORM := unsupported
	LANCEDB_SYSTEM_LDFLAGS :=
endif

LANCEDB_PLATFORM_ARCH := $(LANCEDB_PLATFORM)_$(LANCEDB_ARCH)
LANCEDB_NATIVE_LIB := $(CURDIR)/lib/$(LANCEDB_PLATFORM_ARCH)/liblancedb_go.a
LANCEDB_CGO_CFLAGS := -I$(CURDIR)/include
LANCEDB_CGO_LDFLAGS := $(LANCEDB_NATIVE_LIB) $(LANCEDB_SYSTEM_LDFLAGS)

.DEFAULT_GOAL := build

.PHONY: help dev build release generator enums \
	_web-build-spa _web-dev _prepare-dist _lancedb-artifacts _lancedb-check _lancedb-release-check

help:
	@echo "Available targets:"
	@echo "  make dev        Start backend and frontend development servers"
	@echo "  make build      Build the current system into dist/"
	@echo "  make build LANCEDB=1"
	@echo "                  Build the current-platform LanceDB binary into dist/"
	@echo "  make release    Build linux/darwin/windows release binaries into dist/"
	@echo "  make release LANCEDB=1"
	@echo "                  Build LanceDB release binaries into dist/"
	@echo "  make generator  Run code generation"
	@echo "  make enums      Generate frontend enums"
	@echo "  make help       Show this help"

dev: _lancedb-check
	@KESTRELDESK_SERVER_PORT="$(DEV_PORT)" CGO_ENABLED=1 CGO_CFLAGS="$(LANCEDB_CGO_CFLAGS)" CGO_LDFLAGS="$(LANCEDB_CGO_LDFLAGS)" \
		$(GO) run -tags "dev lancedb" $(MAIN) & \
	server_pid=$$!; \
	trap 'kill $$server_pid 2>/dev/null || true' EXIT INT TERM; \
	echo "Waiting for server at $(DEV_SERVER_URL)..."; \
	until curl -fsS "$(DEV_SERVER_URL)" >/dev/null 2>&1; do \
		if ! kill -0 $$server_pid 2>/dev/null; then \
			wait $$server_pid; \
			exit $$?; \
		fi; \
		sleep 1; \
	done; \
	echo "Server is ready; starting web dev server..."; \
	$(MAKE) _web-dev

ifeq ($(LANCEDB),1)
build: _lancedb-check _prepare-dist _web-build-spa
else
build: _prepare-dist _web-build-spa
endif
	@echo "Building $(BUILD_OUTPUT)..."
ifeq ($(LANCEDB),1)
	@CGO_ENABLED=$(BUILD_CGO_ENABLED) CGO_CFLAGS="$(LANCEDB_CGO_CFLAGS)" CGO_LDFLAGS="$(LANCEDB_CGO_LDFLAGS)" \
		$(GO) build $(BUILD_TAGS) -v -o $(BUILD_OUTPUT) $(MAIN)
else
	@$(GO) build -v -o $(BUILD_OUTPUT) $(MAIN)
endif

ifeq ($(LANCEDB),1)
release: _lancedb-release-check _prepare-dist _web-build-spa
else
release: _prepare-dist _web-build-spa
endif
	@echo "Building release binaries in $(DIST_DIR)..."
	@if [ "$(LANCEDB)" = "1" ]; then \
		set -e; \
		if [ ! -f "$(CURDIR)/include/lancedb.h" ]; then \
			echo "Missing LanceDB header: $(CURDIR)/include/lancedb.h"; \
			exit 1; \
		fi; \
		build_lancedb() { \
			platform="$$1"; \
			arch="$$2"; \
			ext="$$3"; \
			system_ldflags="$$4"; \
			native_lib="$(CURDIR)/lib/$${platform}_$${arch}/liblancedb_go.a"; \
			output="$(DIST_DIR)/$(APP)-lancedb-$${platform}-$${arch}$${ext}"; \
			if [ ! -f "$$native_lib" ]; then \
				echo "Missing LanceDB native library for $${platform}/$${arch}: $$native_lib"; \
				echo "LanceDB release builds require matching native artifacts and a CGO-capable toolchain for each target platform."; \
				exit 1; \
			fi; \
			echo "Building $$output..."; \
			CGO_ENABLED=1 CGO_CFLAGS="-I$(CURDIR)/include" CGO_LDFLAGS="$$native_lib $$system_ldflags" \
				GOOS="$$platform" GOARCH="$$arch" $(GO) build -tags lancedb -v -o "$$output" $(MAIN); \
		}; \
		build_lancedb linux amd64 "" "-lm -ldl -lpthread"; \
		build_lancedb linux arm64 "" "-lm -ldl -lpthread"; \
		build_lancedb darwin amd64 "" "-framework Security -framework CoreFoundation"; \
		build_lancedb darwin arm64 "" "-framework Security -framework CoreFoundation"; \
		build_lancedb windows amd64 ".exe" ""; \
	else \
		GOOS=linux GOARCH=amd64 $(GO) build -v -o $(DIST_DIR)/$(APP)-linux-amd64 $(MAIN); \
		GOOS=linux GOARCH=arm64 $(GO) build -v -o $(DIST_DIR)/$(APP)-linux-arm64 $(MAIN); \
		GOOS=darwin GOARCH=amd64 $(GO) build -v -o $(DIST_DIR)/$(APP)-darwin-amd64 $(MAIN); \
		GOOS=darwin GOARCH=arm64 $(GO) build -v -o $(DIST_DIR)/$(APP)-darwin-arm64 $(MAIN); \
		GOOS=windows GOARCH=amd64 $(GO) build -v -o $(DIST_DIR)/$(APP)-windows-amd64.exe $(MAIN); \
	fi

generator:
	@$(GO) run ./cmd/generator/generator.go

enums:
	@$(GO) run ./cmd/enums/generator.go

_web-build-spa:
	@cd $(WEB_DIR) && $(PNPM) build:sdk && $(PNPM) build

_web-dev:
	@cd $(WEB_DIR) && NEXT_PUBLIC_API_BASE_URL="" NEXT_API_BASE_URL="$(DEV_API_BASE_URL)" $(PNPM) dev

_prepare-dist:
	@mkdir -p $(DIST_DIR)

_lancedb-artifacts:
	@if [ "$(LANCEDB_PLATFORM)" = "unsupported" ] || [ "$(LANCEDB_ARCH)" = "unsupported" ]; then \
		echo "Unsupported LanceDB platform: $(UNAME_S)/$(UNAME_M)"; \
		exit 1; \
	fi
	@if [ -f "$(LANCEDB_NATIVE_LIB)" ] && [ -f "$(CURDIR)/include/lancedb.h" ]; then \
		echo "LanceDB native artifacts already exist for $(LANCEDB_PLATFORM_ARCH)."; \
	else \
		echo "Downloading LanceDB native artifacts $(LANCEDB_VERSION) for $(LANCEDB_PLATFORM_ARCH)..."; \
		curl -sSL "$(LANCEDB_DOWNLOAD_SCRIPT)" | bash -s "$(LANCEDB_VERSION)"; \
	fi

_lancedb-check: _lancedb-artifacts
	@if [ ! -f "$(LANCEDB_NATIVE_LIB)" ]; then \
		echo "Missing LanceDB native library: $(LANCEDB_NATIVE_LIB)"; \
		exit 1; \
	fi
	@if [ ! -f "$(CURDIR)/include/lancedb.h" ]; then \
		echo "Missing LanceDB header: $(CURDIR)/include/lancedb.h"; \
		exit 1; \
	fi

_lancedb-release-check:
	@if [ ! -f "$(CURDIR)/include/lancedb.h" ]; then \
		echo "Missing LanceDB header: $(CURDIR)/include/lancedb.h"; \
		exit 1; \
	fi
	@missing=0; \
	for target in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64; do \
		native_lib="$(CURDIR)/lib/$${target}/liblancedb_go.a"; \
		if [ ! -f "$$native_lib" ]; then \
			echo "Missing LanceDB native library: $$native_lib"; \
			missing=1; \
		fi; \
	done; \
	if [ "$$missing" = "1" ]; then \
		echo "LanceDB release builds require matching native artifacts and a CGO-capable toolchain for each target platform."; \
		exit 1; \
	fi
