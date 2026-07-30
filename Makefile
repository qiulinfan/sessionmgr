.PHONY: build test vet check gui-dev gui-build gui-test clean

GO ?= go
WAILS ?= wails

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/sessionmgr ./cmd/sessionmgr

test:
	CGO_ENABLED=0 $(GO) test ./...

vet:
	CGO_ENABLED=0 $(GO) vet ./...

check: vet test build

gui-dev:
	cd gui && SESSIONMGR_GUI_PREVIEW=1 $(WAILS) dev

gui-build:
	cd gui && $(WAILS) build

gui-test:
	cd gui/frontend && npm test
	cd gui && $(GO) test ./...

clean:
	$(RM) bin/sessionmgr
	$(RM) -r gui/build/bin gui/frontend/dist
