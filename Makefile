# ImageForge -- developer task runner.
# Requires: go, golangci-lint, docker compose.

GO             ?= go
# -race requires cgo; override with 'make test TEST_FLAGS=-count=1' without it.
TEST_FLAGS     ?= -race -count=1
GOLANGCI_LINT  ?= golangci-lint
DOCKER_COMPOSE ?= docker compose
BIN_DIR        ?= bin

.DEFAULT_GOAL := build

.PHONY: build test lint run-api run-worker dev dev-down dev-logs images aws-init aws-up test-integration

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

## dev: bring up the whole stack (LocalStack, api, worker, web)
dev:
	$(DOCKER_COMPOSE) up --build

## dev-down: stop the stack and delete its data
dev-down:
	$(DOCKER_COMPOSE) down --volumes --remove-orphans

## dev-logs: follow the logs of every service
dev-logs:
	$(DOCKER_COMPOSE) logs --follow

## images: build the api and worker container images
images:
	docker build -f deployments/docker/api.Dockerfile -t imageforge-api:latest .
	docker build -f deployments/docker/worker.Dockerfile -t imageforge-worker:latest .

## aws-init: create the LocalStack resources (bucket, queue, DLQ, table)
aws-init:
	$(DOCKER_COMPOSE) exec -T localstack sh /etc/localstack/init/ready.d/localstack-init.sh

## aws-up: start LocalStack and wait until its resources exist
aws-up:
	$(DOCKER_COMPOSE) up -d localstack
	@echo "waiting for localstack..."
	@until $(DOCKER_COMPOSE) exec -T localstack curl -sf http://localhost:4566/_localstack/health >/dev/null 2>&1; do sleep 1; done
	@$(MAKE) aws-init

## test-integration: run the integration suite against a running LocalStack
test-integration:
	$(GO) test -tags integration -count=1 -timeout 10m ./test/integration/...
