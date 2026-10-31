GO ?= go
GO_CMD := $(GO)
TOOLS_MOD := -modfile=go.tools.mod

.PHONY: all build test coverage fmt lint vet install-tools mod-download mod-tidy mod-verify clean help

all: build

## build: compile all Go examples
build:
	$(GO_CMD) build ./...

## test: run tests with race detection and coverage
test:
	$(GO_CMD) test -race -count=1 -coverprofile=coverage.txt ./...

## coverage: view test coverage in a browser
coverage: test
	$(GO_CMD) tool cover -html=coverage.txt

## fmt: format Go source files
fmt:
	$(GO_CMD) tool $(TOOLS_MOD) golangci-lint fmt

## lint: check Go source with the pinned linter
lint:
	$(GO_CMD) tool $(TOOLS_MOD) golangci-lint run

## vet: run Go static checks
vet:
	$(GO_CMD) vet ./...

## install-tools: download pinned tool dependencies
install-tools:
	$(GO_CMD) mod download $(TOOLS_MOD)

## mod-download: download application dependencies
mod-download:
	$(GO_CMD) mod download

## mod-tidy: tidy application dependencies
mod-tidy:
	$(GO_CMD) mod tidy

## mod-verify: verify application and tool checksums
mod-verify:
	$(GO_CMD) mod verify
	$(GO_CMD) mod verify $(TOOLS_MOD)

## clean: remove generated coverage
clean:
	rm -f coverage.txt

## help: list available targets
help:
	@sed -n 's/^## //p' Makefile
