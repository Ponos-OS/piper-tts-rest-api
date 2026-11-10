SHELL         := /bin/bash
.SHELLFLAGS   := -eu -o pipefail -c
.DEFAULT_GOAL := help

.PHONY: help init run build test integration_test lint_check lint clean

BIN  ?= bin/server
LINT := go tool -modfile=tools/go.mod golangci-lint

## Show this help message
help:
	@echo "Makefile targets:"
	@echo ""
	@awk '/^## /{desc = substr($$0, 4)} /^[a-zA-Z_-]+:/{if (desc) {target = $$1; sub(/:.*/, "", target); printf "  make %-20s- %s\n", target, desc; desc = ""}}' $(MAKEFILE_LIST)
	@echo ""

## Initialize project (download dependencies, install git hooks, create .env)
init:
	go mod download
	go mod download -modfile=tools/go.mod
	go tool -modfile=tools/go.mod lefthook install
	cp --update=none .env.example .env || true

## Starts the app with variables from .env
run:
	set -a; . ./.env; set +a; go run ./cmd/server

## Builds the server binary into bin/
build:
	go build -o $(BIN) ./cmd/server

## Runs unit tests
test:
	go test ./...

## Runs integration tests (need Docker)
integration_test:
	go test -tags integration -p 1 ./...

## Checks formatting and lint without changing files
lint_check:
	$(LINT) run

## Applies lint fixes and formatting
lint:
	$(LINT) run --fix

## Removes build artefacts
clean:
	rm -rf bin
