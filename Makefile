.PHONY: build test vet race cross-check dist check clean

GO ?= go

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/sessionmgr ./cmd/sessionmgr

test:
	CGO_ENABLED=0 $(GO) test ./...

vet:
	CGO_ENABLED=0 $(GO) vet ./...

race:
	$(GO) test -race ./...

cross-check:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build ./...
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build ./...
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build ./...

dist:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -o dist/sessionmgr-darwin-arm64 ./cmd/sessionmgr
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -o dist/sessionmgr-darwin-amd64 ./cmd/sessionmgr
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o dist/sessionmgr-linux-amd64 ./cmd/sessionmgr
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -o dist/sessionmgr-linux-arm64 ./cmd/sessionmgr
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -o dist/sessionmgr-windows-amd64.exe ./cmd/sessionmgr
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 $(GO) build -trimpath -o dist/sessionmgr-windows-arm64.exe ./cmd/sessionmgr

check: vet test race cross-check build

clean:
	$(RM) bin/sessionmgr
	$(RM) -r dist
