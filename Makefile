.PHONY: build test vet check clean

GO ?= go

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/sessionmgr ./cmd/sessionmgr

test:
	CGO_ENABLED=0 $(GO) test ./...

vet:
	CGO_ENABLED=0 $(GO) vet ./...

check: vet test build

clean:
	$(RM) bin/sessionmgr
