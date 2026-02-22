SHELL := /bin/bash

GO ?= go
DOCKER ?= docker
BUF ?= buf

GOLANGCI_LINT_VERSION ?= v2.10-alpine
GOLANGCI_LINT_BIN ?= $(shell $(GO) env GOPATH)/bin/golangci-lint
GOLANGCI_LINT_IMAGE ?= golangci/golangci-lint:$(GOLANGCI_LINT_VERSION)

.PHONY: help build test test-race fmt proto proto-check \
	lint lint-install lint-docker \
	docker-up docker-down \
	run-node run-client

help:
	@echo "Targets:"
	@echo "  build        Build all Go packages"
	@echo "  test         Run Go tests"
	@echo "  test-race    Run Go tests with race detector"
	@echo "  fmt          Format Go code"
	@echo "  proto        Regenerate protobuf code via buf"
	@echo "  proto-check  Check protobuf generation is up to date"
	@echo "  lint-docker  Run golangci-lint in Docker"
	@echo "  docker-up    Start local 3-node cluster via docker compose"
	@echo "  docker-down  Stop local cluster"
	@echo "  run-node     Run node binary (go run ./cmd/node)"
	@echo "  run-client   Run client binary (go run ./cmd/client --help)"

build:
	$(GO) build ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

fmt:
	$(GO) fmt ./...

proto:
	$(BUF) generate

proto-check:
	$(BUF) generate
	git diff --exit-code -- pkg/proto

lint-docker:
	@echo "Running golangci-lint in Docker image $(GOLANGCI_LINT_IMAGE)"
	@$(DOCKER) run --rm \
		-e CGO_ENABLED=0 \
		-v $(shell pwd):/app \
		-w /app \
		$(GOLANGCI_LINT_IMAGE) \
		golangci-lint run ./...

docker-up:
	$(DOCKER) compose up --build

docker-down:
	$(DOCKER) compose down

run-node:
	$(GO) run ./cmd/node

run-client:
	$(GO) run ./cmd/client --help
