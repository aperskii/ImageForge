# ImageForge -- developer task runner.
# Requires: go, golangci-lint, docker compose.

GO             ?= go
# -race requires cgo; override with 'make test TEST_FLAGS=-count=1' without it.
TEST_FLAGS     ?= -race -count=1
GOLANGCI_LINT  ?= golangci-lint
DOCKER_COMPOSE ?= docker compose
BIN_DIR        ?= bin

.DEFAULT_GOAL := build

.PHONY: build test lint run-api run-worker dev

## build: compile the api and worker binaries into $(BIN_DIR)
build:
	$(GO) build -o $(BIN_DIR)/ ./cmd/...

## test: run the full test suite (race detector on by default)
test:
	$(GO) test $(TEST_FLAGS) ./...

## lint: run golangci-lint over the whole module
lint:
	$(GOLANGCI_LINT) run ./...

## run-api: run the HTTP API from source
run-api:
	$(GO) run ./cmd/api

## run-worker: run the async worker from source
run-worker:
	$(GO) run ./cmd/worker

## dev: start the local development stack (LocalStack)
dev:
	$(DOCKER_COMPOSE) up
