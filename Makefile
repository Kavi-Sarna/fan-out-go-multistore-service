SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

BINARY := bin/api
PKG    := ./...

.PHONY: help fmt vet lint test race cover build run tidy clean

help: ## Show this help
	@grep -hE '^[a-z0-9-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk -F':.*?## ' '{printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}'

fmt: ## Format the code
	go run golang.org/x/tools/cmd/goimports@v0.39.0 -w -local github.com/kavi-sarna/fan-out-go-multistore-service .
	gofmt -s -w .

vet: ## Report suspicious constructs
	go vet $(PKG)

lint: ## Run the linters
	golangci-lint run

test: ## Run the tests
	go test $(PKG)

race: ## Run the tests under the race detector
	go test -race $(PKG)

cover: ## Run the tests and report total coverage
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -1

build: ## Build the binary
	go build -o $(BINARY) ./cmd/api

run: ## Run the service. ADDR=:9090 to move it
	ADDR=$(ADDR) go run ./cmd/api

tidy: ## Tidy go.mod and fail if it drifted
	go mod tidy
	@if git rev-parse --git-dir >/dev/null 2>&1; then \
	  git diff --exit-code -- go.mod $$(ls go.sum 2>/dev/null); \
	else \
	  echo "tidy: not a git repository, so there is nothing to compare against"; \
	fi

clean: ## Remove build artifacts
	rm -rf bin coverage.out
