.PHONY: build test vet race cross-check dist check clean prepare-bin prepare-dist \
	cross-darwin-arm64 cross-linux-amd64 cross-windows-amd64 \
	dist-darwin-arm64 dist-darwin-amd64 dist-linux-amd64 dist-linux-arm64 \
	dist-windows-amd64 dist-windows-arm64

BIN_DIR := bin
DIST_DIR := dist
NATIVE_WINDOWS := false
GO ?= go

ifeq ($(OS),Windows_NT)
EXEEXT := .exe
ifeq ($(strip $(MSYSTEM)),)
NATIVE_WINDOWS := true
endif
else
EXEEXT :=
endif

BIN_PATH := $(BIN_DIR)/sessionmgr$(EXEEXT)

# Native Windows GNU Make uses cmd.exe even when Make is launched from
# PowerShell. Git Bash sets MSYSTEM and should keep using POSIX commands.
ifeq ($(NATIVE_WINDOWS),true)
prepare-bin:
	if not exist "$(BIN_DIR)" mkdir "$(BIN_DIR)"

prepare-dist:
	if not exist "$(DIST_DIR)" mkdir "$(DIST_DIR)"

clean:
	if exist "$(subst /,\,$(BIN_PATH))" del /Q "$(subst /,\,$(BIN_PATH))"
	if exist "$(DIST_DIR)" rmdir /S /Q "$(DIST_DIR)"

else
prepare-bin:
	mkdir -p "$(BIN_DIR)"

prepare-dist:
	mkdir -p "$(DIST_DIR)"

clean:
	rm -f "$(BIN_PATH)"
	rm -rf "$(DIST_DIR)"

endif

CROSS_TARGETS := cross-darwin-arm64 cross-linux-amd64 cross-windows-amd64
DIST_TARGETS := dist-darwin-arm64 dist-darwin-amd64 \
	dist-linux-amd64 dist-linux-arm64 \
	dist-windows-amd64 dist-windows-arm64
NO_CGO_TARGETS := build test vet $(CROSS_TARGETS) $(DIST_TARGETS)

# Export through Make instead of using the POSIX-only `NAME=value command`
# syntax. Keep the race target on Go's native CGO setting because -race needs
# CGO on supported hosts.
$(NO_CGO_TARGETS): export CGO_ENABLED := 0

build: | prepare-bin
	$(GO) build -trimpath -o "$(BIN_PATH)" ./cmd/sessionmgr

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

race:
	$(GO) test -race ./...

cross-check: $(CROSS_TARGETS)

cross-darwin-arm64: export GOOS := darwin
cross-darwin-arm64: export GOARCH := arm64
cross-darwin-arm64:
	$(GO) build ./...

cross-linux-amd64: export GOOS := linux
cross-linux-amd64: export GOARCH := amd64
cross-linux-amd64:
	$(GO) build ./...

cross-windows-amd64: export GOOS := windows
cross-windows-amd64: export GOARCH := amd64
cross-windows-amd64:
	$(GO) build ./...

dist: $(DIST_TARGETS)

dist-darwin-arm64: export GOOS := darwin
dist-darwin-arm64: export GOARCH := arm64
dist-darwin-arm64: | prepare-dist
	$(GO) build -trimpath -o "$(DIST_DIR)/sessionmgr-darwin-arm64" ./cmd/sessionmgr

dist-darwin-amd64: export GOOS := darwin
dist-darwin-amd64: export GOARCH := amd64
dist-darwin-amd64: | prepare-dist
	$(GO) build -trimpath -o "$(DIST_DIR)/sessionmgr-darwin-amd64" ./cmd/sessionmgr

dist-linux-amd64: export GOOS := linux
dist-linux-amd64: export GOARCH := amd64
dist-linux-amd64: | prepare-dist
	$(GO) build -trimpath -o "$(DIST_DIR)/sessionmgr-linux-amd64" ./cmd/sessionmgr

dist-linux-arm64: export GOOS := linux
dist-linux-arm64: export GOARCH := arm64
dist-linux-arm64: | prepare-dist
	$(GO) build -trimpath -o "$(DIST_DIR)/sessionmgr-linux-arm64" ./cmd/sessionmgr

dist-windows-amd64: export GOOS := windows
dist-windows-amd64: export GOARCH := amd64
dist-windows-amd64: | prepare-dist
	$(GO) build -trimpath -o "$(DIST_DIR)/sessionmgr-windows-amd64.exe" ./cmd/sessionmgr

dist-windows-arm64: export GOOS := windows
dist-windows-arm64: export GOARCH := arm64
dist-windows-arm64: | prepare-dist
	$(GO) build -trimpath -o "$(DIST_DIR)/sessionmgr-windows-arm64.exe" ./cmd/sessionmgr

check: vet test race cross-check build
